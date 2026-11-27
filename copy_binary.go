package kingbase

import (
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// copyBinaryMarker tags a typed binary statement. Both raw and typed import
// speak 'FORMAT binary', so the statement is the only channel that carries the
// caller's intent; the server ignores the trailing comment.
const copyBinaryMarker = "/* kingbase:binary-rows */"

// copyBinaryHeader is the file header the vendor writes and accepts: its own
// signature, then a zero flags word and a zero header-extension length.
var copyBinaryHeader = func() []byte {
	header := make([]byte, 0, 19)
	header = append(header, "KBCOPY\n\xff\r\n\x00"...)
	header = append(header, 0, 0, 0, 0)
	header = append(header, 0, 0, 0, 0)
	return header
}()

// copyBinaryEpoch is the zero instant of the binary date and timestamp formats.
var copyBinaryEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// copyKind is the binary representation the server stores for one column.
type copyKind uint8

const (
	copyInt2 copyKind = iota + 1
	copyInt4
	copyInt8
	copyFloat4
	copyFloat8
	copyBool
	copyText
	copyBytea
	copyDateDays
	copyDateMicros
	copyTimestamp
	copyTimestamptz
	copyTime
	copyUUID
	copyJSON
	copyJSONB
	copyInt1
	copyUint1
	copyUint4
	copyUint8
	copyZonedTimestamp
	copyTimeTicks
)

// String names the storage type in a column error.
func (k copyKind) String() string {
	switch k {
	case copyInt2:
		return "smallint"
	case copyInt4:
		return "integer"
	case copyInt8:
		return "bigint"
	case copyFloat4:
		return "real"
	case copyFloat8:
		return "double precision"
	case copyBool:
		return "boolean"
	case copyText:
		return "text"
	case copyBytea:
		return "bytea"
	case copyDateDays:
		return "date"
	case copyDateMicros:
		return "date with time"
	case copyTimestamp:
		return "timestamp"
	case copyTimestamptz:
		return "timestamp with time zone"
	case copyTime:
		return "time"
	case copyUUID:
		return "uuid"
	case copyJSON:
		return "json"
	case copyJSONB:
		return "jsonb"
	case copyInt1:
		return "tinyint"
	case copyUint1:
		return "unsigned tinyint"
	case copyUint4:
		return "unsigned integer"
	case copyUint8:
		return "unsigned bigint"
	case copyZonedTimestamp:
		return "timestamp"
	case copyTimeTicks:
		return "time"
	}
	return "unknown"
}

// CopyInBinary returns a 'COPY ... FROM STDIN WITH (FORMAT binary)' statement
// for Tx.Prepare. The driver reads the target columns from the server catalog,
// so every Exec argument is one row encoded in the representation that server
// actually stores: a mode that maps REAL to a double or DATE to an eight-byte
// value is encoded correctly instead of by a caller guess. A column type with no
// encoder, a value that does not fit, or a wrong argument count aborts before
// that row is imported. A zero-argument Exec finishes the stream. Use
// CopyInRawBinary to forward an already encoded payload instead.
func CopyInBinary(table string, columns ...string) string {
	names := make([]string, len(columns))
	for i, column := range columns {
		names[i] = QuoteIdentifier(column)
	}
	return "COPY " + QuoteIdentifier(table) + " (" + strings.Join(names, ", ") +
		") FROM STDIN WITH (FORMAT binary) " + copyBinaryMarker
}

// copyKindForType maps one catalog type to its binary encoder. Selection is
// qualified: only a pg_catalog or sys identity selects a layout, so a
// same-named user type never borrows a builtin encoding. Fixed-width storage
// kinds also check the catalog length, the mode decides the tinyint sign, and
// an unverified layout is unsupported instead of guessed.
func copyKindForType(mode CompatibilityMode, schema, typname string, typlen int64) (copyKind, bool) {
	switch schema {
	case schemaPGCatalog:
		return pgCatalogCopyKind(typname, typlen)
	case schemaSys:
		return sysCopyKind(mode, typname, typlen)
	}
	return 0, false
}

func pgCatalogCopyKind(typname string, typlen int64) (copyKind, bool) {
	switch typname {
	case "int2", "smallint":
		return copyInt2, true
	case "int4", "integer":
		return copyInt4, true
	case "int8", "bigint":
		return copyInt8, true
	case "oid":
		return copyUint4, true
	case "float4", "real":
		return copyFloat4, true
	case "float8", "double", "double precision", "float":
		return copyFloat8, true
	case "bool", "boolean":
		return copyBool, true
	case "text", "varchar", "bpchar", "char", "name":
		return copyText, true
	case "bytea":
		return copyBytea, true
	case "date":
		if typlen == 8 {
			return copyDateMicros, true
		}
		return copyDateDays, true
	case "timestamp":
		return copyTimestamp, true
	case "timestamptz":
		return copyTimestamptz, true
	case "time":
		return copyTime, true
	case "uuid":
		return copyUUID, true
	case "json":
		return copyJSON, true
	case "jsonb":
		return copyJSONB, true
	case "tinyint":
		if typlen == 1 {
			return copyInt1, true
		}
	}
	return 0, false
}

func sysCopyKind(mode CompatibilityMode, typname string, typlen int64) (copyKind, bool) {
	switch typname {
	case "tinyint":
		if typlen != 1 {
			return 0, false
		}
		// The SQL Server-compatible tinyint is unsigned; Oracle/MySQL tinyint
		// is signed, as the result codecs also treat it.
		if mode == ModeSQLServer {
			return copyUint1, true
		}
		return copyInt1, true
	case "uint1":
		if typlen == 1 {
			return copyUint1, true
		}
	case "uint4":
		if typlen == 4 {
			return copyUint4, true
		}
	case "uint8":
		if typlen == 8 {
			return copyUint8, true
		}
	case "bit":
		if typlen == 1 {
			return copyBool, true
		}
	case "binary", "varbinary":
		return copyBytea, true
	case "nchar", "nvarchar":
		return copyText, true
	case "timestamp":
		if typlen == 8 {
			// The mode-specific timestamp keeps wall-clock text but stores an
			// instant, so the caller's fields are read in the session zone.
			return copyZonedTimestamp, true
		}
	case "datetime":
		if typlen == 8 {
			// The mode datetime keeps a plain wall clock in both formats.
			return copyTimestamp, true
		}
	case "timestamptz":
		if typlen == 8 {
			return copyTimestamptz, true
		}
	case "time":
		if typlen == 8 {
			// The SQL Server-compatible time counts 100 ns ticks; the other
			// modes count microseconds.
			if mode == ModeSQLServer {
				return copyTimeTicks, true
			}
			return copyTime, true
		}
	case "date":
		if typlen == 8 {
			return copyDateMicros, true
		}
		return copyDateDays, true
	}
	return 0, false
}

// copyBinaryStatement reads the relation and the column list out of a marked
// statement. The marker is required, so an ordinary COPY statement never lands
// in typed mode by accident.
func copyBinaryStatement(query string) (string, []string, bool, error) {
	if !strings.Contains(query, copyBinaryMarker) {
		return "", nil, false, nil
	}
	tokens, err := tokenizeSQL(query)
	if err != nil {
		return "", nil, true, fmt.Errorf("kingbase: parse binary COPY statement: %w", err)
	}
	index := 0
	word := func() (string, bool) {
		if index >= len(tokens) || tokens[index].kind != sqlTokenWord {
			return "", false
		}
		value := tokens[index].text
		index++
		return value, true
	}
	if keyword, ok := word(); !ok || !strings.EqualFold(keyword, "copy") {
		return "", nil, true, errors.New("kingbase: typed binary COPY requires a COPY ... FROM STDIN statement")
	}
	relation, next, ok := copyRelationName(tokens, index)
	if !ok {
		return "", nil, true, errors.New("kingbase: typed binary COPY has no target relation")
	}
	index = next
	if index >= len(tokens) || tokens[index].kind != sqlTokenPunctuation || tokens[index].text != "(" {
		return "", nil, true, errors.New("kingbase: typed binary COPY must name its columns")
	}
	index++
	var columns []string
	for {
		if index >= len(tokens) {
			return "", nil, true, errors.New("kingbase: typed binary COPY column list is not closed")
		}
		token := tokens[index]
		index++
		if token.kind == sqlTokenPunctuation {
			if token.text == ")" {
				break
			}
			if token.text != "," {
				return "", nil, true, errors.New("kingbase: typed binary COPY column list is malformed")
			}
			continue
		}
		if token.kind != sqlTokenWord && token.kind != sqlTokenQuotedIdentifier {
			return "", nil, true, errors.New("kingbase: typed binary COPY column list is malformed")
		}
		columns = append(columns, token.text)
	}
	if len(columns) == 0 {
		return "", nil, true, errors.New("kingbase: typed binary COPY must name its columns")
	}
	if keyword, ok := word(); !ok || !strings.EqualFold(keyword, "from") {
		return "", nil, true, errors.New("kingbase: typed binary COPY must read FROM STDIN")
	}
	if keyword, ok := word(); !ok || !strings.EqualFold(keyword, "stdin") {
		return "", nil, true, errors.New("kingbase: typed binary COPY must read FROM STDIN")
	}
	for index < len(tokens) {
		token := tokens[index]
		index++
		if token.kind != sqlTokenWord || !strings.EqualFold(token.text, "format") {
			continue
		}
		if value, ok := word(); ok && strings.EqualFold(value, "binary") {
			return relation, columns, true, nil
		}
		return "", nil, true, errors.New("kingbase: typed binary COPY requires FORMAT binary")
	}
	return "", nil, true, errors.New("kingbase: typed binary COPY requires FORMAT binary")
}

// copyRelationName reads a one or two part relation name and returns the index
// after it, so the caller keeps its place in the token stream.
func copyRelationName(tokens []sqlToken, index int) (string, int, bool) {
	if index >= len(tokens) {
		return "", index, false
	}
	first := tokens[index]
	if first.kind != sqlTokenWord && first.kind != sqlTokenQuotedIdentifier {
		return "", index, false
	}
	parts := []string{first.text}
	index++
	if index+1 < len(tokens) && tokens[index].kind == sqlTokenPunctuation && tokens[index].text == "." {
		second := tokens[index+1]
		if second.kind != sqlTokenWord && second.kind != sqlTokenQuotedIdentifier {
			return "", index, false
		}
		parts = append(parts, second.text)
		index += 2
	}
	return quoteRelation(parts), index, true
}

// quoteRelation renders a one or two part relation name for to_regclass.
func quoteRelation(parts []string) string {
	quoted := make([]string, len(parts))
	for i, part := range parts {
		quoted[i] = QuoteIdentifier(part)
	}
	return strings.Join(quoted, ".")
}

// copyBinaryColumnQuery lists the storage types of the requested relation. The
// type's schema is part of the identity, so a user type that shares a builtin
// name is never encoded with the builtin layout. A domain over a base type
// borrows its base storage (the MySQL-mode json is a domain over jsonb); a
// domain over another domain keeps its own identity and stays unsupported.
func copyBinaryColumnQuery(relation string) string {
	return `SELECT a.attname,
       COALESCE(bt.typname, t.typname) AS typname,
       COALESCE(bt.typlen, t.typlen) AS typlen,
       COALESCE(bn.nspname, n.nspname) AS nspname
FROM pg_catalog.pg_attribute AS a
JOIN pg_catalog.pg_type AS t ON t.oid = a.atttypid
JOIN pg_catalog.pg_namespace AS n ON n.oid = t.typnamespace
LEFT JOIN pg_catalog.pg_type AS bt ON bt.oid = t.typbasetype AND bt.typtype = 'b'
LEFT JOIN pg_catalog.pg_namespace AS bn ON bn.oid = bt.typnamespace
WHERE a.attrelid = pg_catalog.to_regclass(` + quoteSQLLiteral(relation) + `)
  AND a.attnum > 0
  AND NOT a.attisdropped
ORDER BY a.attnum`
}

// appendCopyBinaryRow encodes one row: its field count, then every field. The
// session location resolves wall-clock values of a mode type that stores an
// instant.
func appendCopyBinaryRow(dst []byte, kinds []copyKind, args []driver.NamedValue, location *time.Location) ([]byte, error) {
	if len(args) != len(kinds) {
		return nil, fmt.Errorf("kingbase: binary COPY row has %d values for %d columns", len(args), len(kinds))
	}
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(kinds)))
	for i, arg := range args {
		next, err := appendCopyBinaryField(dst, kinds[i], arg.Value, location)
		if err != nil {
			return nil, fmt.Errorf("kingbase: column %d (%s) %w", i+1, kinds[i], err)
		}
		dst = next
	}
	return dst, nil
}

