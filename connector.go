package kingbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// Connector creates physical connections from one immutable configuration.
// database/sql owns pooling; a Connector never pools or reuses connections.
// The configured transport and mode are pinned by the first snapshot, so a
// later caller mutation cannot switch either on subsequent connections. A
// Connector must not be copied after first use.
type Connector struct {
	cfg     *Config
	pinOnce sync.Once
	pinned  *Config

	modeMu       sync.Mutex
	modePinned   bool
	resolvedMode CompatibilityMode
}

// Config returns an independent snapshot of the pinned configuration. Mutating
// the returned value cannot change this Connector or another connection.
func (c *Connector) Config() *Config {
	cfg := c.snapshot()
	if cfg == nil {
		return nil
	}
	return cfg.clone()
}

// snapshot pins the caller's configuration once. A nil configuration stays nil
// so Connect reports it instead of panicking.
func (c *Connector) snapshot() *Config {
	c.pinOnce.Do(func() {
		if c.cfg != nil {
			c.pinned = c.cfg.clone()
		}
	})
	return c.pinned
}

// newConnector snapshots the configuration at construction time.
func newConnector(cfg *Config) *Connector {
	connector := &Connector{}
	if cfg != nil {
		connector.cfg = cfg.clone()
	}
	return connector
}

// NewConnector returns a reusable driver.Connector for sql.OpenDB. It is the
// public path for a programmatically built Config, for example one carrying a
// Config.NoticeHandler, which a DSN cannot express. The configuration is
// deep-copied here, so later changes to cfg do not affect opened connections.
func NewConnector(cfg *Config) (*Connector, error) {
	if cfg == nil {
		return nil, errors.New("kingbase: NewConnector requires a configuration")
	}
	return newConnector(cfg), nil
}

// Driver returns the owning driver.
func (c *Connector) Driver() driver.Driver {
	return &Driver{}
}

// Connect establishes one physical connection. Candidate endpoints are tried in
// configuration order under the earlier of the context deadline and the
// configured connect_timeout, which covers DNS, dial, TLS, authentication,
// identity, target selection and catalog work. There is no protocol or implicit
// port fallback; a failed candidate is closed before the next one starts.
func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	cfg := c.snapshot()
	if cfg == nil {
		return nil, fmt.Errorf("kingbase: connector has no configuration")
	}
	if cfg.User == "" {
		return nil, fmt.Errorf("kingbase: user is required")
	}
	if cfg.WireProtocol != WirePG {
		return nil, fmt.Errorf("kingbase: wireProtocol %q is not supported", cfg.WireProtocol.String())
	}

	deadlineCtx, cancel := withConnectTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	var failures []error
	for _, ep := range cfg.endpoints() {
		physical, err := c.connectEndpoint(deadlineCtx, cfg, ep)
		if err == nil {
			if pinErr := c.pinResolvedMode(physical); pinErr != nil {
				_ = physical.Close()
				return nil, pinErr
			}
			return physical, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", ep.address, err))
		if deadlineCtx.Err() != nil {
			return nil, connectFailure(deadlineCtx, errors.Join(failures...))
		}
	}
	if len(failures) == 1 {
		return nil, connectFailure(deadlineCtx, failures[0])
	}
	// ponytail: the whole initialization budget is shared by every candidate, so
	// a black-holed first host can consume it; add a per-host attempt cap when
	// live evidence asks for one.
	return nil, connectFailure(deadlineCtx,
		fmt.Errorf("kingbase: no configured endpoint accepted the connection: %w", errors.Join(failures...)))
}

// pinResolvedMode records the first fully initialized connection's mode and
// refuses a later connection that resolved a different authoritative mode.
// This keeps one Connector from mixing mode semantics during failover.
func (c *Connector) pinResolvedMode(physical driver.Conn) error {
	connection, ok := physical.(*conn)
	if !ok {
		return fmt.Errorf("kingbase: connector returned %T", physical)
	}
	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	if !c.modePinned {
		c.modePinned = true
		c.resolvedMode = connection.mode
		return nil
	}
	if connection.mode != c.resolvedMode {
		return fmt.Errorf("kingbase: connector mode %s conflicts with server database_mode %s reported by %s",
			c.resolvedMode, connection.mode, connection.modeSource)
	}
	return nil
}

