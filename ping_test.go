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

func TestPingSucceeds(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: "?column?", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("1")}); err != nil {
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
	if !physical.IsValid() {
		t.Error("IsValid = false on a fresh connection")
	}
	if err := physical.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	_ = physical.Close()
	if physical.IsValid() {
		t.Error("IsValid = true after Close")
	}
}

func TestPingFailureRetires(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		// Close the connection instead of answering, so the read fails.
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := physical.Ping(context.Background()); err == nil {
		t.Fatal("Ping succeeded, want failure")
	}
	if physical.netConn != nil {
		t.Error("failed ping did not retire the connection")
	}
	if physical.IsValid() {
		t.Error("IsValid = true after a failed ping")
	}
}

func TestExecutionUncertaintyAfterSend(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		// The statement may have executed; drop the connection.
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_, err = physical.ExecContext(context.Background(), "UPDATE t SET x = 1", nil)
	if err == nil {
		t.Fatal("ExecContext succeeded, want failure")
	}
	var uncertainErr *UncertainError
	if !errors.As(err, &uncertainErr) {
		t.Fatalf("error = %v (%T), want *UncertainError", err, err)
	}
	if errors.Is(err, driver.ErrBadConn) {
		t.Fatal("post-send failure matches driver.ErrBadConn and could be replayed")
	}
	if physical.netConn != nil {
		t.Error("uncertain failure did not retire the connection")
	}
}

func TestPreSendFailureIsRetrySafe(t *testing.T) {
	physical := &conn{cfg: &Config{User: testUser}}
	if _, err := physical.ExecContext(context.Background(), "UPDATE t SET x = 1", nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("error = %v, want driver.ErrBadConn", err)
	}
}

func TestServerErrorIsDefinite(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "23505"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: testDuplicateKey},
		); err != nil {
			peer.Errorf("ErrorResponse: %v", err)
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
	_, err = physical.ExecContext(context.Background(), "INSERT INTO t VALUES (1)", nil)
	var serverErr *Error
	if !errors.As(err, &serverErr) || serverErr.Code != "23505" {
		t.Fatalf("error = %v, want definite *gokb.Error", err)
	}
	var uncertainErr *UncertainError
	if errors.As(err, &uncertainErr) {
		t.Fatal("server error was classified as execution-uncertain")
	}
	if physical.netConn == nil {
		t.Error("definite server error retired a reusable connection")
	}
	_ = physical.Close()
}

func TestIsValidRequiresIdleState(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	physical := &conn{netConn: client, txStatus: wire.StatusIdle}
	if !physical.IsValid() {
		t.Error("idle connection reported invalid")
	}
	physical.txStatus = wire.StatusInTransaction
	if physical.IsValid() {
		t.Error("connection inside a transaction reported valid for reuse")
	}
	physical.txStatus = wire.StatusFailedTransaction
	if physical.IsValid() {
		t.Error("failed-transaction connection reported valid")
	}
}
