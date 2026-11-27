package kingbase

import (
	"context"
	"database/sql/driver"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// testTypeInt4 is the type name shared by the registry and codec tests.
const testTypeInt4 = "int4"

// testTypeUint4 is the MySQL-mode unsigned type name shared by the mode and
// binary-COPY mapping tests.
const testTypeUint4 = "uint4"

// userCatalogRow builds one scripted pg_type row in the catalog column order.
func userCatalogRow(oid uint32, schema, name string, kind byte, length int16, base uint32) [][]byte {
	return [][]byte{
		[]byte(strconv.FormatUint(uint64(oid), 10)),
		[]byte(schema),
		[]byte(name),
		[]byte{kind},
		[]byte(strconv.Itoa(int(length))),
		[]byte(strconv.FormatUint(uint64(base), 10)),
		[]byte("0"),
		[]byte("0"),
		[]byte{','},
	}
}

// serveUserTypeCatalog answers the catalog query with the default fixture plus
// extra rows, then one user statement with explicitly typed columns.
func serveUserTypeCatalog(peer *testpeer.Conn, extra [][][]byte, columns []wire.ColumnDescription, row [][]byte) {
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
	rows := append(testpeer.CatalogRows(), extra...)
	for _, catalogRow := range rows {
		_ = peer.DataRow(catalogRow)
	}
	_ = peer.CommandComplete("SELECT " + strconv.Itoa(len(rows)))
	_ = peer.ReadyForQuery(wire.StatusIdle)
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return
	}
	_ = peer.RowDescription(columns)
	_ = peer.DataRow(row)
	_ = peer.CommandComplete("SELECT 1")
	_ = peer.ReadyForQuery(wire.StatusIdle)
	waitTerminate(peer)
}

