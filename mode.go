package gokb

import "strings"

// modeProfile is the verified semantic contract for one compatibility mode.
// Wire framing, server identity and session lexical settings stay in the
// connection context; only behaviour with offline-verified evidence is
// represented here, so an unverified mode never fabricates semantics.
type modeProfile struct {
	// identifierQuotes holds the bytes that open a quoted identifier. The bytes
	// already reserved by the literal scanner (', -, /, E, e, $) are not
	// accepted here.
	identifierQuotes string
}

// pgIdentifierQuotes is the PostgreSQL baseline: only double-quoted identifiers.
const pgIdentifierQuotes = `"`

// pgModeProfile carries the verified PG quoting rules; positional placeholders
// stay the driver's own documented rules ($n native, ? convenience) whenever a
// profile is present.
var pgModeProfile = modeProfile{identifierQuotes: pgIdentifierQuotes}

// profile returns the profile in force for the configured mode. ModeAuto is a
// bootstrap baseline: the transport is configured as PG and no authoritative
// server mode evidence exists yet, so it uses the PG profile instead of
// guessing another mode. ModePG is verified; every other mode is unverified and
// returns no profile, so callers must not rewrite or reinterpret its statements.
func (m CompatibilityMode) profile() (modeProfile, bool) {
	switch m {
	case ModeAuto, ModePG:
		return pgModeProfile, true
	default:
		return modeProfile{}, false
	}
}

// quotesIdentifier reports whether the profile treats char as an opening
// identifier quote.
func (p modeProfile) quotesIdentifier(char byte) bool {
	return strings.IndexByte(p.identifierQuotes, char) >= 0
}
