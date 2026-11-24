package gokb

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
		if err := peer.CommandComplete("BEGIN"); err != nil {
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
	if _, err := physical.ExecContext(context.Background(), "BEGIN", nil); err != nil {
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
