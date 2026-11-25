package kingbase

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// TestExplicitSequenceAccessPinsConnection pins nextval/currval to one
// database/sql connection and keeps generated-ID capture separate: the
// sequence calls use explicit caller SQL, never a MAX/last-sequence guess.
func TestExplicitSequenceAccessPinsConnection(t *testing.T) {
	const nextSQL = `SELECT nextval('orders_id_seq')`
	const currSQL = `SELECT currval('orders_id_seq')`
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		column := wire.ColumnDescription{Name: "value", TypeOID: 20, TypeLen: 8, TypeMod: -1, Format: 0}
		if !serveUserSelect(peer, nextSQL, column, []byte("42")) {
			return
		}
		if !serveUserSelect(peer, currSQL, column, []byte("42")) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		query, err := wire.DecodeQuery(payload)
		if err != nil || query != "SELECT 1" {
			peer.Errorf("query = %q err %v", query, err)
			return
		}
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = connection.Close() }()

	var next, current int64
	if err := connection.QueryRowContext(ctx, nextSQL).Scan(&next); err != nil {
		t.Fatalf("nextval: %v", err)
	}
	if err := connection.QueryRowContext(ctx, currSQL).Scan(&current); err != nil {
		t.Fatalf("currval: %v", err)
	}
	if next != 42 || current != 42 {
		t.Errorf("sequence values = %d/%d, want 42/42", next, current)
	}

	result, err := connection.ExecContext(ctx, "SELECT 1")
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if _, err := result.LastInsertId(); !errors.Is(err, ErrLastInsertIDUnavailable) {
		t.Errorf("LastInsertId error = %v, want ErrLastInsertIDUnavailable", err)
	}
}
