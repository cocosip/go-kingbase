//go:build live

package live_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

// builtinShapeQuery lists the qualified type identities the driver may decode.
const builtinShapeQuery = `SELECT n.nspname, t.typname, t.typtype, t.typlen, t.typbasetype
FROM pg_catalog.pg_type AS t
INNER JOIN pg_catalog.pg_namespace AS n ON n.oid = t.typnamespace
WHERE t.typname = ANY($1::text[])
ORDER BY n.nspname, t.typname`

// TestLiveEvidenceMatrix captures the V-item observations that are not already
// covered by the focused live suites. It is read-only and writes a sanitized
// JSON artifact when KINGBASE_TEST_ARTIFACT_DIR is set.
func TestLiveEvidenceMatrix(t *testing.T) {
	dsn := os.Getenv("KINGBASE_TEST_DSN")
	if dsn == "" {
		t.Fatal("KINGBASE_TEST_DSN must be set for -tags live")
	}
	_, conn, meta := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	report := map[string]any{
		"mode":           meta.Mode.String(),
		"mode_source":    meta.ModeSource.String(),
		"mode_verified":  meta.ModeVerified,
		"wire_protocol":  meta.WireProtocol.String(),
		"release":        meta.Release,
		"server_version": meta.ServerVersion,
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
	}
	record := func(key string, value any) { report[key] = value }

	// V1/V16: authoritative mode and bounded product identity.
	var version string
	if err := conn.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		record("version_error", err.Error())
	} else {
		record("version_sql", version)
	}

	// V2/V3: raw qualified catalog shape, as the driver reads it.
	catalogColumns, catalogRows, catalogErr := captureCatalog(ctx, conn)
	if catalogErr != nil {
		record("catalog_error", catalogErr.Error())
	} else {
		record("catalog_columns", catalogColumns)
		record("catalog_row_count", len(catalogRows))
		record("catalog_shapes", modeCatalogShapes(catalogRows))
	}

	// V4: explicit builtin shapes for the codec-selecting identities.
	shapes, err := builtinShapes(ctx, conn)
	if err != nil {
		record("builtin_error", err.Error())
	} else {
		record("builtin_shapes", shapes)
	}

	// V8: TLS is offered or not; the measured error is the evidence.
	requireDSN := dsn
	if strings.Contains(requireDSN, " sslmode=") {
		requireDSN = strings.Split(requireDSN, " sslmode=")[0] + " sslmode=require"
	} else {
		requireDSN += " sslmode=require"
	}
	requireDB, err := sql.Open("kingbase", requireDSN)
	if err != nil {
		record("tls_require_error", err.Error())
	} else {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := requireDB.PingContext(pingCtx)
		pingCancel()
		if err != nil {
			record("tls_require_error", err.Error())
		} else {
			record("tls_require", "connected")
		}
		_ = requireDB.Close()
	}

	// V18: scalar/temporal boundary values in the mode's own spelling.
	record("boundaries", captureBoundaries(ctx, conn, meta.Mode))

	// V19: transaction state, read-only rejection and reset recovery.
	record("transactions", captureTransactions(ctx, conn))

	if dir := os.Getenv("KINGBASE_TEST_ARTIFACT_DIR"); dir != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatalf("marshal evidence: %v", err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("artifact directory: %v", err)
		}
		path := filepath.Join(dir, "evidence-"+meta.Mode.String()+".json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write evidence: %v", err)
		}
		t.Logf("evidence written to %s", path)
	}
	t.Logf("mode=%s release=%q version=%q", meta.Mode, meta.Release, meta.ServerVersion)
}

// captureCatalog runs the driver's qualified catalog query and returns the
// column names and decoded rows.
func captureCatalog(ctx context.Context, conn *sql.Conn) ([]string, [][]any, error) {
	rows, err := conn.QueryContext(ctx, `SELECT t.oid, n.nspname, t.typname, t.typtype, t.typlen,
       t.typbasetype, t.typelem, t.typrelid, t.typdelim
FROM pg_catalog.pg_type AS t
INNER JOIN pg_catalog.pg_namespace AS n ON n.oid = t.typnamespace`)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var captured [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, err
		}
		captured = append(captured, values)
	}
	return columns, captured, rows.Err()
}

// modeCatalogShapes selects the identities that drive the mode codecs.
func modeCatalogShapes(rows [][]any) map[string]string {
	wanted := map[string]bool{
		"tinyint": true, "datetime": true, "smalldatetime": true, "money": true,
		"bit": true, "year": true, "longtext": true, "blob": true, "uint4": true,
		"uint8": true, "nchar": true, "nvarchar": true, "binary": true, "varbinary": true,
	}
	shapes := map[string]string{}
	for _, row := range rows {
		if len(row) != 9 {
			continue
		}
		schema := fmt.Sprint(row[1])
		name := fmt.Sprint(row[2])
		if !wanted[name] {
			continue
		}
		shapes[schema+"."+name] = fmt.Sprintf("kind=%s len=%v base=%v elem=%v",
			row[3], row[4], row[5], row[6])
	}
	return shapes
}

