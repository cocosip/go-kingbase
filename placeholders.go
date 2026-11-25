package kingbase

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// placeholderPlan is a statement with its placeholders normalized to $n plus
// the number of positional parameters it expects. An explicit qualified cast
// may also name a parameter target for verified binary encoding.
type placeholderPlan struct {
	query   string
	args    int
	targets map[int]targetRef
	names   map[string]int
}

// targetRef is a SQL-spelled qualified type name attached to one parameter.
type targetRef struct {
	schema string
	name   string
}

// targetCollector records explicit cast targets and drops an index whose casts
// disagree, so a repeated placeholder never inherits a guessed target.
type targetCollector struct {
	refs     map[int]targetRef
	disabled map[int]bool
}

func (c *targetCollector) add(query string, index, start int) {
	if c.disabled[index] {
		return
	}
	ref, ok := castTarget(query, start)
	if !ok {
		return
	}
	if c.refs == nil {
		c.refs = make(map[int]targetRef)
	}
	if existing, exists := c.refs[index]; exists && existing != ref {
		delete(c.refs, index)
		if c.disabled == nil {
			c.disabled = make(map[int]bool)
		}
		c.disabled[index] = true
		return
	}
	c.refs[index] = ref
}

// errMixedPlaceholders rejects a statement that mixes ? with $n.
var errMixedPlaceholders = errors.New("kingbase: mixed ? and $n placeholders in one statement")

// errMixedNativePlaceholders rejects a statement that mixes the driver's ?
// rewriting with a compatibility mode's native :n/@var binds.
var errMixedNativePlaceholders = errors.New("kingbase: mixed ? and native :n/@name binds in one statement")

// namedPlaceholders assigns stable ordinals to :name/@name bindings. One
// prefix style is accepted per statement; repeated names reuse one ordinal.
type namedPlaceholders struct {
	names  map[string]int
	prefix byte
}

func (n *namedPlaceholders) rewrite(query string, start int, out *strings.Builder, collector *targetCollector) (int, error) {
	prefix := query[start]
	if n.prefix != 0 && n.prefix != prefix {
		return 0, errors.New("kingbase: mixed @ and : named placeholders in one statement")
	}
	n.prefix = prefix
	end := identifierEnd(query, start+1)
	name := query[start+1 : end]
	index, exists := n.names[name]
	if !exists {
		index = len(n.names) + 1
		n.names[name] = index
	}
	collector.add(query, index, end)
	out.WriteString("$")
	out.WriteString(strconv.Itoa(index))
	return end, nil
}