// appendCopyBinaryField appends one length-prefixed field, or the -1 length that
// means NULL.
func appendCopyBinaryField(dst []byte, kind copyKind, value any, location *time.Location) ([]byte, error) {
	if value == nil {
		return binary.BigEndian.AppendUint32(dst, math.MaxUint32), nil
	}
	encoded, err := encodeCopyBinaryValueAt(kind, value, location)
	if err != nil {
		return nil, err
	}
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(encoded)))
	return append(dst, encoded...), nil
}

func encodeCopyBinaryValue(kind copyKind, value any) ([]byte, error) {
	return encodeCopyBinaryValueAt(kind, value, nil)
}

// encodeCopyBinaryValueAt encodes one field; a mode timestamp that stores an
// instant reads the wall-clock fields in location (UTC when nil).
func encodeCopyBinaryValueAt(kind copyKind, value any, location *time.Location) ([]byte, error) {
	switch kind {
	case copyInt2, copyInt4, copyInt8:
		return encodeCopyInteger(kind, value)
	case copyInt1, copyUint1, copyUint4, copyUint8:
		return encodeCopySmallInteger(kind, value)
	case copyFloat4, copyFloat8:
		return encodeCopyFloat(kind, value)
	case copyBool:
		return encodeCopyBool(value)
	case copyText, copyJSON, copyBytea:
		return encodeCopyText(value)
	case copyJSONB:
		encoded, err := encodeCopyText(value)
		if err != nil {
			return nil, err
		}
		return append([]byte{1}, encoded...), nil
	case copyDateDays, copyDateMicros, copyTimestamp, copyTimestamptz, copyTime, copyZonedTimestamp, copyTimeTicks:
		return encodeCopyTemporal(kind, value, location)
	case copyUUID:
		return encodeCopyBinaryUUID(value)
	}
	return nil, errors.New("has no binary encoding")
}

