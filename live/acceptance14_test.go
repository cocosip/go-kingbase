//go:build live

package live_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
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

// TestLiveNumInputSQLServer pins the logical NumInput rule end to end: SQLServer
// mode reports -1 so database/sql passes arguments through, and the driver's own
// binding plan still rejects a wrong count; other modes keep the server count.
func TestLiveNumInputSQLServer(t *testing.T) {
	_, conn, meta := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prepared, err := conn.PrepareContext(ctx, "SELECT $1::int4")
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	defer func() { _ = prepared.Close() }()
	rows, err := prepared.QueryContext(ctx, int64(1), int64(2))
	if meta.Mode == kingbase.ModeSQLServer {
		if err == nil || !strings.Contains(err.Error(), "statement expects 1 arguments, got 2") {
			t.Fatalf("SQLServer mismatch error = %v, want the driver binding-plan error", err)
		}
	} else {
		if err == nil || !strings.Contains(err.Error(), "sql: expected 1 arguments, got 2") {
			t.Fatalf("%s mismatch error = %v, want database/sql's count check", meta.Mode, err)
		}
	}
	if rows != nil {
		_ = rows.Close()
	}
	var value int64
	if err := prepared.QueryRowContext(ctx, int64(7)).Scan(&value); err != nil || value != 7 {
		t.Fatalf("correctly bound query = %d, %v; want 7", value, err)
	}
}
