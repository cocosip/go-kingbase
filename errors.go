package kingbase

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// ErrLastInsertIDUnavailable reports that LastInsertId has no captured value:
// automatic capture was disabled, the statement was not an eligible INSERT, or
// the resolved insert did not return exactly one usable generated ID.
var ErrLastInsertIDUnavailable = errors.New("kingbase: LastInsertId is unavailable")

// ErrLastInsertIDOverflow reports a captured generated ID outside int64.
var ErrLastInsertIDOverflow = errors.New("kingbase: LastInsertId overflows int64")

// ErrValueConversion reports that a result value with a known type cannot be
// represented by the driver's standard Go value. It is a client-side
// conversion error, not a server or protocol error, and leaves the connection
// usable after the rows are closed/drained.
var ErrValueConversion = errors.New("kingbase: unsupported result value")

// Error is a server error response, including Kingbase extensions. Use
// errors.As to inspect it:
//
//	var pgErr *kingbase.Error
//	if errors.As(err, &pgErr) { ... pgErr.Code ... }
type Error struct {
	// Severity is the localized severity from field 'S'.
	Severity string
	// SeverityNonLocal is the canonical severity from field 'V', when sent.
	SeverityNonLocal string
	Code             string
	Message          string
	Detail           string
	Hint             string
	Position         int
	InternalPosition int
	InternalQuery    string
	Where            string
	SchemaName       string
	TableName        string
	ColumnName       string
	DataTypeName     string
	ConstraintName   string
	File             string
	Line             string
	Routine          string
	// Extra holds extension fields keyed by their raw protocol code,
	// including unparseable positions.
	Extra map[byte]string
}

// UncertainError reports a failure after a command may have reached the
// server. The connection is retired and the statement must not be retried
// automatically, because it may have executed. database/sql never sees
// driver.ErrBadConn for these failures.
type UncertainError struct {
	Err error
}

// Error implements error.
func (e *UncertainError) Error() string {
	return "kingbase: execution state uncertain: " + e.Err.Error()
}

// Unwrap exposes the underlying cause for errors.Is/errors.As.
func (e *UncertainError) Unwrap() error {
	return e.Err
}

// uncertain wraps a post-send failure exactly once.
func uncertain(err error) error {
	if err == nil {
		return nil
	}
	var already *UncertainError
	if errors.As(err, &already) {
		return err
	}
	return &UncertainError{Err: err}
}

// Error renders the severity, message and SQLSTATE. Field values other than
// message and code are available on the struct.
func (e *Error) Error() string {
	var b strings.Builder
	if e.Severity != "" {
		b.WriteString(e.Severity)
		b.WriteString(": ")
	}
	if e.Message != "" {
		b.WriteString(e.Message)
	} else {
		b.WriteString("server error")
	}
	if e.Code != "" {
		b.WriteString(" (SQLSTATE ")
		b.WriteString(e.Code)
		b.WriteByte(')')
	}
	return b.String()
}

// serverError decodes an ErrorResponse payload.
func serverError(payload []byte) error {
	fields, err := wire.DecodeErrorFields(payload)
	if err != nil {
		return fmt.Errorf("kingbase: malformed error response: %w", err)
	}
	return newServerError(fields)
}

func newServerError(fields []wire.ErrorField) *Error {
	e := &Error{}
	for _, field := range fields {
		switch field.Code {
		case wire.ErrorSeverity:
			e.Severity = field.Value
		case wire.ErrorSeverityNonLocal:
			e.SeverityNonLocal = field.Value
		case wire.ErrorCode:
			e.Code = field.Value
		case wire.ErrorMessage:
			e.Message = field.Value
		case wire.ErrorDetail:
			e.Detail = field.Value
		case wire.ErrorHint:
			e.Hint = field.Value
		case wire.ErrorPosition:
			if position, ok := atoiField(field.Value); ok {
				e.Position = position
			} else {
				e.addExtra(field)
			}
		case wire.ErrorInternalPosition:
			if position, ok := atoiField(field.Value); ok {
				e.InternalPosition = position
			} else {
				e.addExtra(field)
			}
		case wire.ErrorInternalQuery:
			e.InternalQuery = field.Value
		case wire.ErrorWhere:
			e.Where = field.Value
		case wire.ErrorSchemaName:
			e.SchemaName = field.Value
		case wire.ErrorTableName:
			e.TableName = field.Value
		case wire.ErrorColumnName:
			e.ColumnName = field.Value
		case wire.ErrorDataTypeName:
			e.DataTypeName = field.Value
		case wire.ErrorConstraintName:
			e.ConstraintName = field.Value
		case wire.ErrorFile:
			e.File = field.Value
		case wire.ErrorLine:
			e.Line = field.Value
		case wire.ErrorRoutine:
			e.Routine = field.Value
		default:
			e.addExtra(field)
		}
	}
	return e
}

func (e *Error) addExtra(field wire.ErrorField) {
	if e.Extra == nil {
		e.Extra = make(map[byte]string)
	}
	e.Extra[field.Code] = field.Value
}

func atoiField(value string) (int, bool) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return n, true
}
