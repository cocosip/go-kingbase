package gokb

import (
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
)

const (
	schemaPGCatalog = "pg_catalog"
	// pgTypeInt2 and pgTypeInt8 appear in both the decode and parameter-encode
	// switches; keep those literals single-sourced.
	pgTypeInt2 = "int2"
	pgTypeInt8 = "int8"
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
	case pgTypeInt2, "int4", pgTypeInt8, "oid", "xid", "cid":
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
// opaqueScanType is the scan type of a value returned without a codec: text
// stays a string and binary stays bytes, matching the decoded value.
func opaqueScanType(format int16) reflect.Type {
	if format == 1 {
		return reflect.TypeOf([]byte(nil))
	}
	return reflect.TypeOf("")
}

// scanType reports the Go type the decoder produces for a verified type and
// wire format. Unsupported binary layouts stay opaque bytes, never the text
// scan type.
func scanType(schema, name string, format int16) reflect.Type {
	if schema != schemaPGCatalog {
		return opaqueScanType(format)
	}
	if format == 1 {
		switch name {
		case "bool":
			return reflect.TypeOf(true)
		case pgTypeInt2, "int4", pgTypeInt8, "oid", "xid", "cid":
			return reflect.TypeOf(int64(0))
		case "float4", "float8":
			return reflect.TypeOf(float64(0))
		case "text", "varchar", "bpchar", "char", "name", "unknown", "json", "jsonb", "uuid":
			return reflect.TypeOf("")
		case "bytea":
			return reflect.TypeOf([]byte(nil))
		case "date", "timestamp", "timestamptz":
			return reflect.TypeOf(time.Time{})
		default:
			return opaqueScanType(format)
		}
	}
	switch name {
	case "bool":
		return reflect.TypeOf(true)
	case pgTypeInt2, "int4", pgTypeInt8, "oid", "xid", "cid":
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

// pgEpoch is the origin of PG-wire binary date and timestamp payloads.
var pgEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// binaryValue decodes one binary-format result value for a catalog type. The
// boolean reports whether a codec exists; callers fall back to the raw bytes so
// an unverified binary layout is never misparsed.
func binaryValue(schema, name string, data []byte) (driver.Value, bool) {
	if schema != schemaPGCatalog {
		return nil, false
	}
	switch name {
	case "bool":
		if len(data) == 1 {
			return data[0] != 0, true
		}
	case "int2":
		if len(data) == 2 {
			return int64(int16(binary.BigEndian.Uint16(data))), true
		}
	case "int4", "oid", "xid", "cid":
		if len(data) == 4 {
			return int64(int32(binary.BigEndian.Uint32(data))), true
		}
	case "int8":
		if len(data) == 8 {
			return int64(binary.BigEndian.Uint64(data)), true
		}
	case "float4":
		if len(data) == 4 {
			return float64(math.Float32frombits(binary.BigEndian.Uint32(data))), true
		}
	case "float8":
		if len(data) == 8 {
			return math.Float64frombits(binary.BigEndian.Uint64(data)), true
		}
	case "text", "varchar", "bpchar", "char", "name", "unknown":
		return string(data), true
	case "bytea":
		return append([]byte(nil), data...), true
	case "json":
		return string(data), true
	case "jsonb":
		// A jsonb payload carries a one-byte format version before the text.
		if len(data) > 0 && data[0] == 1 {
			return string(data[1:]), true
		}
	case "date":
		if len(data) == 4 {
			days := int(int32(binary.BigEndian.Uint32(data)))
			return pgEpoch.AddDate(0, 0, days), true
		}
	case "timestamp", "timestamptz":
		if len(data) == 8 {
			micros := int64(binary.BigEndian.Uint64(data))
			return pgEpoch.Add(time.Duration(micros) * time.Microsecond), true
		}
	case "uuid":
		if len(data) == 16 {
			return formatUUID(data), true
		}
	}
	return nil, false
}

// formatUUID renders the 16-byte wire form as the canonical text form.
func formatUUID(data []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:])
}

// catalogIdentity returns the resolved catalog identity for a known OID,
// following validated domain links. The name is honest for diagnostics.
func (c *conn) catalogIdentity(oid uint32) (typeInfo, bool) {
	if c.catalog == nil {
		return typeInfo{}, false
	}
	return c.catalog.resolve(oid)
}

// codecName returns the qualified builtin identity that may select a codec:
// the pg_catalog namespace plus a base shape. Same-named user types and
// non-base entries stay opaque even when their bare names match a builtin.
func (c *conn) codecName(oid uint32) (schema, name string, ok bool) {
	info, found := c.catalogIdentity(oid)
	if !found || info.kind != 'b' || info.schema != schemaPGCatalog || info.name == "" {
		return "", "", false
	}
	return info.schema, info.name, true
}

// partialTypeName returns an unqualified name from a names-only catalog. The
// value is honest for diagnostics but cannot select a decoder or builtin codec.
func (c *conn) partialTypeName(oid uint32) (string, bool) {
	if c.catalog == nil {
		return "", false
	}
	return c.catalog.nameOnly(oid)
}

// typeMetadata returns the resolved catalog entry for a result type, or false
// when the server did not identify it.
func (c *conn) typeMetadata(oid uint32) (typeInfo, bool) {
	if c.catalog == nil {
		return typeInfo{}, false
	}
	return c.catalog.resolve(oid)
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