func encodeCopyInteger(kind copyKind, value any) ([]byte, error) {
	number, ok := value.(int64)
	if !ok {
		return nil, fmt.Errorf("expects an int64, got %T", value)
	}
	switch kind {
	case copyInt2:
		if number < math.MinInt16 || number > math.MaxInt16 {
			return nil, fmt.Errorf("value %d is out of range", number)
		}
		return binary.BigEndian.AppendUint16(nil, uint16(int16(number))), nil
	case copyInt4:
		if number < math.MinInt32 || number > math.MaxInt32 {
			return nil, fmt.Errorf("value %d is out of range", number)
		}
		return binary.BigEndian.AppendUint32(nil, uint32(int32(number))), nil
	default:
		return binary.BigEndian.AppendUint64(nil, uint64(number)), nil
	}
}

func encodeCopySmallInteger(kind copyKind, value any) ([]byte, error) {
	number, ok := value.(int64)
	if !ok {
		return nil, fmt.Errorf("expects an int64, got %T", value)
	}
	switch kind {
	case copyInt1:
		if number < math.MinInt8 || number > math.MaxInt8 {
			return nil, fmt.Errorf("value %d is out of range", number)
		}
		return []byte{byte(int8(number))}, nil
	case copyUint1:
		if number < 0 || number > math.MaxUint8 {
			return nil, fmt.Errorf("value %d is out of range", number)
		}
		return []byte{byte(number)}, nil
	case copyUint4:
		if number < 0 || number > math.MaxUint32 {
			return nil, fmt.Errorf("value %d is out of range", number)
		}
		return binary.BigEndian.AppendUint32(nil, uint32(number)), nil
	default:
		if number < 0 {
			return nil, fmt.Errorf("value %d is out of range", number)
		}
		return binary.BigEndian.AppendUint64(nil, uint64(number)), nil
	}
}

