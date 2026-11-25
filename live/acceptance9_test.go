//go:build live

package live_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

func TestLiveApplicationName(t *testing.T) {
	db, err := sql.Open("kingbase", os.Getenv("KINGBASE_TEST_DSN")+" application_name=live_probe")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	var name string
	if err := conn.QueryRowContext(ctx, "SHOW application_name").Scan(&name); err != nil {
		t.Fatalf("SHOW application_name: %v", err)
	}
	if name != "live_probe" {
		t.Errorf("application_name = %q, want live_probe", name)
	}
}
