package gokb

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

const (
	testNoticeSeverity = "NOTICE"
	testNoticeCode     = "00000"
	testNoticeMessage  = "table will be created"
)

func noticeScript(peer *testpeer.Conn) {
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return
	}
	if err := peer.NoticeResponse(
		wire.ErrorField{Code: wire.ErrorSeverity, Value: testNoticeSeverity},
		wire.ErrorField{Code: wire.ErrorCode, Value: testNoticeCode},
		wire.ErrorField{Code: wire.ErrorMessage, Value: testNoticeMessage},
	); err != nil {
		peer.Errorf("NoticeResponse: %v", err)
		return
	}
	if err := peer.CommandComplete("SELECT 0"); err != nil {
		peer.Errorf("CommandComplete: %v", err)
		return
	}
	if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
		peer.Errorf("ReadyForQuery: %v", err)
		return
	}
	waitTerminate(peer)
}
func TestNoticeHandlerReceivesServerNotice(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		noticeScript(peer)
	})
	cfg := peerConfig(t, srv, "")
	var notices []Notice
	cfg.NoticeHandler = func(notice Notice) {
		notices = append(notices, notice)
	}
	connected, err := (&Connector{cfg: cfg}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	physical := connected.(*conn)
	defer func() { _ = physical.Close() }()
	if _, err := physical.ExecContext(context.Background(), "SELECT 0", nil); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if len(notices) != 1 {
		t.Fatalf("received %d notices, want 1", len(notices))
	}
	if want := (Notice{Severity: testNoticeSeverity, Code: testNoticeCode, Message: testNoticeMessage}); notices[0] != want {
		t.Errorf("notice = %+v, want %+v", notices[0], want)
	}
}

func TestNoticeHandlerNilDiscardsNotices(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		noticeScript(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	if _, err := physical.ExecContext(context.Background(), "SELECT 0", nil); err != nil {
		t.Fatalf("ExecContext with a discarded notice: %v", err)
	}
}

func TestNoticeHandlerDuringHandshake(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			peer.Errorf("ReadStartup: %v", err)
			return
		}
		if err := peer.NoticeResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testNoticeSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: testNoticeCode},
			wire.ErrorField{Code: wire.ErrorMessage, Value: testNoticeMessage},
		); err != nil {
			peer.Errorf("NoticeResponse: %v", err)
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		waitTerminate(peer)
	})
	cfg := peerConfig(t, srv, "")
	var notices []Notice
	cfg.NoticeHandler = func(notice Notice) {
		notices = append(notices, notice)
	}
	physical, err := (&Connector{cfg: cfg}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	if len(notices) != 1 || notices[0].Message != testNoticeMessage {
		t.Fatalf("handshake notices = %+v, want one startup notice", notices)
	}
}

func TestMalformedNoticeIsDiscarded(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		// An unterminated field value is a malformed notice frame.
		if err := peer.WriteMessage(wire.MsgNoticeResponse, []byte{'S', 'x'}); err != nil {
			peer.Errorf("notice: %v", err)
			return
		}
		_ = peer.CommandComplete("SELECT 0")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	if _, err := physical.ExecContext(context.Background(), "SELECT 0", nil); err != nil {
		t.Fatalf("ExecContext with a malformed notice: %v", err)
	}
	if physical.netConn == nil {
		t.Error("connection was retired by a malformed notice")
	}
}

func TestNoticeHandlerRefusesCopyCompletionCommand(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 1) {
			return
		}
		if _, _, _, err := readCopyStream(peer); err != nil {
			peer.Errorf("copy stream: %v", err)
			return
		}
		// The notice arrives while the client waits for the completion tag.
		if err := peer.NoticeResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testNoticeSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: testNoticeCode},
			wire.ErrorField{Code: wire.ErrorMessage, Value: testNoticeMessage},
		); err != nil {
			peer.Errorf("NoticeResponse: %v", err)
			return
		}
		_ = peer.CommandComplete("COPY 1")
		_ = peer.ReadyForQuery(wire.StatusInTransaction)
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.CommandComplete("COMMIT")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	var statement *copyIn
	var refusal error
	cfg := peerConfig(t, srv, "")
	cfg.NoticeHandler = func(Notice) {
		refusal = statement.Close()
	}
	physical, err := (&Connector{cfg: cfg}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	tx, err := physical.(*conn).BeginTx(context.Background(), driver.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.(*conn).PrepareContext(context.Background(), CopyIn("t", "a"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	statement = prepared.(*copyIn)
	if _, err := statement.Exec([]driver.Value{"1"}); err != nil {
		t.Fatalf("Exec row: %v", err)
	}
	if _, err := statement.Exec(nil); err != nil {
		t.Fatalf("Exec finish: %v", err)
	}
	if refusal == nil || !strings.Contains(refusal.Error(), "notice handler") {
		t.Fatalf("copy Close from handler = %v, want the notice-handler refusal", refusal)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestNoticeHandlerCannotReenterConnection(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		noticeScript(peer)
	})
	cfg := peerConfig(t, srv, "")
	var physical *conn
	var reentryErr error
	cfg.NoticeHandler = func(Notice) {
		_, reentryErr = physical.ExecContext(context.Background(), "SELECT 1", nil)
	}
	connected, err := (&Connector{cfg: cfg}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	physical = connected.(*conn)
	defer func() { _ = physical.Close() }()
	if _, err := physical.ExecContext(context.Background(), "SELECT 0", nil); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if reentryErr == nil || !strings.Contains(reentryErr.Error(), "notice handler") {
		t.Fatalf("reentrant command error = %v, want the notice-handler refusal", reentryErr)
	}
}
