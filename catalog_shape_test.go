package kingbase

import (
	"strconv"
	"strings"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// serveCatalogOnly answers the catalog query with the supplied rows. The
// protocol auto-responders still handle identity and mode probes, which happen
// before the catalog query.
func serveCatalogOnly(peer *testpeer.Conn, rows [][][]byte) {
	peer.DisableAutoCatalog()
	if _, err := peer.ReadStartup(); err != nil {
		return
	}
	if err := peer.StartupOK(nil); err != nil {
		return
	}
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return
	}
	_ = peer.RowDescription(testpeer.CatalogColumns())
	for _, row := range rows {
		_ = peer.DataRow(row)
	}
	_ = peer.CommandComplete("SELECT " + strconv.Itoa(len(rows)))
	_ = peer.ReadyForQuery(wire.StatusIdle)
	_, _, _ = peer.ReadMessage()
}

func TestCatalogRejectsMalformedShapeRows(t *testing.T) {
	base := testpeer.CatalogRows()
	cases := []struct {
		name string
		row  [][]byte
		want string
	}{
		{"zero OID", userCatalogRow(0, "app", "zero", 'b', 4, 0), "outside the uint32 range"},
		{"out-of-range OID", func() [][]byte {
			row := userCatalogRow(70040, "app", "big", 'b', 4, 0)
			row[0] = []byte("4294967296")
			return row
		}(), "outside the uint32 range"},
		{"NULL OID", func() [][]byte {
			row := userCatalogRow(70041, "app", "null_oid", 'b', 4, 0)
			row[0] = nil
			return row
		}(), "malformed OID"},
		{"non-numeric OID", func() [][]byte {
			row := userCatalogRow(70042, "app", "text_oid", 'b', 4, 0)
			row[0] = []byte("not-a-number")
			return row
		}(), "not a valid integer"},
		{"out-of-range length", func() [][]byte {
			row := userCatalogRow(70043, "app", "bad_length", 'b', 4, 0)
			row[4] = []byte("-32769")
			return row
		}(), "out-of-range length"},
		{"upper length bound", func() [][]byte {
			row := userCatalogRow(70044, "app", "big_length", 'b', 4, 0)
			row[4] = []byte("32768")
			return row
		}(), "out-of-range length"},
		{"NULL length", func() [][]byte {
			row := userCatalogRow(70045, "app", "null_length", 'b', 4, 0)
			row[4] = nil
			return row
		}(), "length is NULL"},
		{"multi-byte kind", func() [][]byte {
			row := userCatalogRow(70046, "app", "bad_kind", 'b', 4, 0)
			row[3] = []byte("bb")
			return row
		}(), "malformed kind"},
		{"empty kind", func() [][]byte {
			row := userCatalogRow(70047, "app", "empty_kind", 'b', 4, 0)
			row[3] = []byte{}
			return row
		}(), "malformed kind"},
		{"NULL kind", func() [][]byte {
			row := userCatalogRow(70048, "app", "null_kind", 'b', 4, 0)
			row[3] = nil
			return row
		}(), "NULL namespace, name, kind or delimiter"},
		{"multi-byte delimiter", func() [][]byte {
			row := userCatalogRow(70049, "app", "bad_delim", 'b', 4, 0)
			row[8] = []byte(",,")
			return row
		}(), "malformed delimiter"},
		{"NULL delimiter", func() [][]byte {
			row := userCatalogRow(70050, "app", "null_delim", 'b', 4, 0)
			row[8] = nil
			return row
		}(), "NULL namespace, name, kind or delimiter"},
		{"empty name", userCatalogRow(70051, "app", "", 'b', 4, 0), "empty namespace or name"},
		{"empty schema", userCatalogRow(70052, "", "empty_schema", 'b', 4, 0), "empty namespace or name"},
		{"NULL namespace", func() [][]byte {
			row := userCatalogRow(70053, "app", "null_schema", 'b', 4, 0)
			row[1] = nil
			return row
		}(), "NULL namespace, name, kind or delimiter"},
		{"negative base link", func() [][]byte {
			row := userCatalogRow(70054, "app", "bad_link", 'd', 4, 0)
			row[5] = []byte("-1")
			return row
		}(), "out-of-range link"},
		{"out-of-range element link", func() [][]byte {
			row := userCatalogRow(70055, "app", "bad_element", 'b', 4, 0)
			row[6] = []byte("4294967296")
			return row
		}(), "out-of-range link"},
		{"out-of-range relation link", func() [][]byte {
			row := userCatalogRow(70056, "app", "bad_relation", 'b', 4, 0)
			row[7] = []byte("4294967296")
			return row
		}(), "out-of-range link"},
		{"duplicate OID", userCatalogRow(70010, "app", "collision", 'b', 4, 0), "repeats OID"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rows := append(append([][][]byte{}, base...), testCase.row)
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				serveCatalogOnly(peer, rows)
			})
			_, err := connectPeer(t, srv, "")
			if err == nil || !strings.Contains(err.Error(), "type catalog") ||
				!strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Connect error = %v, want a type-catalog rejection containing %q", err, testCase.want)
			}
		})
	}
}