func encodeCopyFloat(kind copyKind, value any) ([]byte, error) {
	number, ok := copyFloatValue(value)
	if !ok {
		return nil, fmt.Errorf("expects a float, got %T", value)
	}
	if kind == copyFloat8 {
		return binary.BigEndian.AppendUint64(nil, math.Float64bits(number)), nil
	}
	converted := float32(number)
	if !math.IsInf(number, 0) && math.IsInf(float64(converted), 0) {
		return nil, fmt.Errorf("value %v is out of range", number)
	}
	return binary.BigEndian.AppendUint32(nil, math.Float32bits(converted)), nil
}

func encodeCopyBool(value any) ([]byte, error) {
	flag, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("expects a bool, got %T", value)
	}
	if flag {
		return []byte{1}, nil
	}
	return []byte{0}, nil
}

func encodeCopyText(value any) ([]byte, error) {
	if text, ok := value.(string); ok {
		return []byte(text), nil
	}
	raw, ok := value.([]byte)
	if !ok {
		return nil, fmt.Errorf("expects a string or []byte, got %T", value)
	}
	return raw, nil
}

func encodeCopyTemporal(kind copyKind, value any, location *time.Location) ([]byte, error) {
	moment, ok := value.(time.Time)
	if !ok {
		return nil, fmt.Errorf("expects a time.Time, got %T", value)
	}
	switch kind {
	case copyDateDays:
		return binary.BigEndian.AppendUint32(nil, uint32(int32(copyEpochDays(moment)))), nil
	case copyDateMicros, copyTimestamptz:
		return binary.BigEndian.AppendUint64(nil, uint64(copyMicros(moment.UTC()))), nil
	case copyTimestamp:
		wall := time.Date(moment.Year(), moment.Month(), moment.Day(),
			moment.Hour(), moment.Minute(), moment.Second(), moment.Nanosecond(), time.UTC)
		return binary.BigEndian.AppendUint64(nil, uint64(copyMicros(wall))), nil
	case copyZonedTimestamp:
		zone := time.UTC
		if location != nil {
			zone = location
		}
		wall := time.Date(moment.Year(), moment.Month(), moment.Day(),
			moment.Hour(), moment.Minute(), moment.Second(), moment.Nanosecond(), zone)
		return binary.BigEndian.AppendUint64(nil, uint64(copyMicros(wall.UTC()))), nil
	case copyTimeTicks:
		micros := (int64(moment.Hour())*int64(time.Hour) +
			int64(moment.Minute())*int64(time.Minute) +
			int64(moment.Second())*int64(time.Second) +
			int64(moment.Nanosecond())) / int64(time.Microsecond)
		return binary.BigEndian.AppendUint64(nil, uint64(micros*10)), nil
	default:
		micros := (int64(moment.Hour())*int64(time.Hour) +
			int64(moment.Minute())*int64(time.Minute) +
			int64(moment.Second())*int64(time.Second) +
			int64(moment.Nanosecond())) / int64(time.Microsecond)
		return binary.BigEndian.AppendUint64(nil, uint64(micros)), nil
	}
}

