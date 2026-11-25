package kingbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestCatalogQueryIsQualified(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.DisableAutoCatalog()
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		sql, err := wire.DecodeQuery(payload)
		if err != nil || !strings.Contains(sql, "pg_catalog.pg_type AS t") ||
			!strings.Contains(sql, "pg_catalog.pg_namespace AS n") ||
			!strings.Contains(sql, "typbasetype") {
			peer.Errorf("catalog SQL = %q, err %v", sql, err)
			return
		}
		if err := peer.RowDescription(testpeer.CatalogColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		for _, row := range testpeer.CatalogRows() {
			if err := peer.DataRow(row); err != nil {
				peer.Errorf("DataRow: %v", err)
				return
			}
		}
		if err := peer.CommandComplete("SELECT 27"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription(selectColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("7"), []byte("z")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.CommandComplete("SELECT 1"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if labels := physical.typeLabels(); labels[23] != "pg_catalog.int4" {
		t.Errorf("catalog[23] = %q, want pg_catalog.int4", labels[23])
	}
	result, err := physical.QueryContext(context.Background(), "SELECT 7", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 2)
	if err := result.Next(dest); err != nil || dest[0] != int64(7) {
		t.Fatalf("row = %v, err %v, want int64(7)", dest, err)
	}
	_ = result.Close()
	_ = physical.Close()
}

func TestCatalogFailureIsClassified(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.DisableAutoCatalog()
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverityFatal},
			wire.ErrorField{Code: wire.ErrorCode, Value: "42501"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "permission denied for table pg_type"},
		); err != nil {
			peer.Errorf("ErrorResponse: %v", err)
			return
		}
		_ = peer.ReadyForQuery(wire.StatusIdle)
	})

	_, err := connectPeer(t, srv, "")
	if err == nil || !strings.Contains(err.Error(), "type catalog") {
		t.Fatalf("error = %v, want classified catalog error", err)
	}
	var serverErr *Error
	if !errors.As(err, &serverErr) || serverErr.Code != "42501" {
		t.Fatalf("error = %v, want preserved SQLSTATE 42501", err)
	}
}

func TestRowsCoreTextCodecs(t *testing.T) {
	type codecCase struct {
		oid      uint32
		raw      string
		want     driver.Value
		typeName string
		scanType reflect.Type
	}
	cases := []codecCase{
		{16, "t", true, testTypeBool, reflect.TypeOf(true)},
		{20, "42", int64(42), "int8", reflect.TypeOf(int64(0))},
		{701, "1.5", float64(1.5), "float8", reflect.TypeOf(float64(0))},
		{1700, "1.25", "1.25", pgTypeNumeric, reflect.TypeOf("")},
		{25, testHello, testHello, "text", reflect.TypeOf("")},
		{17, `\x48656c6c6f`, []byte("Hello"), "bytea", reflect.TypeOf([]byte(nil))},
		{1082, "2024-01-02", time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), "date", reflect.TypeOf(time.Time{})},
		{1114, "2024-01-02 03:04:05.123456", time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC), "timestamp", reflect.TypeOf(time.Time{})},
		{1184, "2024-01-02 03:04:05.123456+08", time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.FixedZone("", 8*3600)), "timestamptz", reflect.TypeOf(time.Time{})},
		{2950, "550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440000", "uuid", reflect.TypeOf("")},
		{3802, testJSONBody, testJSONBody, "jsonb", reflect.TypeOf("")},
		{1186, "1 day 02:03:04", "1 day 02:03:04", "interval", reflect.TypeOf("")},
	}
	columns := make([]wire.ColumnDescription, len(cases))
	values := make([][]byte, len(cases))
	for i, tc := range cases {
		columns[i] = wire.ColumnDescription{Name: fmt.Sprintf("c%d", i), TypeOID: tc.oid, TypeLen: -1, TypeMod: -1, Format: 0}
		values[i] = []byte(tc.raw)
	}

	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription(columns); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow(values); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.CommandComplete("SELECT 1"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SELECT * FROM codecs", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	typeNamer, ok := result.(driver.RowsColumnTypeDatabaseTypeName)
	if !ok {
		t.Fatal("rows do not expose database type names")
	}
	scanTyper, ok := result.(driver.RowsColumnTypeScanType)
	if !ok {
		t.Fatal("rows do not expose scan types")
	}
	dest := make([]driver.Value, len(cases))
	if err := result.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	for i, tc := range cases {
		if got, want := typeNamer.ColumnTypeDatabaseTypeName(i), strings.ToUpper(tc.typeName); got != want {
			t.Errorf("column %d type name = %q, want %q", i, got, want)
		}
		if got, want := scanTyper.ColumnTypeScanType(i), tc.scanType; got != want {
			t.Errorf("column %d scan type = %v, want %v", i, got, want)
		}
		switch want := tc.want.(type) {
		case time.Time:
			got, isTime := dest[i].(time.Time)
			if !isTime || !got.Equal(want) {
				t.Errorf("column %d = %#v, want %v", i, dest[i], want)
			}
		case []byte:
			got, isBytes := dest[i].([]byte)
			if !isBytes || string(got) != string(want) {
				t.Errorf("column %d = %#v, want %q", i, dest[i], want)
			}
		default:
			if dest[i] != tc.want {
				t.Errorf("column %d = %#v, want %#v", i, dest[i], tc.want)
			}
		}
	}
	_ = result.Close()
	_ = physical.Close()
}

