package gokb

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// notificationQueueSize bounds the pending notifications a Listener holds. A
// full queue reports a gap instead of growing memory or blocking the reader.
const notificationQueueSize = 256

// Notification is one LISTEN/NOTIFY message delivered to a Listener.
type Notification struct {
	Channel string
	Payload string
	PID     uint32
}

// ErrListenerClosed reports that the listener's session ended, after any
// already-received notifications were drained.
var ErrListenerClosed = errors.New("kingbase: listener closed")

// ErrNotificationGap reports that the bounded queue overflowed: messages were
// dropped without replay, the connection was retired, and a zero Notification
// marks the gap before this error is returned. Reconnect with NewListener.
var ErrNotificationGap = errors.New("kingbase: notification queue overflow")

// Listener owns one dedicated connection that has run LISTEN and delivers
// asynchronous notifications through Next. Nothing is delivered until Next is
// called, so the bounded queue is the only buffer. The session ends on Close,
// server disconnect, overflow or an unexpected message; no command may be sent
// once the reader starts, and a new Listener is needed to listen again.
type Listener struct {
	conn     *conn
	channels []string

	mu      sync.Mutex
	queue   []Notification
	lost    bool
	closing bool
	err     error
	wake    chan struct{}

	closeOnce sync.Once
	closeErr  error
}

// NewListener connects, runs one LISTEN statement per channel and starts the
// notification reader. Channel names are quoted as identifiers.
func NewListener(ctx context.Context, cfg *Config, channels ...string) (*Listener, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kingbase: listener requires a configuration")
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("kingbase: listener requires at least one channel")
	}
	quoted := make([]string, len(channels))
	for i, channel := range channels {
		if channel == "" {
			return nil, fmt.Errorf("kingbase: empty channel name")
		}
		quoted[i] = QuoteIdentifier(channel)
	}
	physical, err := (&Connector{cfg: cfg}).Connect(ctx)
	if err != nil {
		return nil, err
	}
	connection := physical.(*conn)
	for _, channel := range quoted {
		if _, err := connection.ExecContext(ctx, "LISTEN "+channel, nil); err != nil {
			_ = connection.Close()
			return nil, err
		}
	}
	listener := &Listener{
		conn:     connection,
		channels: channels,
		wake:     make(chan struct{}, 1),
	}
	go listener.read()
	return listener, nil
}

// Channels returns the channels this listener subscribed to.
func (l *Listener) Channels() []string {
	return append([]string(nil), l.channels...)
}

// Next returns the next notification. It blocks until a notification arrives,
// ctx ends, or the session ends. A zero Notification with a nil error marks a
// gap where the bounded queue overflowed; the following call reports
// ErrNotificationGap.
func (l *Listener) Next(ctx context.Context) (Notification, error) {
	for {
		l.mu.Lock()
		if len(l.queue) > 0 {
			notification := l.queue[0]
			l.queue = l.queue[1:]
			l.mu.Unlock()
			return notification, nil
		}
		if l.lost {
			l.lost = false
			l.mu.Unlock()
			return Notification{}, nil
		}
		if l.err != nil {
			err := l.err
			l.mu.Unlock()
			return Notification{}, err
		}
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return Notification{}, ctx.Err()
		case <-l.wake:
		}
	}
}

// Close ends the listener session; already-received notifications stay readable
// until they are drained, then Next reports ErrListenerClosed. Close never waits
// for the reader, so it is safe from a NoticeHandler.
func (l *Listener) Close() error {
	l.mu.Lock()
	l.closing = true
	l.mu.Unlock()
	l.closeConn()
	return l.closeErr
}

// closeConn closes the physical session exactly once; the reader and Close can
// race without touching the connection concurrently.
func (l *Listener) closeConn() {
	l.closeOnce.Do(func() {
		l.closeErr = l.conn.Close()
	})
}

// read owns the connection until the session ends: only one reader may consume
// the wire, so a listener answers no commands after LISTEN.
func (l *Listener) read() {
	defer l.closeConn()
	for {
		typ, payload, err := l.conn.reader.ReadMessage()
		if err != nil {
			l.fail(err)
			return
		}
		switch typ {
		case wire.MsgNotificationResponse:
			notification, decodeErr := wire.DecodeNotification(payload)
			if decodeErr != nil {
				l.fail(fmt.Errorf("kingbase: malformed notification: %w", decodeErr))
				return
			}
			if !l.push(Notification{Channel: notification.Channel, Payload: notification.Payload, PID: notification.PID}) {
				// Report the gap once the received messages are drained, then end
				// the session: dropped messages cannot be replayed.
				l.mu.Lock()
				l.lost = true
				l.mu.Unlock()
				l.fail(ErrNotificationGap)
				return
			}
		case wire.MsgNoticeResponse, wire.MsgParameterStatus:
			if _, asyncErr := l.conn.handleAsync(typ, payload); asyncErr != nil {
				l.fail(asyncErr)
				return
			}
		default:
			l.fail(fmt.Errorf("kingbase: unexpected message %q while listening", typ))
			return
		}
	}
}

// push appends a notification without blocking; false means the queue is full.
func (l *Listener) push(notification Notification) bool {
	l.mu.Lock()
	if len(l.queue) >= notificationQueueSize {
		l.mu.Unlock()
		return false
	}
	l.queue = append(l.queue, notification)
	l.mu.Unlock()
	l.signal()
	return true
}

// fail records the first terminal cause and wakes any waiting Next. An overflow
// keeps its documented gap error even if Close raced it.
func (l *Listener) fail(err error) {
	l.mu.Lock()
	if l.err == nil {
		switch {
		case errors.Is(err, ErrNotificationGap):
			l.err = err
		case l.closing:
			l.err = ErrListenerClosed
		default:
			l.err = fmt.Errorf("kingbase: listener session ended: %w", err)
		}
	}
	l.mu.Unlock()
	l.signal()
}

func (l *Listener) signal() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}
