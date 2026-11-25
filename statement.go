package kingbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strconv"

	"github.com/cocosip/go-kingbase/internal/wire"
)

var (
	_ driver.Stmt             = (*stmt)(nil)
	_ driver.StmtExecContext  = (*stmt)(nil)
	_ driver.StmtQueryContext = (*stmt)(nil)
)

// stmt is a server-side prepared statement. The server named it, owns its
// parameter count and drops it with the connection, so Close is best effort.
type stmt struct {
	conn      *conn
	name      string
	paramOIDs []uint32
	plan      placeholderPlan
	// query is the rewritten statement text, used only by mode-specific
	// generated-ID capture after an Exec.
	query string
	// generatedQuery is the safe INSERT with RETURNING appended; Exec uses it
	// through an unnamed Parse so Query keeps the caller's original statement.
	generatedQuery string
	// autoIDErr is reported only by Exec, leaving Query with explicit RETURNING
	// usable when automatic capture is enabled.
	autoIDErr error
	closed    bool
}

// Prepare prepares a statement on the connection.
func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

// PrepareContext prepares a statement within the caller budget. The server's
// ParameterDescription is the authoritative NumInput, so no placeholder
// scanning happens here.
func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if c.netConn == nil {
		return nil, driver.ErrBadConn
	}
	if isCopyStatement(query) {
		return c.prepareCopyIn(ctx, query)
	}
	// Prepared statements accept ? and named placeholders too; the server's
	// ParameterDescription then reports the argument count database/sql enforces.
	plan, err := c.rewriteParameters(query)
	if err != nil {
		return nil, err
	}
	query = c.rewriteRoutine(plan.query)
	var generatedQuery string
	var autoIDErr error
	if c.cfg != nil && c.cfg.GetLastInsertID && c.mode == ModePG {
		rewritten, column, err := c.planGeneratedInsert(ctx, query)
		switch {
		case err != nil:
			autoIDErr = err
		case column != "":
			generatedQuery = rewritten
		}
	}
	name := "kb_stmt_" + strconv.Itoa(c.nextStmtID)
	c.nextStmtID++
	if err := c.beginCommand(ctx); err != nil {
		return nil, err
	}
	c.commandSent = true
	steps := []wireStep{
		{typ: wire.MsgParse, payload: wire.EncodeParse(name, query, nil)},
		{typ: wire.MsgDescribe, payload: wire.EncodeDescribe('S', name)},
		{typ: wire.MsgSync},
	}
	if err := c.writeSteps(steps); err != nil {
		c.retire()
		return nil, c.classifyCommandError(err)
	}
	paramOIDs, err := c.readPrepared()
	if err != nil {
		return nil, c.classifyCommandError(err)
	}
	return &stmt{
		conn:           c,
		name:           name,
		paramOIDs:      paramOIDs,
		plan:           plan,
		query:          query,
		generatedQuery: generatedQuery,
		autoIDErr:      autoIDErr,
	}, nil
}

// wireStep is one frontend message in a single-flush batch.
type wireStep struct {
	typ     wire.MsgType
	payload []byte
}

// writeSteps buffers all messages and flushes once.
func (c *conn) writeSteps(steps []wireStep) error {
	for _, item := range steps {
		if err := c.writer.WriteBuffered(item.typ, item.payload); err != nil {
			return fmt.Errorf("kingbase: send statement: %w", err)
		}
	}
	if err := c.writer.Flush(); err != nil {
		return fmt.Errorf("kingbase: send statement: %w", err)
	}
	return nil
}

