package kingbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// copyBufferSize bounds buffered COPY row data before a CopyData message is
// flushed, so a long import does not accumulate rows in memory.
const copyBufferSize = 64 << 10

// copyInCompletionTimeout bounds the wait for the server's terminal state when
// the row context carries no deadline of its own.
const copyInCompletionTimeout = 2 * time.Minute

var (
	errCopyInClosed        = errors.New("kingbase: COPY statement is closed")
	errCopyInProgress      = errors.New("kingbase: another COPY is already in progress on this connection")
	errCopyOutsideTx       = errors.New("kingbase: COPY FROM STDIN is only supported inside a transaction")
	errCopyTypedFormat     = errors.New("kingbase: typed binary COPY was answered with a text COPY response")
	errCopyBinaryFormat    = errors.New("kingbase: row-framed CopyIn does not accept a binary COPY; use CopyInRawBinary and send the payload yourself")
	errCopyOutNotSupported = errors.New("kingbase: COPY TO is not supported")
	errCopyNoCompletion    = errors.New("kingbase: COPY response ended without a completion tag")
	// errCopyThroughExec reports a COPY statement sent through Query/Exec,
	// where the driver has no copy stream to hand the server's data to.
	errCopyThroughExec = errors.New("kingbase: COPY statements are not supported through Query/Exec; use CopyIn on a transaction and Exec its rows")
	// errCopyOutThroughExec reports a COPY TO STDOUT statement sent through
	// Exec, where its payload would be discarded.
	errCopyOutThroughExec = errors.New("kingbase: COPY TO STDOUT must be read through Query, not Exec")
	// errCopyOutWithArgs reports a COPY TO STDOUT statement carrying statement
	// arguments; the COPY grammar has no placeholders, so this is a caller bug.
	errCopyOutWithArgs = errors.New("kingbase: COPY TO STDOUT does not accept statement arguments")
	// errCopyOutPrepared reports a prepared COPY TO STDOUT statement: the
	// extended protocol cannot stream its payload back to the caller.
	errCopyOutPrepared = errors.New("kingbase: COPY TO STDOUT cannot be prepared; run it through QueryContext on a connection")
	// errCopyOutWithoutData reports a COPY that completed without sending a
	// payload, such as a server-side COPY to a file the driver does not inspect.
	errCopyOutWithoutData = errors.New("kingbase: COPY completed without returning data; server-side COPY TO a file is not supported")
	// errUnexpectedCopyResponse reports a COPY response on a command the driver
	// did not start a copy stream for, so the payload cannot be consumed.
	errUnexpectedCopyResponse = errors.New("kingbase: unexpected COPY response")
)

// CopyIn returns a 'COPY ... FROM STDIN' statement for Tx.Prepare. The table is
// resolved through the session search_path; every identifier is quoted
// separately and no value is ever concatenated into the statement.
//
// Call Exec once per row, then Exec with no values to flush and finish the
// import. A single nil argument is a one-column NULL row, not a finish request.
func CopyIn(table string, columns ...string) string {
	return "COPY " + QuoteIdentifier(table) + copyColumnList(columns) + " FROM STDIN"
}

// CopyInSchema is CopyIn with an explicit schema-qualified target table.
func CopyInSchema(schema, table string, columns ...string) string {
	return "COPY " + QuoteIdentifier(schema) + "." + QuoteIdentifier(table) +
		copyColumnList(columns) + " FROM STDIN"
}

// copyColumnList renders the optional column list; an empty list is omitted
// because '()' is not valid COPY grammar.
func copyColumnList(columns []string) string {
	if len(columns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" (")
	for i, column := range columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(QuoteIdentifier(column))
	}
	b.WriteByte(')')
	return b.String()
}

// QuoteIdentifier quotes an SQL identifier by doubling embedded double quotes.
// Anything after a NUL byte is dropped, because a NUL cannot be represented on
// the wire and would otherwise fail the whole statement.
func QuoteIdentifier(name string) string {
	if end := strings.IndexByte(name, 0); end >= 0 {
		name = name[:end]
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// isCopyStatement reports whether the first keyword is COPY. Leading whitespace
// is ignored; a statement that hides COPY behind a leading comment is ordinary
// SQL.
func isCopyStatement(query string) bool {
	query = strings.TrimLeft(query, " \t\r\n")
	return len(query) >= 4 && strings.EqualFold(query[:4], "COPY")
}

// isCopyOutStatement reports whether the statement streams server data to the
// client (COPY ... TO STDOUT). The direction keyword is matched outside quoted
// text, identifiers, comments and parenthesized subqueries, so a column named
// "to" or a subquery returning 'STDOUT' never changes the answer.
func isCopyOutStatement(query string) bool {
	if !isCopyStatement(query) {
		return false
	}
	tokens := sqlKeywords(query)
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i] == "to" {
			return tokens[i+1] == "stdout"
		}
	}
	return false
}

