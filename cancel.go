package gokb

import (
	"io"
	"net"
	"time"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// The PG cancel channel is a separate connection: the original stream keeps
// reading while the server aborts the running command.
const (
	// cancelDialTimeout bounds opening the cancel channel and delivering the
	// 16-byte request.
	cancelDialTimeout = 5 * time.Second
	// cancelSyncTimeout bounds how long the original stream may take to report
	// the cancelled command before the connection is retired.
	cancelSyncTimeout = 2 * time.Second
)

// startCancel delivers a cancel request for the running command and bounds how
// long the original stream may take to report the server's terminal state. An
// operation deadline that is sooner than the cancel window is left untouched;
// a later one is shortened. The connection is retired afterwards (see
// endCommand), so a cancel that loses the race cannot abort the next command.
func (c *conn) startCancel(socket net.Conn, deadline time.Time) {
	if c.backendPID == 0 && c.backendSecret == 0 {
		_ = socket.SetDeadline(time.Unix(1, 0))
		return
	}
	if window := time.Now().Add(cancelSyncTimeout); deadline.IsZero() || window.Before(deadline) {
		_ = socket.SetDeadline(window)
	}
	if err := c.sendCancelRequest(); err != nil {
		// The cancel could not be delivered, so interrupt the blocked read now
		// instead of waiting out the window.
		_ = socket.SetDeadline(time.Unix(1, 0))
	}
}

// sendCancelRequest opens the independent cancel connection, sends the
// documented 16-byte request and observes the channel close. The server closes
// it after processing the request; the deadline prevents a stuck peer from
// pinning this goroutine.
func (c *conn) sendCancelRequest() error {
	network, address := c.cfg.addr()
	dialer := net.Dialer{Timeout: cancelDialTimeout}
	conn, err := dialer.Dial(network, address)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(cancelDialTimeout))
	if _, err := conn.Write(wire.EncodeCancelRequest(c.backendPID, c.backendSecret)); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, conn)
	return err
}
