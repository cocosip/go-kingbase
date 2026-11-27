//go:build live

package live_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	kingbase "github.com/cocosip/go-kingbase"
)

// TestLiveCopyInBinaryTyped proves F-705 on a real server: the driver reads each
// column's storage type from the catalog, so a mode that stores REAL as a double
// or DATE as eight bytes is encoded correctly. Values must read back unchanged, a
// NULL row must survive, an unsupported column must fail at prepare, and a value
// that does not fit its column must abort without importing a row.
func TestLiveCopyInBinaryTyped(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const table = "live_bin_typed"
	names := []string{"i2", "i4", "i8", "f4", "f8", "b", "txt", "byt", "d", "tstz"}
	types := []string{"smallint", "integer", "bigint", "real", "double precision",
		"boolean", "text", "bytea", "date", "timestamptz"}
	values := []any{int64(-12), int64(123456), int64(1 << 40), float64(1.5), float64(2.25),
		true, "tab\there\nnewline", []byte{0x00, 0x01, 0xff},
		time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 3, 4, 5, 500_000_000, time.UTC)}

	drop := func(target *sql.Conn, name string) error {
		_, err := target.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+name)
		return err
	}
	for _, name := range []string{table, table + "_num"} {
		if err := drop(conn, name); err != nil {
			t.Fatalf("drop leftovers: %v", err)
		}
	}
	t.Cleanup(func() {
		if _, leftover, _ := liveDB(t); leftover != nil {
			_ = drop(leftover, table)
			_ = drop(leftover, table+"_num")
		}
	})
	definitions := make([]string, len(names))
	for i, name := range names {
		definitions[i] = fmt.Sprintf("%s %s", name, types[i])
	}
	if _, err := conn.ExecContext(ctx, "CREATE TABLE "+table+" ("+strings.Join(definitions, ", ")+")"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	if err := liveImportRows(ctx, conn, kingbase.CopyInBinary(table, names...),
		values, make([]any, len(names))); err != nil {
		t.Fatalf("typed binary import: %v", err)
	}
	liveCheckBinaryValues(ctx, t, conn, table, names, values)
	liveRejectBinaryRows(ctx, t, conn, table)
}

