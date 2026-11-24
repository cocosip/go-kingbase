package gokb

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// placeholderPlan is a statement with its placeholders normalized to $n plus
// the number of positional parameters it expects.
type placeholderPlan struct {
	query string
	args  int
}

// errMixedPlaceholders rejects a statement that mixes ? with $n.
var errMixedPlaceholders = errors.New("kingbase: mixed ? and $n placeholders in one statement")

// planPlaceholders applies the profile's verified identifier quoting and the
// driver's positional placeholder rules to a statement: ? placeholders become
// sequential $n, explicit $n passes through, and quoted strings, E-strings,
// quoted identifiers, dollar-quoted bodies, comments and the ?|, ?& and @?
// operators stay untouched. A bare ? is always a placeholder, so the jsonb ?
// operator must be written as jsonb_exists() or with ?|/?&; named styles
// (:name, @name) are rejected because the driver binds positional parameters
// only.
func planPlaceholders(profile modeProfile, query string) (placeholderPlan, error) {
	var out strings.Builder
	out.Grow(len(query) + 8)
	next := 0        // ordinal assigned to the next ? placeholder
	explicitMax := 0 // largest explicit $n index
	sawQuestion := false
	sawExplicit := false
	for i := 0; i < len(query); {
		if end, ok := literalSpan(profile, query, i); ok {
			out.WriteString(query[i:end])
			i = end
			continue
		}
		switch char := query[i]; char {
		case '$':
			end := i + 1
			for end < len(query) && isASCIIDigit(query[end]) {
				end++
			}
			if end == i+1 || identifierByteBefore(query, i) {
				// A lone $ or a $ inside an identifier is not a placeholder.
				out.WriteByte(char)
				i++
				continue
			}
			index, err := strconv.Atoi(query[i+1 : end])
			if err != nil || index < 1 {
				return placeholderPlan{}, fmt.Errorf("kingbase: invalid placeholder %q", query[i:end])
			}
			if sawQuestion {
				return placeholderPlan{}, errMixedPlaceholders
			}
			sawExplicit = true
			if index > explicitMax {
				explicitMax = index
			}
			out.WriteString(query[i:end])
			i = end
		case '?':
			if i+1 < len(query) && (query[i+1] == '|' || query[i+1] == '&') {
				out.WriteString(query[i : i+2])
				i += 2
				continue
			}
			if sawExplicit {
				return placeholderPlan{}, errMixedPlaceholders
			}
			sawQuestion = true
			next++
			out.WriteString("$")
			out.WriteString(strconv.Itoa(next))
			i++
		case ':':
			if i+1 < len(query) && query[i+1] == ':' {
				out.WriteString("::")
				i += 2
				continue
			}
			if i+1 < len(query) && isIdentifierStart(query[i+1]) && namedPlaceholderStart(query, i) {
				return placeholderPlan{}, fmt.Errorf("kingbase: named placeholder %q is not supported (use $n or ?)", query[i:identifierEnd(query, i+1)])
			}
			out.WriteByte(char)
			i++
		case '@':
			if i+1 < len(query) && query[i+1] == '?' {
				// The jsonpath @? operator, not a named placeholder.
				out.WriteString("@?")
				i += 2
				continue
			}
			if i+1 < len(query) && isIdentifierStart(query[i+1]) && namedPlaceholderStart(query, i) {
				return placeholderPlan{}, fmt.Errorf("kingbase: named placeholder %q is not supported (use $n or ?)", query[i:identifierEnd(query, i+1)])
			}
			out.WriteByte(char)
			i++
		default:
			out.WriteByte(char)
			i++
		}
	}
	plan := placeholderPlan{query: out.String()}
	if sawQuestion {
		plan.args = next
	} else {
		plan.args = explicitMax
	}
	return plan, nil
}

// rewritePlaceholders applies the profile's verified placeholder rules to a
// statement and reports its positional parameter count. A mode without a
// verified profile passes the statement through unchanged with -1 (no count
// check) instead of fabricating semantics.
func (c *conn) rewritePlaceholders(query string) (string, int, error) {
	profile, verified := c.cfg.CompatibilityMode.profile()
	if !verified {
		return query, -1, nil
	}
	plan, err := planPlaceholders(profile, query)
	if err != nil {
		return "", 0, err
	}
	return plan.query, plan.args, nil
}

