package gokb

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cocosip/go-kingbase/internal/wire"
)

var (
	_ driver.QueryerContext = (*conn)(nil)
	_ driver.ExecerContext  = (*conn)(nil)
)

// QueryContext runs a statement and returns its rows. Arguments use the
// extended protocol; a statement without arguments uses the simple query
// protocol.
func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	values, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}
	var rows driver.Rows
	if len(values) == 0 {
		rows, err = c.querySimple(ctx, query)
	} else {
		rows, err = c.queryExtended(ctx, query, values)
	}
	if err != nil {
		return nil, c.classifyCommandError(err)
	}
	return rows, nil
}

// ExecContext runs a statement and returns its completion result.
func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	values, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}
	var result driver.Result
	if len(values) == 0 {
		result, err = c.execSimple(ctx, query)
	} else {
		result, err = c.execExtended(ctx, query, values)
	}
	if err != nil {
		return nil, c.classifyCommandError(err)
	}
	return result, nil
}

// classifyCommandError marks post-send failures as execution-uncertain. A
// server error response is a definite outcome and stays unwrapped, and
// ErrBadConn only ever comes from pre-send checks.
func (c *conn) classifyCommandError(err error) error {
	if err == nil || !c.commandSent {
		return err
	}
	return markUncertain(err)
}

// markUncertain wraps a failure that happened after a request was written,
// unless it is already classified or is a definite server error.
func markUncertain(err error) error {
	if err == nil {
		return nil
	}
	var serverErr *Error
	if errors.As(err, &serverErr) {
		return err
	}
	if errors.Is(err, driver.ErrBadConn) {
		return err
	}
	return uncertain(err)
}

func namedValuesToValues(args []driver.NamedValue) ([]driver.Value, error) {
	if len(args) == 0 {
		return nil, nil
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		if arg.Name != "" {
			return nil, fmt.Errorf("kingbase: named parameters are not implemented yet")
		}
		if arg.Ordinal != i+1 {
			return nil, fmt.Errorf("kingbase: invalid parameter ordinal %d", arg.Ordinal)
		}
		values[i] = arg.Value
	}
	return values, nil
}

func encodeParameters(values []driver.Value) ([][]byte, error) {
	params := make([][]byte, len(values))
	for i, value := range values {
		encoded, err := encodeParameter(value)
		if err != nil {
			return nil, err
		}
		params[i] = encoded
	}
	return params, nil
}

// beginCommand applies the operation deadline and refuses reuse of a retired
// connection. A closed connection is a pre-execution failure, so ErrBadConn is
// safe here for database/sql to retry on another connection.
func (c *conn) beginCommand(ctx context.Context) error {
	if c.netConn == nil {
		return driver.ErrBadConn
	}
	// A command issued while a COPY stream is open (for example a rollback)
	// first finishes that stream so the wire is back at ReadyForQuery.
	if stream := c.currentCopy(); stream != nil {
		stream.shutdown()
		if c.netConn == nil {
			return driver.ErrBadConn
		}
	}
	c.commandSent = false
	if err := ctx.Err(); err != nil {
		return err
	}
	// A watcher left behind by an abandoned command would otherwise fire during
	// this one; only a watcher that cannot be stopped leaves an unknown state.
	if c.stopWatch != nil {
		if !c.stopWatch() {
			c.stopWatch = nil
			c.retire()
			return driver.ErrBadConn
		}
		c.stopWatch = nil
	}
	var deadline time.Time
	if operationDeadline, ok := ctx.Deadline(); ok {
		if err := c.netConn.SetDeadline(operationDeadline); err != nil {
			c.retire()
			return fmt.Errorf("kingbase: set deadline: %w", err)
		}
		deadline = operationDeadline
	} else if err := c.netConn.SetDeadline(time.Time{}); err != nil {
		c.retire()
		return fmt.Errorf("kingbase: clear deadline: %w", err)
	}
	// Cancellation sends a PG cancel request on the independent cancel channel
	// and bounds how long the original stream may take to report the aborted
	// command. The socket is captured so the watcher never races with
	// retirement.
	socket := c.netConn
	c.stopWatch = context.AfterFunc(ctx, func() {
		c.startCancel(socket, deadline)
	})
	return nil
}

// endCommand clears the operation deadline. A context that was cancelled while
// the command ran makes the protocol state uncertain, so the connection is
// retired instead of reused.
func (c *conn) endCommand() {
	if c.stopWatch != nil {
		if !c.stopWatch() {
			c.stopWatch = nil
			c.retire()
			return
		}
		c.stopWatch = nil
	}
	if c.netConn != nil {
		_ = c.netConn.SetDeadline(time.Time{})
	}
}

