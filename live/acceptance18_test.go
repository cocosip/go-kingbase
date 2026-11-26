//go:build live

package live_test

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	kingbase "github.com/cocosip/go-kingbase"
)

// liveEndpointDSN rebuilds a DSN from the configured profile with an explicit
// endpoint list, quoting values the way the parser accepts them.
func liveEndpointDSN(t *testing.T, cfg *kingbase.Config, hosts []string, ports []int) string {
	t.Helper()
	quote := func(v string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), "'", `\'`) + "'"
	}
	portsText := make([]string, len(ports))
	for i, port := range ports {
		portsText[i] = strconv.Itoa(port)
	}
	text := strings.Join([]string{
		"host=" + strings.Join(hosts, ","),
		"port=" + strings.Join(portsText, ","),
		"user=" + quote(cfg.User),
		"password=" + quote(cfg.Password),
		"dbname=" + quote(cfg.Database),
		"sslmode=" + cfg.SSLMode.String(),
		"connect_timeout=5",
	}, " ")
	if _, err := kingbase.ParseConfig(text); err != nil {
		t.Fatalf("built DSN does not parse: %v", err)
	}
	return text
}

// TestLiveMultiHostFailover proves F-106 on a real deployment: an endpoint list
// skips a candidate that refuses the socket and reports an aggregate when every
// candidate fails, without inventing a successful result.
func TestLiveMultiHostFailover(t *testing.T) {
	base := os.Getenv("KINGBASE_TEST_DSN")
	if base == "" {
		t.Fatal("KINGBASE_TEST_DSN must be set for -tags live")
	}
	cfg, err := kingbase.ParseConfig(base)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// The first candidate cannot resolve, so the driver has to move on to the
	// live endpoint instead of failing the whole connect.
	const missingHost = "kingbase-live-invalid.invalid"
	db, err := sql.Open("kingbase", liveEndpointDSN(t, cfg, []string{missingHost, cfg.Host}, []int{cfg.Port}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("Ping through the endpoint list: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	meta, err := kingbase.ConnectionMetadata(conn)
	_ = conn.Close()
	if err != nil {
		t.Fatalf("ConnectionMetadata: %v", err)
	}
	if !meta.ModeVerified {
		t.Errorf("mode=%s is not verified after the fallback", meta.Mode)
	}
	if want := strings.ToLower(os.Getenv("KINGBASE_TEST_MODE")); want != "" && meta.Mode.String() != want {
		t.Errorf("mode=%s after fallback, want %s", meta.Mode, want)
	}
	t.Logf("connected through the second candidate: mode=%s release=%q", meta.Mode, meta.Release)

	// Every candidate failing must fail and name what was tried; a silent
	// fallback to some other endpoint would hide a configuration error.
	const secondMissingHost = "kingbase-live-missing.invalid"
	bad, err := sql.Open("kingbase", liveEndpointDSN(t, cfg, []string{missingHost, secondMissingHost}, []int{cfg.Port}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = bad.Close() }()
	err = bad.PingContext(ctx)
	if err == nil {
		t.Fatal("Ping with no reachable candidate succeeded")
	}
	for _, host := range []string{missingHost, secondMissingHost} {
		if !strings.Contains(err.Error(), host) {
			t.Errorf("aggregate error %q does not name candidate host %s", err, host)
		}
	}
}