// planPlaceholders applies the profile's verified identifier quoting and the
// driver's placeholder rules to a statement: ? placeholders become sequential
// $n, explicit $n passes through, :name/@name bind by name and are rewritten to
// $n under the PG profile, and quoted strings, E-strings, quoted identifiers
// (including backtick and bracket forms when the mode accepts them),
// dollar-quoted bodies, comments and the ?|, ?& and @? operators stay
// untouched. One style per statement is enforced. A bare ? is always a
// placeholder, so the jsonb ? operator must be written as jsonb_exists() or
// with ?|/?&.
//
// A compatibility-mode statement that contains the server's own :n or @var
// bind syntax is passed through unchanged with -1 (the server owns the count);
// mixing that syntax with ? is rejected before sending.
//
//nolint:gocyclo // One pass over mutually exclusive literal/operator/placeholder states.
func planPlaceholders(profile modeProfile, query string) (placeholderPlan, error) {
	var out strings.Builder
	out.Grow(len(query) + 8)
	next := 0        // ordinal assigned to the next ? placeholder
	explicitMax := 0 // largest explicit $n index
	sawQuestion := false
	sawExplicit := false
	sawNative := false
	named := namedPlaceholders{names: map[string]int{}}
	collector := targetCollector{}
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
			if sawQuestion || len(named.names) != 0 {
				return placeholderPlan{}, errMixedPlaceholders
			}
			sawExplicit = true
			if index > explicitMax {
				explicitMax = index
			}
			collector.add(query, index, end)
			out.WriteString(query[i:end])
			i = end
		case '?':
			if i+1 < len(query) && (query[i+1] == '|' || query[i+1] == '&') {
				out.WriteString(query[i : i+2])
				i += 2
				continue
			}
			if sawExplicit || len(named.names) != 0 {
				return placeholderPlan{}, errMixedPlaceholders
			}
			sawQuestion = true
			next++
			collector.add(query, next, i+1)
			out.WriteString("$")
			out.WriteString(strconv.Itoa(next))
			i++
		case ':':
			if i+1 < len(query) && query[i+1] == ':' {
				out.WriteString("::")
				i += 2
				continue
			}
			if profile.namedPlaceholders && i+1 < len(query) && isIdentifierStart(query[i+1]) && namedPlaceholderStart(query, i) {
				if sawQuestion || sawExplicit {
					return placeholderPlan{}, errMixedPlaceholders
				}
				end, err := named.rewrite(query, i, &out, &collector)
				if err != nil {
					return placeholderPlan{}, err
				}
				i = end
				continue
			}
			if !profile.namedPlaceholders && i+1 < len(query) &&
				(isIdentifierStart(query[i+1]) || isASCIIDigit(query[i+1])) && namedPlaceholderStart(query, i) {
				sawNative = true
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
			if profile.namedPlaceholders && i+1 < len(query) && isIdentifierStart(query[i+1]) && namedPlaceholderStart(query, i) {
				if sawQuestion || sawExplicit {
					return placeholderPlan{}, errMixedPlaceholders
				}
				end, err := named.rewrite(query, i, &out, &collector)
				if err != nil {
					return placeholderPlan{}, err
				}
				i = end
				continue
			}
			if !profile.namedPlaceholders && i+1 < len(query) && isIdentifierStart(query[i+1]) && namedPlaceholderStart(query, i) {
				sawNative = true
			}
			out.WriteByte(char)
			i++
		default:
			out.WriteByte(char)
			i++
		}
	}
	plan := placeholderPlan{query: out.String(), targets: collector.refs}
	switch {
	case sawNative:
		if sawQuestion || sawExplicit || len(named.names) != 0 {
			return placeholderPlan{}, errMixedNativePlaceholders
		}
		return placeholderPlan{query: query, args: -1}, nil
	case sawQuestion:
		plan.args = next
	case sawExplicit:
		plan.args = explicitMax
	case len(named.names) != 0:
		plan.args = len(named.names)
		plan.names = named.names
	}
	return plan, nil
}

// bindValues maps caller arguments onto the plan's ordinals. Positional
// placeholders require empty names and sequential ordinals; named placeholders
// require every supplied name to exist, every placeholder to be supplied once,
// and no duplicate names. A negative args count is an unverified pass-through
// and keeps the positional contract without a client-side count.
func (p placeholderPlan) bindValues(args []driver.NamedValue) ([]driver.Value, error) {
	if p.args < 0 {
		values := make([]driver.Value, len(args))
		for i, arg := range args {
			if arg.Name != "" {
				return nil, fmt.Errorf("kingbase: named parameter %q supplied for a pass-through statement", arg.Name)
			}
			if arg.Ordinal != i+1 {
				return nil, fmt.Errorf("kingbase: invalid parameter ordinal %d", arg.Ordinal)
			}
			values[i] = arg.Value
		}
		return values, nil
	}
	if p.names == nil {
		if len(args) != p.args {
			return nil, fmt.Errorf("kingbase: statement expects %d arguments, got %d", p.args, len(args))
		}
		values := make([]driver.Value, len(args))
		for i, arg := range args {
			if arg.Name != "" {
				return nil, fmt.Errorf("kingbase: named parameter %q supplied for positional placeholders", arg.Name)
			}
			if arg.Ordinal != i+1 {
				return nil, fmt.Errorf("kingbase: invalid parameter ordinal %d", arg.Ordinal)
			}
			values[i] = arg.Value
		}
		return values, nil
	}
	values := make([]driver.Value, p.args)
	seen := make([]bool, p.args)
	for _, arg := range args {
		if arg.Name == "" {
			return nil, errors.New("kingbase: named placeholders require sql.Named arguments")
		}
		index, ok := p.names[arg.Name]
		if !ok {
			return nil, fmt.Errorf("kingbase: unknown named parameter %q", arg.Name)
		}
		if seen[index-1] {
			return nil, fmt.Errorf("kingbase: duplicate named parameter %q", arg.Name)
		}
		seen[index-1] = true
		values[index-1] = arg.Value
	}
	for index, ok := range seen {
		if ok {
			continue
		}
		for name, position := range p.names {
			if position == index+1 {
				return nil, fmt.Errorf("kingbase: missing named parameter %q", name)
			}
		}
	}
	return values, nil
}