// readPrepared consumes the Parse/Describe response through ReadyForQuery.
func (c *conn) readPrepared() ([]uint32, error) {
	var paramOIDs []uint32
	for {
		typ, payload, err := c.reader.ReadMessage()
		if err != nil {
			c.retire()
			return nil, uncertain(fmt.Errorf("kingbase: read prepare response: %w", err))
		}
		if handled, err := c.handleAsync(typ, payload); handled {
			if err != nil {
				c.retire()
				return nil, uncertain(err)
			}
			continue
		}
		switch typ {
		case wire.MsgParseComplete, wire.MsgNoData, wire.MsgRowDescription:
			// Prepared successfully; the description itself is not retained.
		case wire.MsgParameterDescription:
			oids, err := wire.DecodeParameterDescription(payload)
			if err != nil {
				c.retire()
				return nil, uncertain(fmt.Errorf("kingbase: malformed ParameterDescription: %w", err))
			}
			paramOIDs = oids
		case wire.MsgErrorResponse:
			serverErr := serverError(payload)
			return nil, commandError(serverErr, c.drainToReady())
		case wire.MsgReadyForQuery:
			if err := c.setTxStatus(payload); err != nil {
				c.retire()
				return nil, uncertain(err)
			}
			c.endCommand()
			return paramOIDs, nil
		default:
			c.retire()
			return nil, uncertain(fmt.Errorf("kingbase: unexpected message %q during prepare", typ))
		}
	}
}

// NumInput returns the server-reported parameter count, except in SQLServer
// mode where the profile's procedure/EXEC forms make the server count an
// unreliable contract; -1 disables database/sql's pre-check and bindValues still
// enforces the binding plan before anything is sent.
func (s *stmt) NumInput() int {
	if s.conn != nil && s.conn.mode == ModeSQLServer {
		return -1
	}
	return len(s.paramOIDs)
}

// Close drops the server statement. A retired connection drops it implicitly.
func (s *stmt) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	c := s.conn
	if c.netConn == nil {
		return nil
	}
	if err := c.beginCommand(context.Background()); err != nil {
		return nil
	}
	c.commandSent = true
	steps := []wireStep{
		{typ: wire.MsgClose, payload: wire.EncodeClose('S', s.name)},
		{typ: wire.MsgSync},
	}
	if err := c.writeSteps(steps); err != nil {
		c.retire()
		return c.classifyCommandError(err)
	}
	return c.classifyCommandError(c.drainToReady())
}

// Exec runs the prepared statement with positional arguments.
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), legacyNamedValues(args))
}

// Query runs the prepared statement and returns rows.
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), legacyNamedValues(args))
}

// ExecContext runs the prepared statement with positional arguments.
func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	args, outputs := extractOutArguments(args)
	if len(outputs) > 0 {
		return s.execRoutineWithOutputs(ctx, args, outputs)
	}
	if s.autoIDErr != nil {
		return nil, s.autoIDErr
	}
	values, err := s.bindValues(args)
	if err != nil {
		return nil, err
	}
	if s.generatedQuery != "" {
		params, formats, err := s.conn.encodeParametersWithTypes(values, s.paramOIDs)
		if err != nil {
			return nil, err
		}
		if err := s.conn.beginCommand(ctx); err != nil {
			return nil, err
		}
		s.conn.commandSent = true
		if err := s.conn.sendExtended(s.generatedQuery, params, formats, false); err != nil {
			s.conn.retire()
			return nil, s.conn.classifyCommandError(err)
		}
		return s.conn.classifyCommandResult(true)
	}
	params, formats, err := s.conn.encodeParametersWithTypes(values, s.paramOIDs)
	if err != nil {
		return nil, err
	}
	if err := s.conn.beginCommand(ctx); err != nil {
		return nil, err
	}
	s.conn.commandSent = true
	if err := s.conn.bindAndExecute(s.name, params, formats, false, nil); err != nil {
		s.conn.retire()
		return nil, s.conn.classifyCommandError(err)
	}
	commandResult, err := s.conn.classifyCommandResult(false)
	if err != nil {
		return nil, err
	}
	if res, ok := commandResult.(*result); ok {
		s.conn.captureMySQLGeneratedID(ctx, s.query, res)
	}
	return commandResult, nil
}

