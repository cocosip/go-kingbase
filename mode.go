package kingbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
)

// modeProfile is the verified semantic contract for one compatibility mode.
// Wire framing, server identity and session lexical settings stay in the
// connection context; only behaviour with offline-verified evidence is
// represented here, so an unverified mode never fabricates semantics.
type modeProfile struct {
	// name is the verified profile name reported in diagnostics.
	name string
	// identifierQuotes holds the bytes that open a quoted identifier. The bytes
	// already reserved by the literal scanner (', -, /, E, e, $) are not
	// accepted here.
	identifierQuotes string
	// bracketQuotes enables SQLServer-style [identifier] components.
	bracketQuotes bool
	// routineConvenience enables the profile-gated bare-procedure rewrite.
	routineConvenience bool
}

// pgIdentifierQuotes is the PostgreSQL baseline: only double-quoted identifiers.
const pgIdentifierQuotes = `"`

// pgModeProfile carries the verified PG quoting rules; positional placeholders
// stay the driver's own documented rules ($n native, ? convenience) whenever a
// profile is present.
var pgModeProfile = modeProfile{name: "pg", identifierQuotes: pgIdentifierQuotes}

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

// ModeSource labels where an effective mode came from.
type ModeSource uint8

const (
	// ModeSourceExplicit is a declaration with no server report (unverified).
	ModeSourceExplicit ModeSource = iota
	// ModeSourceStartup is a validated startup database_mode report.
	ModeSourceStartup
	// ModeSourceQuery is a validated mode-discovery query result.
	ModeSourceQuery
	// ModeSourceUnavailable is a probe chain that produced no report.
	ModeSourceUnavailable
)

// String renders the provenance label used in diagnostics.
func (s ModeSource) String() string {
	switch s {
	case ModeSourceStartup:
		return "startup"
	case ModeSourceQuery:
		return "query"
	case ModeSourceUnavailable:
		return "unavailable"
	default:
		return "explicit"
	}
}

// serverMode parses the authoritative database_mode setting. Only modes with
// known semantics are accepted; anything else is a malformed report.
func serverMode(value string) (CompatibilityMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pg":
		return ModePG, nil
	case "oracle":
		return ModeOracle, nil
	case "mysql":
		return ModeMySQL, nil
	case "sqlserver":
		return ModeSQLServer, nil
	default:
		return ModeAuto, fmt.Errorf("unknown database_mode %s", boundedValue(value))
	}
}

// resolveMode establishes the effective mode from server evidence and checks it
// against an explicit declaration. Auto with no evidence fails; Auto never
// defaults to PG and never infers a mode from type names.
func (c *conn) resolveMode(ctx context.Context) error {
	declared := c.cfg.CompatibilityMode
	if report, ok := c.params["database_mode"]; ok && strings.TrimSpace(report) != "" {
		actual, err := serverMode(report)
		if err != nil {
			return fmt.Errorf("kingbase: startup report has %w", err)
		}
		return c.adoptMode(actual, ModeSourceStartup, declared)
	}
	if declared != ModeAuto {
		c.mode, c.modeSource, c.modeVerified = declared, ModeSourceExplicit, false
		return nil
	}
	actual, source, evidence, err := c.probeMode(ctx)
	if err != nil {
		return err
	}
	if source == ModeSourceUnavailable {
		if evidence != "" {
			return fmt.Errorf("kingbase: server did not report database_mode (%s); set compatibilityMode explicitly", evidence)
		}
		return fmt.Errorf("kingbase: server did not report database_mode; set compatibilityMode explicitly")
	}
	return c.adoptMode(actual, source, declared)
}

// adoptMode records the effective mode after the declaration check, so a
// conflicting explicit mode fails with expected and actual values.
func (c *conn) adoptMode(actual CompatibilityMode, source ModeSource, declared CompatibilityMode) error {
	if declared != ModeAuto && declared != actual {
		return fmt.Errorf("kingbase: compatibilityMode %s conflicts with server database_mode %s reported by %s", declared, actual, source)
	}
	c.mode, c.modeSource, c.modeVerified = actual, source, true
	return nil
}