// sqlKeywords returns the lowercase unquoted keywords of a statement in order.
// String literals, quoted identifiers, comments and dollar-quoted bodies are
// skipped, so only real SQL keywords remain; words nested in parentheses are
// left out because a subquery cannot change the COPY direction.
func sqlKeywords(query string) []string {
	return scanKeywords(query, true)
}

// allSQLKeywords returns every unquoted keyword including the words inside
// parentheses, which is how the COPY options are read.
func allSQLKeywords(query string) []string {
	return scanKeywords(query, false)
}

func scanKeywords(query string, topLevelOnly bool) []string {
	var keywords []string
	depth := 0
	for i := 0; i < len(query); {
		c := query[i]
		switch {
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			if end := strings.IndexByte(query[i:], '\n'); end >= 0 {
				i += end + 1
			} else {
				i = len(query)
			}
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			i = skipBlockComment(query, i)
		case c == '\'' || c == '"':
			i = skipQuoted(query, i, false)
		case c == '$':
			if end, ok := dollarQuoteAt(query, i); ok {
				i = end
			} else {
				i++
			}
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			i++
		case isIdentifierStart(c):
			end := identifierEnd(query, i)
			if !topLevelOnly || depth == 0 {
				keywords = append(keywords, strings.ToLower(query[i:end]))
			}
			i = end
		default:
			i++
		}
	}
	return keywords
}

// queryCopyOut runs 'COPY ... TO STDOUT' and returns the server payload as a
// chunk stream. Each Next call yields one protocol chunk; concatenating the
// chunks yields the COPY payload, which is text or binary according to the
// statement.
func (c *conn) queryCopyOut(ctx context.Context, query string) (driver.Rows, error) {
	if err := c.beginCommand(ctx); err != nil {
		return nil, err
	}
	c.commandSent = true
	if err := c.writer.WriteMessage(wire.MsgQuery, wire.EncodeQuery(query)); err != nil {
		c.retire()
		return nil, fmt.Errorf("kingbase: send query: %w", err)
	}
	for {
		typ, payload, err := c.reader.ReadMessage()
		if err != nil {
			c.retire()
			return nil, fmt.Errorf("kingbase: read COPY response: %w", err)
		}
		if handled, err := c.handleAsync(typ, payload); handled {
			if err != nil {
				c.retire()
				return nil, err
			}
			continue
		}
		switch typ {
		case wire.MsgCopyOutResponse:
			response, err := wire.DecodeCopyOutResponse(payload)
			if err != nil {
				c.retire()
				return nil, uncertain(fmt.Errorf("kingbase: malformed CopyOutResponse: %w", err))
			}
			return &copyOutRows{conn: c, format: response.Format}, nil
		case wire.MsgErrorResponse:
			return nil, commandError(serverError(payload), c.drainToReady())
		case wire.MsgCommandComplete, wire.MsgEmptyQueryResponse:
			// A COPY that wrote to a server-side file sends no payload; report
			// that instead of inventing an empty stream.
			if err := c.drainToReady(); err != nil {
				return nil, err
			}
			return nil, errCopyOutWithoutData
		default:
			c.retire()
			return nil, uncertain(fmt.Errorf("kingbase: unexpected message %q before a COPY stream", typ))
		}
	}
}

// CopyInRaw returns a 'COPY ... FROM STDIN WITH (FORMAT text)' statement for
// Tx.Prepare. Every Exec argument is one payload chunk written unchanged, so
// the caller owns the row framing and the escaping; an explicit FORMAT selects
// this raw mode. A zero-argument Exec still finishes the stream.
func CopyInRaw(table string, columns ...string) string {
	return "COPY " + QuoteIdentifier(table) + copyColumnList(columns) + " FROM STDIN WITH (FORMAT text)"
}

