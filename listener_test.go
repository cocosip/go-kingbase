package kingbase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func listenScript(peer *testpeer.Conn) bool {
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return false
	}
	if err := peer.CommandComplete("LISTEN"); err != nil {
		peer.Errorf("CommandComplete: %v", err)
		return false
	}
	if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
		peer.Errorf("ReadyForQuery: %v", err)
		return false
	}
	return true
}

func listenContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func TestListenerDeliversNotifications(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !listenScript(peer) {
			return
		}
		if err := peer.NotificationResponse(7, "news", "hello"); err != nil {
			peer.Errorf("NotificationResponse: %v", err)
			return
		}
		if err := peer.NotificationResponse(7, "news", "again"); err != nil {
			peer.Errorf("NotificationResponse: %v", err)
			return
		}
		waitTerminate(peer)
	})
	listener, err := NewListener(context.Background(), peerConfig(t, srv, ""), "news")
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	ctx, cancel := listenContext(t)
	defer cancel()
	first, err := listener.Next(ctx)
	if err != nil {
		t.Fatalf("Next 1: %v", err)
	}
	if want := (Notification{Channel: "news", Payload: "hello", PID: 7}); first != want {
		t.Errorf("notification 1 = %+v, want %+v", first, want)
	}
	second, err := listener.Next(ctx)
	if err != nil {
		t.Fatalf("Next 2: %v", err)
	}
	if second.Payload != "again" {
		t.Errorf("notification 2 = %+v", second)
	}
	if channels := listener.Channels(); len(channels) != 1 || channels[0] != "news" {
		t.Errorf("Channels = %v", channels)
	}
	if err := listener.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := listener.Next(ctx); !errors.Is(err, ErrListenerClosed) {
		t.Errorf("Next after Close = %v, want ErrListenerClosed", err)
	}
}

func TestListenerQuotesChannels(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		for _, want := range []string{`LISTEN "a b"`, `LISTEN "x""y"`} {
			payload, ok := expectMessage(peer, wire.MsgQuery)
			if !ok {
				return
			}
			sql, err := wire.DecodeQuery(payload)
			if err != nil || sql != want {
				peer.Errorf("LISTEN statement = %q, %v, want %q", sql, err, want)
				return
			}
			_ = peer.CommandComplete("LISTEN")
			_ = peer.ReadyForQuery(wire.StatusIdle)
		}
		waitTerminate(peer)
	})
	listener, err := NewListener(context.Background(), peerConfig(t, srv, ""), "a b", `x"y`)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	_ = listener.Close()
}

func TestListenerRequiresChannel(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	if _, err := NewListener(context.Background(), peerConfig(t, srv, ""), ""); err == nil {
		t.Fatal("NewListener with an empty channel succeeded")
	}
}

