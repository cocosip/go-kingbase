package gokb

import (
	"context"
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
	conn     *conn
	name     string
	numInput int
	closed   bool
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
	numInput, err := c.readPrepared()
	if err != nil {
		return nil, c.classifyCommandError(err)
	}
	return &stmt{conn: c, name: name, numInput: numInput}, nil
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
func (c *conn) readPrepared() (int, error) {
	numInput := 0
	for {
		typ, payload, err := c.reader.ReadMessage()
		if err != nil {
			c.retire()
			return 0, uncertain(fmt.Errorf("kingbase: read prepare response: %w", err))
		}
		if handled, err := c.handleAsync(typ, payload); handled {
			if err != nil {
				c.retire()
				return 0, uncertain(err)
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
				return 0, uncertain(fmt.Errorf("kingbase: malformed ParameterDescription: %w", err))
			}
			numInput = len(oids)
		case wire.MsgErrorResponse:
			serverErr := serverError(payload)
			return 0, commandError(serverErr, c.drainToReady())
		case wire.MsgReadyForQuery:
			if err := c.setTxStatus(payload); err != nil {
				c.retire()
				return 0, uncertain(err)
			}
			c.endCommand()
			return numInput, nil
		default:
			c.retire()
			return 0, uncertain(fmt.Errorf("kingbase: unexpected message %q during prepare", typ))
		}
	}
}

// NumInput returns the server-reported parameter count.
func (s *stmt) NumInput() int {
	return s.numInput
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
	params, err := s.encodeArguments(args)
	if err != nil {
		return nil, err
	}
	if err := s.conn.beginCommand(ctx); err != nil {
		return nil, err
	}
	s.conn.commandSent = true
	if err := s.conn.bindAndExecute(s.name, params, false); err != nil {
		return nil, s.conn.classifyCommandError(err)
	}
	return s.conn.classifyCommandResult()
}

// QueryContext runs the prepared statement and returns rows.
func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	params, err := s.encodeArguments(args)
	if err != nil {
		return nil, err
	}
	if err := s.conn.beginCommand(ctx); err != nil {
		return nil, err
	}
	s.conn.commandSent = true
	if err := s.conn.bindAndExecute(s.name, params, true); err != nil {
		return nil, s.conn.classifyCommandError(err)
	}
	columns, ready, err := s.conn.awaitExtendedDescription()
	if err != nil {
		return nil, s.conn.classifyCommandError(err)
	}
	return newRows(s.conn, columns, ready), nil
}

// encodeArguments validates the count before sending anything.
func (s *stmt) encodeArguments(args []driver.NamedValue) ([][]byte, error) {
	values, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}
	if len(values) != s.numInput {
		return nil, fmt.Errorf("kingbase: statement expects %d parameters, got %d", s.numInput, len(values))
	}
	return encodeParameters(values)
}

// bindAndExecute sends Bind[/Describe]/Execute/Sync for a prepared statement.
func (c *conn) bindAndExecute(statement string, params [][]byte, describe bool) error {
	steps := []wireStep{
		{typ: wire.MsgBind, payload: wire.EncodeBind("", statement, nil, params, nil)},
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
func (c *conn) classifyCommandResult() (driver.Result, error) {
	result, err := c.collectCommandResult()
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
