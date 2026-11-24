package gokb

import (
	"database/sql/driver"
	"fmt"
	"io"
	"reflect"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// rows streams one result set. Callers must Close it; Close drains the
// remaining response so the physical connection is reusable.
type rows struct {
	conn      *conn
	columns   []wire.ColumnDescription
	names     []string
	typeNames []string
	decoders  []func([]byte) driver.Value
	scanTypes []reflect.Type
	finished  bool
	ready     bool
	closed    bool
}

var (
	_ driver.Rows                           = (*rows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*rows)(nil)
	_ driver.RowsColumnTypeScanType         = (*rows)(nil)
)

func newRows(c *conn, columns []wire.ColumnDescription, ready bool) *rows {
	r := &rows{
		conn:      c,
		columns:   columns,
		names:     make([]string, len(columns)),
		typeNames: make([]string, len(columns)),
		decoders:  make([]func([]byte) driver.Value, len(columns)),
		scanTypes: make([]reflect.Type, len(columns)),
		finished:  ready,
		ready:     ready,
	}
	for i, column := range columns {
		r.names[i] = column.Name
		schema, name, known := c.codecName(column.TypeOID)
		if !known {
			// Unknown OIDs still decode: text values stay strings and binary
			// values stay bytes, with an empty type name as F-212 requires.
			r.scanTypes[i] = reflect.TypeOf("")
			r.decoders[i] = unknownTypeDecoder(column.Format)
			continue
		}
		r.typeNames[i] = name
		r.scanTypes[i] = scanType(schema, name)
		r.decoders[i] = typeDecoder(schema, name, column.Format)
	}
	return r
}

func unknownTypeDecoder(format int16) func([]byte) driver.Value {
	return func(data []byte) driver.Value {
		return decodeValue(format, data)
	}
}

func typeDecoder(schema, name string, format int16) func([]byte) driver.Value {
	if format != 0 {
		// Binary formats are implemented by the binary-codec task (T-010);
		// until then a known type keeps its raw bytes rather than misparsing.
		return unknownTypeDecoder(format)
	}
	return func(data []byte) driver.Value {
		if value, ok := textValue(schema, name, data); ok {
			return value
		}
		return string(data)
	}
}

// Columns returns the result column names.
func (r *rows) Columns() []string {
	return r.names
}

// ColumnTypeDatabaseTypeName returns the catalog type name, or empty for a
// type the server did not identify.
func (r *rows) ColumnTypeDatabaseTypeName(index int) string {
	return r.typeNames[index]
}

// ColumnTypeScanType returns the Go type produced by Next.
func (r *rows) ColumnTypeScanType(index int) reflect.Type {
	return r.scanTypes[index]
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
			return uncertain(fmt.Errorf("kingbase: read result row: %w", err))
		}
		if handled, err := r.conn.handleAsync(typ, payload); handled {
			if err != nil {
				r.fail()
				return uncertain(err)
			}
			continue
		}
		switch typ {
		case wire.MsgDataRow:
			columns, err := wire.DecodeDataRow(payload)
			if err != nil {
				r.fail()
				return uncertain(fmt.Errorf("kingbase: malformed DataRow: %w", err))
			}
			if len(columns) != len(r.columns) {
				r.fail()
				return uncertain(fmt.Errorf("kingbase: result row has %d columns, want %d", len(columns), len(r.columns)))
			}
			for i, column := range columns {
				if column.Null {
					dest[i] = nil
					continue
				}
				dest[i] = r.decoders[i](column.Data)
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
				return uncertain(err)
			}
			r.conn.endCommand()
			return io.EOF
		case wire.MsgRowDescription:
			r.fail()
			return uncertain(fmt.Errorf("kingbase: multiple result sets are not implemented yet"))
		case wire.MsgCopyInResponse, wire.MsgCopyData, wire.MsgCopyDone:
			r.fail()
			return uncertain(fmt.Errorf("kingbase: COPY is not implemented yet"))
		default:
			r.fail()
			return uncertain(fmt.Errorf("kingbase: unexpected message %q while reading rows", typ))
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
			firstErr = markUncertain(err)
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
		firstErr = markUncertain(err)
	}
	return firstErr
}

// fail retires the connection after a protocol or I/O failure.
func (r *rows) fail() {
	r.closed = true
	r.conn.retire()
}