// probeMode runs the bounded mode-discovery chain: the qualified pg_settings
// candidate, then SHOW database_mode. Missing metadata means "candidate
// unsupported" and continues the chain; permission, timeout and malformed causes
// are retained so absence is never reported for them. The returned evidence
// names each candidate's outcome when no report exists.
func (c *conn) probeMode(ctx context.Context) (CompatibilityMode, ModeSource, string, error) {
	const settingsQuery = `SELECT name, setting FROM pg_catalog.pg_settings
 WHERE name IN ('database_mode','server_version','server_version_num')`
	var evidence []string
	rows, err := c.querySimple(ctx, settingsQuery)
	if err != nil {
		if !missingProbeObject(err) {
			return ModeAuto, 0, "", fmt.Errorf("kingbase: mode discovery: %w", err)
		}
		evidence = append(evidence, "pg_settings: "+sqlstate(err))
	} else {
		mode, found, readErr := readSettingsMode(rows)
		closeErr := rows.Close()
		if readErr != nil {
			return ModeAuto, 0, "", readErr
		}
		if closeErr != nil {
			return ModeAuto, 0, "", fmt.Errorf("kingbase: mode discovery: %w", closeErr)
		}
		if found {
			return mode, ModeSourceQuery, "", nil
		}
		evidence = append(evidence, "pg_settings: no database_mode row")
	}
	rows, err = c.querySimple(ctx, "SHOW database_mode")
	if err != nil {
		if missingProbeObject(err) {
			evidence = append(evidence, "SHOW database_mode: "+sqlstate(err))
			return ModeAuto, ModeSourceUnavailable, strings.Join(evidence, "; "), nil
		}
		return ModeAuto, 0, "", fmt.Errorf("kingbase: mode discovery: %w", err)
	}
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		_ = rows.Close()
		if errors.Is(err, io.EOF) {
			evidence = append(evidence, "SHOW database_mode: no rows")
			return ModeAuto, ModeSourceUnavailable, strings.Join(evidence, "; "), nil
		}
		return ModeAuto, 0, "", fmt.Errorf("kingbase: read database_mode: %w", err)
	}
	value, ok := dest[0].(string)
	closeErr := rows.Close()
	if !ok {
		return ModeAuto, 0, "", fmt.Errorf("kingbase: database_mode has an unexpected type %T", dest[0])
	}
	if closeErr != nil {
		return ModeAuto, 0, "", fmt.Errorf("kingbase: mode discovery: %w", closeErr)
	}
	mode, err := serverMode(value)
	if err != nil {
		return ModeAuto, 0, "", fmt.Errorf("kingbase: query report has %w", err)
	}
	return mode, ModeSourceQuery, "", nil
}

// sqlstate renders the SQLSTATE of a server error for probe evidence.
func sqlstate(err error) string {
	var serverErr *Error
	if errors.As(err, &serverErr) && serverErr.Code != "" {
		return "SQLSTATE " + serverErr.Code
	}
	return "unsupported"
}

// readSettingsMode extracts database_mode from a pg_settings result. A server
// that reports only version settings is unsupported, not a mode.
func readSettingsMode(rows driver.Rows) (CompatibilityMode, bool, error) {
	dest := make([]driver.Value, 2)
	var reported CompatibilityMode
	found := false
	for {
		if err := rows.Next(dest); err != nil {
			if errors.Is(err, io.EOF) {
				return reported, found, nil
			}
			return ModeAuto, false, fmt.Errorf("kingbase: read pg_settings: %w", err)
		}
		name, ok := dest[0].(string)
		if !ok {
			return ModeAuto, false, fmt.Errorf("kingbase: pg_settings name has an unexpected type %T", dest[0])
		}
		if !strings.EqualFold(strings.TrimSpace(name), "database_mode") {
			continue
		}
		setting, ok := dest[1].(string)
		if !ok {
			return ModeAuto, false, fmt.Errorf("kingbase: pg_settings database_mode has an unexpected type %T", dest[1])
		}
		mode, err := serverMode(setting)
		if err != nil {
			return ModeAuto, false, fmt.Errorf("kingbase: pg_settings %w", err)
		}
		if found && mode != reported {
			return ModeAuto, false, fmt.Errorf("kingbase: pg_settings reports conflicting database_mode values")
		}
		reported, found = mode, true
	}
}

// missingProbeObject reports an unsupported discovery candidate: a missing
// relation, column or object. Permission, timeout and other causes stay errors.
func missingProbeObject(err error) bool {
	var serverErr *Error
	if !errors.As(err, &serverErr) {
		return false
	}
	switch serverErr.Code {
	case "42P01", "42703", "42883", "3F000", "42704":
		return true
	default:
		return false
	}
}