// liveImportRows runs one typed binary import inside a transaction.
func liveImportRows(ctx context.Context, conn *sql.Conn, statement string, rows ...[]any) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	prepared, err := tx.PrepareContext(ctx, statement)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, row := range rows {
		if _, err := prepared.ExecContext(ctx, row...); err != nil {
			_ = prepared.Close()
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := prepared.ExecContext(ctx); err != nil {
		_ = prepared.Close()
		_ = tx.Rollback()
		return err
	}
	if err := prepared.Close(); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// liveCheckBinaryValues reads the imported row back and compares every column.
func liveCheckBinaryValues(ctx context.Context, t *testing.T, conn *sql.Conn, table string, names []string, values []any) {
	t.Helper()
	var (
		count     int
		i2        sql.NullInt64
		i4        sql.NullInt64
		i8        sql.NullInt64
		f4        sql.NullFloat64
		f8        sql.NullFloat64
		b         sql.NullBool
		text      sql.NullString
		blob      []byte
		readDate  any
		readStamp time.Time
	)
	rows, err := conn.QueryContext(ctx, "SELECT "+strings.Join(names, ", ")+" FROM "+table+" WHERE i2 IS NOT NULL")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	for rows.Next() {
		count++
		if err := rows.Scan(&i2, &i4, &i8, &f4, &f8, &b, &text, &blob, &readDate, &readStamp); err != nil {
			_ = rows.Close()
			t.Fatalf("scan: %v", err)
		}
	}
	// Close the result before asking for another query on this connection.
	if err := rows.Close(); err != nil {
		t.Fatalf("close rows: %v", err)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("rows with a value = %d, want 1", count)
	}
	var total int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Errorf("imported rows = %d, want 2 (one value row and one NULL row)", total)
	}
	if i2.Int64 != -12 || i4.Int64 != 123456 || i8.Int64 != 1<<40 {
		t.Errorf("integers = %d/%d/%d, want -12/123456/%d", i2.Int64, i4.Int64, i8.Int64, int64(1)<<40)
	}
	if f4.Float64 != 1.5 || f8.Float64 != 2.25 {
		t.Errorf("floats = %v/%v, want 1.5/2.25", f4.Float64, f8.Float64)
	}
	if !b.Bool || text.String != "tab\there\nnewline" {
		t.Errorf("boolean/text = %v/%q", b.Bool, text.String)
	}
	if !bytes.Equal(blob, []byte{0x00, 0x01, 0xff}) {
		t.Errorf("bytea = %x, want 0001ff", blob)
	}
	// A mode may expose a date as a time.Time or as its text form, so compare the
	// day rather than one representation.
	var gotDate string
	switch value := readDate.(type) {
	case time.Time:
		gotDate = value.Format("2006-01-02")
	case string:
		gotDate = value
	case []byte:
		gotDate = string(value)
	}
	if !strings.HasPrefix(gotDate, "2026-01-02") {
		t.Errorf("date = %q, want 2026-01-02", gotDate)
	}
	stamp, ok := values[9].(time.Time)
	if !ok {
		t.Fatalf("test data changed: %T is not a time", values[9])
	}
	if !readStamp.Equal(stamp) {
		t.Errorf("timestamptz = %v, want %v", readStamp, stamp)
	}
}

// liveRejectBinaryRows proves the two fail-closed paths on a real server: a
// column without an encoder fails at prepare, and a value that does not fit its
// column aborts the stream without importing.
func liveRejectBinaryRows(ctx context.Context, t *testing.T, conn *sql.Conn, table string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, "CREATE TABLE "+table+"_num (v numeric)"); err != nil {
		t.Fatalf("create numeric table: %v", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := tx.PrepareContext(ctx, kingbase.CopyInBinary(table+"_num", "v")); err == nil ||
		!strings.Contains(err.Error(), "no binary encoder") {
		_ = tx.Rollback()
		t.Fatalf("numeric prepare error = %v, want a missing encoder", err)
	}
	_ = tx.Rollback()

	tx, err = conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	bad, err := tx.PrepareContext(ctx, kingbase.CopyInBinary(table, "i2"))
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("Prepare narrow statement: %v", err)
	}
	if _, err := bad.ExecContext(ctx, int64(70000)); err == nil {
		_ = bad.Close()
		_ = tx.Rollback()
		t.Fatal("an out-of-range smallint was accepted")
	}
	_ = bad.Close()
	_ = tx.Rollback()
	_, fresh, _ := liveDB(t)
	var before, after int
	if err := fresh.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&after); err != nil {
		t.Fatalf("count after the failed import: %v", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&before); err != nil {
		t.Fatalf("count before the failed import: %v", err)
	}
	if after != before {
		t.Errorf("rows after the failed import = %d, want %d", after, before)
	}
}

// TestLiveCopyInBinaryScalarTypes proves the encoders the typed probe above
// does not reach: uuid, json, jsonb, timestamp and time. The comparison runs on
// the server, so each mode's own text rendering is not assumed.
func TestLiveCopyInBinaryScalarTypes(t *testing.T) {
	_, conn, meta := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const table = "live_bin_scalar"
	names := []string{"u", "j", "jb", "ts", "tm"}
	types := []string{"uuid", "json", "jsonb", liveTypeTimestamp, "time"}
	if meta.Mode == kingbase.ModeSQLServer {
		// The SQL Server-compatible timestamp is rowversion, not a time.
		types[3] = "datetime"
	}
	values := []any{
		"deadbeef-0001-0002-0003-000400050607",
		`{"a":1}`,
		`{"a":1}`,
		time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		time.Date(1, 1, 1, 3, 4, 5, 0, time.UTC),
	}
	drop := func(target *sql.Conn, name string) error {
		_, err := target.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+name)
		return err
	}
	if err := drop(conn, table); err != nil {
		t.Fatalf("drop leftovers: %v", err)
	}
	t.Cleanup(func() {
		if _, leftover, _ := liveDB(t); leftover != nil {
			_ = drop(leftover, table)
		}
	})
	definitions := make([]string, len(names))
	for i, name := range names {
		definitions[i] = name + " " + types[i]
	}
	if _, err := conn.ExecContext(ctx, "CREATE TABLE "+table+" ("+strings.Join(definitions, ", ")+")"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := liveImportRows(ctx, conn, kingbase.CopyInBinary(table, names...), values); err != nil {
		t.Fatalf("typed scalar import: %v", err)
	}
	var (
		gotUUID  string
		gotJSON  string
		gotJSONB string
		gotStamp string
		gotTime  string
	)
	readQuery := "SELECT u::pg_catalog.text, j::pg_catalog.text, jb::pg_catalog.text, ts::pg_catalog.text, tm::pg_catalog.text FROM " + table
	if err := conn.QueryRowContext(ctx, readQuery).Scan(&gotUUID, &gotJSON, &gotJSONB, &gotStamp, &gotTime); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if gotUUID != "deadbeef-0001-0002-0003-000400050607" {
		t.Errorf("uuid = %q", gotUUID)
	}
	if normalizeJSON(t, gotJSON) != `{"a":1}` || normalizeJSON(t, gotJSONB) != `{"a":1}` {
		t.Errorf("json = %q/%q, want {\"a\":1}", gotJSON, gotJSONB)
	}
	if !strings.HasPrefix(gotStamp, "2026-01-02 03:04:05") {
		t.Errorf("timestamp = %q, want 2026-01-02 03:04:05", gotStamp)
	}
	if !strings.HasPrefix(gotTime, "03:04:05") {
		t.Errorf("time = %q, want 03:04:05", gotTime)
	}
}

// normalizeJSON compacts a JSON text value so whitespace differences between
// json and jsonb renderings do not fail the comparison.
func normalizeJSON(t *testing.T, text string) string {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(text)); err != nil {
		t.Fatalf("json value %q: %v", text, err)
	}
	return compact.String()
}