// rewriteParameters returns the normalized statement and its explicit
// qualified cast targets. A mode without a verified profile passes the
// statement through unchanged with -1 (no count check) instead of fabricating
// semantics.
func (c *conn) rewriteParameters(query string) (placeholderPlan, error) {
	profile, verified := c.mode.profile()
	if !verified {
		return placeholderPlan{query: query, args: -1}, nil
	}
	return planPlaceholders(profile, query)
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
			if end, ok := dollarQuoteAt(query, i); ok {
				return end, true
			}
		}
	case '`':
		if profile.quotesIdentifier('`') {
			return skipQuoted(query, i, false), true
		}
	case '[':
		if profile.bracketQuotes {
			return skipBracket(query, i), true
		}
	default:
		if profile.quotesIdentifier(query[i]) {
			return skipQuoted(query, i, false), true
		}
	}
	return 0, false
}

// skipBracket returns the end of a SQLServer [identifier] starting at open; a
// doubled ]] is an escaped closing bracket.
func skipBracket(query string, open int) int {
	for i := open + 1; i < len(query); i++ {
		if query[i] != ']' {
			continue
		}
		if i+1 < len(query) && query[i+1] == ']' {
			i++
			continue
		}
		return i + 1
	}
	return len(query)
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

// dollarQuoteAt recognizes a dollar-quoted body start at i and returns the
// index just past the closing delimiter plus whether it matched. A tag is
// letters, digits and underscores only; '$' terminates it.
func dollarQuoteAt(query string, i int) (end int, ok bool) {
	j := i + 1
	if j < len(query) && isIdentifierStart(query[j]) {
		j++
		for j < len(query) && (isIdentifierStart(query[j]) || isASCIIDigit(query[j])) {
			j++
		}
	}
	if j >= len(query) || query[j] != '$' {
		return 0, false
	}
	tag := query[i : j+1]
	closing := strings.Index(query[j+1:], tag)
	if closing < 0 {
		return len(query), true
	}
	return j + 1 + closing + len(tag), true
}

// castTarget reads an immediately following qualified `::schema.type` cast.
// Only a plain two-part identifier is accepted; aliases, arrays, quoted names
// and type modifiers stay on the inference path instead of being guessed.
func castTarget(query string, start int) (targetRef, bool) {
	i := skipSpace(query, start)
	if i+1 >= len(query) || query[i] != ':' || query[i+1] != ':' {
		return targetRef{}, false
	}
	i = skipSpace(query, i+2)
	if i >= len(query) || !isIdentifierStart(query[i]) {
		return targetRef{}, false
	}
	first := query[i:identifierEnd(query, i)]
	i = skipSpace(query, identifierEnd(query, i))
	if i >= len(query) || query[i] != '.' {
		return targetRef{}, false
	}
	i = skipSpace(query, i+1)
	if i >= len(query) || !isIdentifierStart(query[i]) {
		return targetRef{}, false
	}
	secondEnd := identifierEnd(query, i)
	second := query[i:secondEnd]
	if next := skipSpace(query, secondEnd); next < len(query) && query[next] == '[' {
		return targetRef{}, false
	}
	return targetRef{schema: strings.ToLower(first), name: strings.ToLower(second)}, true
}

func skipSpace(query string, i int) int {
	for i < len(query) {
		switch query[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
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

// namedPlaceholderStart distinguishes a named bind from an array slice or
// postfix position. A preceding identifier byte, ')' or ']' means slicing or
// indexing; whitespace/operators are token boundaries.
func namedPlaceholderStart(query string, i int) bool {
	if i == 0 {
		return true
	}
	switch query[i-1] {
	case ')', ']', '[':
		return false
	}
	return !isIdentifierByte(query[i-1])
}

func identifierByteBefore(query string, i int) bool {
	return i > 0 && isIdentifierByte(query[i-1])
}
