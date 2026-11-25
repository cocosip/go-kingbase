//go:build live

package live_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func TestLiveOrdinaryRoleCatalog(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random role suffix: %v", err)
	}
	role := "live_role_" + hex.EncodeToString(suffix)
	password := hex.EncodeToString(suffix)
	quotedRole := kingbase.QuoteIdentifier(role)
	if _, err := conn.ExecContext(ctx, `CREATE ROLE `+quotedRole+` LOGIN PASSWORD '`+password+`'`); err != nil {
		t.Fatalf("CREATE ROLE: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.ExecContext(cleanupCtx, `DROP ROLE IF EXISTS `+quotedRole)
	})
	var database string
	if err := conn.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `GRANT CONNECT ON DATABASE `+kingbase.QuoteIdentifier(database)+` TO `+quotedRole); err != nil {
		t.Fatalf("GRANT CONNECT: %v", err)
	}
	cfg, err := kingbase.ParseConfig(os.Getenv("KINGBASE_TEST_DSN"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, role, password, cfg.Database, cfg.SSLMode)
	roleDB, err := sql.Open("kingbase", dsn)
	if err != nil {
		t.Fatalf("role Open: %v", err)
	}
	roleConn, err := roleDB.Conn(ctx)
	if err != nil {
		_ = roleDB.Close()
		t.Fatalf("ordinary role failed to connect: %v", err)
	}
	t.Cleanup(func() {
		_ = roleConn.Close()
		_ = roleDB.Close()
	})
	var currentUser string
	if err := roleConn.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentUser); err != nil {
		t.Fatalf("ordinary role current_user: %v", err)
	}
	if currentUser != role {
		t.Errorf("current_user = %q, want %q", currentUser, role)
	}
	var value int64
	if err := roleConn.QueryRowContext(ctx, `SELECT 1::pg_catalog.int4`).Scan(&value); err != nil {
		t.Fatalf("ordinary role built-in query: %v", err)
	}
	if value != 1 {
		t.Errorf("ordinary role value = %d", value)
	}
}