// literalSpan returns the end of a quoted literal or identifier, a
// dollar-quoted body or a comment starting at i, and whether i starts one. An
// explicit $n placeholder is not a literal span.
func literalSpan(profile modeProfile, query string, i int) (int, bool) {
	switch query[i] {
	case '\'':
		return skipQuoted(query, i, false), true
	case '-':
		if i+1 < len(query) && query[i+1] == '-' {
			if end := strings.IndexByte(query[i:], '\n'); end >= 0 {
				return i + end, true
			}
			return len(query), true
		}
	case '/':
		if i+1 < len(query) && query[i+1] == '*' {
			return skipBlockComment(query, i), true
		}
	case 'E', 'e':
		if i+1 < len(query) && query[i+1] == '\'' && !identifierByteBefore(query, i) {
			return skipQuoted(query, i+1, true), true
		}
	case '$':
		if i+1 >= len(query) || !isASCIIDigit(query[i+1]) {
			if _, end, ok := dollarQuoteAt(query, i); ok {
				return end, true
			}
		}
	default:
		if profile.quotesIdentifier(query[i]) {
			return skipQuoted(query, i, false), true
		}
	}
	return 0, false
}

// skipQuoted returns the end of a quoted literal or identifier starting at the
// opening quote. In a plain string only a doubled quote escapes; an E-string
// additionally honors backslash escapes. An unterminated quote runs to the end
// and is left for the server to reject.
func skipQuoted(query string, open int, backslashEscapes bool) int {
	quote := query[open]
	for i := open + 1; i < len(query); i++ {
		switch query[i] {
		case '\\':
			if backslashEscapes {
				i++
			}
		case quote:
			if i+1 < len(query) && query[i+1] == quote {
				i++
				continue
			}
			return i + 1
		}
	}
	return len(query)
}

// skipBlockComment consumes a (nestable) block comment.
func skipBlockComment(query string, open int) int {
	depth := 0
	for i := open; i < len(query); i++ {
		if i+1 < len(query) && query[i] == '/' && query[i+1] == '*' {
			depth++
			i++
			continue
		}
		if i+1 < len(query) && query[i] == '*' && query[i+1] == '/' {
			depth--
			i++
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(query)
}

// dollarQuoteAt recognizes a dollar-quoted body start at i and returns its tag,
// the index just past the closing delimiter, and whether it matched. A tag is
// letters, digits and underscores only; '$' terminates it.
func dollarQuoteAt(query string, i int) (tag string, end int, ok bool) {
	j := i + 1
	if j < len(query) && isIdentifierStart(query[j]) {
		j++
		for j < len(query) && (isIdentifierStart(query[j]) || isASCIIDigit(query[j])) {
			j++
		}
	}
	if j >= len(query) || query[j] != '$' {
		return "", 0, false
	}
	tag = query[i : j+1]
	closing := strings.Index(query[j+1:], tag)
	if closing < 0 {
		return tag, len(query), true
	}
	return tag, j + 1 + closing + len(tag), true
}

func isASCIIDigit(char byte) bool {
	return char >= '0' && char <= '9'
}

func isIdentifierStart(char byte) bool {
	return char == '_' || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char >= 0x80
}

func isIdentifierByte(char byte) bool {
	return isIdentifierStart(char) || isASCIIDigit(char) || char == '$'
}

func identifierEnd(query string, start int) int {
	for start < len(query) && isIdentifierByte(query[start]) {
		start++
	}
	return start
}

// namedPlaceholderStart reports whether a ':' or '@' at i begins a named
// placeholder token rather than an array slice bound or a postfix position. A
// preceding operand (identifier, digit, ')' or ']' or '[') means slicing or
// indexing, which PG accepts.
func namedPlaceholderStart(query string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch query[j] {
		case ' ', '\t', '\n', '\r':
			continue
		case '[', ')', ']':
			return false
		default:
			return !isIdentifierByte(query[j])
		}
	}
	return true
}

func identifierByteBefore(query string, i int) bool {
	return i > 0 && isIdentifierByte(query[i-1])
}