// CopyInRawSchema is CopyInRaw with an explicit schema-qualified target table.
func CopyInRawSchema(schema, table string, columns ...string) string {
	return "COPY " + QuoteIdentifier(schema) + "." + QuoteIdentifier(table) +
		copyColumnList(columns) + " FROM STDIN WITH (FORMAT text)"
}

// CopyInRawBinary is CopyInRaw with the binary wire format. The caller supplies
// the complete payload in the server's encoding, including the header and the
// trailer; the driver never inspects, validates or rewrites those bytes.
func CopyInRawBinary(table string, columns ...string) string {
	return "COPY " + QuoteIdentifier(table) + copyColumnList(columns) + " FROM STDIN WITH (FORMAT binary)"
}

// CopyInRawBinarySchema is CopyInRawBinary with a schema-qualified target table.
func CopyInRawBinarySchema(schema, table string, columns ...string) string {
	return "COPY " + QuoteIdentifier(schema) + "." + QuoteIdentifier(table) +
		copyColumnList(columns) + " FROM STDIN WITH (FORMAT binary)"
}

// copyInIsRaw reports whether the statement names an explicit COPY FORMAT, which
// makes every Exec argument one raw payload chunk instead of a row. A column
// named "format" does not match because the value after it is not a format name.
func copyInIsRaw(query string) bool {
	keywords := allSQLKeywords(query)
	for i := 0; i+1 < len(keywords); i++ {
		if keywords[i] != "format" {
			continue
		}
		switch keywords[i+1] {
		case "text", "csv", "binary":
			return true
		}
	}
	return false
}

// copyOutRows streams the payload of one COPY TO STDOUT response. Chunk
// boundaries follow protocol messages, not rows or lines, so callers must treat
// the concatenation as the authoritative payload.
type copyOutRows struct {
	conn   *conn
	format int8
	ready  bool
	closed bool
}

var _ driver.Rows = (*copyOutRows)(nil)

// Columns reports the single chunk column of the COPY stream.
func (r *copyOutRows) Columns() []string { return []string{"copy"} }

// Next returns the next protocol chunk as []byte. The slice is owned by the
// caller and stays valid after the call.
func (r *copyOutRows) Next(dest []driver.Value) error {
	if len(dest) != 1 {
		return fmt.Errorf("kingbase: COPY stream has 1 column, caller provided %d values", len(dest))
	}
	for {
		if r.ready {
			return io.EOF
		}
		typ, payload, err := r.conn.reader.ReadMessage()
		if err != nil {
			r.fail()
			return uncertain(fmt.Errorf("kingbase: read COPY data: %w", err))
		}
		if handled, err := r.conn.handleAsync(typ, payload); handled {
			if err != nil {
				r.fail()
				return uncertain(err)
			}
			continue
		}
		switch typ {
		case wire.MsgCopyData:
			dest[0] = payload
			return nil
		case wire.MsgCopyDone, wire.MsgCommandComplete:
			continue
		case wire.MsgReadyForQuery:
			r.ready = true
			if err := r.conn.setTxStatus(payload); err != nil {
				r.fail()
				return uncertain(err)
			}
			r.conn.endCommand()
			return io.EOF
		case wire.MsgErrorResponse:
			serverErr := serverError(payload)
			drainErr := r.conn.drainToReady()
			r.ready = drainErr == nil
			if drainErr != nil {
				r.fail()
			}
			return commandError(serverErr, drainErr)
		default:
			r.fail()
			return uncertain(fmt.Errorf("kingbase: unexpected message %q during COPY OUT", typ))
		}
	}
}

// Close drains the rest of the COPY payload and the terminal ReadyForQuery, so
// an abandoned stream still leaves a reusable connection.
func (r *copyOutRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.ready {
		return nil
	}
	var firstErr error
	for {
		typ, payload, err := r.conn.reader.ReadMessage()
		if err != nil {
			r.fail()
			return uncertain(fmt.Errorf("kingbase: drain COPY stream: %w", err))
		}
		if handled, err := r.conn.handleAsync(typ, payload); handled {
			if err != nil {
				r.fail()
				return uncertain(err)
			}
			continue
		}
		switch typ {
		case wire.MsgCopyData, wire.MsgCopyDone, wire.MsgCommandComplete, wire.MsgEmptyQueryResponse:
			continue
		case wire.MsgErrorResponse:
			if firstErr == nil {
				firstErr = serverError(payload)
			}
		case wire.MsgReadyForQuery:
			r.ready = true
			if err := r.conn.setTxStatus(payload); err != nil {
				r.fail()
				return uncertain(err)
			}
			r.conn.endCommand()
			return firstErr
		default:
			r.fail()
			return uncertain(fmt.Errorf("kingbase: unexpected message %q while draining COPY OUT", typ))
		}
	}
}

