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
	// schemaSys holds the compatibility-mode types the live V009R001C010
	// MySQL and SQLServer listeners expose (tinyint, uint4, datetime, binary
	// and friends). Their codecs are selected by mode, never by bare name.
	schemaSys = "sys"
	// Common catalog names are single-sourced because decode, encode, metadata
	// and target-binding paths all switch on them.
	pgTypeBool        = "bool"
	pgTypeTinyint     = "tinyint"
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
	pgTypeTime        = "time"
	pgTypeTimetz      = "timetz"
	pgTypeInet        = "inet"
	pgTypeCidr        = "cidr"
	pgTypeInterval    = "interval"
	pgTypeXML         = "xml"
	pgTypeBit         = "bit"
	pgTypeVarbit      = "varbit"
	pgTypeJSON        = "json"
	pgTypeJSONB       = "jsonb"
	pgTypeName        = "name"
	pgTypeMoney       = "money"
	pgTypeUUID        = "uuid"
	pgTypeMacaddr     = "macaddr"
	pgTypeMacaddr8    = "macaddr8"
	pgTypeUnknown     = "unknown"
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

// modeCodec identifies the verified decode family of one compatibility-mode
// type. The families come from the live V009R001C010 four-mode run and are
// selected only by mode plus a verified sys base shape.
type modeCodec uint8

const (
	// modeCodecTinyint is a signed 1-byte integer decoded as int64.
	modeCodecTinyint modeCodec = iota
	// modeCodecUTinyint is SQLServer's unsigned 1-byte integer decoded as int64.
	modeCodecUTinyint
	// modeCodecUint4 is MySQL's unsigned 4-byte integer decoded as int64.
	modeCodecUint4
	// modeCodecUint8 is MySQL's unsigned 8-byte integer kept as exact decimal
	// text so the full uint64 range survives.
	modeCodecUint8
	// modeCodecDatetime is a wall-clock datetime decoded as time.Time in the
	// text format only; its binary payload is session-timezone dependent.
	modeCodecDatetime
	// modeCodecBytes is binary/varbinary decoded as []byte.
	modeCodecBytes
	// modeCodecNString is nchar/nvarchar decoded as string.
	modeCodecNString
	// modeCodecBoolBit is SQLServer's boolean bit decoded as bool.
	modeCodecBoolBit
)

// modeTypeSpec pins the required base length and decode family of a sys type.
type modeTypeSpec struct {
	length int16
	codec  modeCodec
}

var modeTypeSpecs = map[CompatibilityMode]map[string]modeTypeSpec{
	ModeMySQL: {
		"tinyint":   {1, modeCodecTinyint},
		"uint4":     {4, modeCodecUint4},
		"uint8":     {8, modeCodecUint8},
		"datetime":  {8, modeCodecDatetime},
		"binary":    {-1, modeCodecBytes},
		"varbinary": {-1, modeCodecBytes},
	},
	ModeSQLServer: {
		"tinyint":       {1, modeCodecUTinyint},
		"datetime":      {8, modeCodecDatetime},
		"smalldatetime": {8, modeCodecDatetime},
		"binary":        {-1, modeCodecBytes},
		"varbinary":     {-1, modeCodecBytes},
		"nchar":         {-1, modeCodecNString},
		"nvarchar":      {-1, modeCodecNString},
		"bit":           {1, modeCodecBoolBit},
	},
}

// modeTypeSpecFor returns the verified spec for a sys type in this mode.
func modeTypeSpecFor(mode CompatibilityMode, schema, name string) (modeTypeSpec, bool) {
	if schema != schemaSys {
		return modeTypeSpec{}, false
	}
	specs, ok := modeTypeSpecs[mode]
	if !ok {
		return modeTypeSpec{}, false
	}
	spec, ok := specs[name]
	return spec, ok
}

