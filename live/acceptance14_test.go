//go:build live

package live_test

import (
	"context"
	"testing"
	"time"
)

// TestLiveModePlaceholderAndQuoting verifies the verified non-PG profile
// behaviour on a real deployment: ? is rewritten to $n, the mode-specific
// identifier quotes are honoured, and the SQLServer bare-procedure convenience
// reaches the server as EXEC.
func TestLiveModePlaceholderAndQuoting(t *testing.T) {
	_, conn, meta := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var number int64
	if err := conn.QueryRowContext(ctx, "SELECT ?::int4 + 1", int64(2)).Scan(&number); err != nil {
		t.Fatalf("question placeholder after profile rewrite: %v", err)
	}
	if number != 3 {
		t.Errorf("question placeholder value = %d, want 3", number)
	}

	switch meta.Mode.String() {
	case "mysql":
		var alias int64
		if err := conn.QueryRowContext(ctx, "SELECT ?::int4 AS `probe_alias`", int64(5)).Scan(&alias); err != nil {
			t.Fatalf("backtick identifier: %v", err)
		}
		if alias != 5 {
			t.Errorf("backtick value = %d, want 5", alias)
		}
	case "sqlserver":
		var alias int64
		if err := conn.QueryRowContext(ctx, "SELECT ?::int4 AS [probe_alias]", int64(5)).Scan(&alias); err != nil {
			t.Fatalf("bracket identifier: %v", err)
		}
		if alias != 5 {
			t.Errorf("bracket value = %d, want 5", alias)
		}
	default:
		var quoted int64
		if err := conn.QueryRowContext(ctx, `SELECT ?::int4 AS "probe_alias"`, int64(5)).Scan(&quoted); err != nil {
			t.Fatalf("double-quoted identifier: %v", err)
		}
		if quoted != 5 {
			t.Errorf("quoted value = %d, want 5", quoted)
		}
	}
}

// TestLiveSQLServerRoutineConvenience verifies that a bare SQLServer procedure
// path is rewritten to EXEC before it reaches the live listener, which rejects
// the bare form.
func TestLiveSQLServerRoutineConvenience(t *testing.T) {
	_, conn, meta := liveDB(t)
	if meta.Mode.String() != "sqlserver" {
		t.Skip("SQLServer routine convenience is scoped to the SQLServer profile")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const name = "probe_live_bare_proc"
	if _, err := conn.ExecContext(ctx, "DROP PROCEDURE IF EXISTS "+name); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CREATE PROCEDURE "+name+" AS SELECT 1 AS n"); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.ExecContext(cleanup, "DROP PROCEDURE IF EXISTS "+name)
	}()

	var number int64
	if err := conn.QueryRowContext(ctx, name).Scan(&number); err != nil {
		t.Fatalf("bare procedure call after rewrite: %v", err)
	}
	if number != 1 {
		t.Errorf("bare procedure result = %d, want 1", number)
	}
}
