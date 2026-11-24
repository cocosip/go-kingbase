package gokb

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
		{16, "t", true, "bool", reflect.TypeOf(true)},
		{20, "42", int64(42), "int8", reflect.TypeOf(int64(0))},
		{701, "1.5", float64(1.5), "float8", reflect.TypeOf(float64(0))},
		{1700, "1.25", "1.25", "numeric", reflect.TypeOf("")},
		{25, testHello, testHello, "text", reflect.TypeOf("")},
		{17, `\x48656c6c6f`, []byte("Hello"), "bytea", reflect.TypeOf([]byte(nil))},
		{1082, "2024-01-02", time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), "date", reflect.TypeOf(time.Time{})},
		{1114, "2024-01-02 03:04:05.123456", time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC), "timestamp", reflect.TypeOf(time.Time{})},
		{1184, "2024-01-02 03:04:05.123456+08", time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.FixedZone("", 8*3600)), "timestamptz", reflect.TypeOf(time.Time{})},
		{2950, "550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440000", "uuid", reflect.TypeOf("")},
		{3802, `{"a":1}`, `{"a":1}`, "jsonb", reflect.TypeOf("")},
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
		if got, want := typeNamer.ColumnTypeDatabaseTypeName(i), tc.typeName; got != want {
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
	if name := result.(driver.RowsColumnTypeDatabaseTypeName).ColumnTypeDatabaseTypeName(0); name != "int4" {
		t.Errorf("domain type name = %q, want base int4", name)
	}
	dest := make([]driver.Value, 1)
	if err := result.Next(dest); err != nil || dest[0] != int64(5) {
		t.Fatalf("row = %v, err %v, want int64(5)", dest, err)
	}
	_ = result.Close()
	_ = physical.Close()
}
