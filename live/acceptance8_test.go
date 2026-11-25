//go:build live

package live_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func TestLiveExplicitModeConflict(t *testing.T) {
	_, _, meta := liveDB(t)
	wrong := kingbase.ModeOracle
	if meta.Mode == kingbase.ModeOracle {
		wrong = kingbase.ModePG
	}
	dsn := os.Getenv("KINGBASE_TEST_DSN") + " mode=" + strings.ToLower(wrong.String())
	db, err := sql.Open("kingbase", dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = db.PingContext(ctx)
	if err == nil {
		t.Fatalf("explicit mode %s connected to a %s server", wrong, meta.Mode)
	}
	if !strings.Contains(err.Error(), "conflicts with server database_mode") {
		t.Fatalf("mode conflict error = %v", err)
	}
}

func TestLiveCatalogPoolReuse(t *testing.T) {
	_, admin, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	typeName := fmt.Sprintf("live_pool_%d", time.Now().UnixNano())
	quoted := kingbase.QuoteIdentifier(typeName)
	if _, err := admin.ExecContext(ctx, `CREATE TYPE `+quoted+` AS ENUM ('a')`); err != nil {
		t.Fatalf("CREATE TYPE: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.ExecContext(cleanupCtx, `DROP TYPE IF EXISTS `+quoted)
	})
	pool, err := sql.Open("kingbase", os.Getenv("KINGBASE_TEST_DSN"))
	if err != nil {
		t.Fatalf("Open pool: %v", err)
	}
	defer func() { _ = pool.Close() }()
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	first, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("first Conn: %v", err)
	}
	var value string
	if err := first.QueryRowContext(ctx, `SELECT 'a'::`+quoted).Scan(&value); err != nil {
		_ = first.Close()
		t.Fatalf("first pooled catalog: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `DROP TYPE `+quoted); err != nil {
		t.Fatalf("DROP TYPE: %v", err)
	}
	second, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("second Conn: %v", err)
	}
	if err := second.QueryRowContext(ctx, `SELECT 'a'::`+quoted).Scan(&value); err == nil {
		t.Errorf("stale pooled catalog accepted a dropped type")
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	pool.SetMaxIdleConns(0)
	third, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("third Conn: %v", err)
	}
	if err := third.QueryRowContext(ctx, `SELECT 'a'::`+quoted).Scan(&value); err == nil {
		t.Errorf("fresh catalog accepted a dropped type")
	}
	if err := third.Close(); err != nil {
		t.Fatalf("third Close: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE TYPE `+quoted+` AS ENUM ('a')`); err != nil {
		t.Fatalf("recreate TYPE: %v", err)
	}
	pool.SetMaxIdleConns(1)
	fourth, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("fourth Conn: %v", err)
	}
	defer func() { _ = fourth.Close() }()
	if err := fourth.QueryRowContext(ctx, `SELECT 'a'::`+quoted).Scan(&value); err != nil {
		t.Fatalf("fresh catalog after recreation: %v", err)
	}
}
