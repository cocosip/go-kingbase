package kingbase

import (
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const (
	// maxArrayDimensions mirrors PostgreSQL's MAXDIM bound and caps parser
	// recursion.
	maxArrayDimensions = 6
	// maxArrayElements bounds one array value so a hostile frame cannot force
	// unbounded allocation or parser work.
	maxArrayElements = 1 << 20
	// defaultArrayDelimiter is the element separator used by most builtin types.
	defaultArrayDelimiter = ','
	minArrayLowerBound    = -1 << 31
	maxArrayLowerBound    = 1<<31 - 1
)

// ArrayDimension is one dimension of an Array: its element count and its first
// (lower) bound. PostgreSQL arrays keep lower bounds, so a plain slice cannot
// always represent them.
type ArrayDimension struct {
	Length     int
	LowerBound int
}

// Array is an explicit rectangular array value for one-dimensional and
// multidimensional PostgreSQL text arrays. It implements sql.Scanner and
// driver.Valuer; the driver itself keeps array results as text unless a caller
// scans into an Array.
//
// The zero value is SQL NULL. Valid=true with no elements is an empty array.
// Dimensions is optional for one-dimensional arrays (it is inferred) and
// required to preserve multidimensional shape or a non-default lower bound.
// Elements are stored row-major. A SQL NULL element is represented by nil for
// pointer and interface element types, or by an invalid sql.Null* / driver.Valuer
// element; scanning a NULL element into a non-nullable scalar is an error.
//
// Delimiter is the element separator, comma when zero. It is preserved so an
// array using a type-specific delimiter round-trips; callers must set it for a
// type whose typdelim is not comma because the driver keeps array results as
// opaque text and does not infer the element type. Composite values and
// binary array layouts stay opaque: scan text results only (for example with
// disable_prepared_binary_result); binary values return an explicit error.
type Array[T any] struct {
	Dimensions []ArrayDimension
	Elements   []T
	Valid      bool
	Delimiter  byte
}

var _ sql.Scanner = (*Array[int64])(nil)

var _ driver.Valuer = Array[int64]{}

// Value implements driver.Valuer. It renders the array as a text-format array
// literal, preserving dimensions, lower bounds and NULL elements.
func (a Array[T]) Value() (driver.Value, error) {
	if !a.Valid {
		return nil, nil
	}
	dimensions, err := a.arrayDimensions()
	if err != nil {
		return nil, err
	}
	delimiter := a.Delimiter
	if delimiter == 0 {
		delimiter = defaultArrayDelimiter
	}
	if !validArrayDelimiter(delimiter) {
		return nil, fmt.Errorf("kingbase: invalid array delimiter %q", delimiter)
	}
	if len(a.Elements) == 0 {
		return "{}", nil
	}
	var builder strings.Builder
	if hasArrayLowerBounds(dimensions) {
		for _, dimension := range dimensions {
			builder.WriteByte('[')
			builder.WriteString(strconv.Itoa(dimension.LowerBound))
			builder.WriteByte(':')
			builder.WriteString(strconv.Itoa(dimension.LowerBound + dimension.Length - 1))
			builder.WriteByte(']')
		}
		builder.WriteByte('=')
	}
	index := 0
	if err := writeArrayLevel(&builder, dimensions, 0, a.Elements, &index, delimiter); err != nil {
		return nil, err
	}
	if index != len(a.Elements) {
		// Unreachable after arrayDimensions validated the element count.
		return nil, fmt.Errorf("kingbase: array encoding consumed %d of %d elements", index, len(a.Elements))
	}
	return builder.String(), nil
}

// arrayDimensions validates the declared shape and returns the dimensions to
// encode. An empty one-dimensional array gets an inferred length.
func (a Array[T]) arrayDimensions() ([]ArrayDimension, error) {
	if len(a.Dimensions) == 0 {
		if len(a.Elements) == 0 {
			return nil, nil
		}
		if len(a.Elements) > maxArrayElements {
			return nil, fmt.Errorf("kingbase: array has more than %d elements", maxArrayElements)
		}
		return []ArrayDimension{{Length: len(a.Elements), LowerBound: 1}}, nil
	}
	if len(a.Dimensions) > maxArrayDimensions {
		return nil, fmt.Errorf("kingbase: array has %d dimensions, at most %d are supported", len(a.Dimensions), maxArrayDimensions)
	}
	total := 1
	for _, dimension := range a.Dimensions {
		if dimension.Length <= 0 {
			return nil, fmt.Errorf("kingbase: array dimension length %d is not representable", dimension.Length)
		}
		if dimension.Length > maxArrayElements || total > maxArrayElements/dimension.Length {
			return nil, fmt.Errorf("kingbase: array has more than %d elements", maxArrayElements)
		}
		total *= dimension.Length
		if dimension.LowerBound < minArrayLowerBound || dimension.LowerBound > maxArrayLowerBound {
			return nil, fmt.Errorf("kingbase: array lower bound %d is out of range", dimension.LowerBound)
		}
		upper := int64(dimension.LowerBound) + int64(dimension.Length) - 1
		if upper > maxArrayLowerBound {
			return nil, fmt.Errorf("kingbase: array dimension [%d:...] overflows", dimension.LowerBound)
		}
	}
	if total != len(a.Elements) {
		return nil, fmt.Errorf("kingbase: array shape has %d elements, got %d", total, len(a.Elements))
	}
	return a.Dimensions, nil
}

func hasArrayLowerBounds(dimensions []ArrayDimension) bool {
	for _, dimension := range dimensions {
		if dimension.LowerBound != 1 {
			return true
		}
	}
	return false
}

// writeArrayLevel writes one brace level of the text literal.
func writeArrayLevel[T any](builder *strings.Builder, dimensions []ArrayDimension, depth int, elements []T, index *int, delimiter byte) error {
	builder.WriteByte('{')
	for i := 0; i < dimensions[depth].Length; i++ {
		if i > 0 {
			builder.WriteByte(delimiter)
		}
		if depth == len(dimensions)-1 {
			text, null, err := arrayElementText(elements[*index])
			if err != nil {
				return err
			}
			(*index)++
			if null {
				builder.WriteString("NULL")
				continue
			}
			writeQuotedArrayElement(builder, text)
			continue
		}
		if err := writeArrayLevel(builder, dimensions, depth+1, elements, index, delimiter); err != nil {
			return err
		}
	}
	builder.WriteByte('}')
	return nil
}

// arrayElementText renders one element as the text an array literal carries.
// NULL is reported separately so it is written unquoted.
func arrayElementText(value any) (string, bool, error) {
	standard, err := driver.DefaultParameterConverter.ConvertValue(value)
	if err != nil {
		return "", false, fmt.Errorf("kingbase: array element: %w", err)
	}
	switch typed := standard.(type) {
	case nil:
		return "", true, nil
	case int64:
		return strconv.FormatInt(typed, 10), false, nil
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), false, nil
	case bool:
		if typed {
			return "true", false, nil
		}
		return "false", false, nil
	case string:
		return typed, false, nil
	case []byte:
		if typed == nil {
			return "", true, nil
		}
		return `\x` + hex.EncodeToString(typed), false, nil
	case time.Time:
		return typed.Format("2006-01-02 15:04:05.999999999Z07:00"), false, nil
	default:
		return "", false, fmt.Errorf("kingbase: unsupported array element type %T", standard)
	}
}

