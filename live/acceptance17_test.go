//go:build live

package live_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

// liveSuffix gives every probe its own object name without touching shared data.
func liveSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// TestLiveRuntimeParameterForwarding proves F-113 on a real server: an
// allowlisted runtime value reaches the backend as an exact startup parameter,
// and an unknown parameter is rejected before a connection is published.
func TestLiveRuntimeParameterForwarding(t *testing.T) {
	dsn := os.Getenv("KINGBASE_TEST_DSN")
	if dsn == "" {
		t.Fatal("KINGBASE_TEST_DSN must be set for -tags live")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	db, err := sql.Open("kingbase", dsn+" search_path=pg_catalog")
	if err != nil {
		t.Fatalf("Open with search_path: %v", err)
	}
	defer func() { _ = db.Close() }()
	var path string
	if err := db.QueryRowContext(ctx, "SHOW search_path").Scan(&path); err != nil {
		t.Fatalf("SHOW search_path: %v", err)
	}
	if path != "pg_catalog" {
		t.Fatalf("search_path = %q, want the forwarded pg_catalog", path)
	}

	unknown, err := sql.Open("kingbase", dsn+" live_unknown_setting=1")
	if err != nil {
		t.Logf("unknown parameter rejected at Open: %v", err)
	} else {
		if pingErr := unknown.PingContext(ctx); pingErr == nil {
			t.Error("an unknown runtime parameter opened a session, want an explicit rejection")
		} else {
			t.Logf("unknown parameter rejected at connect: %v", pingErr)
		}
		_ = unknown.Close()
	}
}

// TestLiveCheckoutResetRollsBackTransaction proves F-115 on a real server: a
// connection returned with an open transaction is reset before reuse, so the
// next checkout cannot inherit that transaction.
func TestLiveCheckoutResetRollsBackTransaction(t *testing.T) {
	dsn := os.Getenv("KINGBASE_TEST_DSN")
	if dsn == "" {
		t.Fatal("KINGBASE_TEST_DSN must be set for -tags live")
	}
	db, err := sql.Open("kingbase", dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	first, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("first Conn: %v", err)
	}
	// The SQLServer-compatible listener rejects a bare BEGIN, so open the
	// transaction in the mode's own spelling.
	begin := "BEGIN"
	if meta, metaErr := kingbase.ConnectionMetadata(first); metaErr == nil && meta.Mode == kingbase.ModeSQLServer {
		begin = "BEGIN TRANSACTION"
	}
	if _, err := first.ExecContext(ctx, begin); err != nil {
		t.Fatalf("%s: %v", begin, err)
	}
	if _, err := first.ExecContext(ctx, "CREATE TEMP TABLE live_reset (id int)"); err != nil {
		t.Fatalf("CREATE TEMP TABLE: %v", err)
	}
	if _, err := first.ExecContext(ctx, "INSERT INTO live_reset VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	// Return the connection with the transaction still open; the driver must
	// roll it back before handing the session to the next checkout.
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("second Conn: %v", err)
	}
	defer func() { _ = second.Close() }()
	var locks int64
	if err := second.QueryRowContext(ctx,
		"SELECT count(*) FROM pg_locks WHERE locktype = 'transactionid' AND pid = pg_backend_pid()").Scan(&locks); err != nil {
		t.Fatalf("pg_locks probe: %v", err)
	}
	if locks != 0 {
		t.Fatalf("reused session still holds %d transaction-id locks, want a reset connection", locks)
	}
	var one int64
	if err := second.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("reused session query = %d, %v, want 1", one, err)
	}
}