func TestRowsUnknownOIDFallback(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: "mystery", TypeOID: 999999, TypeLen: -1, TypeMod: -1, Format: 0},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("raw")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.CommandComplete("SELECT 1"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SELECT mystery FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	if name := result.(driver.RowsColumnTypeDatabaseTypeName).ColumnTypeDatabaseTypeName(0); name != "" {
		t.Errorf("unknown OID type name = %q, want empty", name)
	}
	if scan := result.(driver.RowsColumnTypeScanType).ColumnTypeScanType(0); scan != reflect.TypeOf("") {
		t.Errorf("unknown OID scan type = %v, want string", scan)
	}
	dest := make([]driver.Value, 1)
	if err := result.Next(dest); err != nil || dest[0] != "raw" {
		t.Fatalf("row = %v, err %v, want raw string", dest, err)
	}
	_ = result.Close()
	_ = physical.Close()
}

func TestCatalogDomainResolution(t *testing.T) {
	const domainOID = 6000
	rows := append(testpeer.CatalogRows(), [][]byte{
		[]byte("6000"), []byte("public"), []byte("positive_int"), []byte("d"),
		[]byte("4"), []byte("23"), []byte("0"), []byte("0"), []byte(","),
	})

	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.DisableAutoCatalog()
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription(testpeer.CatalogColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		for _, row := range rows {
			if err := peer.DataRow(row); err != nil {
				peer.Errorf("DataRow: %v", err)
				return
			}
		}
		_ = peer.CommandComplete("SELECT 28")
		_ = peer.ReadyForQuery(wire.StatusIdle)

		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: "n", TypeOID: domainOID, TypeLen: 4, TypeMod: -1, Format: 0},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("5")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SELECT n FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	if name := result.(driver.RowsColumnTypeDatabaseTypeName).ColumnTypeDatabaseTypeName(0); name != strings.ToUpper(testTypeInt4) {
		t.Errorf("domain type name = %q, want base INT4", name)
	}
	dest := make([]driver.Value, 1)
	if err := result.Next(dest); err != nil || dest[0] != int64(5) {
		t.Fatalf("row = %v, err %v, want int64(5)", dest, err)
	}
	_ = result.Close()
	_ = physical.Close()
}

// serveCatalogProbe reads one catalog candidate query, checks its SQL contains
// want, and replies with a classified missing-object error.
func serveCatalogProbe(peer *testpeer.Conn, want, code string) bool {
	payload, ok := expectMessage(peer, wire.MsgQuery)
	if !ok {
		return false
	}
	if sql, err := wire.DecodeQuery(payload); err != nil || !strings.Contains(sql, want) {
		peer.Errorf("catalog probe sql = %q err %v, want %q", sql, err, want)
		return false
	}
	_ = peer.ErrorResponse(
		wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverityFatal},
		wire.ErrorField{Code: wire.ErrorCode, Value: code},
		wire.ErrorField{Code: wire.ErrorMessage, Value: "catalog object missing"},
	)
	return peer.ReadyForQuery(wire.StatusIdle) == nil
}

// serveCatalogSuccess reads one candidate query, checks its SQL contains want,
// and answers with the scripted full-shape rows.
func serveCatalogSuccess(peer *testpeer.Conn, want string) bool {
	payload, ok := expectMessage(peer, wire.MsgQuery)
	if !ok {
		return false
	}
	if sql, err := wire.DecodeQuery(payload); err != nil || !strings.Contains(sql, want) {
		peer.Errorf("catalog sql = %q err %v, want %q", sql, err, want)
		return false
	}
	_ = peer.RowDescription(testpeer.CatalogColumns())
	for _, row := range testpeer.CatalogRows() {
		_ = peer.DataRow(row)
	}
	_ = peer.CommandComplete(fmt.Sprintf("SELECT %d", len(testpeer.CatalogRows())))
	return peer.ReadyForQuery(wire.StatusIdle) == nil
}