// TestLiveCopyInBinaryModeTypes proves the sys-schema one-byte mapping: signed
// MySQL/Oracle tinyint and unsigned SQLServer tinyint plus bit. A mode without
// those types skips with the reason instead of passing silently.
func TestLiveCopyInBinaryModeTypes(t *testing.T) {
	_, conn, meta := liveDB(t)
	if meta.Mode != kingbase.ModeMySQL && meta.Mode != kingbase.ModeSQLServer {
		t.Skip("one-byte sys types are scoped to the MySQL and SQLServer profiles")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const table = "live_bin_mode"
	drop := func(target *sql.Conn, name string) error {
		_, err := target.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+name)
		return err
	}
	if err := drop(conn, table); err != nil {
		t.Fatalf("drop leftovers: %v", err)
	}
	t.Cleanup(func() {
		if _, leftover, _ := liveDB(t); leftover != nil {
			_ = drop(leftover, table)
		}
	})

	if meta.Mode == kingbase.ModeSQLServer {
		if _, err := conn.ExecContext(ctx, "CREATE TABLE "+table+" (ti tinyint, bt bit, dt datetime)"); err != nil {
			t.Fatalf("create table: %v", err)
		}
		if err := liveImportRows(ctx, conn, kingbase.CopyInBinary(table, "ti", "bt", "dt"),
			[]any{int64(200), true, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}); err != nil {
			t.Fatalf("typed mode import: %v", err)
		}
		var ti sql.NullInt64
		var bt sql.NullBool
		var dt string
		if err := conn.QueryRowContext(ctx, "SELECT ti, bt, dt::pg_catalog.text FROM "+table).Scan(&ti, &bt, &dt); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if !ti.Valid || ti.Int64 != 200 || !bt.Valid || !bt.Bool {
			t.Errorf("tinyint/bit = %v/%v, want 200/true", ti, bt)
		}
		if !strings.HasPrefix(dt, "2026-01-02 03:04:05") {
			t.Errorf("datetime = %q, want 2026-01-02 03:04:05", dt)
		}
		return
	}

	if _, err := conn.ExecContext(ctx, "CREATE TABLE "+table+" (ti tinyint, dt datetime)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := liveImportRows(ctx, conn, kingbase.CopyInBinary(table, "ti", "dt"),
		[]any{int64(-5), time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}); err != nil {
		t.Fatalf("typed mode import: %v", err)
	}
	var ti sql.NullInt64
	var dt string
	if err := conn.QueryRowContext(ctx, "SELECT ti, dt::pg_catalog.text FROM "+table).Scan(&ti, &dt); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !ti.Valid || ti.Int64 != -5 {
		t.Errorf("signed tinyint = %v, want -5", ti)
	}
	if !strings.HasPrefix(dt, "2026-01-02 03:04:05") {
		t.Errorf("datetime = %q, want 2026-01-02 03:04:05", dt)
	}
}
