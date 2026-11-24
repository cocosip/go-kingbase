package gokb

import (
	"database/sql/driver"
	"fmt"
	"io"
	"reflect"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// maxResultSets bounds how many result sets one response may contain.
const maxResultSets = 256

// rows streams result sets. Callers must Close it; Close drains the remaining
// response so the physical connection is reusable.
type rows struct {
	conn       *conn
	columns    []wire.ColumnDescription
	names      []string
	typeNames  []string
	decoders   []func([]byte) driver.Value
	scanTypes  []reflect.Type
	finished   bool
	ready      bool
	closed     bool
	resultSets int
}

var (
	_ driver.Rows                           = (*rows)(nil)
	_ driver.RowsNextResultSet              = (*rows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*rows)(nil)
	_ driver.RowsColumnTypeScanType         = (*rows)(nil)
)

func newRows(c *conn, columns []wire.ColumnDescription, ready bool) *rows {
	r := &rows{
		conn:       c,
		finished:   ready,
		ready:      ready,
		resultSets: 1,
	}
	r.resetColumns(columns)
	return r
}

// resetColumns rebuilds the decode state for one result set.
func (r *rows) resetColumns(columns []wire.ColumnDescription) {
	r.columns = columns
	r.names = make([]string, len(columns))
	r.typeNames = make([]string, len(columns))
	r.decoders = make([]func([]byte) driver.Value, len(columns))
	r.scanTypes = make([]reflect.Type, len(columns))
	for i, column := range columns {
		r.names[i] = column.Name
		schema, name, known := r.conn.codecName(column.TypeOID)
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
}

func unknownTypeDecoder(format int16) func([]byte) driver.Value {
	return func(data []byte) driver.Value {
		return decodeValue(format, data)
	}
}

// typeDecoder builds the per-column decoder for a verified catalog type.
func typeDecoder(schema, name string, format int16) func([]byte) driver.Value {
	if format == 1 {
		return func(data []byte) driver.Value {
			if value, ok := binaryValue(schema, name, data); ok {
				return value
			}
			// Unverified binary layouts (numeric, interval, inet, ...) stay raw
			// bytes; ponytail: add codecs when their wire layout is proven, and
			// never guess a layout from the type name alone.
			return append([]byte(nil), data...)
		}
	}
	if format != 0 {
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

// HasNextResultSet reports whether another result set may follow. The
// response terminator is only observed by reading, so this stays optimistic
// until ReadyForQuery arrives or the row stream ends.
func (r *rows) HasNextResultSet() bool {
	return !r.closed && !r.ready
}

// NextResultSet advances to the next result set of a multi-statement response.
// It drains an unconsumed set first and returns io.EOF after ReadyForQuery.
func (r *rows) NextResultSet() error {
	if r.closed || r.ready {
		return io.EOF
	}
	for {
		if !r.finished {
			reachedReady, err := r.conn.drainCurrentResult()
			if err != nil {
				return markUncertain(err)
			}
			if reachedReady {
				r.finished = true
				r.ready = true
				return io.EOF
			}
		}
		if r.resultSets >= maxResultSets {
			r.fail()
			return uncertain(fmt.Errorf("kingbase: response has more than %d result sets", maxResultSets))
		}
		typ, payload, err := r.conn.reader.ReadMessage()
		if err != nil {
			r.fail()
			return uncertain(fmt.Errorf("kingbase: read next result set: %w", err))
		}
		if handled, err := r.conn.handleAsync(typ, payload); handled {
			if err != nil {
				r.fail()
				return uncertain(err)
			}
			continue
		}
		switch typ {
		case wire.MsgRowDescription:
			columns, err := wire.DecodeRowDescription(payload)
			if err != nil {
				r.fail()
				return uncertain(fmt.Errorf("kingbase: malformed RowDescription: %w", err))
			}
			r.resetColumns(columns)
			r.finished = false
			r.resultSets++
			return nil
		case wire.MsgCommandComplete, wire.MsgEmptyQueryResponse:
			r.resetColumns(nil)
			r.finished = true
			r.resultSets++
			return nil
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
		case wire.MsgCopyInResponse, wire.MsgCopyData, wire.MsgCopyDone:
			r.fail()
			return uncertain(fmt.Errorf("kingbase: COPY is not implemented yet"))
		default:
			r.fail()
			return uncertain(fmt.Errorf("kingbase: unexpected message %q between result sets", typ))
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
