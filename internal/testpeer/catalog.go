package testpeer

import (
	"fmt"
	"strings"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// scriptedType is one row of the scripted pg_type catalog.
type scriptedType struct {
	oid     int64
	schema  string
	name    string
	kind    byte
	length  int64
	base    int64
	element int64
	relid   int64
	delim   byte
}

// scriptedSchema is the catalog schema every scripted row uses.
const scriptedSchema = "pg_catalog"

// scriptedTypes mirrors the PostgreSQL catalog identities the driver decodes
// offline. OIDs are test data, not built into the driver.
var scriptedTypes = []scriptedType{
	{16, scriptedSchema, "bool", 'b', 1, 0, 0, 0, ','},
	{1560, scriptedSchema, "bit", 'b', -1, 0, 0, 0, ','},
	{1562, scriptedSchema, "varbit", 'b', -1, 0, 0, 0, ','},
	{17, scriptedSchema, "bytea", 'b', -1, 0, 0, 0, ','},
	{18, scriptedSchema, "char", 'b', 1, 0, 0, 0, ','},
	{19, scriptedSchema, "name", 'b', 64, 0, 0, 0, ','},
	{20, scriptedSchema, "int8", 'b', 8, 0, 0, 0, ','},
	{21, scriptedSchema, "int2", 'b', 2, 0, 0, 0, ','},
	{23, scriptedSchema, "int4", 'b', 4, 0, 0, 0, ','},
	{25, scriptedSchema, "text", 'b', -1, 0, 0, 0, ','},
	{26, scriptedSchema, "oid", 'b', 4, 0, 0, 0, ','},
	{114, scriptedSchema, "json", 'b', -1, 0, 0, 0, ','},
	{142, scriptedSchema, "xml", 'b', -1, 0, 0, 0, ','},
	{650, scriptedSchema, "cidr", 'b', -1, 0, 0, 0, ','},
	{700, scriptedSchema, "float4", 'b', 4, 0, 0, 0, ','},
	{701, scriptedSchema, "float8", 'b', 8, 0, 0, 0, ','},
	{705, scriptedSchema, "unknown", 'p', -2, 0, 0, 0, ','},
	{869, scriptedSchema, "inet", 'b', -1, 0, 0, 0, ','},
	{1042, scriptedSchema, "bpchar", 'b', -1, 0, 0, 0, ','},
	{1043, scriptedSchema, "varchar", 'b', -1, 0, 0, 0, ','},
	{1082, scriptedSchema, "date", 'b', 4, 0, 0, 0, ','},
	{1083, scriptedSchema, "time", 'b', 8, 0, 0, 0, ','},
	{1114, scriptedSchema, "timestamp", 'b', 8, 0, 0, 0, ','},
	{1184, scriptedSchema, "timestamptz", 'b', 8, 0, 0, 0, ','},
	{1186, scriptedSchema, "interval", 'b', 16, 0, 0, 0, ','},
	{1266, scriptedSchema, "timetz", 'b', 12, 0, 0, 0, ','},
	{1700, scriptedSchema, "numeric", 'b', -1, 0, 0, 0, ','},
	{2950, scriptedSchema, "uuid", 'b', 16, 0, 0, 0, ','},
	{3802, scriptedSchema, "jsonb", 'b', -1, 0, 0, 0, ','},
	// Array identities keep their element link. Arrays stay opaque text for
	// results; the explicit Array Scanner/Valuer consumes that text.
	{1007, scriptedSchema, "_int4", 'b', -1, 0, 23, 0, ','},
	// A domain over an array resolves through its validated base link and stays
	// opaque text for results.
	{70010, "app", "int4_list", 'd', -1, 1007, 0, 0, ','},
}

// catalogColumns describes the scripted catalog result.
func catalogColumns() []wire.ColumnDescription {
	return []wire.ColumnDescription{
		{Name: "oid", TypeOID: 26, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "nspname", TypeOID: 19, TypeLen: 64, TypeMod: -1, Format: 0},
		{Name: "typname", TypeOID: 19, TypeLen: 64, TypeMod: -1, Format: 0},
		{Name: "typtype", TypeOID: 18, TypeLen: 1, TypeMod: -1, Format: 0},
		{Name: "typlen", TypeOID: 21, TypeLen: 2, TypeMod: -1, Format: 0},
		{Name: "typbasetype", TypeOID: 26, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "typelem", TypeOID: 26, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "typrelid", TypeOID: 26, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "typdelim", TypeOID: 18, TypeLen: 1, TypeMod: -1, Format: 0},
	}
}

// DisableAutoCatalog lets a script observe the raw catalog query.
func (c *Conn) DisableAutoCatalog() {
	c.autoCatalog = false
}

// CatalogColumns exposes the scripted catalog shape to scripts that answer the
// query themselves.
func CatalogColumns() []wire.ColumnDescription {
	return catalogColumns()
}

// CatalogRows returns the scripted catalog rows, for scripts that answer the
// query themselves.
func CatalogRows() [][][]byte {
	rows := make([][][]byte, len(scriptedTypes))
	for i, typ := range scriptedTypes {
		rows[i] = [][]byte{
			[]byte(fmt.Sprintf("%d", typ.oid)),
			[]byte(typ.schema),
			[]byte(typ.name),
			[]byte{typ.kind},
			[]byte(fmt.Sprintf("%d", typ.length)),
			[]byte(fmt.Sprintf("%d", typ.base)),
			[]byte(fmt.Sprintf("%d", typ.element)),
			[]byte(fmt.Sprintf("%d", typ.relid)),
			[]byte{typ.delim},
		}
	}
	return rows
}

func isCatalogQuery(payload []byte) bool {
	sql, err := wire.DecodeQuery(payload)
	if err != nil {
		return false
	}
	return strings.Contains(sql, "pg_catalog.pg_type") && strings.Contains(sql, "pg_catalog.pg_namespace")
}

func (c *Conn) serveCatalog() error {
	if err := c.RowDescription(catalogColumns()); err != nil {
		return err
	}
	for _, row := range CatalogRows() {
		if err := c.DataRow(row); err != nil {
			return err
		}
	}
	if err := c.CommandComplete(fmt.Sprintf("SELECT %d", len(scriptedTypes))); err != nil {
		return err
	}
	return c.ReadyForQuery(wire.StatusIdle)
}
