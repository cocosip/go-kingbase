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

// catalogQuery is the qualified pg_catalog query that loads the type
// namespace before any user SQL. It is used by F-215/F-217 and requires SELECT
// on pg_type and pg_namespace; a failure is a catalog error, not a silent
// fallback.
const catalogQuery = `SELECT t.oid, n.nspname, t.typname, t.typtype, t.typlen,
       t.typbasetype, t.typelem, t.typrelid, t.typdelim
FROM pg_catalog.pg_type AS t
INNER JOIN pg_catalog.pg_namespace AS n ON n.oid = t.typnamespace`

// sysCatalogQuery is the sys-catalog equivalent of catalogQuery, tried only for
// classified missing-object cases. V2 verifies the exact objects, columns and
// ordinary-role access before this candidate can be relied on.
const sysCatalogQuery = `SELECT t.oid, n.nspname, t.typname, t.typtype, t.typlen,
       t.typbasetype, t.typelem, t.typrelid, t.typdelim
FROM sys_catalog.sys_type AS t
INNER JOIN sys_catalog.sys_namespace AS n ON n.oid = t.typnamespace`

// catalogNamesQuery is the two-column fallback: names for diagnostics only,
// never enough to select a decoder or a builtin codec.
const catalogNamesQuery = `SELECT t.oid, t.typname FROM pg_catalog.pg_type AS t`

// maxCatalogTypes bounds catalog capture so a hostile or broken server cannot
// exhaust client memory during connect.
const maxCatalogTypes = 100000

// maxDomainDepth bounds domain resolution chains.
const maxDomainDepth = 16

// typeInfo is one server type mapped by its OID.
type typeInfo struct {
	schema      string
	name        string
	kind        byte
	length      int16
	baseOID     uint32
	elementOID  uint32
	relationOID uint32
	delimiter   byte
}

// CatalogStatus reports the shape a connection's catalog was built from.
type CatalogStatus uint8

const (
	// CatalogUnknown is the zero value: no catalog load has completed.
	CatalogUnknown CatalogStatus = iota
	// CatalogFull means the nine verified columns were loaded.
	CatalogFull
	// CatalogPartial means only unqualified names are available, so codecs,
	// lengths, precision and qualified labels stay disabled.
	CatalogPartial
)

type typeCatalog struct {
	byOID map[uint32]typeInfo
	// names holds unqualified identities from a two-column fallback; codecs and
	// metadata must not consume them as verified builtin identities.
	names map[uint32]string
	// ambiguous holds qualified identities that appeared more than once, so no
	// single OID may claim a builtin codec or verified shape for that name.
	ambiguous map[string]struct{}
}

const (
	maxCatalogOID    = int64(^uint32(0))
	minCatalogLength = -1 << 15 // int16 representability; shape is not interpreted
	maxCatalogLength = int64(^uint16(0) >> 1)
)

// resolve returns the catalog entry for a type OID, following domains to their
// base type so callers also see its length and kind. Broken or cyclic domain
// links resolve to nothing so codecs fall back to the safe opaque
// representation instead of guessing; display identity is still available.
func (c *typeCatalog) resolve(oid uint32) (typeInfo, bool) {
	for depth := 0; depth < maxDomainDepth; depth++ {
		info, found := c.byOID[oid]
		if !found {
			return typeInfo{}, false
		}
		if info.kind != 'd' || info.baseOID == 0 || info.baseOID == oid {
			return info, true
		}
		oid = info.baseOID
	}
	return typeInfo{}, false
}

func catalogNameKey(info typeInfo) string {
	return info.schema + "\x00" + info.name
}

// ambiguousIdentity reports whether more than one OID claimed this qualified
// name. The name is still honest for display, but no shape or codec may be
// selected from it.
func (c *typeCatalog) ambiguousIdentity(info typeInfo) bool {
	_, ambiguous := c.ambiguous[catalogNameKey(info)]
	return ambiguous
}

// nameOnly returns an unqualified name from a two-column fallback catalog.
func (c *typeCatalog) nameOnly(oid uint32) (string, bool) {
	name, ok := c.names[oid]
	return name, ok
}

// markAmbiguousNames records duplicate qualified identities. A catalog that
// names two OIDs the same way cannot prove which one owns a builtin codec.
func (c *typeCatalog) markAmbiguousNames() {
	c.ambiguous = make(map[string]struct{})
	seen := make(map[string]uint32, len(c.byOID))
	for oid, info := range c.byOID {
		key := catalogNameKey(info)
		if _, duplicate := seen[key]; duplicate {
			c.ambiguous[key] = struct{}{}
			continue
		}
		seen[key] = oid
	}
}

