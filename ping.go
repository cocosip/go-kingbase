package gokb

import (
	"context"
	"database/sql/driver"

	"github.com/cocosip/go-kingbase/internal/wire"
)

var (
	_ driver.Pinger    = (*conn)(nil)
	_ driver.Validator = (*conn)(nil)
)

// Ping verifies that the physical connection still answers. A failed ping is
// reported as-is; the connection is retired by the failing command path.
func (c *conn) Ping(ctx context.Context) error {
	rows, err := c.QueryContext(ctx, "SELECT 1", nil)
	if err != nil {
		return err
	}
	return rows.Close()
}

// IsValid reports whether the connection can be reused without a round trip.
// A connection with an open transaction, a retired socket or an unknown
// transaction state is not reusable.
func (c *conn) IsValid() bool {
	return c.netConn != nil && c.txStatus == wire.StatusIdle
}
