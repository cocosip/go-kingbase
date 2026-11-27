//go:build live

package live_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

// TestLivePerformanceBaseline measures the scoped single-row and COPY IN
// workloads on the real deployment. It is a P2 baseline (no threshold), gated by
// KINGBASE_TEST_PERF=1 so the acceptance suite stays fast, and records the
// measured rates in perf-<mode>.json when KINGBASE_TEST_ARTIFACT_DIR is set.
func TestLivePerformanceBaseline(t *testing.T) {
	if os.Getenv("KINGBASE_TEST_PERF") != "1" {
		t.Skip("set KINGBASE_TEST_PERF=1 to run the performance baseline")
	}
	_, conn, meta := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	const rounds = 500
	const copyRows = 5000
	report := map[string]any{
		"mode":             meta.Mode.String(),
		"release":          meta.Release,
		liveFieldTimestamp: time.Now().UTC().Format(time.RFC3339),
	}

	measure := func(name string, workload func() error) {
		start := time.Now()
		if err := workload(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		elapsed := time.Since(start)
		report[name+"_total_ms"] = float64(elapsed.Microseconds()) / 1000
		report[name+"_per_op_us"] = float64(elapsed.Microseconds()) / rounds
	}

	measure("select_single_row", func() error {
		for i := 0; i < rounds; i++ {
			var value int64
			if err := conn.QueryRowContext(ctx, "SELECT 1").Scan(&value); err != nil {
				return err
			}
			if value != 1 {
				return fmt.Errorf("value = %d", value)
			}
		}
		return nil
	})
	measure("select_parameterized", func() error {
		for i := 0; i < rounds; i++ {
			var value int64
			if err := conn.QueryRowContext(ctx, "SELECT $1::int4", int64(i)).Scan(&value); err != nil {
				return err
			}
			if value != int64(i) {
				return fmt.Errorf("value = %d, want %d", value, i)
			}
		}
		return nil
	})

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE live_perf_copy (id int, name text)"); err != nil {
		t.Fatalf("CREATE TEMP TABLE: %v", err)
	}
	statement, err := tx.PrepareContext(ctx, kingbase.CopyIn("live_perf_copy", "id", "name"))
	if err != nil {
		t.Fatalf("Prepare COPY: %v", err)
	}
	start := time.Now()
	for i := 0; i < copyRows; i++ {
		if _, err := statement.ExecContext(ctx, int64(i), fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatalf("COPY row %d: %v", i, err)
		}
	}
	if _, err := statement.ExecContext(ctx); err != nil {
		t.Fatalf("COPY finish: %v", err)
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("COPY close: %v", err)
	}
	elapsed := time.Since(start)
	report["copy_in_total_ms"] = float64(elapsed.Microseconds()) / 1000
	report["copy_in_per_row_us"] = float64(elapsed.Microseconds()) / copyRows
	var copied int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM live_perf_copy").Scan(&copied); err != nil {
		t.Fatalf("COPY count: %v", err)
	}
	if copied != copyRows {
		t.Fatalf("copied rows = %d, want %d", copied, copyRows)
	}

	t.Logf("mode=%s single-row=%v/us parameterized=%v/us copy-in=%v us/row",
		meta.Mode, report["select_single_row_per_op_us"], report["select_parameterized_per_op_us"],
		report["copy_in_per_row_us"])
	if dir := os.Getenv("KINGBASE_TEST_ARTIFACT_DIR"); dir != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatalf("marshal perf: %v", err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("artifact directory: %v", err)
		}
		path := filepath.Join(dir, "perf-"+meta.Mode.String()+".json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write perf: %v", err)
		}
	}
}