// serveUserSelect reads one simple query, checks it equals want, and answers a
// single typed column.
func serveUserSelect(peer *testpeer.Conn, want string, column wire.ColumnDescription, value []byte) bool {
	payload, ok := expectMessage(peer, wire.MsgQuery)
	if !ok {
		return false
	}
	if sql, err := wire.DecodeQuery(payload); err != nil || sql != want {
		peer.Errorf("user sql = %q err %v, want %q", sql, err, want)
		return false
	}
	_ = peer.RowDescription([]wire.ColumnDescription{column})
	_ = peer.DataRow([][]byte{value})
	_ = peer.CommandComplete("SELECT 1")
	return peer.ReadyForQuery(wire.StatusIdle) == nil
}

func serveSysFallbackScript(peer *testpeer.Conn) {
	peer.DisableAutoCatalog()
	if _, err := peer.ReadStartup(); err != nil {
		return
	}
	if err := peer.StartupOK(nil); err != nil {
		return
	}
	if !serveCatalogProbe(peer, "pg_catalog.pg_type", "42P01") {
		return
	}
	if !serveCatalogSuccess(peer, "sys_catalog.sys_type") {
		return
	}
	waitTerminate(peer)
}

func serveNamesOnlyScript(peer *testpeer.Conn) {
	peer.DisableAutoCatalog()
	if _, err := peer.ReadStartup(); err != nil {
		return
	}
	if err := peer.StartupOK(nil); err != nil {
		return
	}
	if !serveCatalogProbe(peer, "pg_catalog.pg_type", "42703") {
		return
	}
	if !serveCatalogProbe(peer, "sys_catalog.sys_type", "42P01") {
		return
	}
	payload, ok := expectMessage(peer, wire.MsgQuery)
	if !ok {
		return
	}
	if sql, err := wire.DecodeQuery(payload); err != nil ||
		!strings.Contains(sql, "pg_catalog.pg_type") || !strings.Contains(sql, "typname") {
		peer.Errorf("names catalog sql = %q err %v", sql, err)
		return
	}
	_ = peer.RowDescription([]wire.ColumnDescription{
		{Name: "oid", TypeOID: 26, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "typname", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
	})
	_ = peer.DataRow([][]byte{[]byte("23"), []byte(testTypeInt4)})
	_ = peer.CommandComplete("SELECT 1")
	_ = peer.ReadyForQuery(wire.StatusIdle)
	if !serveUserSelect(peer, "SELECT 7",
		wire.ColumnDescription{Name: "n", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0}, []byte("7")) {
		return
	}
	if !serveUserSelect(peer, "SELECT 8",
		wire.ColumnDescription{Name: "n", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 1}, []byte{0, 0, 0, 8}) {
		return
	}
	waitTerminate(peer)
}

func serveAllMissingScript(peer *testpeer.Conn) {
	peer.DisableAutoCatalog()
	if _, err := peer.ReadStartup(); err != nil {
		return
	}
	if err := peer.StartupOK(nil); err != nil {
		return
	}
	if !serveCatalogProbe(peer, "pg_catalog.pg_type", "42P01") ||
		!serveCatalogProbe(peer, "sys_catalog.sys_type", "42P01") {
		return
	}
	if _, _, err := peer.ReadMessage(); err == nil {
		peer.Errorf("connection stayed open after exhausting catalog candidates")
	}
}

func TestCatalogFallbackToSysRelation(t *testing.T) {
	srv := testpeer.Start(t, serveSysFallbackScript)
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	if labels := physical.typeLabels(); labels[23] != "pg_catalog.int4" {
		t.Errorf("catalog[23] = %q, want the sys fallback to load full shape", labels[23])
	}
	if physical.catalogStatus != CatalogFull {
		t.Errorf("catalogStatus = %d, want full", physical.catalogStatus)
	}
}

func TestCatalogNamesOnlyFallbackNeverEnablesCodecs(t *testing.T) {
	srv := testpeer.Start(t, serveNamesOnlyScript)
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	if physical.catalogStatus != CatalogPartial || physical.catalogCause == "" {
		t.Fatalf("catalogStatus = %d cause %q, want a labelled partial catalog",
			physical.catalogStatus, physical.catalogCause)
	}
	if name, ok := physical.partialTypeName(23); !ok || name != testTypeInt4 {
		t.Fatalf("partial name = %q %v, want int4", name, ok)
	}
	if _, ok := physical.typeMetadata(23); ok {
		t.Fatal("names-only catalog exposed full metadata")
	}
	textRows := mustQueryRows(t, physical, "SELECT 7")
	assertOpaqueString(t, textRows, "7")
	binaryRows := mustQueryRows(t, physical, "SELECT 8")
	assertOpaqueBytes(t, binaryRows)
}

func mustQueryRows(t *testing.T, c *conn, query string) *rows {
	t.Helper()
	result, err := c.QueryContext(context.Background(), query, nil)
	if err != nil {
		t.Fatalf("QueryContext(%q): %v", query, err)
	}
	typed, ok := result.(*rows)
	if !ok {
		t.Fatalf("QueryContext returned %T, want *rows", result)
	}
	t.Cleanup(func() { _ = typed.Close() })
	return typed
}

func assertOpaqueString(t *testing.T, r *rows, want string) {
	t.Helper()
	dest := make([]driver.Value, 1)
	if err := r.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got, ok := dest[0].(string); !ok || got != want {
		t.Errorf("value = %T(%v), want the opaque string %q", dest[0], dest[0], want)
	}
	if r.ColumnTypeScanType(0) != reflect.TypeOf("") {
		t.Errorf("ScanType = %v, want string for an opaque fallback type", r.ColumnTypeScanType(0))
	}
	if r.ColumnTypeDatabaseTypeName(0) != strings.ToUpper(testTypeInt4) {
		t.Errorf("DatabaseTypeName = %q, want the fallback name", r.ColumnTypeDatabaseTypeName(0))
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close result: %v", err)
	}
}

func assertOpaqueBytes(t *testing.T, r *rows) {
	t.Helper()
	dest := make([]driver.Value, 1)
	if err := r.Next(dest); err != nil {
		t.Fatalf("binary Next: %v", err)
	}
	got, ok := dest[0].([]byte)
	if !ok || len(got) != 4 {
		t.Errorf("binary value = %T(%v), want opaque bytes", dest[0], dest[0])
	}
	if r.ColumnTypeScanType(0) != reflect.TypeOf([]byte(nil)) {
		t.Errorf("binary ScanType = %v, want bytes for an opaque fallback type", r.ColumnTypeScanType(0))
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close binary result: %v", err)
	}
}

func TestCatalogFallbackAggregatesMissingEvidence(t *testing.T) {
	srv := testpeer.Start(t, serveAllMissingScript)
	_, err := connectPeer(t, srv, "")
	if err == nil || !strings.Contains(err.Error(), "pg_catalog.pg_type: SQLSTATE 42P01") ||
		!strings.Contains(err.Error(), "sys_catalog.sys_type: SQLSTATE 42P01") {
		t.Fatalf("Connect = %v, want per-candidate missing-object evidence", err)
	}
}

// testCatalogCapture replays the scripted rows under the replay origin so the
// capture path is exercised without claiming a server capture.
func testCatalogCapture() testpeer.CatalogCapture {
	return testpeer.CatalogCapture{
		Provenance: testpeer.Provenance{Origin: testpeer.OriginReplay},
		Rows:       testpeer.CatalogRows(),
	}
}

func serveCapturedCatalog(peer *testpeer.Conn) {
	peer.DisableAutoCatalog()
	if _, err := peer.ReadStartup(); err != nil {
		return
	}
	if err := peer.StartupOK(nil); err != nil {
		return
	}
	for {
		message, payload, err := peer.ReadMessage()
		if err != nil {
			return
		}
		if message == wire.MsgQuery {
			sql, err := wire.DecodeQuery(payload)
			if err != nil || sql != catalogQuery {
				peer.Errorf("catalog query = %q err %v, want the recorded capture query", sql, err)
				return
			}
			break
		}
	}
	if err := peer.ServeCatalogCapture(testCatalogCapture()); err != nil {
		peer.Errorf("ServeCatalogCapture: %v", err)
		return
	}
	waitTerminate(peer)
}

func TestCatalogCaptureReplay(t *testing.T) {
	srv := testpeer.Start(t, serveCapturedCatalog)
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	if labels := physical.typeLabels(); labels[23] != "pg_catalog.int4" {
		t.Errorf("catalog[23] = %q, want the capture to load verified identities", labels[23])
	}
	if physical.catalogStatus != CatalogFull {
		t.Errorf("catalogStatus = %d, want full", physical.catalogStatus)
	}
}