// builtinShapes queries the explicit builtin identities the driver decodes.
func builtinShapes(ctx context.Context, conn *sql.Conn) (map[string]string, error) {
	names := []string{
		"bool", "int2", "int4", "int8", "text", "varchar", "bpchar", "name", "char",
		"float4", "float8", "numeric", "bytea", "date", "time", "timetz", "timestamp",
		"timestamptz", "interval", "uuid", "json", "jsonb", "xml", "inet", "cidr",
		"bit", "varbit", "oid", "unknown", "tinyint", "datetime", "smalldatetime",
		"money", "year", "longtext", "blob", "uint4", "uint8", "nchar", "nvarchar",
		"binary", "varbinary",
	}
	shapes := map[string]string{}
	rows, err := conn.QueryContext(ctx, builtinShapeQuery, names)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var schema, name, kind string
		var length int64
		var base int64
		if err := rows.Scan(&schema, &name, &kind, &length, &base); err != nil {
			return nil, err
		}
		shapes[schema+"."+name] = fmt.Sprintf("kind=%s len=%d base=%d", kind, length, base)
	}
	return shapes, rows.Err()
}

// captureBoundaries records the mode's scalar/temporal boundary values.
func captureBoundaries(ctx context.Context, conn *sql.Conn, mode kingbase.CompatibilityMode) map[string]string {
	probes := map[string]string{
		"high_numeric": "SELECT CAST('123456789012345678901234567890.123456789' AS numeric)",
		"max_uint64":   "SELECT CAST('18446744073709551615' AS numeric)",
		"nan_float8":   "SELECT CAST('NaN' AS float8)",
		"inf_float8":   "SELECT CAST('Infinity' AS float8)",
		"interval":     "SELECT CAST('1 day 02:03:04' AS interval)",
		"timestamptz":  "SELECT CAST('2024-03-10 06:30:00+00' AS timestamptz)",
	}
	switch mode {
	case kingbase.ModeMySQL:
		probes["tinyint"] = "SELECT CAST(127 AS tinyint)"
		probes["year"] = "SELECT CAST('2024' AS year)"
		probes["datetime"] = "SELECT CAST('2024-01-02 03:04:05' AS datetime)"
		probes["uint4"] = "SELECT CAST(4294967295 AS uint4)"
	case kingbase.ModeSQLServer:
		probes["tinyint"] = "SELECT CAST(200 AS tinyint)"
		probes["money"] = "SELECT CAST(1234.56 AS money)"
		probes["bit"] = "SELECT CAST(1 AS bit)"
		probes["smalldatetime"] = "SELECT CAST('2024-01-02 03:04' AS smalldatetime)"
	case kingbase.ModeOracle:
		probes["tinyint"] = "SELECT CAST(5 AS tinyint)"
	}
	out := map[string]string{}
	for name, query := range probes {
		var value any
		if err := conn.QueryRowContext(ctx, query).Scan(&value); err != nil {
			out[name] = "error: " + err.Error()
			continue
		}
		out[name] = fmt.Sprintf("%#v (%T)", value, value)
	}
	return out
}

// captureTransactions records a commit, a read-only write rejection and pool
// recovery.
func captureTransactions(ctx context.Context, conn *sql.Conn) map[string]string {
	out := map[string]string{}
	tx, err := conn.BeginTx(ctx, nil)
	switch {
	case err != nil:
		out["begin"] = "error: " + err.Error()
	default:
		var one int64
		if err := tx.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
			out["query"] = "error: " + err.Error()
		} else {
			out["query"] = fmt.Sprintf("%d", one)
		}
		if err := tx.Commit(); err != nil {
			out["commit"] = "error: " + err.Error()
		} else {
			out["commit"] = "ok"
		}
	}
	readOnly, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		out["read_only_begin"] = "error: " + err.Error()
	} else {
		_, execErr := readOnly.ExecContext(ctx, "CREATE TABLE live_ro_forbidden (id int)")
		switch {
		case execErr == nil:
			out["read_only_write"] = "accepted"
		default:
			out["read_only_write"] = "rejected: " + execErr.Error()
		}
		_ = readOnly.Rollback()
	}
	var after int64
	if err := conn.QueryRowContext(ctx, "SELECT 1").Scan(&after); err != nil {
		out["recovery"] = "error: " + err.Error()
	} else {
		out["recovery"] = fmt.Sprintf("ok %d", after)
	}
	return out
}