// loadCatalog reads the server type namespace inside the connection budget.
// Catalog failure aborts connection setup instead of returning a connection
// that cannot name its result types. Candidates are tried only for classified
// missing-object failures: permissions, timeouts and malformed responses abort
// instead of being hidden by a fallback. A names-only fallback completes
// initialization only as a labelled partial catalog whose codecs are disabled.
func (c *conn) loadCatalog(ctx context.Context) error {
	catalog := &typeCatalog{
		byOID: make(map[uint32]typeInfo, 512),
		names: make(map[uint32]string, 512),
	}
	var unavailable []string
	pgFull := catalogCandidate{label: "pg_catalog.pg_type", query: catalogQuery, full: true}
	loaded, pgColumnMissing, err := c.probeCatalog(ctx, catalog, pgFull, &unavailable)
	if err != nil {
		return err
	}
	if loaded {
		return c.useCatalog(catalog, CatalogFull, "")
	}
	sysFull := catalogCandidate{label: "sys_catalog.sys_type", query: sysCatalogQuery, full: true}
	loaded, _, err = c.probeCatalog(ctx, catalog, sysFull, &unavailable)
	if err != nil {
		return err
	}
	if loaded {
		return c.useCatalog(catalog, CatalogFull, "")
	}
	if pgColumnMissing {
		names := catalogCandidate{label: "pg_catalog.pg_type (names)", query: catalogNamesQuery}
		loaded, _, err = c.probeCatalog(ctx, catalog, names, &unavailable)
		if err != nil {
			return err
		}
		if loaded {
			return c.useCatalog(catalog, CatalogPartial, strings.Join(unavailable, "; "))
		}
	}
	return fmt.Errorf("kingbase: load type catalog: no usable relation (%s)", strings.Join(unavailable, "; "))
}

// useCatalog publishes the catalog together with its shape, so callers can tell
// a verified full catalog from a diagnostic names-only one.
func (c *conn) useCatalog(catalog *typeCatalog, status CatalogStatus, cause string) error {
	catalog.markAmbiguousNames()
	c.catalog = catalog
	c.catalogStatus = status
	c.catalogCause = cause
	return nil
}

// probeCatalog tries one candidate. A missing-object failure continues the
// chain only while the session is healthy; a column-level failure additionally
// allows the names-only fallback (reported by columnMissing).
func (c *conn) probeCatalog(ctx context.Context, catalog *typeCatalog, candidate catalogCandidate, unavailable *[]string) (loaded, columnMissing bool, err error) {
	rows, err := c.querySimple(ctx, candidate.query)
	if err != nil {
		if c.netConn == nil || !missingProbeObject(err) {
			return false, false, fmt.Errorf("kingbase: load type catalog: %w", err)
		}
		*unavailable = append(*unavailable, candidate.label+": "+sqlstate(err))
		return false, columnMissingError(err), nil
	}
	fillErr := catalog.fill(rows, candidate.full)
	closeErr := rows.Close()
	if fillErr != nil {
		return false, false, fmt.Errorf("kingbase: read type catalog: %w", fillErr)
	}
	if closeErr != nil {
		return false, false, fmt.Errorf("kingbase: load type catalog: %w", closeErr)
	}
	if candidate.full && len(catalog.byOID) == 0 {
		return false, false, fmt.Errorf("kingbase: load type catalog: %s returned no type rows", candidate.label)
	}
	return true, false, nil
}

// columnMissingError reports a missing column or function, the only failures
// that make a narrower names-only query over the same relation worthwhile.
func columnMissingError(err error) bool {
	var serverErr *Error
	if !errors.As(err, &serverErr) {
		return false
	}
	switch serverErr.Code {
	case "42703", "42883":
		return true
	default:
		return false
	}
}

// catalogCandidate is one ordered catalog probe. A full candidate carries the
// nine verified columns; a names-only candidate supplies unqualified names for
// diagnostics and can never enable a codec.
type catalogCandidate struct {
	label string
	query string
	full  bool
}

// fill consumes one candidate result. Full rows replace byOID; names-only rows
// record unqualified names that diagnostics may show but codecs must not use.
func (c *typeCatalog) fill(rows driver.Rows, full bool) error {
	width := 2
	if full {
		width = 9
	}
	dest := make([]driver.Value, width)
	for {
		if err := rows.Next(dest); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if len(c.byOID)+len(c.names) >= maxCatalogTypes {
			return fmt.Errorf("type catalog exceeds %d entries", maxCatalogTypes)
		}
		oid, err := catalogOID(dest[0])
		if err != nil {
			return err
		}
		if !full {
			if err := c.fillName(dest, oid); err != nil {
				return err
			}
			continue
		}
		if err := c.fillType(dest, oid); err != nil {
			return err
		}
	}
}

