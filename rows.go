package gokb

import (
	"database/sql/driver"
	"fmt"
	"io"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// rows streams one result set. Callers must Close it; Close drains the
// remaining response so the physical connection is reusable.
type rows struct {
	conn     *conn
	columns  []wire.ColumnDescription
	names    []string
	finished bool
	ready    bool
	closed   bool
}

var _ driver.Rows = (*rows)(nil)

func newRows(c *conn, columns []wire.ColumnDescription, ready bool) *rows {
	names := make([]string, len(columns))
	for i, column := range columns {
		names[i] = column.Name
	}
	return &rows{conn: c, columns: columns, names: names, finished: ready, ready: ready}
}

// Columns returns the result column names.
func (r *rows) Columns() []string {
	return r.names
}

// Next fills dest with the next row and returns io.EOF at the end of the set.
func (r *rows) Next(dest []driver.Value) error {
	if r.closed || r.finished {
		return io.EOF
	}
	for {
		typ, payload, err := r.conn.reader.ReadMessage()
		if err != nil {
			r.fail()
			return fmt.Errorf("kingbase: read result row: %w", err)
		}
		if handled, err := r.conn.handleAsync(typ, payload); handled {
			if err != nil {
				r.fail()
				return err
			}
			continue
		}
		switch typ {
		case wire.MsgDataRow:
			columns, err := wire.DecodeDataRow(payload)
			if err != nil {
				r.fail()
				return fmt.Errorf("kingbase: malformed DataRow: %w", err)
			}
			if len(columns) != len(r.columns) {
				r.fail()
				return fmt.Errorf("kingbase: result row has %d columns, want %d", len(columns), len(r.columns))
			}
			for i, column := range columns {
				if column.Null {
					dest[i] = nil
					continue
				}
				dest[i] = decodeValue(r.columns[i].Format, column.Data)
			}
			return nil
		case wire.MsgCommandComplete, wire.MsgEmptyQueryResponse:
			r.finished = true
			return io.EOF
		case wire.MsgErrorResponse:
			r.finished = true
			return serverError(payload)
		case wire.MsgReadyForQuery:
			r.finished = true
			r.ready = true
			if err := r.conn.setTxStatus(payload); err != nil {
				r.fail()
				return err
			}
			r.conn.endCommand()
			return io.EOF
		case wire.MsgRowDescription:
			r.fail()
			return fmt.Errorf("kingbase: multiple result sets are not implemented yet")
		case wire.MsgCopyInResponse, wire.MsgCopyData, wire.MsgCopyDone:
			r.fail()
			return fmt.Errorf("kingbase: COPY is not implemented yet")
		default:
			r.fail()
			return fmt.Errorf("kingbase: unexpected message %q while reading rows", typ)
		}
	}
}

// Close drains the current result set and the terminal ReadyForQuery.
func (r *rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.ready {
		return nil
	}
	var firstErr error
	if !r.finished {
		reachedReady, err := r.conn.drainCurrentResult()
		if err != nil {
			firstErr = err
		}
		if reachedReady {
			return firstErr
		}
		// A failed drain retires the connection; there is nothing left to read.
		if r.conn.netConn == nil {
			return firstErr
		}
	}
	if err := r.conn.drainToReady(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// fail retires the connection after a protocol or I/O failure.
func (r *rows) fail() {
	r.closed = true
	r.conn.retire()
}
