//go:build live

package live_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func liveDB(t *testing.T) (*sql.DB, *sql.Conn, kingbase.ServerMetadata) {
	t.Helper()
	dsn := os.Getenv("KINGBASE_TEST_DSN")
	if dsn == "" {
		t.Fatal("KINGBASE_TEST_DSN must be set for -tags live")
	}
	db, err := sql.Open("kingbase", dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	meta, err := kingbase.ConnectionMetadata(conn)
	if err != nil {
		t.Fatalf("ConnectionMetadata: %v", err)
	}
	return db, conn, meta
}

func TestLiveCatalogAndBuiltins(t *testing.T) {
	_, conn, meta := liveDB(t)
	t.Logf("mode=%s verified=%t catalog=%v release=%q", meta.Mode, meta.ModeVerified, meta.CatalogStatus, meta.Release)
	if !meta.ModeVerified {
		t.Errorf("mode=%s is not verified", meta.Mode)
	}
	if want := os.Getenv("KINGBASE_TEST_MODE"); want != "" && !strings.EqualFold(meta.Mode.String(), want) {
		t.Errorf("mode=%s, want %s", meta.Mode, want)
	}
	if meta.CatalogStatus != kingbase.CatalogFull {
		t.Errorf("catalog status=%v, want full", meta.CatalogStatus)
	}
	if !strings.HasPrefix(meta.Release, "KingbaseES ") {
		t.Errorf("release=%q", meta.Release)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var (
		integer int64
		numeric string
		boolean bool
		date    time.Time
		timeOf  string
		inter   string
		text    string
		jsonb   string
		bit     string
		cidr    string
	)
	err := conn.QueryRowContext(ctx, `SELECT
		1::pg_catalog.int4,
		1.25::pg_catalog.numeric,
		true::pg_catalog.bool,
		'2024-01-02'::pg_catalog.date,
		'12:34:56.123456'::pg_catalog.time,
		'1 day 02:03:04'::pg_catalog.interval,
		'hello'::pg_catalog.text,
		'{"a":1}'::pg_catalog.jsonb,
		'101'::pg_catalog.bit(3),
		'10.0.0.0/24'::pg_catalog.cidr`).Scan(
		&integer, &numeric, &boolean, &date, &timeOf, &inter, &text, &jsonb, &bit, &cidr,
	)
	if err != nil {
		t.Fatalf("builtin scan: %v", err)
	}
	if integer != 1 || numeric != "1.25" || !boolean || text != "hello" || jsonb != `{"a": 1}` ||
		bit != "101" || cidr != "10.0.0.0/24" || !strings.HasPrefix(timeOf, "12:34:56") ||
		!strings.Contains(inter, "2:03:04") || date.Format("2006-01-02") != "2024-01-02" {
		t.Errorf("builtin values: int=%d numeric=%q bool=%t date=%s time=%q interval=%q text=%q jsonb=%q bit=%q cidr=%q",
			integer, numeric, boolean, date, timeOf, inter, text, jsonb, bit, cidr)
	}
}