// retire closes the physical connection after an uncertain protocol or I/O
// failure. A retired connection is never reused.
func (c *conn) retire() {
	if c.stopWatch != nil {
		c.stopWatch()
		c.stopWatch = nil
	}
	if c.netConn != nil {
		_ = c.netConn.Close()
		c.netConn = nil
	}
}

// handleAsync consumes legal asynchronous messages and reports whether it did.
func (c *conn) handleAsync(typ wire.MsgType, payload []byte) (bool, error) {
	switch typ {
	case wire.MsgNoticeResponse:
		// Notices are discarded by default; callbacks are notification work.
		return true, nil
	case wire.MsgParameterStatus:
		name, value, err := wire.DecodeParameterStatus(payload)
		if err != nil {
			return true, fmt.Errorf("kingbase: malformed ParameterStatus: %w", err)
		}
		// A setting that changes how the server parses string literals makes the
		// session context unverifiable; retire instead of continuing under
		// unknown lexical rules.
		if name == stringLiteralParameter {
			if err := checkStringLiteralMode(value); err != nil {
				return true, err
			}
		}
		c.params[name] = value
		return true, nil
	case wire.MsgNotificationResponse:
		// LISTEN/NOTIFY delivery is a separate task; it cannot reenter here.
		return true, nil
	default:
		return false, nil
	}
}

func (c *conn) setTxStatus(payload []byte) error {
	if len(payload) != 1 {
		return fmt.Errorf("kingbase: malformed ReadyForQuery")
	}
	c.txStatus = payload[0]
	return nil
}

func (c *conn) querySimple(ctx context.Context, query string) (driver.Rows, error) {
	if err := c.beginCommand(ctx); err != nil {
		return nil, err
	}
	c.commandSent = true
	if err := c.writer.WriteMessage(wire.MsgQuery, wire.EncodeQuery(query)); err != nil {
		c.retire()
		return nil, fmt.Errorf("kingbase: send query: %w", err)
	}
	columns, terminal, err := c.awaitFirstResult()
	if err != nil {
		return nil, err
	}
	return newRows(c, columns, terminal), nil
}

// commandError joins an already seen server error with a later read or drain
// failure, so a FATAL followed by EOF keeps its SQLSTATE and classification.
func commandError(first, rest error) error {
	if first == nil {
		return rest
	}
	if rest == nil {
		return first
	}
	return errors.Join(first, rest)
}

// awaitFirstResult consumes messages until the first result description or a
// terminal response. A terminal response is fully drained before returning.
func (c *conn) awaitFirstResult() ([]wire.ColumnDescription, bool, error) {
	for {
		typ, payload, err := c.reader.ReadMessage()
		if err != nil {
			c.retire()
			return nil, false, fmt.Errorf("kingbase: read query response: %w", err)
		}
		if handled, err := c.handleAsync(typ, payload); handled {
			if err != nil {
				c.retire()
				return nil, false, err
			}
			continue
		}
		switch typ {
		case wire.MsgRowDescription:
			columns, err := wire.DecodeRowDescription(payload)
			if err != nil {
				c.retire()
				return nil, false, fmt.Errorf("kingbase: malformed RowDescription: %w", err)
			}
			return columns, false, nil
		case wire.MsgCommandComplete, wire.MsgEmptyQueryResponse:
			if err := c.drainToReady(); err != nil {
				return nil, false, err
			}
			return nil, true, nil
		case wire.MsgErrorResponse:
			serverErr := serverError(payload)
			return nil, false, commandError(serverErr, c.drainToReady())
		case wire.MsgCopyInResponse, wire.MsgCopyData, wire.MsgCopyDone:
			c.retire()
			return nil, false, fmt.Errorf("kingbase: COPY is not implemented yet")
		default:
			c.retire()
			return nil, false, fmt.Errorf("kingbase: unexpected message %q before a result", typ)
		}
	}
}

func (c *conn) queryExtended(ctx context.Context, query string, values []driver.Value) (driver.Rows, error) {
	params, err := encodeParameters(values)
	if err != nil {
		return nil, err
	}
	if err := c.beginCommand(ctx); err != nil {
		return nil, err
	}
	c.commandSent = true
	if err := c.sendExtended(query, params, true); err != nil {
		return nil, err
	}
	columns, ready, err := c.awaitExtendedDescription()
	if err != nil {
		return nil, err
	}
	return newRows(c, columns, ready), nil
}

func (c *conn) execSimple(ctx context.Context, query string) (driver.Result, error) {
	if err := c.beginCommand(ctx); err != nil {
		return nil, err
	}
	c.commandSent = true
	if err := c.writer.WriteMessage(wire.MsgQuery, wire.EncodeQuery(query)); err != nil {
		c.retire()
		return nil, fmt.Errorf("kingbase: send query: %w", err)
	}
	return c.collectCommandResult()
}

