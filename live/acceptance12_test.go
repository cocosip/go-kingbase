//go:build live

package live_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func TestLiveRoutineCallRows(t *testing.T) {
	_, conn, meta := liveDB(t)
	if meta.Mode != kingbase.ModePG {
		t.Skip("routine CALL/INOUT rows are scoped to the PG profile")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	function := fmt.Sprintf("live_fn_%d", suffix)
	procedure := fmt.Sprintf("live_proc_%d", suffix)
	quotedFunction := kingbase.QuoteIdentifier(function)
	quotedProcedure := kingbase.QuoteIdentifier(procedure)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.ExecContext(cleanupCtx, "DROP FUNCTION IF EXISTS "+quotedFunction+"(integer, integer)")
		_, _ = conn.ExecContext(cleanupCtx, "DROP PROCEDURE IF EXISTS "+quotedProcedure+"(integer, integer)")
	}()
	if _, err := conn.ExecContext(ctx, "CREATE FUNCTION "+quotedFunction+
		"(integer, integer) RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT $1 + $2'"); err != nil {
		t.Fatalf("CREATE FUNCTION: %v", err)
	}
	var sum int64
	if err := conn.QueryRowContext(ctx, "SELECT "+quotedFunction+"($1, $2)", int64(2), int64(3)).Scan(&sum); err != nil {
		t.Fatalf("function call: %v", err)
	}
	if sum != 5 {
		t.Errorf("function sum = %d, want 5", sum)
	}
	if _, err := conn.ExecContext(ctx, "CREATE PROCEDURE "+quotedProcedure+
		"(IN a integer, INOUT b integer) LANGUAGE plpgsql AS $$ BEGIN b := b + a; END $$"); err != nil {
		t.Fatalf("CREATE PROCEDURE: %v", err)
	}
	var result int64
	if err := conn.QueryRowContext(ctx, "CALL "+quotedProcedure+"($1, $2)", int64(1), int64(2)).Scan(&result); err != nil {
		t.Fatalf("CALL INOUT row: %v", err)
	}
	if result != 3 {
		t.Errorf("CALL INOUT result = %d, want 3", result)
	}
}
