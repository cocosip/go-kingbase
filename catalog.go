package gokb

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
}

// lookup resolves a type OID to its catalog identity, following domains to
// their base type.
func (c *typeCatalog) lookup(oid uint32) (schema, name string, ok bool) {
	info, ok := c.resolve(oid)
	return info.schema, info.name, ok
}

// nameOnly returns an unqualified name from a two-column fallback catalog.
func (c *typeCatalog) nameOnly(oid uint32) (string, bool) {
	name, ok := c.names[oid]
	return name, ok
}

// resolve returns the catalog entry for a type OID, following domains to their
// base type so callers also see its length and kind.
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
		oid, err := catalogInt(dest[0])
		if err != nil {
			return fmt.Errorf("type catalog has a non-numeric OID: %w", err)
		}
		if !full {
			if dest[1] == nil {
				return fmt.Errorf("type catalog has a NULL type name")
			}
			c.names[uint32(oid)] = catalogString(dest[1])
			continue
		}
		if dest[1] == nil || dest[2] == nil {
			return fmt.Errorf("type catalog has a NULL namespace or type name")
		}
		length, _ := catalogInt(dest[4])
		base, _ := catalogInt(dest[5])
		element, _ := catalogInt(dest[6])
		relation, _ := catalogInt(dest[7])
		c.byOID[uint32(oid)] = typeInfo{
			schema:      catalogString(dest[1]),
			name:        catalogString(dest[2]),
			kind:        kindByte(catalogString(dest[3])),
			length:      int16(length),
			baseOID:     uint32(base),
			elementOID:  uint32(element),
			relationOID: uint32(relation),
			delimiter:   kindByte(catalogString(dest[8])),
		}
	}
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

func kindByte(kind string) byte {
	if kind == "" {
		return 0
	}
	return kind[0]
}