func (c *conn) execExtended(ctx context.Context, query string, values []driver.Value) (driver.Result, error) {
	params, err := encodeParameters(values)
	if err != nil {
		return nil, err
	}
	if err := c.beginCommand(ctx); err != nil {
		return nil, err
	}
	c.commandSent = true
	if err := c.sendExtended(query, params, false); err != nil {
		return nil, err
	}
	return c.collectCommandResult()
}

// sendExtended writes one Parse/Bind[/Describe]/Execute/Sync batch with a
// single flush. A failed send may leave the protocol state unknown, so the
// connection is retired.
func (c *conn) sendExtended(query string, params [][]byte, describe bool) error {
	type step struct {
		typ     wire.MsgType
		payload []byte
	}
	steps := []step{
		{typ: wire.MsgParse, payload: wire.EncodeParse("", query, nil)},
		{typ: wire.MsgBind, payload: wire.EncodeBind("", "", nil, params, nil)},
	}
	if describe {
		steps = append(steps, step{typ: wire.MsgDescribe, payload: wire.EncodeDescribe('P', "")})
	}
	steps = append(steps,
		step{typ: wire.MsgExecute, payload: wire.EncodeExecute("", 0)},
		step{typ: wire.MsgSync},
	)
	for _, item := range steps {
		if err := c.writer.WriteBuffered(item.typ, item.payload); err != nil {
			c.retire()
			return fmt.Errorf("kingbase: send query: %w", err)
		}
	}
	if err := c.writer.Flush(); err != nil {
		c.retire()
		return fmt.Errorf("kingbase: send query: %w", err)
	}
	return nil
}

func (c *conn) awaitExtendedDescription() ([]wire.ColumnDescription, bool, error) {
	for {
		typ, payload, err := c.reader.ReadMessage()
		if err != nil {
			c.retire()
			return nil, false, fmt.Errorf("kingbase: read query response: %w", err)
		}
		if handled, err := c.handleAsync(typ, payload); handled {
			if err != nil {
				c.retire()
				return nil, false, err
			}
			continue
		}
		switch typ {
		case wire.MsgParseComplete, wire.MsgBindComplete, wire.MsgParameterDescription,
			wire.MsgCloseComplete:
			continue
		case wire.MsgRowDescription:
			columns, err := wire.DecodeRowDescription(payload)
			if err != nil {
				c.retire()
				return nil, false, fmt.Errorf("kingbase: malformed RowDescription: %w", err)
			}
			return columns, false, nil
		case wire.MsgNoData:
			// No result columns; Execute still follows with its completion.
			return nil, false, nil
		case wire.MsgCommandComplete, wire.MsgEmptyQueryResponse:
			return nil, true, c.drainToReady()
		case wire.MsgErrorResponse:
			serverErr := serverError(payload)
			return nil, true, commandError(serverErr, c.drainToReady())
		case wire.MsgCopyInResponse, wire.MsgCopyData, wire.MsgCopyDone:
			c.retire()
			return nil, false, fmt.Errorf("kingbase: COPY is not implemented yet")
		default:
			c.retire()
			return nil, false, fmt.Errorf("kingbase: unexpected message %q before a result", typ)
		}
	}
}

// collectCommandResult consumes a command response through ReadyForQuery and
// returns the last completion tag. A trailing error wins over success, and
// rows are discarded.
func (c *conn) collectCommandResult() (driver.Result, error) {
	result := &result{rowsAffected: -1}
	var firstErr error
	for {
		typ, payload, err := c.reader.ReadMessage()
		if err != nil {
			c.retire()
			return nil, commandError(firstErr, fmt.Errorf("kingbase: read command response: %w", err))
		}
		if handled, err := c.handleAsync(typ, payload); handled {
			if err != nil {
				c.retire()
				return nil, commandError(firstErr, err)
			}
			continue
		}
		switch typ {
		case wire.MsgCommandComplete:
			tag, err := wire.DecodeCommandComplete(payload)
			if err != nil {
				c.retire()
				return nil, fmt.Errorf("kingbase: malformed CommandComplete: %w", err)
			}
			if affected, ok := parseCommandTag(tag); ok {
				result.rowsAffected = affected
			}
		case wire.MsgRowDescription, wire.MsgDataRow, wire.MsgEmptyQueryResponse,
			wire.MsgParseComplete, wire.MsgBindComplete, wire.MsgNoData,
			wire.MsgParameterDescription, wire.MsgCloseComplete:
			// Exec discards rows, for example INSERT ... RETURNING.
		case wire.MsgErrorResponse:
			if firstErr == nil {
				firstErr = serverError(payload)
			}
		case wire.MsgReadyForQuery:
			if err := c.setTxStatus(payload); err != nil {
				c.retire()
				return nil, err
			}
			c.endCommand()
			if firstErr != nil {
				return nil, firstErr
			}
			return result, nil
		case wire.MsgCopyInResponse, wire.MsgCopyData, wire.MsgCopyDone:
			c.retire()
			return nil, fmt.Errorf("kingbase: COPY is not implemented yet")
		default:
			c.retire()
			return nil, fmt.Errorf("kingbase: unexpected message %q during command", typ)
		}
	}
}