// copyEpochDays counts whole days from the binary epoch.
func copyEpochDays(moment time.Time) int64 {
	day := time.Date(moment.Year(), moment.Month(), moment.Day(), 0, 0, 0, 0, time.UTC)
	return int64(day.Sub(copyBinaryEpoch) / (24 * time.Hour))
}

// copyMicros counts microseconds from the binary epoch.
func copyMicros(instant time.Time) int64 {
	return int64(instant.Sub(copyBinaryEpoch) / time.Microsecond)
}

// copyFloatValue accepts either float shape, because database/sql passes float32
// and float64 through unchanged.
func copyFloatValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	}
	return 0, false
}

// encodeCopyBinaryUUID returns the 16 bytes of a uuid value.
func encodeCopyBinaryUUID(value any) ([]byte, error) {
	switch raw := value.(type) {
	case []byte:
		if len(raw) == 16 {
			return raw, nil
		}
		return encodeCopyBinaryUUID(string(raw))
	case string:
		hex := strings.ReplaceAll(raw, "-", "")
		if len(hex) != 32 {
			return nil, fmt.Errorf("value %q is not a uuid", raw)
		}
		out := make([]byte, 16)
		for i := 0; i < 16; i++ {
			high, ok := copyHexDigit(hex[i*2])
			if !ok {
				return nil, fmt.Errorf("value %q is not a uuid", raw)
			}
			low, ok := copyHexDigit(hex[i*2+1])
			if !ok {
				return nil, fmt.Errorf("value %q is not a uuid", raw)
			}
			out[i] = high<<4 | low
		}
		return out, nil
	}
	return nil, fmt.Errorf("expects a uuid string or 16 bytes, got %T", value)
}

func copyHexDigit(digit byte) (byte, bool) {
	switch {
	case digit >= '0' && digit <= '9':
		return digit - '0', true
	case digit >= 'a' && digit <= 'f':
		return digit - 'a' + 10, true
	case digit >= 'A' && digit <= 'F':
		return digit - 'A' + 10, true
	}
	return 0, false
}

// appendCopyBinaryTrailer appends the -1 field count that ends a binary stream.
func appendCopyBinaryTrailer(dst []byte) []byte {
	return append(dst, 0xff, 0xff)
}
