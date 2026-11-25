//go:build live

package live_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

// TestLiveSmoke is the first scoped V9 profile check. It runs only with
// -tags live and KINGBASE_TEST_DSN; a successful testpeer replay is not a
// substitute. Set KINGBASE_TEST_MODE to assert the reported compatibility mode.
func TestLiveSmoke(t *testing.T) {
	dsn := os.Getenv("KINGBASE_TEST_DSN")
	if dsn == "" {
		t.Fatal("KINGBASE_TEST_DSN must be set for -tags live")
	}
	db, err := sql.Open("kingbase", dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = connection.Close() }()
	metadata, err := kingbase.ConnectionMetadata(connection)
	if err != nil {
		t.Fatalf("ConnectionMetadata: %v", err)
	}
	t.Logf("mode=%s source=%s verified=%t wire=%s release=%q version=%q",
		metadata.Mode, metadata.ModeSource, metadata.ModeVerified, metadata.WireProtocol,
		metadata.Release, metadata.ServerVersion)
	if metadata.WireProtocol != kingbase.WirePG {
		t.Fatalf("wire protocol = %s, want pg", metadata.WireProtocol)
	}
	if want := os.Getenv("KINGBASE_TEST_MODE"); want != "" &&
		!strings.EqualFold(metadata.Mode.String(), want) {
		t.Fatalf("mode = %s, want %s", metadata.Mode, want)
	}
	var number int64
	if err := connection.QueryRowContext(ctx, "SELECT $1::pg_catalog.int4", int64(7)).Scan(&number); err != nil {
		t.Fatalf("parameter query: %v", err)
	}
	if number != 7 {
		t.Fatalf("parameter value = %d, want 7", number)
	}
	var version string
	if err := connection.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatalf("version query: %v", err)
	}
	if strings.TrimSpace(version) == "" {
		t.Fatal("version() returned an empty report")
	}
	t.Logf("server version report: %s", version)
	if dir := os.Getenv("KINGBASE_TEST_ARTIFACT_DIR"); dir != "" {
		report := map[string]any{
			"mode":           metadata.Mode.String(),
			"mode_source":    metadata.ModeSource.String(),
			"mode_verified":  metadata.ModeVerified,
			"wire_protocol":  metadata.WireProtocol.String(),
			"release":        metadata.Release,
			"server_version": metadata.ServerVersion,
			"version_sql":    version,
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatalf("marshal artifact: %v", err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("artifact directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "live-smoke.json"), data, 0o644); err != nil {
			t.Fatalf("write artifact: %v", err)
		}
	}
}
