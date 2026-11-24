package gokb

import (
	"context"
	"database/sql/driver"
	"errors"

	"github.com/cocosip/go-kingbase/internal/wire"
)

var _ driver.SessionResetter = (*conn)(nil)

// ResetSession returns a pooled connection to a reusable state before
// checkout. An open or failed transaction is rolled back; if the bounded reset
// cannot restore an idle connection, the connection is retired instead of
// reused. Session settings pinned by the caller are not rewritten here (that is
// the allowlisted-settings task); this reset only guarantees transaction state.
func (c *conn) ResetSession(ctx context.Context) error {
	if c.netConn == nil {
		return driver.ErrBadConn
	}
	if c.txStatus == wire.StatusIdle {
		return nil
	}
	if _, err := c.ExecContext(ctx, "ROLLBACK", nil); err != nil {
		// A refusal from a notice handler happened before anything was sent and
		// leaves the connection untouched; retiring it would lose a healthy
		// session the handler was only inspecting.
		if errors.Is(err, errNoticeReentry) {
			return err
		}
		c.retire()
		return driver.ErrBadConn
	}
	if c.txStatus != wire.StatusIdle {
		c.retire()
		return driver.ErrBadConn
	}
	return nil
}