// QueryContext runs the prepared statement and returns rows.
func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	values, err := s.bindValues(args)
	if err != nil {
		return nil, err
	}
	params, formats, err := s.conn.encodeParametersWithTypes(values, s.paramOIDs)
	if err != nil {
		return nil, err
	}
	if err := s.conn.beginCommand(ctx); err != nil {
		return nil, err
	}
	s.conn.commandSent = true
	if err := s.conn.bindAndExecute(s.name, params, formats, true, s.conn.resultFormats()); err != nil {
		s.conn.retire()
		return nil, s.conn.classifyCommandError(err)
	}
	columns, ready, err := s.conn.awaitExtendedDescription()
	if err != nil {
		return nil, s.conn.classifyCommandError(err)
	}
	return newRows(s.conn, columns, ready), nil
}

// bindValues maps named/positional arguments onto the statement plan and
// enforces the server-reported parameter count.
func (s *stmt) bindValues(args []driver.NamedValue) ([]driver.Value, error) {
	values, err := s.plan.bindValues(args)
	if err != nil {
		return nil, err
	}
	if len(values) != len(s.paramOIDs) {
		return nil, fmt.Errorf("kingbase: statement expects %d parameters, got %d", len(s.paramOIDs), len(values))
	}
	return values, nil
}

// bindAndExecute sends Bind[/Describe]/Execute/Sync for a prepared statement.
func (c *conn) bindAndExecute(statement string, params [][]byte, paramFormats []int16, describe bool, resultFormats []int16) error {
	steps := []wireStep{
		{typ: wire.MsgBind, payload: wire.EncodeBind("", statement, paramFormats, params, resultFormats)},
	}
	if describe {
		steps = append(steps, wireStep{typ: wire.MsgDescribe, payload: wire.EncodeDescribe('P', "")})
	}
	steps = append(steps,
		wireStep{typ: wire.MsgExecute, payload: wire.EncodeExecute("", 0)},
		wireStep{typ: wire.MsgSync},
	)
	return c.writeSteps(steps)
}

// classifyCommandResult finishes an exec-style response.
func (c *conn) classifyCommandResult(capture bool) (driver.Result, error) {
	result, err := c.collectCommandResult(capture)
	if err != nil {
		return nil, c.classifyCommandError(err)
	}
	return result, nil
}

func legacyNamedValues(args []driver.Value) []driver.NamedValue {
	values := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		values[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return values
}

// resultFormats selects the Bind result format for prepared statements.
// Binary results are the default, matching the
// disable_prepared_binary_result option that turns them off.
func (c *conn) resultFormats() []int16 {
	if c.cfg != nil && c.cfg.DisablePreparedBinaryResult {
		return nil
	}
	return []int16{1}
}

var _ driver.NamedValueChecker = (*conn)(nil)

// CheckNamedValue normalizes one argument with the standard converter before
// encoding. Names are retained so the statement plan can bind them; uint64
// values above MaxInt64 or broken Valuers fail here, before any bytes reach the
// server.
func (c *conn) CheckNamedValue(argument *driver.NamedValue) error {
	// Keep the compatibility wrappers and the full uint64 range intact until
	// binding; the default converter would collapse them into string/int64 and
	// lose the caller's target intent or reject values above MaxInt64.
	switch value := argument.Value.(type) {
	case sql.Out:
		out, err := newOutArgument(value)
		if err != nil {
			return fmt.Errorf("kingbase: parameter %d: %w", argument.Ordinal, err)
		}
		argument.Value = out
		return nil
	case uint64, VarChar, VarCharMax, NVarCharMax, NChar, DateTime1:
		return nil
	}
	value, err := driver.DefaultParameterConverter.ConvertValue(argument.Value)
	if err != nil {
		return fmt.Errorf("kingbase: parameter %d: %w", argument.Ordinal, err)
	}
	argument.Value = value
	return nil
}