// TestLiveSequenceOwnership proves F-605 on a real server: nextval/currval are
// session-scoped on one pinned connection and never inferred by the driver.
func TestLiveSequenceOwnership(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	name := "live_seq_" + liveSuffix()
	sequence := kingbase.QuoteIdentifier(name)
	if _, err := conn.ExecContext(ctx, "CREATE SEQUENCE "+sequence); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanupCancel()
	defer func() { _, _ = conn.ExecContext(cleanupCtx, "DROP SEQUENCE IF EXISTS "+sequence) }()

	// nextval/currval take a relation-name literal, not an identifier.
	var first, second, current int64
	if err := conn.QueryRowContext(ctx, "SELECT nextval('"+name+"')").Scan(&first); err != nil {
		t.Fatalf("first nextval: %v", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT nextval('"+name+"')").Scan(&second); err != nil {
		t.Fatalf("second nextval: %v", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT currval('"+name+"')").Scan(&current); err != nil {
		t.Fatalf("currval: %v", err)
	}
	if first != 1 || second != 2 || current != 2 {
		t.Fatalf("sequence values = %d, %d, currval %d, want 1, 2, 2", first, second, current)
	}
	result, err := conn.ExecContext(ctx, "SELECT 1")
	if err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if _, err := result.LastInsertId(); !errors.Is(err, kingbase.ErrLastInsertIDUnavailable) {
		t.Fatalf("LastInsertId after a sequence call = %v, want ErrLastInsertIDUnavailable", err)
	}
}

// TestLiveNoticeHandler proves F-704 on a real server: notices reach the
// handler configured on the public Connector path.
func TestLiveNoticeHandler(t *testing.T) {
	dsn := os.Getenv("KINGBASE_TEST_DSN")
	if dsn == "" {
		t.Fatal("KINGBASE_TEST_DSN must be set for -tags live")
	}
	cfg, err := kingbase.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	var mu sync.Mutex
	var notices []kingbase.Notice
	cfg.NoticeHandler = func(notice kingbase.Notice) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, notice)
	}
	connector, err := kingbase.NewConnector(cfg)
	if err != nil {
		t.Fatalf("NewConnector: %v", err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	table := kingbase.QuoteIdentifier("live_notice_" + liveSuffix())
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
		t.Fatalf("DROP TABLE IF EXISTS: %v", err)
	}
	mu.Lock()
	captured := len(notices)
	mu.Unlock()
	if captured == 0 {
		t.Fatal("no notice reached Config.NoticeHandler")
	}
	for _, notice := range notices {
		if notice.Severity == "" || notice.Code == "" || notice.Message == "" {
			t.Fatalf("notice with incomplete fields: %+v", notice)
		}
	}
}

// TestLiveMultipleResultSets proves F-406 on a real server: a multi-statement
// simple query exposes each result set and ends cleanly on the same connection.
func TestLiveMultipleResultSets(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, "SELECT 1; SELECT 2")
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var first int64
	if !rows.Next() {
		t.Fatalf("first result set has no row: %v", rows.Err())
	}
	if err := rows.Scan(&first); err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	if first != 1 {
		t.Errorf("first value = %d, want 1", first)
	}
	if !rows.NextResultSet() {
		t.Fatalf("no second result set: %v", rows.Err())
	}
	var second int64
	if !rows.Next() {
		t.Fatalf("second result set has no row: %v", rows.Err())
	}
	if err := rows.Scan(&second); err != nil {
		t.Fatalf("second Scan: %v", err)
	}
	if second != 2 {
		t.Errorf("second value = %d, want 2", second)
	}
	if rows.NextResultSet() {
		t.Error("unexpected third result set")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("result set iteration: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var one int64
	if err := conn.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("query after multiple result sets = %d, %v, want 1", one, err)
	}
}

// TestLiveServerErrorFields proves F-901/F-903 on a real server: a definite
// server error keeps its fields and leaves the connection reusable.
func TestLiveServerErrorFields(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	missing := kingbase.QuoteIdentifier("live_missing_" + liveSuffix())
	_, err := conn.ExecContext(ctx, "SELECT * FROM "+missing)
	if err == nil {
		t.Fatal("query of a missing relation succeeded, want a server error")
	}
	var serverErr *kingbase.Error
	if !errors.As(err, &serverErr) {
		t.Fatalf("error = %v (%T), want *kingbase.Error", err, err)
	}
	if serverErr.Severity == "" || serverErr.Code == "" || serverErr.Message == "" {
		t.Fatalf("server error fields = %+v, want severity, code and message", serverErr)
	}
	var uncertainErr *kingbase.UncertainError
	if errors.As(err, &uncertainErr) {
		t.Fatal("a definite server error was reported as execution-uncertain")
	}
	var one int64
	if err := conn.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("query after a definite server error = %d, %v, want 1", one, err)
	}
}