// textValue decodes one text-format result value for a verified catalog type.
// The boolean reports whether a codec exists; callers fall back to string for
// unknown types so unknown OIDs never fail a query. A known type whose value
// cannot be represented returns ErrValueConversion.
func textValue(mode CompatibilityMode, schema, name string, data []byte) (driver.Value, bool, error) {
	if schema == schemaSys {
		spec, ok := modeTypeSpecFor(mode, schema, name)
		if !ok {
			return nil, false, nil
		}
		value, err := modeTextValue(spec.codec, name, data)
		return value, true, err
	}
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
	case pgTypeTinyint:
		if value, ok := parseSignedText(data, math.MinInt8, math.MaxInt8); ok {
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
	case pgTypeNumeric, pgTypeMoney, pgTypeTime, pgTypeTimetz, pgTypeInterval, pgTypeUUID, pgTypeJSON, pgTypeJSONB,
		pgTypeXML, pgTypeInet, pgTypeCidr, pgTypeMacaddr, pgTypeMacaddr8, pgTypeText, pgTypeVarchar, pgTypeBpchar,
		pgTypeChar, pgTypeName, pgTypeUnknown, pgTypeBit, pgTypeVarbit:
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

// modeTextValue decodes a verified sys-type text value. A malformed value is a
// typed conversion error, never a silent zero or a fabricated instant.
func modeTextValue(codec modeCodec, name string, data []byte) (driver.Value, error) {
	switch codec {
	case modeCodecTinyint:
		if value, ok := parseSignedText(data, math.MinInt8, math.MaxInt8); ok {
			return value, nil
		}
	case modeCodecUTinyint:
		if value, ok := parseSignedText(data, 0, math.MaxUint8); ok {
			return value, nil
		}
	case modeCodecUint4:
		if value, ok := parseSignedText(data, 0, math.MaxUint32); ok {
			return value, nil
		}
	case modeCodecUint8:
		if _, err := strconv.ParseUint(string(data), 10, 64); err == nil {
			return string(data), nil
		}
	case modeCodecDatetime:
		if value, err := parseTimestamp(string(data)); err == nil {
			return value, nil
		}
	case modeCodecBytes:
		if len(data) >= 2 && data[0] == '0' && (data[1] == 'x' || data[1] == 'X') {
			decoded := make([]byte, hex.DecodedLen(len(data)-2))
			if _, err := hex.Decode(decoded, data[2:]); err == nil {
				return decoded, nil
			}
		}
	case modeCodecNString:
		return string(data), nil
	case modeCodecBoolBit:
		if flag, ok := modeBitBool(string(data)); ok {
			return flag, nil
		}
	}
	return nil, textConversionError(name, data)
}

// modeBinaryValue decodes a verified sys-type binary payload. The datetime
// layout is deliberately absent: the listeners encode a wall-clock datetime as
// a session-timezone instant, so it stays raw bytes.
func modeBinaryValue(codec modeCodec, data []byte) (driver.Value, bool) {
	switch codec {
	case modeCodecTinyint:
		if len(data) == 1 {
			return int64(int8(data[0])), true
		}
	case modeCodecUTinyint:
		if len(data) == 1 {
			return int64(data[0]), true
		}
	case modeCodecUint4:
		if len(data) == 4 {
			return int64(binary.BigEndian.Uint32(data)), true
		}
	case modeCodecUint8:
		if len(data) == 8 {
			return strconv.FormatUint(binary.BigEndian.Uint64(data), 10), true
		}
	case modeCodecBytes:
		return append([]byte(nil), data...), true
	case modeCodecNString:
		return string(data), true
	case modeCodecBoolBit:
		return boolBinaryValue(data)
	}
	return nil, false
}

// modeScanType is the Go type modeTextValue/modeBinaryValue produce.
func modeScanType(codec modeCodec, format int16) reflect.Type {
	switch codec {
	case modeCodecTinyint, modeCodecUTinyint, modeCodecUint4:
		return reflect.TypeOf(int64(0))
	case modeCodecUint8, modeCodecNString:
		return reflect.TypeOf("")
	case modeCodecBytes:
		return reflect.TypeOf([]byte(nil))
	case modeCodecBoolBit:
		return reflect.TypeOf(true)
	case modeCodecDatetime:
		if format == 1 {
			return reflect.TypeOf([]byte(nil))
		}
		return reflect.TypeOf(time.Time{})
	default:
		return opaqueScanType(format)
	}
}

// parseSignedText parses a strict decimal integer within [low, high].
func parseSignedText(data []byte, low, high int64) (int64, bool) {
	value, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil || value < low || value > high {
		return 0, false
	}
	return value, true
}

// modeBitBool accepts the SQLServer bit text spelling only.
func modeBitBool(value string) (bool, bool) {
	switch value {
	case "0":
		return false, true
	case "1":
		return true, true
	default:
		return false, false
	}
}

// scanType reports the Go type the decoder produces for a verified type and
// wire format. Unsupported binary layouts stay opaque bytes, never the text
// scan type.
func scanType(mode CompatibilityMode, schema, name string, format int16) reflect.Type {
	if schema == schemaSys {
		spec, ok := modeTypeSpecFor(mode, schema, name)
		if !ok {
			return opaqueScanType(format)
		}
		return modeScanType(spec.codec, format)
	}
	if schema != schemaPGCatalog {
		return opaqueScanType(format)
	}
	if format == 1 {
		switch name {
		case pgTypeBool:
			return reflect.TypeOf(true)
		case pgTypeInt2, pgTypeInt4, pgTypeInt8, pgTypeOid, pgTypeXid, pgTypeCid, pgTypeTinyint:
			return reflect.TypeOf(int64(0))
		case pgTypeFloat4, pgTypeFloat8:
			return reflect.TypeOf(float64(0))
		case pgTypeText, pgTypeVarchar, pgTypeBpchar, pgTypeChar, pgTypeName, pgTypeUnknown, pgTypeJSON, pgTypeJSONB, pgTypeUUID, pgTypeNumeric,
			pgTypeTime, pgTypeTimetz, pgTypeInet, pgTypeCidr, pgTypeInterval, pgTypeXML, pgTypeBit, pgTypeVarbit:
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
	case pgTypeInt2, pgTypeInt4, pgTypeInt8, pgTypeOid, pgTypeXid, pgTypeCid, pgTypeTinyint:
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
// unsupported type that may fall back to opaque bytes. The compatibility-mode
// datetime binary payload is session-timezone dependent (the live V009R001C010
// listeners encode the wall-clock text as a UTC instant), so its layout stays
// unverified and falls back to raw bytes.
func binaryLayoutKnown(mode CompatibilityMode, schema, name string) bool {
	if schema == schemaSys {
		spec, ok := modeTypeSpecFor(mode, schema, name)
		return ok && spec.codec != modeCodecDatetime
	}
	if schema != schemaPGCatalog {
		return false
	}
	switch name {
	case pgTypeBool, pgTypeInt2, pgTypeInt4, pgTypeInt8, pgTypeTinyint, pgTypeOid, pgTypeXid, pgTypeCid, pgTypeFloat4,
		pgTypeFloat8, pgTypeText, pgTypeVarchar, pgTypeBpchar, pgTypeChar, pgTypeName, pgTypeUnknown, pgTypeBytea,
		pgTypeJSON, pgTypeJSONB, pgTypeUUID, pgTypeDate, pgTypeTimestamp, pgTypeTimestamptz, pgTypeNumeric, pgTypeTime,
		pgTypeTimetz, pgTypeInet, pgTypeCidr, pgTypeInterval, pgTypeXML, pgTypeBit, pgTypeVarbit:
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

func uuidValue(data []byte) (driver.Value, bool) {
	if len(data) == 16 {
		return formatUUID(data), true
	}
	return nil, false
}

// textBinaryValue dispatches binary layouts whose driver value is text.
func textBinaryValue(name string, data []byte) (driver.Value, bool) {
	switch name {
	case pgTypeTime, pgTypeTimetz:
		return timeOrTimetzValue(name, data)
	case pgTypeInet, pgTypeCidr:
		return inetValue(name, data)
	case pgTypeInterval:
		return intervalValue(data)
	case pgTypeBit, pgTypeVarbit:
		return bitBinaryValue(data)
	default:
		return nil, false
	}
}

// binaryValue decodes one binary-format result value for a catalog type. The
// boolean reports whether a codec exists; callers fall back to the raw bytes so
// an unverified binary layout is never misparsed.
func binaryValue(mode CompatibilityMode, schema, name string, data []byte) (driver.Value, bool) {
	if schema == schemaSys {
		spec, ok := modeTypeSpecFor(mode, schema, name)
		if !ok {
			return nil, false
		}
		return modeBinaryValue(spec.codec, data)
	}
	if schema != schemaPGCatalog {
		return nil, false
	}
	return binaryPGValue(name, data)
}

// binaryPGValue decodes a pg_catalog binary layout. The compatibility-mode
// layouts live in modeBinaryValue.
func binaryPGValue(name string, data []byte) (driver.Value, bool) {
	switch name {
	case pgTypeBool:
		return boolBinaryValue(data)
	case pgTypeTinyint:
		if len(data) == 1 {
			return int64(int8(data[0])), true
		}
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
	case pgTypeText, pgTypeVarchar, pgTypeBpchar, pgTypeChar, pgTypeName, pgTypeUnknown:
		return string(data), true
	case pgTypeBytea:
		return append([]byte(nil), data...), true
	case pgTypeJSON, pgTypeXML:
		return string(data), true
	case pgTypeJSONB:
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
	case pgTypeTime, pgTypeTimetz, pgTypeInet, pgTypeCidr, pgTypeInterval, pgTypeBit, pgTypeVarbit:
		return textBinaryValue(name, data)
	case pgTypeUUID:
		return uuidValue(data)
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

// builtinShapes pins the qualified PG-catalog base shape required before a
// codec may be selected. A same-named type, domain base or malformed catalog
// entry with a different kind/length stays opaque instead of decoding with a
// layout that was never verified for it. Lengths come from the live V9
// V009R001C010 catalog capture and the standard pg_catalog definitions.
var builtinShapes = map[string]struct {
	kind   byte
	length int16
}{
	"bool":         {'b', 1},
	pgTypeTinyint:  {'b', 1},
	"bytea":        {'b', -1},
	"char":         {'b', 1},
	pgTypeName:     {'b', 64},
	"int2":         {'b', 2},
	"int4":         {'b', 4},
	"int8":         {'b', 8},
	"oid":          {'b', 4},
	"xid":          {'b', 4},
	"cid":          {'b', 4},
	"text":         {'b', -1},
	pgTypeJSON:     {'b', -1},
	pgTypeJSONB:    {'b', -1},
	"float4":       {'b', 4},
	"float8":       {'b', 8},
	pgTypeMoney:    {'b', 8},
	"bpchar":       {'b', -1},
	"varchar":      {'b', -1},
	"date":         {'b', 4},
	"time":         {'b', 8},
	"timetz":       {'b', 12},
	"timestamp":    {'b', 8},
	"timestamptz":  {'b', 8},
	"interval":     {'b', 16},
	"bit":          {'b', -1},
	"varbit":       {'b', -1},
	"numeric":      {'b', -1},
	pgTypeUUID:     {'b', 16},
	"xml":          {'b', -1},
	"inet":         {'b', -1},
	"cidr":         {'b', -1},
	pgTypeMacaddr:  {'b', 6},
	pgTypeMacaddr8: {'b', 8},
}

// validBuiltinShape reports whether the catalog entry has the required base
// kind and length for its qualified builtin name.
func validBuiltinShape(info typeInfo) bool {
	shape, ok := builtinShapes[info.name]
	return ok && info.kind == shape.kind && info.length == shape.length
}

// codecName returns the qualified identity that may select a codec: the
// pg_catalog namespace plus a verified base shape, or a sys identity verified
// for this compatibility mode. Same-named user types, non-base entries and
// mismatched lengths stay opaque even when their bare names match a builtin.
func (c *conn) codecName(oid uint32) (schema, name string, ok bool) {
	info, found := c.catalogIdentity(oid)
	if !found || info.kind != 'b' || info.name == "" {
		return "", "", false
	}
	if c.catalog.ambiguousIdentity(info) {
		return "", "", false
	}
	if info.schema != schemaPGCatalog {
		spec, verified := modeTypeSpecFor(c.mode, info.schema, info.name)
		if !verified || info.length != spec.length {
			return "", "", false
		}
		return info.schema, info.name, true
	}
	if !validBuiltinShape(info) {
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