// catalogOID parses and range-checks a positive uint32 catalog OID.
func catalogOID(value driver.Value) (uint32, error) {
	oid, err := catalogFieldInt(value, "OID")
	if err != nil {
		return 0, fmt.Errorf("type catalog has a malformed OID: %w", err)
	}
	if oid <= 0 || oid > maxCatalogOID {
		return 0, fmt.Errorf("type catalog OID %d is outside the uint32 range", oid)
	}
	return uint32(oid), nil
}

// fillName records one unqualified name from the diagnostic-only fallback.
func (c *typeCatalog) fillName(dest []driver.Value, oid uint32) error {
	if dest[1] == nil {
		return fmt.Errorf("type catalog has a NULL type name")
	}
	name := catalogString(dest[1])
	if name == "" {
		return fmt.Errorf("type catalog has an empty type name")
	}
	if _, duplicate := c.names[oid]; duplicate {
		return fmt.Errorf("type catalog repeats OID %d", oid)
	}
	c.names[oid] = name
	return nil
}

// fillType validates and records one full-shape catalog row. Structural
// inconsistencies are rejected instead of truncating a field into a plausible
// but wrong shape; type-specific required shapes remain live-evidence work.
func (c *typeCatalog) fillType(dest []driver.Value, oid uint32) error {
	if dest[1] == nil || dest[2] == nil || dest[3] == nil || dest[8] == nil {
		return fmt.Errorf("type catalog has a NULL namespace, name, kind or delimiter")
	}
	schema, name := catalogString(dest[1]), catalogString(dest[2])
	kind, delimiter := catalogString(dest[3]), catalogString(dest[8])
	if schema == "" || name == "" {
		return fmt.Errorf("type catalog OID %d has an empty namespace or name", oid)
	}
	if len(kind) != 1 {
		return fmt.Errorf("type catalog OID %d has a malformed kind %s", oid, boundedValue(kind))
	}
	if len(delimiter) != 1 {
		return fmt.Errorf("type catalog OID %d has a malformed delimiter %s", oid, boundedValue(delimiter))
	}
	length, err := catalogFieldInt(dest[4], "length")
	if err != nil {
		return fmt.Errorf("type catalog OID %d: %w", oid, err)
	}
	if length < minCatalogLength || length > maxCatalogLength {
		return fmt.Errorf("type catalog OID %d has an out-of-range length %d", oid, length)
	}
	base, err := catalogFieldInt(dest[5], "base type")
	if err != nil {
		return fmt.Errorf("type catalog OID %d: %w", oid, err)
	}
	element, err := catalogFieldInt(dest[6], "element type")
	if err != nil {
		return fmt.Errorf("type catalog OID %d: %w", oid, err)
	}
	relation, err := catalogFieldInt(dest[7], "relation")
	if err != nil {
		return fmt.Errorf("type catalog OID %d: %w", oid, err)
	}
	if base < 0 || base > maxCatalogOID ||
		element < 0 || element > maxCatalogOID ||
		relation < 0 || relation > maxCatalogOID {
		return fmt.Errorf("type catalog OID %d has an out-of-range link (base %d, element %d, relation %d)",
			oid, base, element, relation)
	}
	if _, duplicate := c.byOID[oid]; duplicate {
		return fmt.Errorf("type catalog repeats OID %d", oid)
	}
	c.byOID[oid] = typeInfo{
		schema:      schema,
		name:        name,
		kind:        kind[0],
		length:      int16(length),
		baseOID:     uint32(base),
		elementOID:  uint32(element),
		relationOID: uint32(relation),
		delimiter:   delimiter[0],
	}
	return nil
}

// catalogFieldInt parses a required numeric catalog field and reports NULL
// instead of silently defaulting it to zero.
func catalogFieldInt(value driver.Value, field string) (int64, error) {
	if value == nil {
		return 0, fmt.Errorf("%s is NULL", field)
	}
	parsed, err := catalogInt(value)
	if err != nil {
		return 0, fmt.Errorf("%s is not a valid integer", field)
	}
	return parsed, nil
}

// catalogInt accepts both the bootstrap text decoding and typed decoding.
func catalogInt(value driver.Value) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected value %T", value)
	}
}

func catalogString(value driver.Value) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}