// fail retires the connection after a protocol or I/O failure.
func (r *copyOutRows) fail() {
	r.closed = true
	r.conn.retire()
}

// prepareCopyIn starts a COPY FROM STDIN stream on a transaction-pinned
// connection. The COPY statement is sent immediately; the returned statement
// accepts rows until Exec is called without values or Close.
func (c *conn) prepareCopyIn(ctx context.Context, query string) (driver.Stmt, error) {
	if isCopyOutStatement(query) {
		return nil, errCopyOutPrepared
	}
	if c.inNotice.Load() > 0 {
		return nil, errNoticeReentry
	}
	if c.txStatus != wire.StatusInTransaction {
		return nil, errCopyOutsideTx
	}
	if c.currentCopy() != nil {
		return nil, errCopyInProgress
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A malformed typed-binary statement fails before anything is sent.
	types, typed, err := c.copyBinaryKinds(ctx, query)
	if err != nil {
		return nil, err
	}
	raw := !typed && copyInIsRaw(query)
	// The prepare context covers only handing the statement to the server; row
	// contexts bound the stream, so no deadline or cancel watcher stays armed
	// here.
	_ = c.netConn.SetDeadline(time.Time{})
	c.commandSent = true
	if err := c.writer.WriteMessage(wire.MsgQuery, wire.EncodeQuery(query)); err != nil {
		c.retire()
		return nil, uncertain(fmt.Errorf("kingbase: send COPY: %w", err))
	}
	format, err := c.awaitCopyInResponse(raw || typed)
	if err != nil {
		return nil, err
	}
	if typed && format != 1 {
		c.retire()
		return nil, errCopyTypedFormat
	}
	ci := &copyIn{
		conn:     c,
		buffer:   make([]byte, 0, copyBufferSize),
		done:     make(chan struct{}),
		affected: -1,
		raw:      raw,
		typed:    typed,
		kinds:    types,
	}
	c.setActiveCopy(ci)
	go ci.readResult()
	return ci, nil
}

// awaitCopyInResponse consumes the response up to CopyInResponse. A server
// error is a definite outcome and keeps the connection usable; anything the
// driver does not stream (COPY TO, binary COPY, protocol garbage) retires the
// connection instead of leaving a half-consumed copy stream behind.
func (c *conn) awaitCopyInResponse(raw bool) (int8, error) {
	for {
		typ, payload, err := c.reader.ReadMessage()
		if err != nil {
			c.retire()
			return 0, uncertain(fmt.Errorf("kingbase: read COPY response: %w", err))
		}
		if handled, err := c.handleAsync(typ, payload); handled {
			if err != nil {
				c.retire()
				return 0, uncertain(err)
			}
			continue
		}
		switch typ {
		case wire.MsgCopyInResponse:
			response, err := wire.DecodeCopyInResponse(payload)
			if err != nil {
				c.retire()
				return 0, uncertain(fmt.Errorf("kingbase: malformed CopyInResponse: %w", err))
			}
			if response.Format != 0 && !raw {
				c.retire()
				return 0, errCopyBinaryFormat
			}
			return response.Format, nil
		case wire.MsgCopyOutResponse:
			c.retire()
			return 0, errCopyOutNotSupported
		case wire.MsgErrorResponse:
			return 0, c.classifyCommandError(commandError(serverError(payload), c.drainToReady()))
		default:
			c.retire()
			return 0, uncertain(fmt.Errorf("kingbase: unexpected message %q before COPY data", typ))
		}
	}
}

// copyBinaryKinds resolves a typed binary statement's target columns to their
// storage types through the qualified catalog query, so a mode-specific mapping
// is honoured instead of guessed.
func (c *conn) copyBinaryKinds(ctx context.Context, query string) ([]copyKind, bool, error) {
	relation, columns, marked, err := copyBinaryStatement(query)
	if err != nil {
		return nil, marked, err
	}
	if !marked {
		return nil, false, nil
	}
	kinds, unsupported, err := c.copyBinaryColumnKinds(ctx, relation)
	if err != nil {
		return nil, true, err
	}
	resolved := make([]copyKind, 0, len(columns))
	for _, column := range columns {
		kind, ok := kinds[column]
		if !ok {
			if typname, known := unsupported[column]; known {
				return nil, true, fmt.Errorf("kingbase: binary COPY column %q stores %s, which has no binary encoder", column, typname)
			}
			return nil, true, fmt.Errorf("kingbase: binary COPY column %q is not in relation %s", column, relation)
		}
		resolved = append(resolved, kind)
	}
	return resolved, true, nil
}

// copyBinaryColumnKinds reads the relation's storage types. A type without an
// encoder is reported by name instead of failing silently.
func (c *conn) copyBinaryColumnKinds(ctx context.Context, relation string) (map[string]copyKind, map[string]string, error) {
	rows, err := c.querySimple(ctx, copyBinaryColumnQuery(relation))
	if err != nil {
		return nil, nil, fmt.Errorf("kingbase: resolve binary COPY columns: %w", err)
	}
	kinds := make(map[string]copyKind)
	unsupported := make(map[string]string)
	values := make([]driver.Value, 4)
	for {
		err := rows.Next(values)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = rows.Close()
			return nil, nil, fmt.Errorf("kingbase: read binary COPY columns: %w", err)
		}
		name := catalogString(values[0])
		typname := catalogString(values[1])
		typlen, err := catalogInt(values[2])
		if err != nil {
			_ = rows.Close()
			return nil, nil, fmt.Errorf("kingbase: invalid length for binary COPY column %q: %v", name, err)
		}
		schema := catalogString(values[3])
		if kind, ok := copyKindForType(c.mode, schema, typname, typlen); ok {
			kinds[name] = kind
			continue
		}
		unsupported[name] = typname
	}
	if err := rows.Close(); err != nil {
		return nil, nil, fmt.Errorf("kingbase: close binary COPY column query: %w", err)
	}
	return kinds, unsupported, nil
}

