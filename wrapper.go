package kingbase

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"
)

// These compatibility wrappers mirror the vendor SDK's SQLServer-compatible
// value types. They keep the caller's intent while encoding, instead of
// silently reinterpreting the value as an unrelated target type.

// DateTime1 is a timestamp/datetime value carried as a time.Time.
type DateTime1 time.Time

// VarChar is a variable-length character value.
type VarChar string

// VarCharMax is an unbounded variable-length character value.
type VarCharMax string

// NVarCharMax is an unbounded Unicode character value.
type NVarCharMax string

// NChar is a fixed-length Unicode character value.
type NChar string

var (
	_ driver.Valuer = VarChar("")
	_ driver.Valuer = VarCharMax("")
	_ driver.Valuer = NVarCharMax("")
	_ driver.Valuer = NChar("")
	_ driver.Valuer = DateTime1{}
	_ sql.Scanner   = (*VarChar)(nil)
	_ sql.Scanner   = (*VarCharMax)(nil)
	_ sql.Scanner   = (*NVarCharMax)(nil)
	_ sql.Scanner   = (*NChar)(nil)
	_ sql.Scanner   = (*DateTime1)(nil)
)

// Value implements driver.Valuer for the string wrappers.
func (v VarChar) Value() (driver.Value, error) { return string(v), nil }

// Value implements driver.Valuer for the unbounded string wrapper.
func (v VarCharMax) Value() (driver.Value, error) { return string(v), nil }

// Value implements driver.Valuer for the Unicode string wrapper.
func (v NVarCharMax) Value() (driver.Value, error) { return string(v), nil }

// Value implements driver.Valuer for the fixed Unicode wrapper.
func (v NChar) Value() (driver.Value, error) { return string(v), nil }

// Value implements driver.Valuer for the datetime wrapper.
func (v DateTime1) Value() (driver.Value, error) { return time.Time(v), nil }

// Scan implements sql.Scanner for the string wrappers.
func (v *VarChar) Scan(src any) error {
	text, err := wrapperText(src)
	if err == nil {
		*v = VarChar(text)
	}
	return err
}

// Scan implements sql.Scanner for the unbounded string wrapper.
func (v *VarCharMax) Scan(src any) error {
	text, err := wrapperText(src)
	if err == nil {
		*v = VarCharMax(text)
	}
	return err
}

// Scan implements sql.Scanner for the Unicode string wrapper.
func (v *NVarCharMax) Scan(src any) error {
	text, err := wrapperText(src)
	if err == nil {
		*v = NVarCharMax(text)
	}
	return err
}

// Scan implements sql.Scanner for the fixed Unicode wrapper.
func (v *NChar) Scan(src any) error {
	text, err := wrapperText(src)
	if err == nil {
		*v = NChar(text)
	}
	return err
}

// Scan implements sql.Scanner for the datetime wrapper. The driver's temporal
// codecs produce time.Time for valid values and strings for unsupported forms.
func (v *DateTime1) Scan(src any) error {
	moment, ok := src.(time.Time)
	if !ok {
		return fmt.Errorf("kingbase: cannot scan %T into DateTime1", src)
	}
	*v = DateTime1(moment)
	return nil
}

func wrapperText(src any) (string, error) {
	switch typed := src.(type) {
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	default:
		return "", fmt.Errorf("kingbase: cannot scan %T into a character wrapper", src)
	}
}
