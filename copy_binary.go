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

// copyKindForType maps one catalog type to its binary encoder. The vendor keeps
// the storage type in the catalog, which is why a mode-specific mapping (REAL as
// a double, DATE as eight bytes) needs no special case here.
func copyKindForType(typname string, typlen int64) (copyKind, bool) {
	switch typname {
	case "int2", "smallint", "tinyint", "uint1":
		return copyInt2, true
	case "int4", "integer", "int", "mediumint", "uint2", "uint4", "oid":
		return copyInt4, true
	case "int8", "bigint", "uint8", "serial", "bigserial":
		return copyInt8, true
	case "float4", "real":
		return copyFloat4, true
	case "float8", "double", "double precision", "float":
		return copyFloat8, true
	case "bool", "boolean", "bit":
		return copyBool, true
	case "text", "varchar", "varcharbyte", "bpchar", "char", "name", "nvarchar", "nchar", "clob", "longtext":
		return copyText, true
	case "bytea", "blob", "binary", "varbinary":
		return copyBytea, true
	case "date":
		if typlen == 8 {
			return copyDateMicros, true
		}
		return copyDateDays, true
	case "timestamp", "datetime", "smalldatetime":
		return copyTimestamp, true
	case "timestamptz":
		return copyTimestamptz, true
	case "time", "timetz":
		return copyTime, true
	case "uuid", "uniqueidentifier":
		return copyUUID, true
	case "json":
		return copyJSON, true
	case "jsonb":
		return copyJSONB, true
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

// copyBinaryColumnQuery lists the storage types of the requested relation.
func copyBinaryColumnQuery(relation string) string {
	return `SELECT a.attname, t.typname, t.typlen
FROM pg_catalog.pg_attribute AS a
JOIN pg_catalog.pg_type AS t ON t.oid = a.atttypid
WHERE a.attrelid = pg_catalog.to_regclass(` + quoteSQLLiteral(relation) + `)
  AND a.attnum > 0
  AND NOT a.attisdropped
ORDER BY a.attnum`
}

// appendCopyBinaryRow encodes one row: its field count, then every field.
func appendCopyBinaryRow(dst []byte, kinds []copyKind, args []driver.NamedValue) ([]byte, error) {
	if len(args) != len(kinds) {
		return nil, fmt.Errorf("kingbase: binary COPY row has %d values for %d columns", len(args), len(kinds))
	}
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(kinds)))
	for i, arg := range args {
		next, err := appendCopyBinaryField(dst, kinds[i], arg.Value)
		if err != nil {
			return nil, fmt.Errorf("kingbase: column %d (%s) %w", i+1, kinds[i], err)
		}
		dst = next
	}
	return dst, nil
}

// appendCopyBinaryField appends one length-prefixed field, or the -1 length that
// means NULL.
func appendCopyBinaryField(dst []byte, kind copyKind, value any) ([]byte, error) {
	if value == nil {
		return binary.BigEndian.AppendUint32(dst, math.MaxUint32), nil
	}
	encoded, err := encodeCopyBinaryValue(kind, value)
	if err != nil {
		return nil, err
	}
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(encoded)))
	return append(dst, encoded...), nil
}

func encodeCopyBinaryValue(kind copyKind, value any) ([]byte, error) {
	switch kind {
	case copyInt2, copyInt4, copyInt8:
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
	case copyFloat4:
		number, ok := copyFloatValue(value)
		if !ok {
			return nil, fmt.Errorf("expects a float, got %T", value)
		}
		return binary.BigEndian.AppendUint32(nil, math.Float32bits(float32(number))), nil
	case copyFloat8:
		number, ok := copyFloatValue(value)
		if !ok {
			return nil, fmt.Errorf("expects a float, got %T", value)
		}
		return binary.BigEndian.AppendUint64(nil, math.Float64bits(number)), nil
	case copyBool:
		flag, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("expects a bool, got %T", value)
		}
		if flag {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case copyText, copyJSON, copyBytea:
		text, ok := value.(string)
		if ok {
			return []byte(text), nil
		}
		raw, ok := value.([]byte)
		if !ok {
			return nil, fmt.Errorf("expects a string or []byte, got %T", value)
		}
		return raw, nil
	case copyJSONB:
		encoded, err := encodeCopyBinaryValue(copyJSON, value)
		if err != nil {
			return nil, err
		}
		return append([]byte{1}, encoded...), nil
	case copyDateDays, copyDateMicros, copyTimestamp, copyTimestamptz, copyTime:
		moment, ok := value.(time.Time)
		if !ok {
			return nil, fmt.Errorf("expects a time.Time, got %T", value)
		}
		switch kind {
		case copyDateDays:
			return binary.BigEndian.AppendUint32(nil, uint32(int32(copyEpochDays(moment)))), nil
		case copyDateMicros:
			return binary.BigEndian.AppendUint64(nil, uint64(copyMicros(moment.UTC()))), nil
		case copyTimestamptz:
			return binary.BigEndian.AppendUint64(nil, uint64(copyMicros(moment.UTC()))), nil
		case copyTimestamp:
			wall := time.Date(moment.Year(), moment.Month(), moment.Day(),
				moment.Hour(), moment.Minute(), moment.Second(), moment.Nanosecond(), time.UTC)
			return binary.BigEndian.AppendUint64(nil, uint64(copyMicros(wall))), nil
		default:
			micros := (int64(moment.Hour())*int64(time.Hour) +
				int64(moment.Minute())*int64(time.Minute) +
				int64(moment.Second())*int64(time.Second) +
				int64(moment.Nanosecond())) / int64(time.Microsecond)
			return binary.BigEndian.AppendUint64(nil, uint64(micros)), nil
		}
	case copyUUID:
		return encodeCopyBinaryUUID(value)
	}
	return nil, errors.New("has no binary encoding")
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
