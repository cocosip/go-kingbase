package kingbase

import (
	"database/sql/driver"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

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
	decoders   []func([]byte) (driver.Value, error)
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
	_ driver.RowsColumnTypeNullable         = (*rows)(nil)
	_ driver.RowsColumnTypeLength           = (*rows)(nil)
	_ driver.RowsColumnTypePrecisionScale   = (*rows)(nil)
)

func newRows(c *conn, columns []wire.ColumnDescription, ready bool) *rows {
	r := &rows{
		conn:       c,
		finished:   ready,
		ready:      ready,
		resultSets: 1,
	}
	r.resetColumns(columns)
	if !ready {
		c.setActiveRows(r)
	}
	return r
}

// resetColumns rebuilds the decode state for one result set.
func (r *rows) resetColumns(columns []wire.ColumnDescription) {
	r.columns = columns
	r.names = make([]string, len(columns))
	r.typeNames = make([]string, len(columns))
	r.decoders = make([]func([]byte) (driver.Value, error), len(columns))
	r.scanTypes = make([]reflect.Type, len(columns))
	for i, column := range columns {
		r.names[i] = column.Name
		// Any known identity names the type honestly; a names-only fallback is
		// honest too but only at the diagnostic level.
		if info, known := r.conn.catalogIdentity(column.TypeOID); known {
			r.typeNames[i] = info.name
		} else if name, ok := r.conn.partialTypeName(column.TypeOID); ok {
			r.typeNames[i] = name
		}
		schema, name, verified := r.conn.codecName(column.TypeOID)
		if !verified {
			// Opaque types still decode: text stays string and binary stays
			// bytes, and no decoder or scan type is selected for them.
			r.scanTypes[i] = opaqueScanType(column.Format)
			r.decoders[i] = unknownTypeDecoder(column.Format)
			continue
		}
		r.scanTypes[i] = scanType(r.conn.mode, schema, name, column.Format)
		r.decoders[i] = typeDecoder(r.conn.mode, schema, name, column.Format, r.conn.sessionLocation())
	}
}

func unknownTypeDecoder(format int16) func([]byte) (driver.Value, error) {
	return func(data []byte) (driver.Value, error) {
		return decodeValue(format, data), nil
	}
}

// typeDecoder builds the per-column decoder for a verified catalog type.
func typeDecoder(mode CompatibilityMode, schema, name string, format int16, location *time.Location) func([]byte) (driver.Value, error) {
	if format == 1 {
		return func(data []byte) (driver.Value, error) {
			if value, ok := binaryValue(mode, schema, name, data); ok {
				return value, nil
			}
			if binaryLayoutKnown(mode, schema, name) {
				return nil, fmt.Errorf("%w: malformed %s binary value", ErrValueConversion, name)
			}
			// Unverified binary layouts (macaddr, xml, mode datetime, ...) stay
			// raw bytes; ponytail: add codecs when their wire layout is proven,
			// and never guess a layout from the type name alone.
			return append([]byte(nil), data...), nil
		}
	}
	if format != 0 {
		return unknownTypeDecoder(format)
	}
	return func(data []byte) (driver.Value, error) {
		if value, ok, err := textValue(mode, schema, name, data, location); ok {
			return value, err
		}
		return string(data), nil
	}
}

// Columns returns the result column names.
func (r *rows) Columns() []string {
	return r.names
}

// ColumnTypeDatabaseTypeName returns the public uppercase catalog/backend type
// name without a length suffix, or empty for a type the server did not
// identify. The exact catalog spelling stays private to the connection.
func (r *rows) ColumnTypeDatabaseTypeName(index int) string {
	return strings.ToUpper(r.typeNames[index])
}

// ColumnTypeScanType returns the Go type produced by Next.
func (r *rows) ColumnTypeScanType(index int) reflect.Type {
	return r.scanTypes[index]
}

// ColumnTypeNullable reports nullability as unknown: RowDescription carries no
// nullable flag, so claiming a definite answer would be dishonest.
func (r *rows) ColumnTypeNullable(_ int) (bool, bool) {
	return true, false
}

// ColumnTypeLength returns the fixed catalog length or the declared character
// length from the column typmod. Character types report characters; unbounded
// and opaque types report unknown.
func (r *rows) ColumnTypeLength(index int) (int64, bool) {
	column := r.columns[index]
	info, known := r.conn.typeMetadata(column.TypeOID)
	if !known || info.kind != 'b' || info.schema != schemaPGCatalog {
		return 0, false
	}
	switch info.name {
	case pgTypeVarchar, pgTypeBpchar, pgTypeChar:
		if column.TypeMod >= 4 {
			return int64(column.TypeMod - 4), true
		}
		return 0, false
	}
	if info.length > 0 {
		return int64(info.length), true
	}
	return 0, false
}

// ColumnTypePrecisionScale returns numeric precision and scale from the typmod.
// PostgreSQL stores scale in an 11-bit signed field, so negative scale values
// are reported exactly instead of being masked into a large positive number.
func (r *rows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	column := r.columns[index]
	info, known := r.conn.typeMetadata(column.TypeOID)
	if !known || info.kind != 'b' || info.schema != schemaPGCatalog || info.name != pgTypeNumeric || column.TypeMod < 4 {
		return 0, 0, false
	}
	typmod := column.TypeMod - 4
	rawScale := typmod & 0x7ff
	scale := int64((rawScale ^ 0x400) - 0x400)
	return int64((typmod >> 16) & 0xffff), scale, true
}

// Next fills dest with the next row and returns io.EOF at the end of the set.
func (r *rows) Next(dest []driver.Value) error {
	if r.closed || r.finished {
		return io.EOF
	}
	if len(dest) != len(r.columns) {
		return fmt.Errorf("kingbase: result row has %d columns, caller provided %d values", len(r.columns), len(dest))
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
				value, err := r.decoders[i](column.Data)
				if err != nil {
					return err
				}
				dest[i] = value
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
			return uncertain(errUnexpectedRowDescription)
		case wire.MsgCopyInResponse, wire.MsgCopyOutResponse, wire.MsgCopyData, wire.MsgCopyDone:
			r.fail()
			return uncertain(errUnexpectedCopyResponse)
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
		case wire.MsgCopyInResponse, wire.MsgCopyOutResponse, wire.MsgCopyData, wire.MsgCopyDone:
			r.fail()
			return uncertain(errUnexpectedCopyResponse)
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
	r.conn.clearActiveRows(r)
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
	r.conn.clearActiveRows(r)
	r.conn.retire()
}
