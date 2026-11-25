package kingbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func startTransactionPeer(t *testing.T, beginSQL, completeTag string) *testpeer.Server {
	t.Helper()
	return testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		sql, err := wire.DecodeQuery(payload)
		if err != nil || sql != beginSQL {
			peer.Errorf("BEGIN statement = %q, err %v, want %q", sql, err, beginSQL)
			return
		}
		if err := peer.CommandComplete("BEGIN"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusInTransaction); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.CommandComplete(completeTag); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})
}

func TestBeginCommit(t *testing.T) {
	srv := startTransactionPeer(t, "BEGIN", "COMMIT")
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	txn, err := physical.BeginTx(context.Background(), driver.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if physical.txStatus != wire.StatusInTransaction {
		t.Fatalf("txStatus = %q, want in-transaction", physical.txStatus)
	}
	if err := txn.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if physical.txStatus != wire.StatusIdle {
		t.Errorf("txStatus = %q after commit, want idle", physical.txStatus)
	}
	_ = physical.Close()
}

func TestBeginRollback(t *testing.T) {
	srv := startTransactionPeer(t, "BEGIN", "ROLLBACK")
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	txn, err := physical.BeginTx(context.Background(), driver.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if physical.txStatus != wire.StatusIdle {
		t.Errorf("txStatus = %q after rollback, want idle", physical.txStatus)
	}
	_ = physical.Close()
}

func TestBeginTxOptions(t *testing.T) {
	cases := []struct {
		name string
		opts driver.TxOptions
		sql  string
	}{
		{name: "default", opts: driver.TxOptions{}, sql: "BEGIN"},
		{name: "read committed", opts: driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelReadCommitted)}, sql: "BEGIN ISOLATION LEVEL READ COMMITTED"},
		{name: "repeatable read", opts: driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelRepeatableRead)}, sql: "BEGIN ISOLATION LEVEL REPEATABLE READ"},
		{name: "serializable read only", opts: driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelSerializable), ReadOnly: true}, sql: "BEGIN ISOLATION LEVEL SERIALIZABLE READ ONLY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startTransactionPeer(t, tc.sql, "COMMIT")
			physical, err := connectPeer(t, srv, "")
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			txn, err := physical.BeginTx(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			if err := txn.Commit(); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			_ = physical.Close()
		})
	}
}

func TestBeginTxUnsupportedIsolation(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_, err = physical.BeginTx(context.Background(), driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelSnapshot)})
	if err == nil || !strings.Contains(err.Error(), "unsupported isolation level") {
		t.Fatalf("error = %v, want unsupported isolation error", err)
	}
	if physical.netConn == nil {
		t.Error("unsupported options retired the connection before sending")
	}
	_ = physical.Close()
}

func TestBeginOnRetiredConnection(t *testing.T) {
	physical := &conn{netConn: nil}
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("error = %v, want driver.ErrBadConn", err)
	}
}

func TestBeginWhileActiveRejected(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	physical := &conn{netConn: client, txStatus: wire.StatusInTransaction}
	_, err := physical.BeginTx(context.Background(), driver.TxOptions{})
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("error = %v, want existing-transaction error", err)
	}
}

func TestCommitOnRetiredConnection(t *testing.T) {
	physical := &conn{}
	if err := physical.finishTransaction("COMMIT"); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("Commit error = %v, want driver.ErrBadConn", err)
	}
}