// writeQuotedArrayElement quotes an element, escaping backslashes and quotes.
// Quoting every non-NULL element keeps delimiters, braces and whitespace exact.
func writeQuotedArrayElement(builder *strings.Builder, text string) {
	builder.WriteByte('"')
	for i := 0; i < len(text); i++ {
		if text[i] == '"' || text[i] == '\\' {
			builder.WriteByte('\\')
		}
		builder.WriteByte(text[i])
	}
	builder.WriteByte('"')
}

// validArrayDelimiter rejects separators that would be ambiguous with array
// literal syntax.
func validArrayDelimiter(delimiter byte) bool {
	switch delimiter {
	case 0, '{', '}', '"', '\\', ' ', '\t', '\r', '\n', '[', ']', '=':
		return false
	default:
		return true
	}
}

// Scan implements sql.Scanner for a text-format array value. A nil source is
// SQL NULL; string and []byte sources are parsed as PostgreSQL array literals.
func (a *Array[T]) Scan(source any) error {
	switch typed := source.(type) {
	case nil:
		delimiter := a.Delimiter
		*a = Array[T]{Delimiter: delimiter}
		return nil
	case string:
		return a.scanText([]byte(typed))
	case []byte:
		return a.scanText(typed)
	default:
		return fmt.Errorf("kingbase: cannot scan %T into an array", source)
	}
}

