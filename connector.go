package gokb

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// Connector creates physical connections from one immutable configuration.
// database/sql owns pooling; a Connector never pools or reuses connections.
type Connector struct {
	cfg *Config
}

// Config returns the immutable connection configuration.
func (c *Connector) Config() *Config {
	return c.cfg
}

// Driver returns the owning driver.
func (c *Connector) Driver() driver.Driver {
	return &Driver{}
}

// Connect establishes one physical connection. The earlier of the context
// deadline and the configured connect_timeout covers DNS, dial, TLS,
// authentication, identity and any required catalog work; a timeout, cancel or
// error closes the partial connection and never publishes it.
func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.cfg == nil {
		return nil, fmt.Errorf("kingbase: connector has no configuration")
	}
	if c.cfg.User == "" {
		return nil, fmt.Errorf("kingbase: user is required")
	}

	deadlineCtx, cancel := withConnectTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()

	network, address := c.cfg.addr()
	var dialer net.Dialer
	netConn, err := dialer.DialContext(deadlineCtx, network, address)
	if err != nil {
		return nil, connectFailure(deadlineCtx, fmt.Errorf("kingbase: dial %s: %w", address, err))
	}
	ok := false
	defer func() {
		if !ok {
			_ = netConn.Close()
		}
	}()

	if deadline, hasDeadline := deadlineCtx.Deadline(); hasDeadline {
		if err := netConn.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("kingbase: set connect deadline: %w", err)
		}
	}
	// A cancel-only context would otherwise leave the handshake blocked
	// forever, so cancellation also forces the socket deadline to expire.
	stopWatch := context.AfterFunc(deadlineCtx, func() {
		_ = netConn.SetDeadline(time.Unix(1, 0))
	})
	defer stopWatch()

	physical := &conn{
		cfg:     c.cfg,
		netConn: netConn,
		params:  make(map[string]string, 8),
	}
	if err := physical.negotiateTLS(deadlineCtx); err != nil {
		return nil, connectFailure(deadlineCtx, err)
	}
	physical.reader = wire.NewReader(physical.netConn, 0)
	physical.writer = wire.NewWriter(physical.netConn)
	if err := physical.handshake(); err != nil {
		return nil, connectFailure(deadlineCtx, err)
	}
	if err := physical.loadCatalog(deadlineCtx); err != nil {
		return nil, connectFailure(deadlineCtx, err)
	}
	if !stopWatch() || deadlineCtx.Err() != nil {
		return nil, connectFailure(deadlineCtx, fmt.Errorf("kingbase: connection setup was interrupted"))
	}
	if physical.netConn == nil {
		return nil, fmt.Errorf("kingbase: connect: connection was retired during setup")
	}
	// The handshake deadline is temporary; command deadlines are set per
	// operation from its context.
	if err := physical.netConn.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("kingbase: clear connect deadline: %w", err)
	}
	ok = true
	return physical, nil
}

// connectFailure classifies budget expiry and cancellation, which otherwise
// surface as an opaque socket timeout. The last error is kept for diagnostics.
func connectFailure(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("kingbase: connect: %w: %v", ctxErr, err)
	}
	// The socket deadline can fire just before the context timer observes it,
	// so a timeout after the budget deadline is the budget.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return fmt.Errorf("kingbase: connect: %w: %v", context.DeadlineExceeded, err)
		}
	}
	return err
}

// withConnectTimeout applies the earlier of the caller deadline and the
// configured whole-initialization budget. Zero with no caller deadline means
// unlimited.
func withConnectTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

// addr resolves the configured endpoint. A host starting with "/" is a unix
// socket directory and is only accepted by configuration on non-Windows
// platforms.
func (c *Config) addr() (network, address string) {
	if c.Network == networkUnix {
		return networkUnix, c.Host + "/.s.PGSQL." + strconv.Itoa(c.Port)
	}
	return networkTCP, net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}
