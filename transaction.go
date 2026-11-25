package kingbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/cocosip/go-kingbase/internal/wire"
)

var _ driver.ConnBeginTx = (*conn)(nil)

// Begin starts a transaction with default options.
func (c *conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx starts a transaction. The PG-wire profiles accept the four standard
// isolation levels and READ ONLY; anything else is rejected before sending.
func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.netConn == nil {
		return nil, driver.ErrBadConn
	}
	if c.txStatus != wire.StatusIdle {
		return nil, fmt.Errorf("kingbase: transaction already in progress")
	}
	beginSQL, err := beginStatement(opts)
	if err != nil {
		return nil, err
	}
	if _, err := c.ExecContext(ctx, beginSQL, nil); err != nil {
		return nil, err
	}
	if c.txStatus != wire.StatusInTransaction {
		c.retire()
		return nil, fmt.Errorf("kingbase: server did not enter a transaction")
	}
	return &tx{conn: c}, nil
}

// beginStatement renders the BEGIN statement for the requested options.
func beginStatement(opts driver.TxOptions) (string, error) {
	parts := []string{"BEGIN"}
	switch sql.IsolationLevel(opts.Isolation) {
	case sql.LevelDefault:
	case sql.LevelReadUncommitted:
		parts = append(parts, "ISOLATION LEVEL READ UNCOMMITTED")
	case sql.LevelReadCommitted:
		parts = append(parts, "ISOLATION LEVEL READ COMMITTED")
	case sql.LevelRepeatableRead:
		parts = append(parts, "ISOLATION LEVEL REPEATABLE READ")
	case sql.LevelSerializable:
		parts = append(parts, "ISOLATION LEVEL SERIALIZABLE")
	default:
		return "", fmt.Errorf("kingbase: unsupported isolation level %d", opts.Isolation)
	}
	if opts.ReadOnly {
		parts = append(parts, "READ ONLY")
	}
	return strings.Join(parts, " "), nil
}

type tx struct {
	conn *conn
}

var _ driver.Tx = (*tx)(nil)

// Commit commits the transaction. A retired connection is reported as
// ErrBadConn without replaying the COMMIT, since no statement was attempted.
func (t *tx) Commit() error {
	return t.conn.finishTransaction("COMMIT")
}

// Rollback rolls the transaction back.
func (t *tx) Rollback() error {
	return t.conn.finishTransaction("ROLLBACK")
}

func (c *conn) finishTransaction(command string) error {
	if c.netConn == nil {
		return driver.ErrBadConn
	}
	if _, err := c.ExecContext(context.Background(), command, nil); err != nil {
		return err
	}
	if c.txStatus != wire.StatusIdle {
		c.retire()
		return fmt.Errorf("kingbase: transaction did not close after %s", command)
	}
	return nil
}
