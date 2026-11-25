//go:build live

package live_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

func TestLiveRoutineOutAssignment(t *testing.T) {
	db, conn, meta := liveDB(t)
	if meta.Mode == kingbase.ModeSQLServer {
		t.Skip("parameterized CALL returns no output row in SQLServer mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	procedure := fmt.Sprintf("live_out_%d", time.Now().UnixNano())
	quoted := kingbase.QuoteIdentifier(procedure)
	if _, err := conn.ExecContext(ctx, "CREATE PROCEDURE "+quoted+
		"(IN a integer, INOUT b integer) LANGUAGE plpgsql AS $$ BEGIN b := b + a; END $$"); err != nil {
		t.Fatalf("CREATE PROCEDURE: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.ExecContext(cleanupCtx, "DROP PROCEDURE IF EXISTS "+quoted+"(integer, integer)")
	}()
	var result int64 = 2
	if _, err := db.ExecContext(ctx, "CALL "+quoted+"($1, $2)", int64(1), sql.Out{Dest: &result, In: true}); err != nil {
		t.Fatalf("CALL with sql.Out: %v", err)
	}
	if result != 3 {
		t.Errorf("INOUT result = %d, want 3", result)
	}
	prepared, err := db.PrepareContext(ctx, "CALL "+quoted+"($1, $2)")
	if err != nil {
		t.Fatalf("PrepareContext CALL: %v", err)
	}
	defer func() { _ = prepared.Close() }()
	result = 2
	if _, err := prepared.ExecContext(ctx, int64(1), sql.Out{Dest: &result, In: true}); err != nil {
		t.Fatalf("prepared CALL with sql.Out: %v", err)
	}
	if result != 3 {
		t.Errorf("prepared INOUT result = %d, want 3", result)
	}
}

// TestLiveSQLServerRoutineOutUnsupported pins the server-reported limitation:
// the SQLServer-mode listener returns the CALL row on the simple protocol only,
// so a parameterized sql.Out call must fail explicitly rather than assign zero.
func TestLiveSQLServerRoutineOutUnsupported(t *testing.T) {
	db, conn, meta := liveDB(t)
	if meta.Mode != kingbase.ModeSQLServer {
		t.Skip("SQLServer-mode limitation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	procedure := fmt.Sprintf("live_sql_out_%d", time.Now().UnixNano())
	quoted := kingbase.QuoteIdentifier(procedure)
	if _, err := conn.ExecContext(ctx, "CREATE PROCEDURE "+quoted+
		"(IN a integer, INOUT b integer) LANGUAGE plpgsql AS $$ BEGIN b := b + a; END $$"); err != nil {
		t.Fatalf("CREATE PROCEDURE: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.ExecContext(cleanupCtx, "DROP PROCEDURE IF EXISTS "+quoted+"(integer, integer)")
	}()
	var result int64 = 2
	_, err := db.ExecContext(ctx, "CALL "+quoted+"($1, $2)", int64(1), sql.Out{Dest: &result, In: true})
	if err == nil || !strings.Contains(err.Error(), "no output row for a parameterized CALL") {
		t.Fatalf("sql.Out error = %v, want the parameterized-CALL limitation", err)
	}
	if result != 2 {
		t.Errorf("result = %d after rejected call, want the input untouched", result)
	}
}

// TestLiveRoutineExecDiscardsStatus pins that Exec of a parameterized CALL
// succeeds and leaves the connection reusable in every mode, including the
// SQLServer-mode listener that returns no output result set for that call.
func TestLiveRoutineExecDiscardsStatus(t *testing.T) {
	_, conn, meta := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	procedure := fmt.Sprintf("live_status_%d", time.Now().UnixNano())
	quoted := kingbase.QuoteIdentifier(procedure)
	if _, err := conn.ExecContext(ctx, "CREATE PROCEDURE "+quoted+
		"(IN a integer, INOUT b integer) LANGUAGE plpgsql AS $$ BEGIN b := b + a; END $$"); err != nil {
		t.Fatalf("CREATE PROCEDURE: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.ExecContext(cleanupCtx, "DROP PROCEDURE IF EXISTS "+quoted+"(integer, integer)")
	}()
	prepared, err := conn.PrepareContext(ctx, "CALL "+quoted+"($1, $2)")
	if err != nil {
		t.Fatalf("PrepareContext CALL: %v", err)
	}
	defer func() { _ = prepared.Close() }()
	if _, err := prepared.ExecContext(ctx, int64(1), int64(2)); err != nil {
		t.Fatalf("ExecContext CALL: %v", err)
	}
	var one int64
	if err := conn.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("query after CALL = %d, %v; want 1 on the reused connection", one, err)
	}
	if meta.Mode == kingbase.ModeSQLServer {
		var out int64
		if err := conn.QueryRowContext(ctx, "CALL "+quoted+"(1, NULL)").Scan(&out); err == nil {
			t.Logf("simple-protocol NULL output call returned %d", out)
		}
	}
}
