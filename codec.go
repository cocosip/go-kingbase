package kingbase

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
	// Common catalog names are single-sourced because decode, encode, metadata
	// and target-binding paths all switch on them.
	pgTypeBool        = "bool"
	pgTypeInt2        = "int2"
	pgTypeInt4        = "int4"
	pgTypeInt8        = "int8"
	pgTypeOid         = "oid"
	pgTypeXid         = "xid"
	pgTypeCid         = "cid"
	pgTypeFloat4      = "float4"
	pgTypeFloat8      = "float8"
	pgTypeText        = "text"
	pgTypeVarchar     = "varchar"
	pgTypeBpchar      = "bpchar"
	pgTypeChar        = "char"
	pgTypeBytea       = "bytea"
	pgTypeDate        = "date"
	pgTypeTimestamp   = "timestamp"
	pgTypeTimestamptz = "timestamptz"
	pgTypeNumeric     = "numeric"
)

var (
	dateLayouts        = []string{"2006-01-02"}
	timestampLayouts   = []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"}
	timestamptzLayouts = []string{
		"2006-01-02 15:04:05.999999999Z07:00:00",
		"2006-01-02 15:04:05Z07:00:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05.999999999Z07",
		"2006-01-02 15:04:05Z07",
	}
)

// textValue decodes one text-format result value for a catalog type. The
// boolean reports whether a codec exists; callers fall back to string for
// unknown types so unknown OIDs never fail a query. A known type whose value
// cannot be represented returns ErrValueConversion.
func textValue(schema, name string, data []byte) (driver.Value, bool, error) {
	if schema != schemaPGCatalog {
		return nil, false, nil
	}
	switch name {
	case pgTypeBool:
		if value, ok := strictBool(string(data)); ok {
			return value, true, nil
		}
		return nil, true, textConversionError(name, data)
	case pgTypeInt2, pgTypeInt4, pgTypeInt8, pgTypeOid, pgTypeXid, pgTypeCid:
		if value, err := strconv.ParseInt(string(data), 10, 64); err == nil {
			return value, true, nil
		}
		return nil, true, textConversionError(name, data)
	case pgTypeFloat4, pgTypeFloat8:
		if value, err := strconv.ParseFloat(string(data), 64); err == nil {
			return value, true, nil
		}
		return nil, true, textConversionError(name, data)
	case pgTypeBytea:
		return decodeBytea(data), true, nil
	case pgTypeDate:
		value, err := parseDate(string(data))
		if err != nil {
			return nil, true, err
		}
		return value, true, nil
	case pgTypeTimestamp:
		value, err := parseTimestamp(string(data))
		if err != nil {
			return nil, true, err
		}
		return value, true, nil
	case pgTypeTimestamptz:
		value, err := parseTimestamptz(string(data))
		if err != nil {
			return nil, true, err
		}
		return value, true, nil
	case pgTypeNumeric, "money", "time", "timetz", "interval", "uuid", "json", "jsonb",
		"xml", "inet", "cidr", "macaddr", "macaddr8", pgTypeText, pgTypeVarchar, pgTypeBpchar,
		pgTypeChar, "name", "unknown":
		return string(data), true, nil
	default:
		return nil, false, nil
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
		case pgTypeBool:
			return reflect.TypeOf(true)
		case pgTypeInt2, pgTypeInt4, pgTypeInt8, pgTypeOid, pgTypeXid, pgTypeCid:
			return reflect.TypeOf(int64(0))
		case pgTypeFloat4, pgTypeFloat8:
			return reflect.TypeOf(float64(0))
		case pgTypeText, pgTypeVarchar, pgTypeBpchar, pgTypeChar, "name", "unknown", "json", "jsonb", "uuid", pgTypeNumeric:
			return reflect.TypeOf("")
		case pgTypeBytea:
			return reflect.TypeOf([]byte(nil))
		case pgTypeDate, pgTypeTimestamp, pgTypeTimestamptz:
			return reflect.TypeOf(time.Time{})
		default:
			return opaqueScanType(format)
		}
	}
	switch name {
	case pgTypeBool:
		return reflect.TypeOf(true)
	case pgTypeInt2, pgTypeInt4, pgTypeInt8, pgTypeOid, pgTypeXid, pgTypeCid:
		return reflect.TypeOf(int64(0))
	case pgTypeFloat4, pgTypeFloat8:
		return reflect.TypeOf(float64(0))
	case pgTypeBytea:
		return reflect.TypeOf([]byte(nil))
	case pgTypeDate, pgTypeTimestamp, pgTypeTimestamptz:
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

// strictBool accepts only the PostgreSQL text boolean spellings. Anything else
// is preserved rather than coerced to false.
func strictBool(value string) (bool, bool) {
	switch value {
	case "t":
		return true, true
	case "f":
		return false, true
	default:
		return false, false
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

func parseDate(value string) (time.Time, error) {
	if parsed, err := time.Parse(dateLayouts[0], value); err == nil && validTemporalYear(parsed) {
		return parsed, nil
	}
	return time.Time{}, textConversionError(pgTypeDate, []byte(value))
}

func parseTimestamp(value string) (time.Time, error) {
	for _, layout := range timestampLayouts {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil && validTemporalYear(parsed) {
			return parsed, nil
		}
	}
	return time.Time{}, textConversionError(pgTypeTimestamp, []byte(value))
}

func parseTimestamptz(value string) (time.Time, error) {
	for _, layout := range timestamptzLayouts {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil && validTemporalYear(parsed) {
			return parsed, nil
		}
	}
	return time.Time{}, textConversionError(pgTypeTimestamptz, []byte(value))
}

// textConversionError keeps diagnostics bounded and identifies the failure as
// a client-side conversion rather than a protocol error.
func textConversionError(name string, data []byte) error {
	const maxDiagnostic = 64
	if len(data) > maxDiagnostic {
		data = data[:maxDiagnostic]
	}
	return fmt.Errorf("%w: cannot decode %s value %q", ErrValueConversion, name, data)
}

// binaryLayoutKnown reports whether the driver implements this builtin's
// binary layout. A false decode for a known layout is malformed input, not an
// unsupported type that may fall back to opaque bytes.
func binaryLayoutKnown(name string) bool {
	switch name {
	case pgTypeBool, pgTypeInt2, pgTypeInt4, pgTypeInt8, pgTypeOid, pgTypeXid, pgTypeCid, pgTypeFloat4, pgTypeFloat8,
		pgTypeText, pgTypeVarchar, pgTypeBpchar, pgTypeChar, "name", "unknown", pgTypeBytea, "json",
		"jsonb", "uuid", pgTypeDate, pgTypeTimestamp, pgTypeTimestamptz, pgTypeNumeric:
		return true
	default:
		return false
	}
}

// validTemporalYear keeps text dates and timestamps inside the year range that
// database/sql callers and time's text forms can round-trip. Zero, BC and
// out-of-range years stay text instead of becoming a misleading instant.
func validTemporalYear(value time.Time) bool {
	return value.Year() >= 1 && value.Year() <= 9999
}

// pgEpoch is the origin of PG-wire binary date and timestamp payloads.
var pgEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// boolBinaryValue accepts only canonical 0/1 bytes.
func boolBinaryValue(data []byte) (driver.Value, bool) {
	if len(data) == 1 && (data[0] == 0 || data[0] == 1) {
		return data[0] == 1, true
	}
	return nil, false
}

// binaryValue decodes one binary-format result value for a catalog type. The
// boolean reports whether a codec exists; callers fall back to the raw bytes so
// an unverified binary layout is never misparsed.
func binaryValue(schema, name string, data []byte) (driver.Value, bool) {
	if schema != schemaPGCatalog {
		return nil, false
	}
	switch name {
	case pgTypeBool:
		return boolBinaryValue(data)
	case pgTypeInt2:
		if len(data) == 2 {
			return int64(int16(binary.BigEndian.Uint16(data))), true
		}
	case pgTypeInt4, pgTypeOid, pgTypeXid, pgTypeCid:
		if len(data) == 4 {
			return int64(int32(binary.BigEndian.Uint32(data))), true
		}
	case pgTypeInt8:
		if len(data) == 8 {
			return int64(binary.BigEndian.Uint64(data)), true
		}
	case pgTypeFloat4:
		if len(data) == 4 {
			return float64(math.Float32frombits(binary.BigEndian.Uint32(data))), true
		}
	case pgTypeFloat8:
		if len(data) == 8 {
			return math.Float64frombits(binary.BigEndian.Uint64(data)), true
		}
	case pgTypeText, pgTypeVarchar, pgTypeBpchar, pgTypeChar, "name", "unknown":
		return string(data), true
	case pgTypeBytea:
		return append([]byte(nil), data...), true
	case "json":
		return string(data), true
	case "jsonb":
		// A jsonb payload carries a one-byte format version before the text.
		if len(data) > 0 && data[0] == 1 {
			return string(data[1:]), true
		}
	case pgTypeDate:
		if len(data) == 4 {
			if moment, ok := dateFromDays(int32(binary.BigEndian.Uint32(data))); ok {
				return moment, true
			}
		}
	case pgTypeTimestamp, pgTypeTimestamptz:
		if len(data) == 8 {
			if moment, ok := timestampFromMicros(int64(binary.BigEndian.Uint64(data))); ok {
				return moment, true
			}
		}
	case pgTypeNumeric:
		return numericValue(data)
	case "uuid":
		if len(data) == 16 {
			return formatUUID(data), true
		}
	}
	return nil, false
}

// maxTemporalDays bounds binary date/timestamp conversion before integer
// widening and rejects PG infinity/sentinel values, which are not instants.
const maxTemporalDays = 4_000_000

const microsPerDay = int64(24 * 60 * 60 * 1_000_000)

func dateFromDays(days int32) (time.Time, bool) {
	if days > maxTemporalDays || days < -maxTemporalDays {
		return time.Time{}, false
	}
	moment := pgEpoch.AddDate(0, 0, int(days))
	if !validTemporalYear(moment) {
		return time.Time{}, false
	}
	return moment, true
}

func timestampFromMicros(micros int64) (time.Time, bool) {
	days := micros / microsPerDay
	remainder := micros % microsPerDay
	if days > maxTemporalDays || days < -maxTemporalDays {
		return time.Time{}, false
	}
	moment := pgEpoch.AddDate(0, 0, int(days)).Add(time.Duration(remainder) * time.Microsecond)
	if !validTemporalYear(moment) {
		return time.Time{}, false
	}
	return moment, true
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
	if c.catalog.ambiguousIdentity(info) {
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
// when the server did not identify it or its shape is ambiguous.
func (c *conn) typeMetadata(oid uint32) (typeInfo, bool) {
	if c.catalog == nil {
		return typeInfo{}, false
	}
	info, found := c.catalog.resolve(oid)
	if !found || c.catalog.ambiguousIdentity(info) {
		return typeInfo{}, false
	}
	return info, true
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