// scanText parses and converts one text array value, assigning only on success.
func (a *Array[T]) scanText(source []byte) error {
	trimmed := strings.TrimLeft(string(source), " \t\r\n")
	if trimmed == "" {
		return fmt.Errorf("kingbase: scan array: empty array text")
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		// Prepared statements request binary results by default; binary array
		// payloads start with a dimension count, not a brace.
		return fmt.Errorf("kingbase: scan array: binary array values are not supported, use text results (disable_prepared_binary_result)")
	}
	parsed, err := parseArrayText(source, a.Delimiter)
	if err != nil {
		return fmt.Errorf("kingbase: scan array: %w", err)
	}
	elements, err := arrayElements[T](parsed.elements)
	if err != nil {
		return fmt.Errorf("kingbase: scan array: %w", err)
	}
	a.Dimensions = parsed.dimensions
	a.Elements = elements
	a.Valid = true
	if a.Delimiter == 0 {
		a.Delimiter = defaultArrayDelimiter
	}
	return nil
}

// arrayElement is one parsed element: NULL or its unescaped text.
type arrayElement struct {
	text string
	null bool
}

// parsedArray is the shape and elements read from one text literal.
type parsedArray struct {
	dimensions []ArrayDimension
	elements   []arrayElement
}

// arrayParser scans one text-format array literal.
type arrayParser struct {
	data      []byte
	pos       int
	delimiter byte
}

// parseArrayText parses a PostgreSQL text array literal, including the optional
// dimension decoration, quoted elements, NULLs and nested braces.
func parseArrayText(data []byte, delimiter byte) (parsedArray, error) {
	if delimiter == 0 {
		delimiter = defaultArrayDelimiter
	}
	if !validArrayDelimiter(delimiter) {
		return parsedArray{}, fmt.Errorf("invalid array delimiter %q", delimiter)
	}
	parser := &arrayParser{data: data, delimiter: delimiter}
	parser.skipSpace()
	var declared []ArrayDimension
	if parser.peek() == '[' {
		var err error
		declared, err = parser.parseDimensions()
		if err != nil {
			return parsedArray{}, err
		}
		parser.skipSpace()
		if parser.peek() != '=' {
			return parsedArray{}, fmt.Errorf("array dimensions must be followed by '='")
		}
		parser.pos++
		parser.skipSpace()
	}
	if parser.peek() != '{' {
		return parsedArray{}, fmt.Errorf("expected '{' at byte %d", parser.pos)
	}
	var lengths []int
	var elements []arrayElement
	if err := parser.parseLevel(0, &lengths, &elements); err != nil {
		return parsedArray{}, err
	}
	parser.skipSpace()
	if parser.pos != len(parser.data) {
		return parsedArray{}, fmt.Errorf("unexpected trailing data at byte %d", parser.pos)
	}
	if len(elements) == 0 {
		if len(lengths) > 1 || (len(lengths) == 1 && lengths[0] != 0) {
			return parsedArray{}, fmt.Errorf("array has a zero-length dimension")
		}
		if len(declared) > 0 {
			return parsedArray{}, fmt.Errorf("empty array cannot carry dimensions")
		}
		return parsedArray{}, nil
	}
	if len(declared) > 0 && len(declared) != len(lengths) {
		return parsedArray{}, fmt.Errorf("array has %d dimensions, declared %d", len(lengths), len(declared))
	}
	dimensions := make([]ArrayDimension, len(lengths))
	total := 1
	for i, length := range lengths {
		if length <= 0 {
			return parsedArray{}, fmt.Errorf("array has a zero-length dimension")
		}
		lower := 1
		if len(declared) > 0 {
			if declared[i].Length != length {
				return parsedArray{}, fmt.Errorf("dimension %d has %d elements, declared %d", i+1, length, declared[i].Length)
			}
			lower = declared[i].LowerBound
		}
		dimensions[i] = ArrayDimension{Length: length, LowerBound: lower}
		if total > maxArrayElements/length {
			return parsedArray{}, fmt.Errorf("array has more than %d elements", maxArrayElements)
		}
		total *= length
	}
	if total != len(elements) {
		return parsedArray{}, fmt.Errorf("array has a non-rectangular shape")
	}
	return parsedArray{dimensions: dimensions, elements: elements}, nil
}

