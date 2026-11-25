package kingbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// generatedColumnType names the verified integral types a generated identity
// may use. Other generated columns cannot supply LastInsertId.
func generatedColumnType(schema, name string) bool {
	if schema != schemaPGCatalog {
		return false
	}
	switch name {
	case pgTypeInt2, pgTypeInt4, pgTypeInt8:
		return true
	default:
		return false
	}
}

// insertTarget is the parsed relation of a single-row INSERT and the byte
// position where RETURNING must be inserted.
type insertTarget struct {
	schema       string
	schemaQuoted bool
	table        string
	tableQuoted  bool
	// insertAt is the byte position before which RETURNING is inserted.
	insertAt int
}

// regclass renders the target as the text accepted by to_regclass. Unquoted
// identifiers fold to lowercase exactly as the server folds them.
func (t insertTarget) regclass() string {
	if t.schema == "" {
		return t.regclassPart(t.table, t.tableQuoted)
	}
	return t.regclassPart(t.schema, t.schemaQuoted) + "." + t.regclassPart(t.table, t.tableQuoted)
}

func (t insertTarget) regclassPart(name string, quoted bool) string {
	if quoted {
		return quoteIdentifier(name)
	}
	// PostgreSQL folds only ASCII letters for UTF-8 unquoted identifiers.
	folded := []byte(name)
	for i, char := range folded {
		if char >= 'A' && char <= 'Z' {
			folded[i] = char + ('a' - 'A')
		}
	}
	return string(folded)
}

// quoteIdentifier renders one SQL identifier with doubled quotes.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteSQLLiteral renders one SQL string literal with doubled single quotes.
func quoteSQLLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// sqlTokenKind is one lexical class of the INSERT parser.
type sqlTokenKind uint8

const (
	sqlTokenWord sqlTokenKind = iota
	sqlTokenQuotedIdentifier
	sqlTokenString
	sqlTokenPunctuation
)

// sqlToken is one lexeme with its source span.
type sqlToken struct {
	kind       sqlTokenKind
	text       string
	start, end int
}

// tokenizeSQL splits a statement into words, quoted identifiers, strings and
// single-byte punctuation. Comments, dollar-quoted bodies and string literals
// are consumed so keywords inside them cannot be mistaken for SQL.
func tokenizeSQL(query string) ([]sqlToken, error) {
	tokens := make([]sqlToken, 0, 16)
	for i := 0; i < len(query); {
		end, skipped, err := skipSQLTrivia(query, i)
		if err != nil {
			return nil, err
		}
		if skipped {
			i = end
			continue
		}
		token, err := scanSQLToken(query, i)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
		i = token.end
	}
	return tokens, nil
}

// skipSQLTrivia consumes whitespace, line comments and block comments.
func skipSQLTrivia(query string, i int) (int, bool, error) {
	switch {
	case query[i] == ' ' || query[i] == '\t' || query[i] == '\n' || query[i] == '\r':
		for i < len(query) && (query[i] == ' ' || query[i] == '\t' || query[i] == '\n' || query[i] == '\r') {
			i++
		}
		return i, true, nil
	case query[i] == '-' && i+1 < len(query) && query[i+1] == '-':
		i += 2
		for i < len(query) && query[i] != '\n' {
			i++
		}
		return i, true, nil
	case query[i] == '/' && i+1 < len(query) && query[i+1] == '*':
		end, err := skipSQLBlockComment(query, i)
		return end, true, err
	default:
		return i, false, nil
	}
}