func TestListenerQueueOverflowReportsGap(t *testing.T) {
	const sent = notificationQueueSize + 44
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !listenScript(peer) {
			return
		}
		for i := 0; i < sent; i++ {
			if err := peer.NotificationResponse(uint32(i), "news", "payload"); err != nil {
				return
			}
		}
		waitTerminate(peer)
	})
	listener, err := NewListener(context.Background(), peerConfig(t, srv, ""), "news")
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	ctx, cancel := listenContext(t)
	defer cancel()
	// The bounded queue overflows only while the consumer is idle; wait for the
	// reader to report it before draining.
	deadline := time.Now().Add(2 * time.Second)
	for {
		listener.mu.Lock()
		lost := listener.lost
		queued := len(listener.queue)
		listener.mu.Unlock()
		if lost {
			if queued != notificationQueueSize {
				t.Fatalf("queued %d notifications at overflow, want %d", queued, notificationQueueSize)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener queue did not overflow")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < notificationQueueSize; i++ {
		notification, err := listener.Next(ctx)
		if err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
		if notification.Channel != "news" || notification.PID != uint32(i) {
			t.Fatalf("notification %d = %+v, want in-order delivery", i, notification)
		}
	}
	gap, err := listener.Next(ctx)
	if err != nil || gap != (Notification{}) {
		t.Fatalf("gap marker = %+v, %v, want a zero notification", gap, err)
	}
	if _, err := listener.Next(ctx); !errors.Is(err, ErrNotificationGap) {
		t.Fatalf("overflow error = %v, want ErrNotificationGap", err)
	}
}

func waitQueued(t *testing.T, listener *Listener, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		listener.mu.Lock()
		queued := len(listener.queue)
		listener.mu.Unlock()
		if queued >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued %d notifications, want %d", queued, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestListenerCloseDrainsQueued(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !listenScript(peer) {
			return
		}
		if err := peer.NotificationResponse(3, "news", "one"); err != nil {
			peer.Errorf("NotificationResponse: %v", err)
			return
		}
		if err := peer.NotificationResponse(3, "news", "two"); err != nil {
			peer.Errorf("NotificationResponse: %v", err)
			return
		}
		waitTerminate(peer)
	})
	listener, err := NewListener(context.Background(), peerConfig(t, srv, ""), "news")
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	waitQueued(t, listener, 2)
	if err := listener.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	ctx, cancel := listenContext(t)
	defer cancel()
	for _, want := range []string{"one", "two"} {
		notification, err := listener.Next(ctx)
		if err != nil {
			t.Fatalf("Next after Close: %v", err)
		}
		if notification.Payload != want {
			t.Fatalf("drained notification = %+v, want %q", notification, want)
		}
	}
	if _, err := listener.Next(ctx); !errors.Is(err, ErrListenerClosed) {
		t.Fatalf("Next after drain = %v, want ErrListenerClosed", err)
	}
}

func TestListenerEndsOnUnexpectedMessage(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !listenScript(peer) {
			return
		}
		if err := peer.WriteMessage(wire.MsgRowDescription, wire.EncodeRowDescription(nil)); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		waitTerminate(peer)
	})
	listener, err := NewListener(context.Background(), peerConfig(t, srv, ""), "news")
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := listenContext(t)
	defer cancel()
	if _, err := listener.Next(ctx); err == nil || !strings.Contains(err.Error(), "unexpected message") {
		t.Fatalf("Next after unexpected message = %v", err)
	}
}

func TestListenerNextHonorsContext(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !listenScript(peer) {
			return
		}
		waitTerminate(peer)
	})
	listener, err := NewListener(context.Background(), peerConfig(t, srv, ""), "news")
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := listener.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next = %v, want the context deadline", err)
	}
}

func TestListenerCloseFromNoticeHandler(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !listenScript(peer) {
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
		waitTerminate(peer)
	})
	cfg := peerConfig(t, srv, "")
	ready := make(chan struct{})
	closed := make(chan error, 1)
	var listener *Listener
	cfg.NoticeHandler = func(Notice) {
		<-ready
		closed <- listener.Close()
	}
	listener, err := NewListener(context.Background(), cfg, "news")
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	close(ready)
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close from notice handler: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close from a notice handler blocked the reader")
	}
	ctx, cancel := listenContext(t)
	defer cancel()
	if _, err := listener.Next(ctx); !errors.Is(err, ErrListenerClosed) {
		t.Fatalf("Next after close = %v, want ErrListenerClosed", err)
	}
}

func TestListenerDeliversNoticesToHandler(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !listenScript(peer) {
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
		if err := peer.NotificationResponse(9, "news", "hello"); err != nil {
			peer.Errorf("NotificationResponse: %v", err)
			return
		}
		waitTerminate(peer)
	})
	cfg := peerConfig(t, srv, "")
	var notices []Notice
	cfg.NoticeHandler = func(notice Notice) {
		notices = append(notices, notice)
	}
	listener, err := NewListener(context.Background(), cfg, "news")
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := listenContext(t)
	defer cancel()
	notification, err := listener.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if notification.Payload != "hello" {
		t.Errorf("notification = %+v", notification)
	}
	if len(notices) != 1 || notices[0].Message != testNoticeMessage {
		t.Errorf("notices = %+v, want the listening notice", notices)
	}
}