// copyIn is one COPY FROM STDIN stream. The completion read runs on its own
// goroutine and only records state; applying that state to the connection
// (transaction status, deadlines, retirement) happens on the owning goroutine so
// connection fields are never mutated concurrently.
type copyIn struct {
	conn   *conn
	buffer []byte
	done   chan struct{}
	raw    bool
	typed  bool
	kinds  []copyKind
	header bool

	mu       sync.Mutex
	err      error
	bad      bool
	aborting bool
	affected int64
	status   byte
	applied  bool
	stop     func() bool
	deadline bool
	closed   bool
}

var (
	_ driver.Stmt            = (*copyIn)(nil)
	_ driver.StmtExecContext = (*copyIn)(nil)
)

// NumInput reports that COPY takes no bound parameters.
func (ci *copyIn) NumInput() int {
	return -1
}

// Exec appends one row without a context.
func (ci *copyIn) Exec(args []driver.Value) (driver.Result, error) {
	return ci.ExecContext(context.Background(), legacyNamedValues(args))
}

// Query rejects a COPY statement that is used as a query source.
func (ci *copyIn) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("kingbase: a COPY statement cannot be queried")
}

// ExecContext appends one row as COPY text. No values flush and finish the
// import. An unencodable value aborts the COPY with CopyFail so the server
// imports nothing from this stream.
func (ci *copyIn) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	// A notice handler runs on whichever goroutine reads the wire; a command
	// from it would wait for a completion this same goroutine must close.
	if ci.conn.inNotice.Load() > 0 {
		return nil, errNoticeReentry
	}
	if ci.closed {
		return nil, errCopyInClosed
	}
	if ci.badConn() {
		return nil, driver.ErrBadConn
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ci.doneClosed() {
		// The server already ended the stream; never write CopyData afterwards.
		_ = ci.complete()
		if err := ci.resultError(); err != nil {
			return nil, err
		}
		return nil, errCopyInClosed
	}
	if err := ci.checkResult(); err != nil {
		return nil, err
	}
	ci.arm(ctx)
	if len(args) == 0 {
		return ci.finish()
	}
	if ci.typed {
		return ci.execTyped(args)
	}
	if ci.raw {
		return ci.execRaw(args)
	}
	for i, arg := range args {
		if arg.Name != "" {
			err := errors.New("kingbase: named parameters are not supported by COPY")
			ci.abort(err)
			_ = ci.complete()
			return nil, err
		}
		if i > 0 {
			ci.buffer = append(ci.buffer, '\t')
		}
		encoded, err := appendCopyValue(ci.buffer, arg.Value)
		if err != nil {
			ci.abort(err)
			_ = ci.complete()
			return nil, err
		}
		ci.buffer = encoded
	}
	ci.buffer = append(ci.buffer, '\n')
	if len(ci.buffer) > wire.MaxMessageSize {
		err := errors.New("kingbase: COPY row exceeds the maximum message size")
		ci.abort(err)
		_ = ci.complete()
		return nil, err
	}
	if len(ci.buffer) >= copyBufferSize {
		if err := ci.flush(); err != nil {
			return nil, ci.complete()
		}
	}
	ci.disarm()
	return driver.RowsAffected(0), nil
}