func TestCatalogRejectsEmptyFullCatalog(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		serveCatalogOnly(peer, nil)
	})
	_, err := connectPeer(t, srv, "")
	if err == nil || !strings.Contains(err.Error(), "returned no type rows") {
		t.Fatalf("Connect error = %v, want an empty full catalog rejection", err)
	}
}

func TestCatalogDuplicateQualifiedNameDisablesCodec(t *testing.T) {
	extra := [][][]byte{
		userCatalogRow(70030, "pg_catalog", testTypeInt4, 'b', 4, 0),
	}
	columns := []wire.ColumnDescription{
		{Name: "ambiguous", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
	}
	row := [][]byte{[]byte("7")}
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		serveUserTypeCatalog(peer, extra, columns, row)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	typed, dest := queryScriptedColumns(t, physical, "SELECT ambiguous", 1)
	if got, ok := dest[0].(string); !ok || got != "7" {
		t.Errorf("ambiguous pg_catalog.int4 = %T(%v), want an opaque string", dest[0], dest[0])
	}
	if name := typed.ColumnTypeDatabaseTypeName(0); !strings.EqualFold(name, pgTypeInt4) {
		t.Errorf("ambiguous DatabaseTypeName = %q, want the honest catalog name int4", name)
	}
	if length, ok := typed.ColumnTypeLength(0); ok {
		t.Errorf("ambiguous length = %d, want unknown", length)
	}
}

func TestCatalogBrokenDomainLinksStayOpaque(t *testing.T) {
	extra := [][][]byte{
		userCatalogRow(70030, "app", "cycle_a", 'd', 4, 70031),
		userCatalogRow(70031, "app", "cycle_b", 'd', 4, 70030),
		userCatalogRow(70032, "app", "missing_base", 'd', 4, 999999),
		userCatalogRow(70033, "app", "int", 'b', 4, 0),
		userCatalogRow(70034, "app", "year", 'b', 2, 0),
		userCatalogRow(70035, "app", "bit", 'b', 1, 0),
	}
	columns := []wire.ColumnDescription{
		{Name: "cycle", TypeOID: 70030, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "missing", TypeOID: 70032, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "int", TypeOID: 70033, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "year", TypeOID: 70034, TypeLen: 2, TypeMod: -1, Format: 0},
		{Name: pgTypeBit, TypeOID: 70035, TypeLen: 1, TypeMod: -1, Format: 0},
	}
	row := [][]byte{[]byte("5"), []byte("5"), []byte("5"), []byte("5"), []byte("5")}
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		serveUserTypeCatalog(peer, extra, columns, row)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	if physical.mode != ModePG {
		t.Errorf("mode = %s, want the server-reported pg", physical.mode)
	}
	typed, dest := queryScriptedColumns(t, physical, "SELECT broken", len(columns))
	for i, value := range dest {
		if got, ok := value.(string); !ok || got != "5" {
			t.Errorf("column %d = %T(%v), want an opaque string", i, value, value)
		}
	}
	for i := 0; i < 2; i++ {
		if name := typed.ColumnTypeDatabaseTypeName(i); name != "" {
			t.Errorf("column %d DatabaseTypeName = %q, want empty for an unresolvable link", i, name)
		}
	}
	// user-defined int/year/bit keep their honest names but never select a
	// builtin codec or change the server-reported mode.
	for i, want := range []string{"int", "year", "bit"} {
		if name := typed.ColumnTypeDatabaseTypeName(i + 2); !strings.EqualFold(name, want) {
			t.Errorf("column %d DatabaseTypeName = %q, want %q", i+2, name, want)
		}
	}
}

func TestBuiltinShapeMismatchDisablesCodec(t *testing.T) {
	cases := []struct {
		name  string
		info  typeInfo
		valid bool
	}{
		{"int4 correct", typeInfo{schema: schemaPGCatalog, name: pgTypeInt4, kind: 'b', length: 4}, true},
		{"int4 wrong length", typeInfo{schema: schemaPGCatalog, name: pgTypeInt4, kind: 'b', length: 8}, false},
		{"int4 wrong kind", typeInfo{schema: schemaPGCatalog, name: pgTypeInt4, kind: 'd', length: 4}, false},
		{"unknown name", typeInfo{schema: schemaPGCatalog, name: "not_builtin", kind: 'b', length: 4}, false},
		{"varchar variable length", typeInfo{schema: schemaPGCatalog, name: "varchar", kind: 'b', length: -1}, true},
		{"varchar fixed length", typeInfo{schema: schemaPGCatalog, name: "varchar", kind: 'b', length: 4}, false},
		{"bit variable length", typeInfo{schema: schemaPGCatalog, name: pgTypeBit, kind: 'b', length: -1}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			catalog := &typeCatalog{byOID: map[uint32]typeInfo{23: testCase.info}}
			connection := &conn{catalog: catalog}
			_, _, ok := connection.codecName(23)
			if ok != testCase.valid {
				t.Errorf("codecName = %t, want %t", ok, testCase.valid)
			}
		})
	}
}
