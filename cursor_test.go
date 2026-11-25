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

func serveCursorFetch(peer *testpeer.Conn, want string, value []byte) bool {
	payload, ok := expectMessage(peer, wire.MsgQuery)
	if !ok {
		return false
	}
	if got, err := wire.DecodeQuery(payload); err != nil || got != want {
		peer.Errorf("FETCH statement = %q, err %v, want %q", got, err, want)
		return false
	}
	_ = peer.RowDescription([]wire.ColumnDescription{
		{Name: "n", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
	})
	_ = peer.DataRow([][]byte{value})
	_ = peer.CommandComplete("FETCH 1")
	return peer.ReadyForQuery(wire.StatusInTransaction) == nil
}

func serveCursorClose(peer *testpeer.Conn, want string) bool {
	payload, ok := expectMessage(peer, wire.MsgQuery)
	if !ok {
		return false
	}
	if got, err := wire.DecodeQuery(payload); err != nil || got != want {
		peer.Errorf("CLOSE statement = %q, err %v, want %q", got, err, want)
		return false
	}
	_ = peer.CommandComplete("CLOSE CURSOR")
	return peer.ReadyForQuery(wire.StatusInTransaction) == nil
}

func beginCursorPeer(t *testing.T, script func(*testpeer.Conn) bool) *testpeer.Server {
	t.Helper()
	return testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		if got, err := wire.DecodeQuery(payload); err != nil || got != "BEGIN" {
			peer.Errorf("BEGIN statement = %q, err %v", got, err)
			return
		}
		_ = peer.CommandComplete("BEGIN")
		if peer.ReadyForQuery(wire.StatusInTransaction) != nil {
			return
		}
		if !script(peer) {
			return
		}
		payload, ok = expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		if got, err := wire.DecodeQuery(payload); err != nil || got != "COMMIT" {
			peer.Errorf("COMMIT statement = %q, err %v", got, err)
			return
		}
		_ = peer.CommandComplete("COMMIT")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
}

func TestCursorFetchCloseAndOwnerEnd(t *testing.T) {
	srv := beginCursorPeer(t, func(peer *testpeer.Conn) bool {
		return serveCursorFetch(peer, `FETCH FORWARD 2 FROM "my""cursor"`, []byte("7")) &&
			serveCursorFetch(peer, `FETCH FORWARD ALL FROM "my""cursor"`, []byte("9")) &&
			serveCursorClose(peer, `CLOSE "my""cursor"`)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	cursor, err := NewCursor(tx, `my"cursor`)
	if err != nil {
		t.Fatalf("NewCursor: %v", err)
	}
	if cursor.Name() != `my"cursor` {
		t.Errorf("Name = %q", cursor.Name())
	}
	rows, err := cursor.Fetch(ctx, 2)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !rows.Next() {
		t.Fatalf("Fetch rows: %v", rows.Err())
	}
	var number int64
	if err := rows.Scan(&number); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if number != 7 {
		t.Errorf("fetched number = %d, want 7", number)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("rows.Close: %v", err)
	}
	remaining, err := cursor.FetchAll(ctx)
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if !remaining.Next() {
		t.Fatalf("FetchAll rows: %v", remaining.Err())
	}
	if err := remaining.Scan(&number); err != nil {
		t.Fatalf("FetchAll Scan: %v", err)
	}
	if number != 9 {
		t.Errorf("remaining number = %d, want 9", number)
	}
	if err := remaining.Close(); err != nil {
		t.Fatalf("FetchAll rows.Close: %v", err)
	}
	if err := cursor.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := cursor.Fetch(ctx, 1); !errors.Is(err, sql.ErrTxDone) {
		t.Errorf("Fetch after commit = %v, want ErrTxDone", err)
	}
	if err := cursor.Close(ctx); !errors.Is(err, sql.ErrTxDone) {
		t.Errorf("Close after commit = %v, want ErrTxDone", err)
	}
}

func TestCursorMultipleOnOwner(t *testing.T) {
	srv := beginCursorPeer(t, func(peer *testpeer.Conn) bool {
		return serveCursorFetch(peer, `FETCH FORWARD 1 FROM "a"`, []byte("1")) &&
			serveCursorFetch(peer, `FETCH FORWARD 1 FROM "b"`, []byte("2")) &&
			serveCursorClose(peer, `CLOSE "a"`) &&
			serveCursorClose(peer, `CLOSE "b"`)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	first, err := NewCursor(tx, "a")
	if err != nil {
		t.Fatalf("NewCursor(a): %v", err)
	}
	second, err := NewCursor(tx, "b")
	if err != nil {
		t.Fatalf("NewCursor(b): %v", err)
	}
	for _, cursor := range []*Cursor{first, second} {
		rows, err := cursor.Fetch(ctx, 1)
		if err != nil {
			t.Fatalf("%s Fetch: %v", cursor.Name(), err)
		}
		if !rows.Next() {
			t.Fatalf("%s rows: %v", cursor.Name(), rows.Err())
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("%s rows.Close: %v", cursor.Name(), err)
		}
	}
	for _, cursor := range []*Cursor{first, second} {
		if err := cursor.Close(ctx); err != nil {
			t.Fatalf("%s Close: %v", cursor.Name(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestCursorRejectsInvalidHandlesAndCounts(t *testing.T) {
	if _, err := NewCursor(nil, "c"); err == nil {
		t.Error("NewCursor(nil) succeeded")
	}
	tx := (*sql.Tx)(nil)
	if _, err := NewCursor(tx, ""); err == nil {
		t.Error("NewCursor(empty name) succeeded")
	}
	if _, err := NewCursor(tx, "c\x00d"); err == nil {
		t.Error("NewCursor(NUL name) succeeded")
	}
	cursor := &Cursor{}
	if _, err := cursor.Fetch(context.Background(), 0); err == nil {
		t.Error("Fetch(0) succeeded")
	}
}

func TestCursorCancellationLeavesTransactionUsable(t *testing.T) {
	srv := beginCursorPeer(t, func(_ *testpeer.Conn) bool {
		return true
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	cursor, err := NewCursor(tx, "c")
	if err != nil {
		t.Fatalf("NewCursor: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cursor.Fetch(cancelled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Fetch = %v, want context.Canceled", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit after cancelled Fetch: %v", err)
	}
}
