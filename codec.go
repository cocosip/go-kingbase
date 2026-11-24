package gokb

import (
	"database/sql/driver"
	"encoding/hex"
	"reflect"
	"strconv"
	"time"
)

const (
	schemaPGCatalog = "pg_catalog"
)

var (
	dateLayouts        = []string{"2006-01-02"}
	timestampLayouts   = []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"}
	timestamptzLayouts = []string{
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05.999999999Z07",
		"2006-01-02 15:04:05Z07",
	}
)

// textValue decodes one text-format result value for a catalog type. The
// boolean reports whether a codec exists; callers fall back to string for
// unknown types so unknown OIDs never fail a query.
func textValue(schema, name string, data []byte) (driver.Value, bool) {
	if schema != schemaPGCatalog {
		return nil, false
	}
	switch name {
	case "bool":
		return parseBool(string(data)), true
	case "int2", "int4", "int8", "oid", "xid", "cid":
		if value, err := strconv.ParseInt(string(data), 10, 64); err == nil {
			return value, true
		}
		return string(data), true
	case "float4", "float8":
		if value, err := strconv.ParseFloat(string(data), 64); err == nil {
			return value, true
		}
		return string(data), true
	case "bytea":
		return decodeBytea(data), true
	case "date":
		return parseDate(string(data)), true
	case "timestamp":
		return parseTimestamp(string(data)), true
	case "timestamptz":
		return parseTimestamptz(string(data)), true
	case "numeric", "money", "time", "timetz", "interval", "uuid", "json", "jsonb",
		"xml", "inet", "cidr", "macaddr", "macaddr8", "text", "varchar", "bpchar",
		"char", "name", "unknown":
		return string(data), true
	default:
		return nil, false
	}
}

// scanType reports the Go type a decoded value has, for Rows.ScanType.
func scanType(schema, name string) reflect.Type {
	if schema != schemaPGCatalog {
		return reflect.TypeOf("")
	}
	switch name {
	case "bool":
		return reflect.TypeOf(true)
	case "int2", "int4", "int8", "oid", "xid", "cid":
		return reflect.TypeOf(int64(0))
	case "float4", "float8":
		return reflect.TypeOf(float64(0))
	case "bytea":
		return reflect.TypeOf([]byte(nil))
	case "date", "timestamp", "timestamptz":
		return reflect.TypeOf(time.Time{})
	default:
		return reflect.TypeOf("")
	}
}

func parseBool(value string) bool {
	switch value {
	case "t", "true", "y", "yes", "1":
		return true
	default:
		return false
	}
}

func decodeBytea(data []byte) []byte {
	if len(data) >= 2 && data[0] == '\\' && data[1] == 'x' {
		decoded := make([]byte, hex.DecodedLen(len(data)-2))
		if _, err := hex.Decode(decoded, data[2:]); err == nil {
			return decoded
		}
	}
	// The legacy escape format is returned verbatim rather than mangled.
	return data
}

func parseDate(value string) driver.Value {
	if parsed, err := time.Parse(dateLayouts[0], value); err == nil {
		return parsed
	}
	return value
}

func parseTimestamp(value string) driver.Value {
	for _, layout := range timestampLayouts {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed
		}
	}
	return value
}

func parseTimestamptz(value string) driver.Value {
	for _, layout := range timestamptzLayouts {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed
		}
	}
	return value
}

// codecName returns the qualified catalog name for a type OID, or empty when
// the type is not in the catalog.
func (c *conn) codecName(oid uint32) (schema, name string, ok bool) {
	if c.catalog == nil {
		return "", "", false
	}
	return c.catalog.lookup(oid)
}

// typeLabels is the catalog view used by tests and diagnostics.
func (c *conn) typeLabels() map[uint32]string {
	labels := make(map[uint32]string)
	if c.catalog == nil {
		return labels
	}
	for oid, info := range c.catalog.byOID {
		labels[oid] = info.schema + "." + info.name
	}
	return labels
}