// parseDimensions reads the optional [lower:upper] decoration.
func (p *arrayParser) parseDimensions() ([]ArrayDimension, error) {
	var dimensions []ArrayDimension
	for p.peek() == '[' {
		if len(dimensions) >= maxArrayDimensions {
			return nil, fmt.Errorf("array has more than %d dimensions", maxArrayDimensions)
		}
		p.pos++
		lower, err := p.parseDimensionInt()
		if err != nil {
			return nil, err
		}
		if p.peek() != ':' {
			return nil, fmt.Errorf("expected ':' in array dimension")
		}
		p.pos++
		upper, err := p.parseDimensionInt()
		if err != nil {
			return nil, err
		}
		if p.peek() != ']' {
			return nil, fmt.Errorf("expected ']' in array dimension")
		}
		p.pos++
		if upper < lower {
			return nil, fmt.Errorf("array dimension [%d:%d] has no elements", lower, upper)
		}
		length := int64(upper) - int64(lower) + 1
		if length > maxArrayElements {
			return nil, fmt.Errorf("array dimension has more than %d elements", maxArrayElements)
		}
		dimensions = append(dimensions, ArrayDimension{Length: int(length), LowerBound: lower})
		p.skipSpace()
	}
	return dimensions, nil
}

// parseDimensionInt reads one signed dimension bound.
func (p *arrayParser) parseDimensionInt() (int, error) {
	p.skipSpace()
	start := p.pos
	if p.peek() == '-' || p.peek() == '+' {
		p.pos++
	}
	digits := p.pos
	for isASCIIDigit(p.peek()) {
		p.pos++
	}
	if p.pos == digits {
		return 0, fmt.Errorf("expected a numeric array dimension bound")
	}
	value, err := strconv.ParseInt(string(p.data[start:p.pos]), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("array dimension bound %q is out of range", p.data[start:p.pos])
	}
	return int(value), nil
}

// parseLevel parses one brace level and records its length. It rejects ragged
// nesting, trailing delimiters and mixed scalar/nested levels.
func (p *arrayParser) parseLevel(depth int, lengths *[]int, elements *[]arrayElement) error {
	if depth >= maxArrayDimensions {
		return fmt.Errorf("array has more than %d dimensions", maxArrayDimensions)
	}
	if p.peek() != '{' {
		return fmt.Errorf("expected '{' at byte %d", p.pos)
	}
	p.pos++
	p.skipSpace()
	if p.peek() == '}' {
		p.pos++
		return p.recordLength(lengths, depth, 0)
	}
	length := 0
	nested := false
	kind := false
	for {
		childNested := p.peek() == '{'
		if kind && childNested != nested {
			return fmt.Errorf("array mixes scalars and nested levels")
		}
		nested, kind = childNested, true
		if childNested {
			if err := p.parseLevel(depth+1, lengths, elements); err != nil {
				return err
			}
		} else {
			element, err := p.parseElement()
			if err != nil {
				return err
			}
			*elements = append(*elements, element)
			if len(*elements) > maxArrayElements {
				return fmt.Errorf("array has more than %d elements", maxArrayElements)
			}
		}
		length++
		p.skipSpace()
		switch p.peek() {
		case p.delimiter:
			p.pos++
			p.skipSpace()
			if p.peek() == '}' {
				return fmt.Errorf("array has a trailing delimiter")
			}
		case '}':
			p.pos++
			return p.recordLength(lengths, depth, length)
		default:
			return fmt.Errorf("expected ',' or '}' at byte %d", p.pos)
		}
	}
}

