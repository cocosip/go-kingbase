//go:build live

package live_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func TestLiveTransactionPreparedAndNamed(t *testing.T) {
	_, conn, meta := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE live_tx (id int, name text)"); err != nil {
		t.Fatalf("CREATE TEMP TABLE: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO live_tx (id, name) VALUES ($1, $2)", int64(1), "alpha"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	statement, err := tx.PrepareContext(ctx, "SELECT name FROM live_tx WHERE id = $1")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	var name string
	if err := statement.QueryRowContext(ctx, int64(1)).Scan(&name); err != nil {
		t.Fatalf("prepared query: %v", err)
	}
	if name != "alpha" {
		t.Errorf("prepared name = %q, want alpha", name)
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("statement.Close: %v", err)
	}
	if meta.Mode == kingbase.ModePG {
		var number int64
		if err := conn.QueryRowContext(ctx, "SELECT :id::pg_catalog.int4", sql.Named("id", int64(7))).Scan(&number); err != nil {
			t.Fatalf("named query: %v", err)
		}
		if number != 7 {
			t.Errorf("named number = %d, want 7", number)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestLiveCopyInAndCursor(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE live_copy (id int, name text)"); err != nil {
		t.Fatalf("CREATE TEMP TABLE: %v", err)
	}
	copyStatement, err := tx.PrepareContext(ctx, kingbase.CopyIn("live_copy", "id", "name"))
	if err != nil {
		t.Fatalf("Prepare COPY: %v", err)
	}
	for _, row := range [][2]any{{int64(1), "alpha"}, {int64(2), "tab\tnewline\nbackslash\\"}, {int64(3), nil}} {
		if _, err := copyStatement.ExecContext(ctx, row[0], row[1]); err != nil {
			t.Fatalf("COPY row: %v", err)
		}
	}
	if _, err := copyStatement.ExecContext(ctx); err != nil {
		t.Fatalf("COPY finish: %v", err)
	}
	if err := copyStatement.Close(); err != nil {
		t.Fatalf("COPY close: %v", err)
	}
	var copied int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM live_copy").Scan(&copied); err != nil {
		t.Fatalf("COPY count: %v", err)
	}
	if copied != 3 {
		t.Errorf("copied rows = %d, want 3", copied)
	}
	var special string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM live_copy WHERE id = 2").Scan(&special); err != nil {
		t.Fatalf("COPY special value: %v", err)
	}
	if special != "tab\tnewline\nbackslash\\" {
		t.Errorf("COPY special value = %q", special)
	}
	var nullName sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT name FROM live_copy WHERE id = 3").Scan(&nullName); err != nil {
		t.Fatalf("COPY NULL value: %v", err)
	}
	if nullName.Valid {
		t.Errorf("COPY NULL value = %+v", nullName)
	}
	if _, err := tx.ExecContext(ctx, "DECLARE live_cursor CURSOR FOR SELECT 41, 42"); err != nil {
		t.Fatalf("DECLARE cursor: %v", err)
	}
	cursor, err := kingbase.NewCursor(tx, "live_cursor")
	if err != nil {
		t.Fatalf("NewCursor: %v", err)
	}
	rows, err := cursor.FetchAll(ctx)
	if err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	if !rows.Next() {
		t.Fatalf("cursor rows: %v", rows.Err())
	}
	var first, second int64
	if err := rows.Scan(&first, &second); err != nil {
		t.Fatalf("cursor scan: %v", err)
	}
	if first != 41 || second != 42 {
		t.Errorf("cursor values = %d, %d", first, second)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("cursor rows.Close: %v", err)
	}
	if err := cursor.Close(ctx); err != nil {
		t.Fatalf("cursor.Close: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestLiveTransactionOptions(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cases := []struct {
		name string
		opts sql.TxOptions
	}{
		{"default", sql.TxOptions{}},
		{"read committed", sql.TxOptions{Isolation: sql.LevelReadCommitted}},
		{"repeatable read", sql.TxOptions{Isolation: sql.LevelRepeatableRead}},
		{"serializable", sql.TxOptions{Isolation: sql.LevelSerializable}},
		{"read only", sql.TxOptions{ReadOnly: true}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tx, err := conn.BeginTx(ctx, &testCase.opts)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			var value int64
			if err := tx.QueryRowContext(ctx, "SELECT 1").Scan(&value); err != nil {
				_ = tx.Rollback()
				t.Fatalf("query in transaction: %v", err)
			}
			if value != 1 {
				_ = tx.Rollback()
				t.Errorf("transaction value = %d", value)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("Commit: %v", err)
			}
		})
	}
}

func TestLiveCancelAndRecovery(t *testing.T) {
	_, conn, _ := liveDB(t)
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	_, err := conn.QueryContext(ctx, "SELECT pg_sleep(5)")
	cancel()
	if err == nil {
		t.Fatal("cancelled pg_sleep returned no error")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("cancellation took %s", elapsed)
	}
	t.Logf("cancel error type: %T", err)
	db, second, _ := liveDB(t)
	defer func() { _ = second.Close() }()
	var value int64
	if err := db.QueryRowContext(context.Background(), "SELECT 1").Scan(&value); err != nil {
		t.Fatalf("query after cancellation: %v", err)
	}
	if value != 1 {
		t.Errorf("value after cancellation = %d", value)
	}
}
