package gokb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"unicode/utf8"
)

// identityQuery is the fixed bounded candidate for raw product/kernel identity
// (V16). The reported string is retained unparsed: a release is never fabricated
// from it without live evidence.
const identityQuery = "SELECT version()"

// maxReleaseBytes bounds how much of the raw version string is retained, so a
// hostile server cannot make a connection hold unbounded diagnostic text.
const maxReleaseBytes = 4096

// IdentityStatus reports whether a connection captured its raw identity.
type IdentityStatus uint8

const (
	// IdentityUnknown is the zero value: no identity probe has completed.
	IdentityUnknown IdentityStatus = iota
	// IdentityCaptured means a raw, non-empty release was retained.
	IdentityCaptured
	// IdentityUnavailable means the probe failed without making the session
	// unusable; IdentityCause records why.
	IdentityUnavailable
)

// ServerMetadata is the diagnostics recorded for one physical connection. All
// strings are raw server reports; nothing is inferred from type names.
type ServerMetadata struct {
	// ConfiguredMode is the compatibilityMode the caller configured.
	ConfiguredMode CompatibilityMode
	// Mode is the effective mode after server evidence, or the explicit
	// declaration when discovery was unavailable.
	Mode CompatibilityMode
	// ModeSource records how Mode was established.
	ModeSource ModeSource
	// ModeVerified reports whether a validated server report established Mode.
	ModeVerified bool
	// WireProtocol is the configured transport.
	WireProtocol WireProtocol
	// Profile is the verified mode-profile name in force, empty when the mode
	// has no verified profile.
	Profile string
	// IdentityStatus reports whether the raw identity probe succeeded.
	IdentityStatus IdentityStatus
	// IdentityCause is the bounded probe failure, when IdentityUnavailable.
	IdentityCause string
	// ServerVersion is the raw startup server_version, when reported.
	ServerVersion string
	// ServerVersionNum is the raw startup server_version_num, when reported.
	ServerVersionNum string
	// Release is the raw, bounded SELECT version() report.
	Release string
	// ReleaseTruncated reports that Release was cut to the retention bound.
	ReleaseTruncated bool
}

// captureIdentity runs the bounded identity candidate inside the connect budget.
// A failed probe is retained as diagnostics and does not fail the connection:
// no accepted profile requires this identity yet (V16). A retired session or an
// IO failure still fails initialization, because then nothing can continue.
func (c *conn) captureIdentity(ctx context.Context) error {
	rows, err := c.querySimple(ctx, identityQuery)
	if err != nil {
		return c.recordIdentityUnavailable(err)
	}
	dest := make([]driver.Value, 1)
	nextErr := rows.Next(dest)
	closeErr := rows.Close()
	if nextErr != nil {
		return c.recordIdentityUnavailable(nextErr)
	}
	if closeErr != nil {
		// A drain failure leaves the session desynchronized.
		return fmt.Errorf("kingbase: capture server identity: %w", closeErr)
	}
	release, ok := dest[0].(string)
	switch {
	case !ok:
		return c.recordIdentityUnavailable(fmt.Errorf("server identity has an unexpected value %T", dest[0]))
	case release == "":
		return c.recordIdentityUnavailable(errors.New("server identity report is empty"))
	}
	if len(release) > maxReleaseBytes {
		release = truncateRunes(release, maxReleaseBytes)
		c.releaseTruncated = true
	}
	c.release = release
	c.identityStatus = IdentityCaptured
	return nil
}

// recordIdentityUnavailable keeps a failed identity probe as diagnostics while
// the session stays usable. A retired session is a transport failure and still
// aborts initialization.
func (c *conn) recordIdentityUnavailable(cause error) error {
	if c.netConn == nil {
		return fmt.Errorf("kingbase: capture server identity: %w", cause)
	}
	c.identityStatus = IdentityUnavailable
	c.identityCause = boundedValue(cause.Error())
	return nil
}

// truncateRunes cuts s to at most limit bytes without splitting a UTF-8 rune.
func truncateRunes(s string, limit int) string {
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

// serverMetadata builds the diagnostic view of this connection.
func (c *conn) serverMetadata() ServerMetadata {
	profile, ok := c.mode.profile()
	profileName := ""
	if ok {
		profileName = profile.name
	}
	return ServerMetadata{
		ConfiguredMode:   c.cfg.CompatibilityMode,
		Mode:             c.mode,
		ModeSource:       c.modeSource,
		ModeVerified:     c.modeVerified,
		WireProtocol:     c.cfg.WireProtocol,
		Profile:          profileName,
		IdentityStatus:   c.identityStatus,
		IdentityCause:    c.identityCause,
		ServerVersion:    c.params["server_version"],
		ServerVersionNum: c.params["server_version_num"],
		Release:          c.release,
		ReleaseTruncated: c.releaseTruncated,
	}
}

// ConnectionMetadata returns the diagnostics recorded for the physical
// connection leased by c. It must be called while c is open, before Close, and
// works through database/sql's Raw hook:
//
//	sc, err := db.Conn(ctx)
//	meta, err := gokb.ConnectionMetadata(sc)
func ConnectionMetadata(c *sql.Conn) (ServerMetadata, error) {
	if c == nil {
		return ServerMetadata{}, errors.New("kingbase: nil connection")
	}
	var metadata ServerMetadata
	err := c.Raw(func(driverConn any) error {
		physical, ok := driverConn.(interface{ serverMetadata() ServerMetadata })
		if !ok {
			return fmt.Errorf("kingbase: connection %T is not a kingbase physical connection", driverConn)
		}
		metadata = physical.serverMetadata()
		return nil
	})
	if err != nil {
		return ServerMetadata{}, err
	}
	return metadata, nil
}
