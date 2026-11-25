package kingbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// catalogWithoutOID returns a full catalog fixture with one type removed, as a
// server would look before that type existed.
func catalogWithoutOID(oid uint32) [][][]byte {
	want := strconv.FormatUint(uint64(oid), 10)
	rows := make([][][]byte, 0, len(testpeer.CatalogRows()))
	for _, row := range testpeer.CatalogRows() {
		if string(row[0]) == want {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

// serveRefreshCatalog answers one catalog snapshot and then a sequence of
// simple queries on the same physical connection. The snapshot is fixed for the
// life of the connection; a DDL-style change only appears on a new connection.
func serveRefreshCatalog(peer *testpeer.Conn, rows [][][]byte) {
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
	_ = peer.RowDescription(testpeer.CatalogColumns())
	for _, row := range rows {
		_ = peer.DataRow(row)
	}
	_ = peer.CommandComplete("SELECT " + strconv.Itoa(len(rows)))
	_ = peer.ReadyForQuery(wire.StatusIdle)
	for {
		typ, payload, err := peer.ReadMessage()
		if err != nil {
			return
		}
		if typ == wire.MsgTerminate {
			return
		}
		if typ != wire.MsgQuery {
			peer.Errorf("unexpected message %q after catalog", typ)
			return
		}
		query, err := wire.DecodeQuery(payload)
		if err != nil || !strings.Contains(query, "SELECT n") {
			peer.Errorf("unexpected query %q: %v", query, err)
			return
		}
		_ = peer.RowDescription([]wire.ColumnDescription{
			{Name: "n", TypeOID: 20, TypeLen: 8, TypeMod: -1, Format: 0},
		})
		_ = peer.DataRow([][]byte{[]byte("42")})
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
	}
}

func TestCatalogRefreshRequiresNewPhysicalConnection(t *testing.T) {
	oldRows := catalogWithoutOID(20) // pg_catalog.int8 does not exist yet
	fullRows := testpeer.CatalogRows()
	var connections atomic.Int32
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		rows := oldRows
		if connections.Add(1) > 1 {
			rows = fullRows
		}
		serveRefreshCatalog(peer, rows)
	})

	first, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	firstResult, err := first.querySimple(context.Background(), "SELECT n")
	if err != nil {
		t.Fatalf("first query: %v", err)
	}
	firstRows, ok := firstResult.(*rows)
	if !ok {
		t.Fatalf("first query returned %T, want *rows", firstResult)
	}
	firstDest := make([]driver.Value, 1)
	if err := firstRows.Next(firstDest); err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if got, ok := firstDest[0].(string); !ok || got != "42" {
		t.Fatalf("stale unknown OID = %T(%v), want an opaque string", firstDest[0], firstDest[0])
	}
	if name := firstRows.ColumnTypeDatabaseTypeName(0); name != "" {
		t.Errorf("stale unknown DatabaseTypeName = %q, want empty", name)
	}
	if err := firstRows.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := first.ResetSession(context.Background()); err != nil {
		t.Fatalf("ResetSession: %v", err)
	}
	afterReset, err := first.querySimple(context.Background(), "SELECT n")
	if err != nil {
		t.Fatalf("query after ResetSession: %v", err)
	}
	if err := afterReset.Close(); err != nil {
		t.Fatalf("close query after ResetSession: %v", err)
	}
	_ = first.Close()
	second, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	defer func() { _ = second.Close() }()
	secondResult, err := second.querySimple(context.Background(), "SELECT n")
	if err != nil {
		t.Fatalf("second query: %v", err)
	}
	secondRows, ok := secondResult.(*rows)
	if !ok {
		t.Fatalf("second query returned %T, want *rows", secondResult)
	}
	secondDest := make([]driver.Value, 1)
	if err := secondRows.Next(secondDest); err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if got, ok := secondDest[0].(int64); !ok || got != 42 {
		t.Fatalf("refreshed OID = %T(%v), want int64(42)", secondDest[0], secondDest[0])
	}
	if name := secondRows.ColumnTypeDatabaseTypeName(0); !strings.EqualFold(name, "int8") {
		t.Errorf("refreshed DatabaseTypeName = %q, want int8", name)
	}
}

func TestPoolReuseDoesNotRefreshCatalog(t *testing.T) {
	oldRows := catalogWithoutOID(20)
	fullRows := testpeer.CatalogRows()
	var connections atomic.Int32
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		rows := oldRows
		if connections.Add(1) > 1 {
			rows = fullRows
		}
		serveRefreshCatalog(peer, rows)
	})

	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	var value any
	if err := sc.QueryRowContext(ctx, "SELECT n").Scan(&value); err != nil {
		t.Fatalf("first QueryRow: %v", err)
	}
	if got, ok := value.(string); !ok || got != "42" {
		t.Fatalf("pooled stale value = %T(%v), want an opaque string", value, value)
	}
	if err := sc.Close(); err != nil {
		t.Fatalf("sql.Conn.Close: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT n").Scan(&value); err != nil {
		t.Fatalf("reused QueryRow: %v", err)
	}
	if got, ok := value.(string); !ok || got != "42" {
		t.Fatalf("pool reuse refreshed the catalog: %T(%v)", value, value)
	}
	if got := connections.Load(); got != 1 {
		t.Errorf("physical connections = %d, want the pooled reuse of one", got)
	}
}