// execRaw writes one caller-supplied payload chunk without adding row framing.
// A binary payload is opaque: the driver forwards it exactly as given.
func (ci *copyIn) execRaw(args []driver.NamedValue) (driver.Result, error) {
	if len(args) != 1 {
		err := fmt.Errorf("kingbase: raw COPY takes one payload argument, got %d", len(args))
		ci.abort(err)
		_ = ci.complete()
		return nil, err
	}
	if args[0].Name != "" {
		err := errors.New("kingbase: named parameters are not supported by COPY")
		ci.abort(err)
		_ = ci.complete()
		return nil, err
	}
	var chunk []byte
	switch value := args[0].Value.(type) {
	case []byte:
		chunk = value
	case string:
		chunk = []byte(value)
	default:
		err := fmt.Errorf("kingbase: raw COPY payload must be []byte or string, got %T", args[0].Value)
		ci.abort(err)
		_ = ci.complete()
		return nil, err
	}
	if len(chunk) > wire.MaxMessageSize {
		err := errors.New("kingbase: COPY chunk exceeds the maximum message size")
		ci.abort(err)
		_ = ci.complete()
		return nil, err
	}
	ci.buffer = append(ci.buffer, chunk...)
	if err := ci.flush(); err != nil {
		return nil, ci.complete()
	}
	ci.disarm()
	return driver.RowsAffected(0), nil
}

// execTyped writes one binary row for a statement built by CopyInBinary: the
// file header once, then the row. A value that does not match its declared type
// aborts the stream, so a partial row is never imported.
func (ci *copyIn) execTyped(args []driver.NamedValue) (driver.Result, error) {
	for _, arg := range args {
		if arg.Name != "" {
			err := errors.New("kingbase: named parameters are not supported by COPY")
			ci.abort(err)
			_ = ci.complete()
			return nil, err
		}
	}
	if !ci.header {
		ci.buffer = append(ci.buffer, copyBinaryHeader...)
		ci.header = true
	}
	next, err := appendCopyBinaryRow(ci.buffer, ci.kinds, args, ci.conn.sessionLocation())
	if err != nil {
		ci.abort(err)
		_ = ci.complete()
		return nil, err
	}
	ci.buffer = next
	if len(ci.buffer) > wire.MaxMessageSize {
		err := errors.New("kingbase: COPY row exceeds the maximum message size")
		ci.abort(err)
		_ = ci.complete()
		return nil, err
	}
	if len(ci.buffer) >= copyBufferSize {
		if err := ci.flush(); err != nil {
			return nil, ci.complete()
		}
	}
	ci.disarm()
	return driver.RowsAffected(0), nil
}

// Close flushes buffered rows and completes the import. Import failures are
// reported here because a statement close is the last chance to surface them.
func (ci *copyIn) Close() error {
	if ci.conn.inNotice.Load() > 0 {
		return errNoticeReentry
	}
	if ci.closed {
		return nil
	}
	ci.closed = true
	if ci.badConn() {
		return ci.complete()
	}
	if err := ci.resultError(); err != nil {
		return ci.complete()
	}
	_, err := ci.finish()
	return err
}

// shutdown aborts an open stream so the connection can run the next command
// (for example a rollback). It waits for the server's terminal state.
func (ci *copyIn) shutdown() {
	if !ci.doneClosed() {
		ci.abort(errCopyInProgress)
	}
	ci.boundCompletion()
	_ = ci.complete()
}

