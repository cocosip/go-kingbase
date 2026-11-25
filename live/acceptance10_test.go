//go:build live

package live_test

import (
	"context"
	"testing"
	"time"
)

func TestLiveCatalogBuiltinShapes(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, `SELECT t.typname, t.typtype, t.typlen, t.typdelim
		FROM pg_catalog.pg_type AS t
		JOIN pg_catalog.pg_namespace AS n ON n.oid = t.typnamespace
		WHERE n.nspname = 'pg_catalog'
		  AND t.typname IN ('bool', 'bytea', 'char', 'name', 'int2', 'int4', 'int8', 'oid',
		      'xid', 'cid', 'text', 'json', 'jsonb', 'float4', 'float8', 'money', 'bpchar',
		      'varchar', 'date', 'time', 'timetz', 'timestamp', 'timestamptz', 'interval',
		      'bit', 'varbit', 'numeric', 'uuid', 'xml', 'inet', 'cidr')`)
	if err != nil {
		t.Fatalf("catalog shape query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	expected := map[string]struct {
		kind   string
		length int64
	}{
		"bool": {"b", 1}, "bytea": {"b", -1}, "char": {"b", 1}, "name": {"b", 64},
		"int2": {"b", 2}, "int4": {"b", 4}, "int8": {"b", 8}, "oid": {"b", 4},
		"xid": {"b", 4}, "cid": {"b", 4}, "text": {"b", -1}, "json": {"b", -1},
		"jsonb": {"b", -1}, "float4": {"b", 4}, "float8": {"b", 8}, "money": {"b", 8},
		"bpchar": {"b", -1}, "varchar": {"b", -1}, "date": {"b", 4}, "time": {"b", 8},
		"timetz": {"b", 12}, "timestamp": {"b", 8}, "timestamptz": {"b", 8},
		"interval": {"b", 16}, "bit": {"b", -1}, "varbit": {"b", -1},
		"numeric": {"b", -1}, "uuid": {"b", 16}, "xml": {"b", -1},
		"inet": {"b", -1}, "cidr": {"b", -1},
	}
	seen := make(map[string]bool, len(expected))
	for rows.Next() {
		var name, kind, delimiter string
		var length int64
		if err := rows.Scan(&name, &kind, &length, &delimiter); err != nil {
			t.Fatalf("shape scan: %v", err)
		}
		want, ok := expected[name]
		if !ok {
			continue
		}
		seen[name] = true
		if kind != want.kind || length != want.length || delimiter != "," {
			t.Errorf("%s shape = %q/%d/%q, want %q/%d/\",\"", name, kind, length, delimiter, want.kind, want.length)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("shape rows: %v", err)
	}
	for name := range expected {
		if !seen[name] {
			t.Errorf("builtin type %s is missing from the catalog", name)
		}
	}
}
