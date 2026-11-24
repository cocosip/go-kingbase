package gokb

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// catalogQuery is the qualified pg_catalog query that loads the type
// namespace before any user SQL. It is used by F-215/F-217 and requires SELECT
// on pg_type and pg_namespace; a failure is a catalog error, not a silent
// fallback.
const catalogQuery = `SELECT t.oid, n.nspname, t.typname, t.typtype, t.typlen,
       t.typbasetype, t.typelem, t.typrelid, t.typdelim
FROM pg_catalog.pg_type AS t
INNER JOIN pg_catalog.pg_namespace AS n ON n.oid = t.typnamespace`

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

type typeCatalog struct {
	byOID map[uint32]typeInfo
}

// lookup resolves a type OID to its catalog identity, following domains to
// their base type.
func (c *typeCatalog) lookup(oid uint32) (schema, name string, ok bool) {
	for depth := 0; depth < maxDomainDepth; depth++ {
		info, found := c.byOID[oid]
		if !found {
			return "", "", false
		}
		if info.kind != 'd' || info.baseOID == 0 || info.baseOID == oid {
			return info.schema, info.name, true
		}
		oid = info.baseOID
	}
	return "", "", false
}

// loadCatalog reads the server type namespace inside the connection budget.
// Catalog failure aborts connection setup instead of returning a connection
// that cannot name its result types.
func (c *conn) loadCatalog(ctx context.Context) error {
	rows, err := c.querySimple(ctx, catalogQuery)
	if err != nil {
		return fmt.Errorf("kingbase: load type catalog: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	catalog := &typeCatalog{byOID: make(map[uint32]typeInfo, 512)}
	dest := make([]driver.Value, 9)
	for {
		if err := rows.Next(dest); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("kingbase: read type catalog: %w", err)
		}
		if len(catalog.byOID) >= maxCatalogTypes {
			return fmt.Errorf("kingbase: type catalog exceeds %d entries", maxCatalogTypes)
		}
		oid, err := catalogInt(dest[0])
		if err != nil {
			return fmt.Errorf("kingbase: type catalog has a non-numeric OID: %w", err)
		}
		length, _ := catalogInt(dest[4])
		base, _ := catalogInt(dest[5])
		element, _ := catalogInt(dest[6])
		relation, _ := catalogInt(dest[7])
		catalog.byOID[uint32(oid)] = typeInfo{
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
	c.catalog = catalog
	return nil
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