// finish flushes, sends CopyDone and waits for the server's terminal state.
func (ci *copyIn) finish() (driver.Result, error) {
	ci.closed = true
	if ci.typed {
		if !ci.header {
			ci.buffer = append(ci.buffer, copyBinaryHeader...)
			ci.header = true
		}
		ci.buffer = appendCopyBinaryTrailer(ci.buffer)
	}
	if err := ci.flush(); err != nil {
		return nil, ci.complete()
	}
	if err := ci.conn.writer.WriteMessage(wire.MsgCopyDone, nil); err != nil {
		ci.setErr(uncertain(fmt.Errorf("kingbase: complete COPY: %w", err)))
		ci.markBad()
	}
	ci.boundCompletion()
	if err := ci.complete(); err != nil {
		return nil, err
	}
	return driver.RowsAffected(ci.rowsAffected()), nil
}

// abort tells the server to reject the whole COPY stream, so a partly encoded
// row never becomes a partly imported table.
func (ci *copyIn) abort(cause error) {
	ci.setErr(cause)
	ci.mu.Lock()
	ci.aborting = true
	ci.mu.Unlock()
	if err := ci.conn.writer.WriteMessage(wire.MsgCopyFail, wire.EncodeCopyFail(cause.Error())); err != nil {
		ci.markBad()
	}
}

// flush sends the buffered CopyData message, if any.
func (ci *copyIn) flush() error {
	if len(ci.buffer) == 0 {
		return nil
	}
	err := ci.conn.writer.WriteMessage(wire.MsgCopyData, wire.EncodeCopyData(ci.buffer))
	if err == nil {
		ci.buffer = ci.buffer[:0]
		return nil
	}
	ci.setErr(uncertain(fmt.Errorf("kingbase: write COPY data: %w", err)))
	ci.markBad()
	return ci.resultError()
}

// complete waits for the completion read, then applies its result on the owning
// goroutine. It is idempotent, so Exec, Close and a following command can all
// observe the same terminal state.
func (ci *copyIn) complete() error {
	<-ci.done
	ci.disarm()
	ci.conn.clearActiveCopy(ci)

	ci.mu.Lock()
	status := ci.status
	bad := ci.bad
	err := ci.err
	apply := !ci.applied
	ci.applied = true
	ci.mu.Unlock()

	if !apply {
		return err
	}
	if status != 0 {
		if statusErr := ci.conn.setTxStatus([]byte{status}); statusErr != nil {
			ci.conn.retire()
			if err == nil {
				err = statusErr
			}
		} else {
			ci.conn.endCommand()
		}
	}
	if bad {
		ci.conn.retire()
	}
	return err
}

// checkResult waits for the copy to finish when an error was already observed,
// so callers never run a new command while the copy stream is still being read.
func (ci *copyIn) checkResult() error {
	if ci.resultError() == nil {
		return nil
	}
	return ci.complete()
}

// readResult consumes the completion of the copy stream through ReadyForQuery
// and records the outcome without touching connection fields.
func (ci *copyIn) readResult() {
	defer ci.conn.clearActiveCopy(ci)
	defer close(ci.done)
	var firstErr error
	var status byte
	var completed bool
	for {
		typ, payload, err := ci.conn.reader.ReadMessage()
		if err != nil {
			ci.setErr(commandError(firstErr, uncertain(fmt.Errorf("kingbase: read COPY completion: %w", err))))
			ci.markBad()
			return
		}
		if handled, err := ci.conn.handleAsync(typ, payload); handled {
			if err != nil {
				ci.setErr(commandError(firstErr, err))
				ci.markBad()
				return
			}
			continue
		}
		switch typ {
		case wire.MsgCommandComplete:
			tag, err := wire.DecodeCommandComplete(payload)
			if err != nil {
				ci.setErr(commandError(firstErr, uncertain(fmt.Errorf("kingbase: malformed CommandComplete: %w", err))))
				ci.markBad()
				return
			}
			if affected, ok := parseCommandTag(tag); ok {
				ci.mu.Lock()
				ci.affected = affected
				ci.mu.Unlock()
			}
			completed = true
		case wire.MsgErrorResponse:
			if firstErr == nil {
				firstErr = serverError(payload)
			}
		case wire.MsgReadyForQuery:
			if len(payload) != 1 {
				ci.setErr(uncertain(errors.New("kingbase: malformed ReadyForQuery during COPY")))
				ci.markBad()
				return
			}
			status = payload[0]
			ci.mu.Lock()
			ci.status = status
			ci.mu.Unlock()
			switch {
			case firstErr != nil:
				ci.setErr(firstErr)
				if !ci.clientAborted() {
					// CopyData may have been buffered while the server rejected
					// the stream; retire instead of racing the next command.
					ci.markBad()
				}
			case !completed:
				ci.setErr(uncertain(errCopyNoCompletion))
				ci.markBad()
			}
			return
		default:
			ci.setErr(uncertain(fmt.Errorf("kingbase: unexpected message %q while completing COPY", typ)))
			ci.markBad()
			return
		}
	}
}