// drainToReady consumes the remaining command response through ReadyForQuery.
// Unexpected rows mean an additional result set, which this task does not
// consume, so the connection is retired instead of reused.
func (c *conn) drainToReady() error {
	var firstErr error
	for {
		typ, payload, err := c.reader.ReadMessage()
		if err != nil {
			c.retire()
			return commandError(firstErr, fmt.Errorf("kingbase: drain response: %w", err))
		}
		if handled, err := c.handleAsync(typ, payload); handled {
			if err != nil {
				c.retire()
				return commandError(firstErr, err)
			}
			continue
		}
		switch typ {
		case wire.MsgReadyForQuery:
			if err := c.setTxStatus(payload); err != nil {
				c.retire()
				return err
			}
			c.endCommand()
			return firstErr
		case wire.MsgErrorResponse:
			if firstErr == nil {
				firstErr = serverError(payload)
			}
		case wire.MsgCommandComplete, wire.MsgEmptyQueryResponse, wire.MsgParseComplete,
			wire.MsgBindComplete, wire.MsgNoData, wire.MsgParameterDescription,
			wire.MsgCloseComplete:
			continue
		case wire.MsgRowDescription, wire.MsgDataRow:
			// Additional result sets are legal; drain them instead of retiring.
			continue
		case wire.MsgCopyInResponse, wire.MsgCopyData, wire.MsgCopyDone:
			c.retire()
			if firstErr == nil {
				firstErr = fmt.Errorf("kingbase: COPY is not implemented yet")
			}
			return firstErr
		default:
			c.retire()
			if firstErr == nil {
				firstErr = fmt.Errorf("kingbase: unexpected message %q while draining", typ)
			}
			return firstErr
		}
	}
}

// drainCurrentResult consumes rows of the current result set through its
// CommandComplete. It reports whether ReadyForQuery was already reached.
func (c *conn) drainCurrentResult() (bool, error) {
	for {
		typ, payload, err := c.reader.ReadMessage()
		if err != nil {
			c.retire()
			return false, fmt.Errorf("kingbase: drain rows: %w", err)
		}
		if handled, err := c.handleAsync(typ, payload); handled {
			if err != nil {
				c.retire()
				return false, err
			}
			continue
		}
		switch typ {
		case wire.MsgDataRow:
			continue
		case wire.MsgCommandComplete, wire.MsgEmptyQueryResponse:
			return false, nil
		case wire.MsgErrorResponse:
			return false, serverError(payload)
		case wire.MsgReadyForQuery:
			if err := c.setTxStatus(payload); err != nil {
				c.retire()
				return true, err
			}
			c.endCommand()
			return true, nil
		case wire.MsgRowDescription:
			c.retire()
			return false, fmt.Errorf("kingbase: multiple result sets are not implemented yet")
		default:
			c.retire()
			return false, fmt.Errorf("kingbase: unexpected message %q while draining rows", typ)
		}
	}
}

// result is one command completion. LastInsertId stays unavailable until the
// generated-identity task implements its restricted contract.
type result struct {
	rowsAffected int64
}

var _ driver.Result = (*result)(nil)

// LastInsertId reports that automatic generated-id capture is not enabled.
func (r *result) LastInsertId() (int64, error) {
	return 0, fmt.Errorf("kingbase: LastInsertId is unavailable (enable get_last_insert_id)")
}

// RowsAffected returns the parsed affected-row count, or -1 when the tag does
// not carry one.
func (r *result) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}

// parseCommandTag extracts an affected-row count from a completion tag.
func parseCommandTag(tag string) (int64, bool) {
	fields := strings.Fields(tag)
	switch {
	case len(fields) == 3 && fields[0] == "INSERT":
		if affected, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
			return affected, true
		}
	case len(fields) == 2:
		switch fields[0] {
		case "UPDATE", "DELETE", "SELECT", "MOVE", "FETCH", "COPY", "MERGE":
			if affected, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return affected, true
			}
		}
	}
	return -1, false
}