// connectEndpoint opens, initializes and validates one candidate endpoint.
func (c *Connector) connectEndpoint(ctx context.Context, cfg *Config, ep endpoint) (driver.Conn, error) {
	dialer := net.Dialer{KeepAlive: cfg.keepAliveInterval()}
	netConn, err := dialer.DialContext(ctx, ep.network, ep.address)
	if err != nil {
		return nil, fmt.Errorf("kingbase: dial %s: %w", ep.address, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = netConn.Close()
		}
	}()

	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		if err := netConn.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("kingbase: set connect deadline: %w", err)
		}
	}
	// A cancel-only context would otherwise leave the handshake blocked
	// forever, so cancellation also forces the socket deadline to expire.
	stopWatch := context.AfterFunc(ctx, func() {
		_ = netConn.SetDeadline(time.Unix(1, 0))
	})
	defer stopWatch()

	physical := &conn{
		cfg:         cfg,
		netEndpoint: ep,
		netConn:     netConn,
		params:      make(map[string]string, 8),
	}
	if err := physical.negotiateTLS(ctx, ep.host); err != nil {
		return nil, err
	}
	physical.reader = wire.NewReader(physical.netConn, 0)
	physical.writer = wire.NewWriter(physical.netConn)
	if err := physical.handshake(); err != nil {
		return nil, fmt.Errorf("kingbase: %s handshake failed: %w", cfg.Network, err)
	}
	if err := physical.captureIdentity(ctx); err != nil {
		return nil, err
	}
	if err := physical.checkTargetSession(ctx); err != nil {
		return nil, err
	}
	if err := physical.resolveMode(ctx); err != nil {
		return nil, err
	}
	if err := physical.loadCatalog(ctx); err != nil {
		return nil, err
	}
	if !stopWatch() || ctx.Err() != nil {
		return nil, connectFailure(ctx, fmt.Errorf("kingbase: connection setup was interrupted"))
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

// checkTargetSession enforces the opt-in target_session_attrs policy with the
// session's own transaction_read_only report, before the catalog is read.
func (c *conn) checkTargetSession(ctx context.Context) error {
	if c.cfg.TargetSessionAttrs == TargetSessionAny {
		return nil
	}
	rows, err := c.querySimple(ctx, "SHOW transaction_read_only")
	if err != nil {
		return err
	}
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		_ = rows.Close()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("kingbase: server reported no transaction_read_only value")
		}
		return err
	}
	value := ""
	switch typed := dest[0].(type) {
	case string:
		value = typed
	case []byte:
		value = string(typed)
	case nil:
		_ = rows.Close()
		return fmt.Errorf("kingbase: server reported no transaction_read_only value")
	}
	readOnly, err := parseReadOnlyReport(value)
	if err != nil {
		_ = rows.Close()
		return err
	}
	switch c.cfg.TargetSessionAttrs {
	case TargetSessionReadWrite:
		if readOnly {
			_ = rows.Close()
			return fmt.Errorf("kingbase: target_session_attrs=read-write is not satisfied (transaction_read_only=%s)", boundedValue(value))
		}
	case TargetSessionReadOnly:
		if !readOnly {
			_ = rows.Close()
			return fmt.Errorf("kingbase: target_session_attrs=read-only is not satisfied (transaction_read_only=%s)", boundedValue(value))
		}
	}
	return rows.Close()
}

// parseReadOnlyReport interprets the server's transaction_read_only report; an
// unrecognized value fails closed instead of guessing a session kind.
func parseReadOnlyReport(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "on", "true", "1", "yes":
		return true, nil
	case "off", "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("kingbase: unexpected transaction_read_only value %s", boundedValue(value))
	}
}

// connectFailure classifies budget expiry and cancellation, which otherwise
// surface as an opaque socket timeout. The last error is kept for diagnostics.
func connectFailure(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("kingbase: connect: %w: %w", ctxErr, err)
	}
	// The socket deadline can fire just before the context timer observes it,
	// so a timeout after the budget deadline is the budget.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return fmt.Errorf("kingbase: connect: %w: %w", context.DeadlineExceeded, err)
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

// keepAliveInterval maps the configured keepalive policy onto net.Dialer,
// where a negative value disables keepalives and zero keeps the OS default.
func (c *Config) keepAliveInterval() time.Duration {
	if !c.KeepAlives {
		return -1
	}
	return c.KeepAlivesIdle
}