// arm applies the caller context to the stream: a deadline and a watcher that
// interrupts a stalled read or write.
func (ci *copyIn) arm(ctx context.Context) {
	ci.disarm()
	socket := ci.conn.netConn
	if socket == nil {
		return
	}
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		_ = socket.SetDeadline(deadline)
	} else {
		_ = socket.SetDeadline(time.Time{})
	}
	stop := context.AfterFunc(ctx, func() { _ = socket.SetDeadline(time.Unix(1, 0)) })
	ci.mu.Lock()
	ci.stop = stop
	ci.deadline = hasDeadline
	ci.mu.Unlock()
}

// boundCompletion makes the wait for the server's terminal state finite when the
// row context did not supply a deadline.
func (ci *copyIn) boundCompletion() {
	ci.mu.Lock()
	hasDeadline := ci.deadline
	ci.mu.Unlock()
	if hasDeadline {
		return
	}
	if socket := ci.conn.netConn; socket != nil {
		_ = socket.SetDeadline(time.Now().Add(copyInCompletionTimeout))
	}
}

// disarm drops the row context and its socket deadline.
func (ci *copyIn) disarm() {
	ci.mu.Lock()
	stop := ci.stop
	ci.stop = nil
	ci.mu.Unlock()
	if stop != nil {
		stop()
	}
	if ci.conn.netConn != nil {
		_ = ci.conn.netConn.SetDeadline(time.Time{})
	}
}

func (ci *copyIn) doneClosed() bool {
	select {
	case <-ci.done:
		return true
	default:
		return false
	}
}

func (ci *copyIn) setErr(err error) {
	if err == nil {
		return
	}
	ci.mu.Lock()
	if ci.err == nil {
		ci.err = err
	}
	ci.mu.Unlock()
}

func (ci *copyIn) resultError() error {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	return ci.err
}

func (ci *copyIn) markBad() {
	ci.mu.Lock()
	ci.bad = true
	ci.mu.Unlock()
}

func (ci *copyIn) badConn() bool {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	return ci.bad
}

func (ci *copyIn) clientAborted() bool {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	return ci.aborting
}

func (ci *copyIn) rowsAffected() int64 {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	return ci.affected
}

// appendCopyValue renders one COPY text value. NULL is the \N marker; text and
// byte values escape the COPY control characters. A nil field is never a
// visible string, so an empty string stays an empty field.
func appendCopyValue(buf []byte, value driver.Value) ([]byte, error) {
	if value == nil {
		return append(buf, '\\', 'N'), nil
	}
	text, err := encodeParameter(value)
	if err != nil {
		return nil, err
	}
	return appendCopyText(buf, text), nil
}

// appendCopyText escapes backslash, newline, carriage return and tab, which are
// the COPY text-format metacharacters.
func appendCopyText(buf, text []byte) []byte {
	for _, c := range text {
		switch c {
		case '\\':
			buf = append(buf, '\\', '\\')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		case '\t':
			buf = append(buf, '\\', 't')
		default:
			buf = append(buf, c)
		}
	}
	return buf
}

// currentCopy returns the COPY stream that owns the wire, if any.
func (c *conn) currentCopy() *copyIn {
	c.copyMu.Lock()
	defer c.copyMu.Unlock()
	return c.activeCopy
}

func (c *conn) setActiveCopy(ci *copyIn) {
	c.copyMu.Lock()
	c.activeCopy = ci
	c.copyMu.Unlock()
}

func (c *conn) clearActiveCopy(ci *copyIn) {
	c.copyMu.Lock()
	if c.activeCopy == ci {
		c.activeCopy = nil
	}
	c.copyMu.Unlock()
}