// recordLength stores one level's element count and enforces rectangularity
// across siblings.
func (p *arrayParser) recordLength(lengths *[]int, depth, length int) error {
	for len(*lengths) <= depth {
		*lengths = append(*lengths, -1)
	}
	if (*lengths)[depth] == -1 {
		(*lengths)[depth] = length
		return nil
	}
	if (*lengths)[depth] != length {
		return fmt.Errorf("array is not rectangular at dimension %d", depth+1)
	}
	return nil
}

// parseElement reads one scalar element, either quoted or bare. A bare element
// supports PostgreSQL backslash escapes, so an escaped delimiter or brace does
// not split the element.
func (p *arrayParser) parseElement() (arrayElement, error) {
	if p.peek() == '"' {
		return p.parseQuotedElement()
	}
	start := p.pos
	for p.pos < len(p.data) {
		if p.data[p.pos] == '\\' {
			if p.pos+1 >= len(p.data) {
				return arrayElement{}, fmt.Errorf("array element ends with a backslash")
			}
			p.pos += 2
			continue
		}
		character := p.data[p.pos]
		if character == p.delimiter || character == '}' || character == '{' {
			break
		}
		p.pos++
	}
	raw := trimUnescapedArraySpace(string(p.data[start:p.pos]))
	if raw == "" {
		return arrayElement{}, fmt.Errorf("array element at byte %d is empty without quotes", start)
	}
	text, escaped, err := unescapeArrayText(raw)
	if err != nil {
		return arrayElement{}, err
	}
	if !escaped && strings.EqualFold(text, "NULL") {
		return arrayElement{null: true}, nil
	}
	return arrayElement{text: text}, nil
}

// trimUnescapedArraySpace trims PostgreSQL's ASCII whitespace around a bare
// element while preserving a whitespace byte that is itself backslash-escaped.
func trimUnescapedArraySpace(raw string) string {
	start := 0
	for start < len(raw) && isArraySpace(raw[start]) {
		start++
	}
	end := len(raw)
	for end > start && isArraySpace(raw[end-1]) && !arrayByteEscaped(raw, end-1) {
		end--
	}
	return raw[start:end]
}

// arrayByteEscaped reports whether the byte at index is preceded by an odd
// number of backslashes.
func arrayByteEscaped(raw string, index int) bool {
	backslashes := 0
	for i := index - 1; i >= 0 && raw[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

// unescapeArrayText resolves bare-element backslash escapes. The boolean
// reports whether any escape was present, so an escaped "NULL" stays literal.
func unescapeArrayText(raw string) (string, bool, error) {
	var builder strings.Builder
	builder.Grow(len(raw))
	escaped := false
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			builder.WriteByte(raw[i])
			continue
		}
		if i+1 >= len(raw) {
			return "", false, fmt.Errorf("array element ends with a backslash")
		}
		i++
		builder.WriteByte(raw[i])
		escaped = true
	}
	return builder.String(), escaped, nil
}

// parseQuotedElement reads a double-quoted element with backslash escapes. A
// backslash escapes the next byte, as PostgreSQL's array reader does.
func (p *arrayParser) parseQuotedElement() (arrayElement, error) {
	p.pos++
	var builder strings.Builder
	for p.pos < len(p.data) {
		character := p.data[p.pos]
		switch character {
		case '\\':
			if p.pos+1 >= len(p.data) {
				return arrayElement{}, fmt.Errorf("array element ends with a backslash")
			}
			builder.WriteByte(p.data[p.pos+1])
			p.pos += 2
		case '"':
			p.pos++
			return arrayElement{text: builder.String()}, nil
		default:
			builder.WriteByte(character)
			p.pos++
		}
	}
	return arrayElement{}, fmt.Errorf("unterminated quoted array element")
}

// isArraySpace reports the ASCII whitespace PostgreSQL ignores around a bare
// array element. Unicode spaces are element data, not separators.
func isArraySpace(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}

// skipSpace consumes whitespace between array tokens.
func (p *arrayParser) skipSpace() {
	for p.pos < len(p.data) && isArraySpace(p.data[p.pos]) {
		p.pos++
	}
}

// peek returns the next byte or zero at the end of input.
func (p *arrayParser) peek() byte {
	if p.pos >= len(p.data) {
		return 0
	}
	return p.data[p.pos]
}