// scanSQLToken reads one non-trivia token at start.
func scanSQLToken(query string, start int) (sqlToken, error) {
	char := query[start]
	switch {
	case char == '\'':
		end, err := skipStringLiteral(query, start, false)
		if err != nil {
			return sqlToken{}, err
		}
		return sqlToken{kind: sqlTokenString, start: start, end: end}, nil
	case (char == 'E' || char == 'e') && start+1 < len(query) && query[start+1] == '\'' &&
		(start == 0 || !isIdentifierByte(query[start-1])):
		end, err := skipStringLiteral(query, start+1, true)
		if err != nil {
			return sqlToken{}, err
		}
		return sqlToken{kind: sqlTokenString, start: start, end: end}, nil
	case (char == 'U' || char == 'u') && start+2 < len(query) && query[start+1] == '&' &&
		query[start+2] == '\'' && (start == 0 || !isIdentifierByte(query[start-1])):
		end, err := skipStringLiteral(query, start+2, false)
		if err != nil {
			return sqlToken{}, err
		}
		return sqlToken{kind: sqlTokenString, start: start, end: end}, nil
	case char == '"':
		value, end, err := decodeQuotedIdentifier(query, start)
		if err != nil {
			return sqlToken{}, err
		}
		return sqlToken{kind: sqlTokenQuotedIdentifier, text: value, start: start, end: end}, nil
	case char == '$':
		if end, ok := dollarQuoteAt(query, start); ok {
			return sqlToken{kind: sqlTokenString, start: start, end: end}, nil
		}
		return scanWordToken(query, start), nil
	case isIdentifierStart(char):
		return scanWordToken(query, start), nil
	default:
		return sqlToken{kind: sqlTokenPunctuation, text: query[start : start+1], start: start, end: start + 1}, nil
	}
}

// scanWordToken reads a bare or dollar-prefixed word/identifier.
func scanWordToken(query string, start int) sqlToken {
	i := start + 1
	for i < len(query) && isIdentifierByte(query[i]) {
		i++
	}
	return sqlToken{kind: sqlTokenWord, text: query[start:i], start: start, end: i}
}

// skipSQLBlockComment consumes one (possibly nested) block comment.
func skipSQLBlockComment(query string, start int) (int, error) {
	depth := 0
	for i := start; i < len(query); {
		switch {
		case i+1 < len(query) && query[i] == '/' && query[i+1] == '*':
			depth++
			i += 2
		case i+1 < len(query) && query[i] == '*' && query[i+1] == '/':
			depth--
			i += 2
			if depth == 0 {
				return i, nil
			}
		default:
			i++
		}
	}
	return 0, fmt.Errorf("unterminated block comment")
}