func queryScriptedColumns(t *testing.T, physical *conn, query string, width int) (*rows, []driver.Value) {
	t.Helper()
	result, err := physical.QueryContext(context.Background(), query, nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	typed, ok := result.(*rows)
	if !ok {
		t.Fatalf("QueryContext returned %T, want *rows", result)
	}
	t.Cleanup(func() { _ = typed.Close() })
	dest := make([]driver.Value, width)
	if err := typed.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	return typed, dest
}

func TestUserTypesNeverUseBuiltinCodecs(t *testing.T) {
	extra := [][][]byte{
		userCatalogRow(70001, "app", testTypeInt4, 'b', 4, 0),
		userCatalogRow(70002, "app", "bool", 'b', 1, 0),
		userCatalogRow(70003, "app", "bytea", 'b', -1, 0),
		userCatalogRow(70004, "app", "bit", 'b', -1, 0),
	}
	columns := []wire.ColumnDescription{
		{Name: "a", TypeOID: 70001, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "b", TypeOID: 70002, TypeLen: 1, TypeMod: -1, Format: 0},
		{Name: "c", TypeOID: 70003, TypeLen: -1, TypeMod: -1, Format: 1},
		{Name: "d", TypeOID: 70004, TypeLen: -1, TypeMod: -1, Format: 1},
		{Name: "e", TypeOID: 70001, TypeLen: 4, TypeMod: -1, Format: 1},
	}
	row := [][]byte{
		[]byte("7"), []byte("t"), {1, 2}, {1}, {0, 0, 0, 9},
	}
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		serveUserTypeCatalog(peer, extra, columns, row)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	typed, dest := queryScriptedColumns(t, physical, "SELECT user_types", 5)
	if got, ok := dest[0].(string); !ok || got != "7" {
		t.Errorf("app.int4 text = %T(%v), want an opaque string", dest[0], dest[0])
	}
	if got, ok := dest[1].(string); !ok || got != "t" {
		t.Errorf("app.bool text = %T(%v), want an opaque string", dest[1], dest[1])
	}
	if got, ok := dest[2].([]byte); !ok || len(got) != 2 {
		t.Errorf("app.bytea binary = %T(%v), want opaque bytes", dest[2], dest[2])
	}
	if got, ok := dest[3].([]byte); !ok || len(got) != 1 {
		t.Errorf("app.bit binary = %T(%v), want opaque bytes", dest[3], dest[3])
	}
	if got, ok := dest[4].([]byte); !ok || len(got) != 4 {
		t.Errorf("app.int4 binary = %T(%v), want opaque bytes", dest[4], dest[4])
	}
	wantNames := []string{testTypeInt4, "bool", "bytea", "bit", testTypeInt4}
	wantScans := []reflect.Type{
		reflect.TypeOf(""), reflect.TypeOf(""),
		reflect.TypeOf([]byte(nil)), reflect.TypeOf([]byte(nil)), reflect.TypeOf([]byte(nil)),
	}
	for i := range dest {
		if typed.ColumnTypeScanType(i) != wantScans[i] {
			t.Errorf("column %d ScanType = %v, want %v", i, typed.ColumnTypeScanType(i), wantScans[i])
		}
		if name := typed.ColumnTypeDatabaseTypeName(i); !strings.EqualFold(name, wantNames[i]) {
			t.Errorf("column %d DatabaseTypeName = %q, want the actual catalog name %q", i, name, wantNames[i])
		}
		if _, ok := typed.ColumnTypeLength(i); ok {
			t.Errorf("column %d reports a length for a user type", i)
		}
		if _, _, ok := typed.ColumnTypePrecisionScale(i); ok {
			t.Errorf("column %d reports precision for a user type", i)
		}
	}
}

func TestBuiltinIdentityRequiresQualifiedBaseShape(t *testing.T) {
	extra := [][][]byte{
		userCatalogRow(70020, "app", testTypeInt4, 'b', 4, 0),
		userCatalogRow(70011, "pg_catalog", "int4_other", 'c', 4, 0),
		userCatalogRow(70012, "app", "int4_domain", 'd', 4, 23),
		userCatalogRow(70013, "app", "user_domain", 'd', 4, 70020),
	}
	columns := []wire.ColumnDescription{
		{Name: "builtin", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "user", TypeOID: 70020, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "wrong_shape", TypeOID: 70011, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "domain", TypeOID: 70012, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "user_domain", TypeOID: 70013, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "unknown", TypeOID: 705, TypeLen: -2, TypeMod: -1, Format: 1},
	}
	row := [][]byte{[]byte("7"), []byte("7"), []byte("7"), []byte("7"), []byte("7"), {1, 2}}
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		serveUserTypeCatalog(peer, extra, columns, row)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	typed, dest := queryScriptedColumns(t, physical, "SELECT identities", 6)
	if got, ok := dest[0].(int64); !ok || got != 7 {
		t.Errorf("pg_catalog.int4 = %T(%v), want int64", dest[0], dest[0])
	}
	if got, ok := dest[1].(string); !ok || got != "7" {
		t.Errorf("app.int4 = %T(%v), want an opaque string", dest[1], dest[1])
	}
	if got, ok := dest[2].(string); !ok || got != "7" {
		t.Errorf("composite pg_catalog.int4_other = %T(%v), want an opaque string", dest[2], dest[2])
	}
	if got, ok := dest[3].(int64); !ok || got != 7 {
		t.Errorf("domain over int4 = %T(%v), want the verified base decoding", dest[3], dest[3])
	}
	if got, ok := dest[4].(string); !ok || got != "7" {
		t.Errorf("domain over app.int4 = %T(%v), want an opaque string", dest[4], dest[4])
	}
	if got, ok := dest[5].([]byte); !ok || len(got) != 2 {
		t.Errorf("pseudo unknown = %T(%v), want opaque bytes", dest[5], dest[5])
	}
	wantNames := []string{testTypeInt4, testTypeInt4, "int4_other", testTypeInt4, testTypeInt4, "unknown"}
	for i, want := range wantNames {
		if name := typed.ColumnTypeDatabaseTypeName(i); !strings.EqualFold(name, want) {
			t.Errorf("column %d DatabaseTypeName = %q, want the actual catalog name %q", i, name, want)
		}
	}
	if length, ok := typed.ColumnTypeLength(0); !ok || length != 4 {
		t.Errorf("builtin int4 length = %d %v, want 4", length, ok)
	}
	if _, ok := typed.ColumnTypeLength(2); ok {
		t.Error("composite pg_catalog.int4 reports a length")
	}
}

func TestOpaqueScanTypesFollowWireFormat(t *testing.T) {
	extra := [][][]byte{
		userCatalogRow(70020, "app", testTypeInt4, 'b', 4, 0),
	}
	columns := []wire.ColumnDescription{
		{Name: "unknown_text", TypeOID: 999998, TypeLen: -1, TypeMod: -1, Format: 0},
		{Name: "unknown_binary", TypeOID: 999999, TypeLen: -1, TypeMod: -1, Format: 1},
		{Name: "array_binary", TypeOID: 1007, TypeLen: -1, TypeMod: -1, Format: 1},
		{Name: "date_binary", TypeOID: 1082, TypeLen: 4, TypeMod: -1, Format: 1},
		{Name: "user_binary", TypeOID: 70020, TypeLen: 4, TypeMod: -1, Format: 1},
	}
	row := [][]byte{
		[]byte("raw"), {1, 2}, {0, 0, 0, 0}, {0, 0, 0, 1}, {0, 0, 0, 5},
	}
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		serveUserTypeCatalog(peer, extra, columns, row)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	typed, dest := queryScriptedColumns(t, physical, "SELECT opaque", 5)
	if got, ok := dest[0].(string); !ok || got != "raw" {
		t.Errorf("unknown text = %T(%v), want string", dest[0], dest[0])
	}
	if got, ok := dest[1].([]byte); !ok || len(got) != 2 {
		t.Errorf("unknown binary = %T(%v), want bytes", dest[1], dest[1])
	}
	if got, ok := dest[2].([]byte); !ok || len(got) != 4 {
		t.Errorf("array binary = %T(%v), want opaque bytes", dest[2], dest[2])
	}
	if _, ok := dest[3].(time.Time); !ok {
		t.Errorf("date binary = %T(%v), want time.Time", dest[3], dest[3])
	}
	if got, ok := dest[4].([]byte); !ok || len(got) != 4 {
		t.Errorf("app.int4 binary = %T(%v), want opaque bytes", dest[4], dest[4])
	}
	wantScan := []reflect.Type{
		reflect.TypeOf(""), reflect.TypeOf([]byte(nil)), reflect.TypeOf([]byte(nil)),
		reflect.TypeOf(time.Time{}), reflect.TypeOf([]byte(nil)),
	}
	wantNames := []string{"", "", "_int4", pgTypeDate, testTypeInt4}
	for i := range dest {
		if got := typed.ColumnTypeScanType(i); got != wantScan[i] {
			t.Errorf("column %d ScanType = %v, want %v", i, got, wantScan[i])
		}
		if got := typed.ColumnTypeDatabaseTypeName(i); !strings.EqualFold(got, wantNames[i]) {
			t.Errorf("column %d DatabaseTypeName = %q, want %q", i, got, wantNames[i])
		}
	}
}