// arrayElements converts parsed elements into the typed slice.
func arrayElements[T any](items []arrayElement) ([]T, error) {
	slice := reflect.MakeSlice(reflect.SliceOf(reflect.TypeFor[T]()), len(items), len(items))
	for i, item := range items {
		if err := scanArrayElement(slice.Index(i), item); err != nil {
			return nil, fmt.Errorf("element %d: %w", i+1, err)
		}
	}
	return slice.Interface().([]T), nil
}

// scanArrayElement stores one parsed element in an addressable destination,
// honoring sql.Scanner implementations and rejecting NULL where the destination
// cannot represent it.
func scanArrayElement(destination reflect.Value, item arrayElement) error {
	if item.null {
		if destination.Kind() == reflect.Pointer {
			destination.SetZero()
			return nil
		}
		if scanner, ok := arrayScanner(destination); ok {
			return scanner.Scan(nil)
		}
		switch destination.Kind() {
		case reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
			destination.SetZero()
			return nil
		default:
			return fmt.Errorf("NULL cannot be represented by %s", destination.Type())
		}
	}
	if destination.Kind() == reflect.Pointer {
		if destination.IsNil() {
			destination.Set(reflect.New(destination.Type().Elem()))
		}
		if scanner, ok := destination.Interface().(sql.Scanner); ok {
			return scanner.Scan(item.text)
		}
		return scanArrayElement(destination.Elem(), item)
	}
	if scanner, ok := arrayScanner(destination); ok {
		return scanner.Scan(item.text)
	}
	switch destination.Kind() {
	case reflect.String:
		destination.SetString(item.text)
	case reflect.Bool:
		value, err := parseArrayBool(item.text)
		if err != nil {
			return err
		}
		destination.SetBool(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(item.text, 10, destination.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid %s element %q", destination.Type(), item.text)
		}
		destination.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, err := strconv.ParseUint(item.text, 10, destination.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid %s element %q", destination.Type(), item.text)
		}
		destination.SetUint(value)
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(item.text, destination.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid %s element %q", destination.Type(), item.text)
		}
		destination.SetFloat(value)
	case reflect.Slice:
		if destination.Type().Elem().Kind() != reflect.Uint8 {
			return fmt.Errorf("unsupported array element type %s", destination.Type())
		}
		decoded := decodeBytea([]byte(item.text))
		if decoded == nil {
			// Keep empty bytea distinct from a NULL element.
			decoded = []byte{}
		}
		destination.SetBytes(decoded)
	case reflect.Interface:
		value := reflect.ValueOf(item.text)
		if !value.Type().AssignableTo(destination.Type()) {
			return fmt.Errorf("string element cannot be represented by %s", destination.Type())
		}
		destination.Set(value)
	case reflect.Struct:
		if destination.Type() != reflect.TypeFor[time.Time]() {
			return fmt.Errorf("unsupported array element type %s", destination.Type())
		}
		moment, err := parseArrayTime(item.text)
		if err != nil {
			return err
		}
		destination.Set(reflect.ValueOf(moment))
	default:
		return fmt.Errorf("unsupported array element type %s", destination.Type())
	}
	return nil
}

// arrayScanner returns an sql.Scanner implemented by the destination or its
// pointer, if any.
func arrayScanner(destination reflect.Value) (sql.Scanner, bool) {
	if destination.CanAddr() {
		if scanner, ok := destination.Addr().Interface().(sql.Scanner); ok {
			return scanner, true
		}
	}
	if destination.CanInterface() {
		if scanner, ok := destination.Interface().(sql.Scanner); ok {
			return scanner, true
		}
	}
	return nil, false
}

// parseArrayBool accepts the boolean spellings PostgreSQL emits and documents.
func parseArrayBool(text string) (bool, error) {
	switch strings.ToLower(text) {
	case "t", "true", "y", "yes", "1":
		return true, nil
	case "f", "false", "n", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean element %q", text)
	}
}

// parseArrayTime parses the date, timestamp and timestamptz forms PostgreSQL
// emits for temporal arrays.
func parseArrayTime(text string) (time.Time, error) {
	for _, layout := range arrayTimeLayouts {
		if moment, err := time.ParseInLocation(layout, text, time.UTC); err == nil {
			return moment, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time element %q", text)
}

var arrayTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999Z07",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02",
}
