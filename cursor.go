package kingbase

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// Cursor is an explicit server-side refcursor handle. It does not transfer
// ownership or fetch automatically: every operation runs on the owning
// *sql.Tx, so no other pooled session can see the cursor. The name is always
// quoted as one identifier and is never concatenated as SQL.
type Cursor struct {
	tx   *sql.Tx
	name string
}

// NewCursor returns a handle for a refcursor opened by the caller, for example
// a cursor name returned by a function call in the same transaction. The
// transaction must own the cursor and remain open for Fetch and Close.
func NewCursor(tx *sql.Tx, name string) (*Cursor, error) {
	if tx == nil {
		return nil, errors.New("kingbase: cursor requires an owning transaction")
	}
	if name == "" {
		return nil, errors.New("kingbase: cursor name is empty")
	}
	if strings.IndexByte(name, 0) >= 0 {
		return nil, errors.New("kingbase: cursor name contains a NUL byte")
	}
	return &Cursor{tx: tx, name: name}, nil
}

// Name returns the unquoted cursor name.
func (c *Cursor) Name() string {
	return c.name
}

// Fetch returns at most count rows with FETCH FORWARD. The caller must close
// the returned rows; an incomplete or cancelled fetch leaves no implicit
// cursor state behind.
func (c *Cursor) Fetch(ctx context.Context, count int) (*sql.Rows, error) {
	if count <= 0 {
		return nil, errors.New("kingbase: cursor fetch count must be positive")
	}
	return c.tx.QueryContext(ctx, "FETCH FORWARD "+strconv.Itoa(count)+" FROM "+QuoteIdentifier(c.name))
}

// FetchAll returns all remaining rows with FETCH FORWARD ALL.
func (c *Cursor) FetchAll(ctx context.Context) (*sql.Rows, error) {
	return c.tx.QueryContext(ctx, "FETCH FORWARD ALL FROM "+QuoteIdentifier(c.name))
}

// Close closes the server-side cursor with CLOSE. It does not close the
// transaction; a cancelled close leaves the cursor owned by the caller.
func (c *Cursor) Close(ctx context.Context) error {
	_, err := c.tx.ExecContext(ctx, "CLOSE "+QuoteIdentifier(c.name))
	return err
}
