//go:build live

package live_test

import (
	"context"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func TestLiveTimezoneAndDST(t *testing.T) {
	_, conn, meta := liveDB(t)
	if meta.Mode != kingbase.ModePG {
		t.Skip("timezone/DST check is scoped to the PG profile")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(ctx, "SET TIME ZONE 'America/New_York'"); err != nil {
		t.Fatalf("SET TIME ZONE: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = conn.ExecContext(cleanupCtx, "SET TIME ZONE 'UTC'")
	})
	var wall time.Time
	if err := conn.QueryRowContext(ctx, `SELECT '2024-03-10 01:30:00'::pg_catalog.timestamp`).Scan(&wall); err != nil {
		t.Fatalf("wall timestamp: %v", err)
	}
	wantWall := time.Date(2024, 3, 10, 1, 30, 0, 0, time.UTC)
	if !wall.Equal(wantWall) {
		t.Errorf("wall timestamp = %s, want %s", wall, wantWall)
	}
	var instant time.Time
	if err := conn.QueryRowContext(ctx, `SELECT '2024-03-10 01:30:00-05'::pg_catalog.timestamptz`).Scan(&instant); err != nil {
		t.Fatalf("DST timestamptz: %v", err)
	}
	wantInstant := time.Date(2024, 3, 10, 6, 30, 0, 0, time.UTC)
	if !instant.Equal(wantInstant) {
		t.Errorf("DST instant = %s, want %s", instant, wantInstant)
	}
}
