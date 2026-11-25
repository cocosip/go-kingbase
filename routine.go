package kingbase

import "strings"

// rewriteRoutine prefixes EXEC only for an accepted profile whose convenience
// rule is enabled and whose entire statement is a parsed procedure identifier.
// Ordinary SQL, calls with arguments/parentheses and explicit EXEC/EXECUTE/CALL
// are left untouched; no fragment is ever copied into a different dialect.
func (p modeProfile) rewriteRoutine(query string) string {
	if !p.routineConvenience {
		return query
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" || hasRoutineVerb(trimmed) {
		return query
	}
	end, ok := p.procedurePath(trimmed)
	if !ok || end != len(trimmed) {
		return query
	}
	return "EXEC " + trimmed
}

func hasRoutineVerb(query string) bool {
	for _, verb := range []string{"EXEC", "EXECUTE", "CALL"} {
		if len(query) < len(verb) || !strings.EqualFold(query[:len(verb)], verb) {
			continue
		}
		if len(query) == len(verb) || isSpaceByte(query[len(verb)]) {
			return true
		}
	}
	return false
}

func isSpaceByte(char byte) bool {
	return char == ' ' || char == '\t' || char == '\r' || char == '\n'
}

// procedurePath parses a complete dotted identifier path and returns its end.
// Components may be bare, double-quoted, or bracketed when the profile enables
// them; whitespace around dots is accepted but arguments are not.
func (p modeProfile) procedurePath(query string) (int, bool) {
	index := 0
	for {
		index = skipSpaceBytes(query, index)
		end, ok := p.identifierComponent(query, index)
		if !ok {
			return 0, false
		}
		index = skipSpaceBytes(query, end)
		if index == len(query) {
			return index, true
		}
		if query[index] != '.' {
			return 0, false
		}
		index++
	}
}

func skipSpaceBytes(query string, index int) int {
	for index < len(query) && isSpaceByte(query[index]) {
		index++
	}
	return index
}

func (p modeProfile) identifierComponent(query string, start int) (int, bool) {
	if start >= len(query) {
		return 0, false
	}
	switch query[start] {
	case '[':
		if !p.bracketQuotes {
			return 0, false
		}
		return skipBracketed(query, start)
	case '"':
		if !p.quotesIdentifier('"') {
			return 0, false
		}
		return skipQuotedIdentifier(query, start)
	}
	if !isRoutineIdentifierStart(query[start]) {
		return 0, false
	}
	index := start + 1
	for index < len(query) && isRoutineIdentifierByte(query[index]) {
		index++
	}
	return index, true
}

// skipBracketed consumes [component], treating ]] as one escaped closing
// bracket. An empty or unterminated component is not a procedure path.
func skipBracketed(query string, start int) (int, bool) {
	index := start + 1
	for index < len(query) {
		switch query[index] {
		case ']':
			if index+1 < len(query) && query[index+1] == ']' {
				index += 2
				continue
			}
			if index == start+1 {
				return 0, false
			}
			return index + 1, true
		}
		index++
	}
	return 0, false
}

func skipQuotedIdentifier(query string, start int) (int, bool) {
	index := start + 1
	for index < len(query) {
		if query[index] == '"' {
			if index+1 < len(query) && query[index+1] == '"' {
				index += 2
				continue
			}
			if index == start+1 {
				return 0, false
			}
			return index + 1, true
		}
		index++
	}
	return 0, false
}

func isRoutineIdentifierStart(char byte) bool {
	return char == '_' || char == '@' || char == '#' ||
		(char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func isRoutineIdentifierByte(char byte) bool {
	return isRoutineIdentifierStart(char) || isASCIIDigit(char) || char == '$'
}

// rewriteRoutine applies the in-force profile's convenience rule. No profile
// (or a disabled rule) leaves the statement untouched.
func (c *conn) rewriteRoutine(query string) string {
	profile, verified := c.mode.profile()
	if !verified {
		return query
	}
	return profile.rewriteRoutine(query)
}
