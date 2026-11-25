package kingbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestResetSessionRollsBack(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.CommandComplete(testBeginSQL); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusInTransaction); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		sql, err := wire.DecodeQuery(payload)
		if err != nil || sql != "ROLLBACK" {
			peer.Errorf("reset query = %q, err %v, want ROLLBACK", sql, err)
			return
		}
		if err := peer.CommandComplete("ROLLBACK"); err != nil {
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
	if _, err := physical.ExecContext(context.Background(), testBeginSQL, nil); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if physical.txStatus != wire.StatusInTransaction {
		t.Fatalf("txStatus = %q, want in-transaction", physical.txStatus)
	}
	if err := physical.ResetSession(context.Background()); err != nil {
		t.Fatalf("ResetSession: %v", err)
	}
	if physical.txStatus != wire.StatusIdle {
		t.Errorf("txStatus = %q after reset, want idle", physical.txStatus)
	}
	_ = physical.Close()
}

func TestResetSessionKeepsPreparedStatements(t *testing.T) {
	const query = "UPDATE t SET x = $1"
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !describePrepared(peer, query, []uint32{23}) {
			return
		}
		// BEGIN opens a transaction, then a checkout reset rolls it back.
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		if sql, err := wire.DecodeQuery(payload); err != nil || sql != testBeginSQL {
			peer.Errorf("query = %q, err %v, want BEGIN", sql, err)
			return
		}
		_ = peer.CommandComplete(testBeginSQL)
		_ = peer.ReadyForQuery(wire.StatusInTransaction)
		if !expectSimpleQuery(peer) {
			return
		}
		// The server-side statement survived the rollback and must be reusable.
		if !readBind(peer, "kb_stmt_0", []string{"7"}, false, nil) {
			return
		}
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		_ = peer.CommandComplete("UPDATE 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		if !closeStatement(peer, "kb_stmt_0") {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	statement := prepared.(*stmt)
	if _, err := physical.ExecContext(context.Background(), testBeginSQL, nil); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if err := physical.ResetSession(context.Background()); err != nil {
		t.Fatalf("ResetSession: %v", err)
	}
	if _, err := statement.ExecContext(context.Background(), []driver.NamedValue{{Ordinal: 1, Value: int64(7)}}); err != nil {
		t.Fatalf("statement reuse after reset: %v", err)
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("statement.Close: %v", err)
	}
	_ = physical.Close()
}

func TestResetSessionRetiresRetiredConnection(t *testing.T) {
	physical := &conn{cfg: &Config{User: testUser}}
	if err := physical.ResetSession(context.Background()); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("error = %v, want driver.ErrBadConn", err)
	}
}

func TestResetSessionIdleIsNoop(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	physical := &conn{netConn: client, txStatus: wire.StatusIdle}
	if err := physical.ResetSession(context.Background()); err != nil {
		t.Fatalf("ResetSession on idle connection: %v", err)
	}
}
