package gokb

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// TestCancelRequestAbortsAndSynchronizes covers the PG cancel channel: the
// client opens an independent connection carrying the backend key, the server
// aborts the command on the original stream, and the connection is retired so a
// late cancel cannot reach the next command.
func TestCancelRequestAbortsAndSynchronizes(t *testing.T) {
	cancelled := make(chan struct{})
	keys := make(chan [2]uint32, 1)
	var once sync.Once
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription(selectColumns()[:1]); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		select {
		case <-cancelled:
		case <-time.After(3 * time.Second):
			peer.Errorf("no cancel request arrived")
			return
		}
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "57014"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "canceling statement due to user request"},
		)
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	srv.OnCancel(func(pid, secret uint32) {
		select {
		case keys <- [2]uint32{pid, secret}:
		default:
		}
		once.Do(func() { close(cancelled) })
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	rows, err := physical.QueryContext(ctx, "SELECT id FROM t", nil)
	if err != nil {
		cancel()
		t.Fatalf("QueryContext: %v", err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	err = rows.Next(make([]driver.Value, 1))
	var serverErr *Error
	if !errors.As(err, &serverErr) || serverErr.Code != "57014" {
		t.Fatalf("Next = %v, want a 57014 server error", err)
	}
	if errors.Is(err, driver.ErrBadConn) {
		t.Fatal("cancelled command was reported as ErrBadConn and could be replayed")
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if physical.netConn != nil {
		t.Fatal("connection was not retired after cancellation")
	}
	if _, err := physical.ExecContext(context.Background(), "SELECT 1", nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("Exec after cancellation = %v, want ErrBadConn", err)
	}
	select {
	case got := <-keys:
		if got != [2]uint32{4242, 9876} {
			t.Errorf("cancel key = %v, want the BackendKeyData pair", got)
		}
	case <-time.After(time.Second):
		t.Error("cancel request carried no backend key")
	}
}

// TestCancelShortensLateDeadline covers the common WithTimeout shape: the
// operation deadline is far away, so the cancel window, not that deadline, must
// bound how long a server that ignores the cancel can pin the connection.
func TestCancelShortensLateDeadline(t *testing.T) {
	reading := make(chan struct{})
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription(selectColumns()[:1]); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		close(reading)
		// Ignore the cancellation: the client must give up within its window.
		_, _, _ = peer.ReadMessage()
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	rows, err := physical.QueryContext(ctx, "SELECT id FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	go func() {
		<-reading
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	if err := rows.Next(make([]driver.Value, 1)); err == nil {
		t.Fatal("Next succeeded, want a cancellation error")
	}
	if elapsed := time.Since(started); elapsed > cancelSyncTimeout+time.Second {
		t.Fatalf("Next took %v, want the cancel window to bound the wait", elapsed)
	}
	_ = rows.Close()
	if physical.netConn != nil {
		t.Error("connection was not retired after cancellation")
	}
}