// skipStringLiteral consumes one single-quoted string. E-strings and Unicode
// escape strings honor backslash escapes; plain strings do not, matching the
// standard-conforming lexical mode enforced at connect.
func skipStringLiteral(query string, start int, backslashEscapes bool) (int, error) {
	for i := start + 1; i < len(query); i++ {
		switch query[i] {
		case '\\':
			if backslashEscapes {
				i++
			}
		case '\'':
			if i+1 < len(query) && query[i+1] == '\'' {
				i++
				continue
			}
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("unterminated string literal")
}

// decodeQuotedIdentifier reads a double-quoted identifier with doubled quotes.
func decodeQuotedIdentifier(query string, start int) (string, int, error) {
	var builder strings.Builder
	for i := start + 1; i < len(query); i++ {
		if query[i] != '"' {
			builder.WriteByte(query[i])
			continue
		}
		if i+1 < len(query) && query[i+1] == '"' {
			builder.WriteByte('"')
			i++
			continue
		}
		return builder.String(), i + 1, nil
	}
	return "", 0, fmt.Errorf("unterminated quoted identifier")
}

// isSQLWord reports whether a token is the given bare keyword.
func isSQLWord(token sqlToken, word string) bool {
	return token.kind == sqlTokenWord && strings.EqualFold(token.text, word)
}

// sqlIdentifier returns one table/column identifier token.
func sqlIdentifier(token sqlToken) (value string, quoted bool, ok bool) {
	switch token.kind {
	case sqlTokenWord, sqlTokenQuotedIdentifier:
		return token.text, token.kind == sqlTokenQuotedIdentifier, true
	default:
		return "", false, false
	}
}

// skipBalanced returns the index after the parenthesis group starting at open.
func skipBalanced(tokens []sqlToken, open int) (int, bool) {
	if open >= len(tokens) || tokens[open].kind != sqlTokenPunctuation || tokens[open].text != "(" {
		return 0, false
	}
	depth := 0
	for i := open; i < len(tokens); i++ {
		if tokens[i].kind != sqlTokenPunctuation {
			continue
		}
		switch tokens[i].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// startsWithSQLInsert recognizes an INSERT prefix after whitespace/comments
// even when the rest of the statement cannot be tokenized.
func startsWithSQLInsert(query string) bool {
	i := 0
	for {
		for i < len(query) && (query[i] == ' ' || query[i] == '\t' || query[i] == '\n' || query[i] == '\r') {
			i++
		}
		if i+1 < len(query) && query[i] == '-' && query[i+1] == '-' {
			i += 2
			for i < len(query) && query[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(query) && query[i] == '/' && query[i+1] == '*' {
			end, err := skipSQLBlockComment(query, i)
			if err != nil {
				return false
			}
			i = end
			continue
		}
		break
	}
	if i+6 > len(query) || !strings.EqualFold(query[i:i+6], "insert") {
		return false
	}
	end := i + 6
	return end == len(query) || !isIdentifierByte(query[end])
}

// parseGeneratedInsert recognizes the only INSERT shapes eligible for
// automatic generated-ID capture. It returns isInsert=false for other
// statements; an eligible shape returns the target and insertion point.
func parseGeneratedInsert(query string) (insertTarget, bool, error) {
	tokens, err := tokenizeSQL(query)
	if err != nil {
		if startsWithSQLInsert(query) {
			return insertTarget{}, false, fmt.Errorf("cannot tokenize INSERT: %v", err)
		}
		return insertTarget{}, false, nil
	}
	if len(tokens) == 0 {
		return insertTarget{}, false, nil
	}
	if isSQLWord(tokens[0], "with") {
		if hasTopLevelInsert(tokens) {
			return insertTarget{}, false, fmt.Errorf("WITH ... INSERT is not supported")
		}
		return insertTarget{}, false, nil
	}
	if !isSQLWord(tokens[0], "insert") {
		return insertTarget{}, false, nil
	}
	index := 1
	if index >= len(tokens) || !isSQLWord(tokens[index], "into") {
		return insertTarget{}, false, fmt.Errorf("INSERT without INTO is not supported")
	}
	index++
	target := insertTarget{}
	if index >= len(tokens) {
		return insertTarget{}, false, fmt.Errorf("INSERT relation is missing")
	}
	first, firstQuoted, ok := sqlIdentifier(tokens[index])
	if !ok {
		return insertTarget{}, false, fmt.Errorf("INSERT relation is missing")
	}
	index++
	if index < len(tokens) && tokens[index].kind == sqlTokenPunctuation && tokens[index].text == "." {
		index++
		if index >= len(tokens) {
			return insertTarget{}, false, fmt.Errorf("INSERT relation is missing")
		}
		second, secondQuoted, ok := sqlIdentifier(tokens[index])
		if !ok {
			return insertTarget{}, false, fmt.Errorf("INSERT relation is missing")
		}
		target.schema, target.schemaQuoted = first, firstQuoted
		target.table, target.tableQuoted = second, secondQuoted
		index++
		if index < len(tokens) && tokens[index].kind == sqlTokenPunctuation && tokens[index].text == "." {
			return insertTarget{}, false, fmt.Errorf("INSERT relation has too many qualifiers")
		}
	} else {
		target.table, target.tableQuoted = first, firstQuoted
	}
	if index < len(tokens) && tokens[index].kind == sqlTokenPunctuation && tokens[index].text == "(" {
		index, ok = skipBalanced(tokens, index)
		if !ok {
			return insertTarget{}, false, fmt.Errorf("INSERT column list is unbalanced")
		}
	}
	switch {
	case index < len(tokens) && isSQLWord(tokens[index], "values"):
		index++
		index, ok = skipBalanced(tokens, index)
		if !ok {
			return insertTarget{}, false, fmt.Errorf("INSERT VALUES list is unbalanced")
		}
	case index+1 < len(tokens) && isSQLWord(tokens[index], "default") && isSQLWord(tokens[index+1], "values"):
		index += 2
	default:
		return insertTarget{}, false, fmt.Errorf("INSERT form is not a supported single-row VALUES statement")
	}
	return finishInsertTarget(tokens, index, target)
}

// finishInsertTarget accepts only the end of the statement or one trailing
// semicolon after the VALUES group.
func finishInsertTarget(tokens []sqlToken, index int, target insertTarget) (insertTarget, bool, error) {
	if index == len(tokens) {
		target.insertAt = tokens[index-1].end
		return target, true, nil
	}
	if tokens[index].kind == sqlTokenPunctuation && tokens[index].text == ";" && index+1 == len(tokens) {
		target.insertAt = tokens[index].start
		return target, true, nil
	}
	return insertTarget{}, false, fmt.Errorf("INSERT has an unsupported trailing clause")
}

// hasTopLevelInsert reports whether a WITH statement's main statement is an
// INSERT. INSERTs inside parenthesized CTE bodies are not the main statement.
func hasTopLevelInsert(tokens []sqlToken) bool {
	depth := 0
	for _, token := range tokens {
		if token.kind == sqlTokenPunctuation {
			switch token.text {
			case "(":
				depth++
			case ")":
				if depth > 0 {
					depth--
				}
			}
			continue
		}
		if depth == 0 && isSQLWord(token, "insert") {
			return true
		}
	}
	return false
}

// generatedColumnQuery lists the attributes of the target relation. It is a
// verified qualified pg_catalog query; to_regclass applies the session
// search_path for an unqualified name.
func generatedColumnQuery(regclass string) string {
	return `SELECT a.attname, a.atttypid, a.attidentity, a.attgenerated,
       COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), '')
FROM pg_catalog.pg_attribute AS a
LEFT JOIN pg_catalog.pg_attrdef AS d
       ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = pg_catalog.to_regclass(` + quoteSQLLiteral(regclass) + `)
  AND a.attnum > 0
  AND NOT a.attisdropped
ORDER BY a.attnum`
}

// generatedColumn is one catalog candidate for automatic capture.
type generatedColumn struct {
	name string
	oid  uint32
}

// resolveGeneratedColumn finds exactly one catalog-verified integral
// generated/identity column on the target relation.
func (c *conn) resolveGeneratedColumn(ctx context.Context, target insertTarget) (string, error) {
	rows, err := c.querySimple(ctx, generatedColumnQuery(target.regclass()))
	if err != nil {
		return "", fmt.Errorf("%w: resolve generated column: %w", ErrLastInsertIDUnavailable, err)
	}
	var candidates []generatedColumn
	values := make([]driver.Value, 5)
	for {
		err := rows.Next(values)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("%w: read generated column: %w", ErrLastInsertIDUnavailable, err)
		}
		if values[0] == nil {
			break
		}
		name := catalogString(values[0])
		oid, err := catalogInt(values[1])
		if err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("%w: invalid generated column type: %v", ErrLastInsertIDUnavailable, err)
		}
		identity := catalogString(values[2])
		generated := catalogString(values[3])
		defaultExpr := catalogString(values[4])
		if identity == "a" || identity == "d" || generated == "s" || isNextvalDefault(defaultExpr) {
			candidates = append(candidates, generatedColumn{name: name, oid: uint32(oid)})
		}
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("%w: close generated column query: %w", ErrLastInsertIDUnavailable, err)
	}
	if len(candidates) == 0 {
		exists, err := c.relationExists(ctx, target)
		if err != nil {
			return "", fmt.Errorf("%w: check relation %s: %w", ErrLastInsertIDUnavailable, target.regclass(), err)
		}
		if !exists {
			return "", fmt.Errorf("%w: relation %s does not exist", ErrLastInsertIDUnavailable, target.regclass())
		}
		return "", fmt.Errorf("%w: relation %s has no generated or identity column", ErrLastInsertIDUnavailable, target.regclass())
	}
	var integral []generatedColumn
	for _, candidate := range candidates {
		schema, name, ok := c.codecName(candidate.oid)
		if ok && generatedColumnType(schema, name) {
			integral = append(integral, candidate)
		}
	}
	if len(integral) == 0 {
		if c.catalogStatus == CatalogPartial {
			return "", fmt.Errorf("%w: cannot verify an integral generated column for %s with a partial type catalog",
				ErrLastInsertIDUnavailable, target.regclass())
		}
		return "", fmt.Errorf("%w: relation %s has no integral generated or identity column", ErrLastInsertIDUnavailable, target.regclass())
	}
	if len(integral) > 1 {
		names := make([]string, len(integral))
		for i, candidate := range integral {
			names[i] = candidate.name
		}
		return "", fmt.Errorf("%w: relation %s has ambiguous generated columns %s",
			ErrLastInsertIDUnavailable, target.regclass(), strings.Join(names, ", "))
	}
	return integral[0].name, nil
}

// relationExists distinguishes a missing relation from a relation without a
// generated column when the attribute query returned no rows.
func (c *conn) relationExists(ctx context.Context, target insertTarget) (bool, error) {
	rows, err := c.querySimple(ctx, "SELECT pg_catalog.to_regclass("+quoteSQLLiteral(target.regclass())+") IS NOT NULL")
	if err != nil {
		return false, err
	}
	values := make([]driver.Value, 1)
	err = rows.Next(values)
	if errors.Is(err, io.EOF) {
		_ = rows.Close()
		return false, nil
	}
	if err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if values[0] == nil {
		return false, nil
	}
	if exists, ok := values[0].(bool); ok {
		return exists, nil
	}
	return parseBool(catalogString(values[0])), nil
}

// isNextvalDefault reports whether a column default is a sequence nextval call,
// which marks a serial-style generated column.
func isNextvalDefault(expression string) bool {
	normalized := strings.ToLower(strings.TrimSpace(expression))
	if strings.HasPrefix(normalized, "nextval(") {
		return true
	}
	return strings.HasPrefix(normalized, "nextval (") ||
		strings.HasPrefix(normalized, "pg_catalog.nextval(") ||
		strings.HasPrefix(normalized, "pg_catalog.nextval (")
}

// planGeneratedInsert validates an INSERT and returns the rewritten statement
// with RETURNING plus the captured column. A non-INSERT statement is returned
// unchanged with an empty column.
func (c *conn) planGeneratedInsert(ctx context.Context, query string) (string, string, error) {
	target, isInsert, err := parseGeneratedInsert(query)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v; use QueryRow with an explicit RETURNING column", ErrLastInsertIDUnavailable, err)
	}
	if !isInsert {
		return query, "", nil
	}
	column, err := c.resolveGeneratedColumn(ctx, target)
	if err != nil {
		return "", "", err
	}
	at := target.insertAt
	rewritten := query[:at] + " RETURNING " + quoteIdentifier(column) + query[at:]
	return rewritten, column, nil
}

// parseGeneratedIDValue converts the text-format generated ID and reports
// int64 overflow explicitly.
func parseGeneratedIDValue(data []byte) (int64, error) {
	value, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return 0, ErrLastInsertIDOverflow
		}
		return 0, fmt.Errorf("%w: invalid generated ID: %v", ErrLastInsertIDUnavailable, err)
	}
	return value, nil
}
