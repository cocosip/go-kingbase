package gokb

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// Error is a server error response, including Kingbase extensions. Use
// errors.As to inspect it:
//
//	var pgErr *gokb.Error
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
