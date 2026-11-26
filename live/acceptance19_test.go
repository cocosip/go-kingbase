//go:build live

package live_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	kingbase "github.com/cocosip/go-kingbase"
)

// readCopyOut consumes a COPY TO STDOUT stream and returns its concatenated
// payload.
func readCopyOut(ctx context.Context, t *testing.T, conn *sql.Conn, statement string) []byte {
	t.Helper()
	rows, err := conn.QueryContext(ctx, statement)
	if err != nil {
		t.Fatalf("QueryContext(%s): %v", statement, err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("Columns: %v", err)
	}
	if len(columns) != 1 || columns[0] != "copy" {
		t.Fatalf("Columns() = %v, want [copy]", columns)
	}
	var payload []byte
	chunks := 0
	for rows.Next() {
		var chunk []byte
		if err := rows.Scan(&chunk); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		payload = append(payload, chunk...)
		chunks++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if chunks == 0 {
		t.Fatalf("COPY returned no chunk")
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return payload
}

// TestLiveCopyOut proves F-706 on a real server: COPY ... TO STDOUT streams the
// payload as chunks in text and binary form, reports server errors, and leaves
// the session reusable.
func TestLiveCopyOut(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	text := readCopyOut(ctx, t, conn, "COPY (SELECT 1 UNION ALL SELECT 2) TO STDOUT")
	if string(text) != "1\n2\n" {
		t.Errorf("text payload = %q, want %q", text, "1\n2\n")
	}

	binary := readCopyOut(ctx, t, conn, "COPY (SELECT 1 UNION ALL SELECT 2) TO STDOUT WITH (FORMAT binary)")
	// KingbaseES uses its own binary header signature (KBCOPY) instead of the
	// PostgreSQL one; the driver streams the payload opaquely, so this is a
	// server difference the test records rather than rewrites.
	signature := []byte("KBCOPY\n\xff\r\n\x00")
	if !bytes.HasPrefix(binary, signature) {
		t.Fatalf("binary payload = %q, want the KBCOPY signature", binary[:min(len(binary), 19)])
	}
	if !bytes.HasSuffix(binary, []byte("\xff\xff")) {
		t.Errorf("binary payload does not end with the file trailer: %q", binary[len(binary)-min(len(binary), 32):])
	}

	// A missing relation is reported as a server error, not as an empty stream,
	// and the session survives it.
	_, err := conn.QueryContext(ctx, "COPY (SELECT * FROM live_missing_copy_out) TO STDOUT")
	if err == nil {
		t.Fatal("COPY of a missing relation succeeded")
	}
	var serverErr *kingbase.Error
	if !errors.As(err, &serverErr) {
		t.Fatalf("error = %v, want *kingbase.Error", err)
	}
	if serverErr.Code == "" || !strings.Contains(strings.ToLower(serverErr.Message), "live_missing_copy_out") {
		t.Errorf("server error = %+v, want a SQLSTATE and the relation name", serverErr)
	}

	// Exec cannot read a payload, so it must fail before sending anything.
	if _, err := conn.ExecContext(ctx, "COPY (SELECT 1) TO STDOUT"); err == nil {
		t.Fatal("ExecContext accepted COPY TO STDOUT")
	}

	after := readCopyOut(ctx, t, conn, "COPY (SELECT 3) TO STDOUT")
	if string(after) != "3\n" {
		t.Errorf("payload after the error = %q, want %q", after, "3\n")
	}
}

// TestLiveCopyInRawRoundTrip proves F-707 on a real server: a payload produced by
// COPY TO STDOUT is streamed back verbatim through a raw COPY FROM STDIN
// statement, in text and binary form, without the driver decoding or re-encoding
// a single byte.
func TestLiveCopyInRawRoundTrip(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dropTables := func(target *sql.Conn) error {
		for _, statement := range []string{
			"DROP TABLE IF EXISTS live_raw_dst",
			"DROP TABLE IF EXISTS live_raw_src",
		} {
			if _, err := target.ExecContext(context.Background(), statement); err != nil {
				return err
			}
		}
		return nil
	}
	if err := dropTables(conn); err != nil {
		t.Fatalf("drop leftovers: %v", err)
	}
	// A COPY import failure retires its connection by contract, so cleanup runs
	// best effort on whatever connection is still alive.
	t.Cleanup(func() {
		if _, leftover, _ := liveDB(t); leftover != nil {
			_ = dropTables(leftover)
		}
	})
	if _, err := conn.ExecContext(ctx, "CREATE TABLE live_raw_src (id integer, name text)"); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO live_raw_src (id, name) VALUES (1, 'one'), (2, 'two')"); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CREATE TABLE live_raw_dst (id integer, name text)"); err != nil {
		t.Fatalf("create target: %v", err)
	}

	importPayload := func(statement string, payload []byte) {
		t.Helper()
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		prepared, err := tx.PrepareContext(ctx, statement)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("Prepare(%s): %v", statement, err)
		}
		// Two chunks prove the driver forwards each Exec payload unchanged
		// instead of assuming the caller sends one buffer.
		half := len(payload) / 2
		if _, err := prepared.ExecContext(ctx, payload[:half]); err != nil {
			_ = prepared.Close()
			_ = tx.Rollback()
			t.Fatalf("ExecContext(first chunk): %v", err)
		}
		if _, err := prepared.ExecContext(ctx, payload[half:]); err != nil {
			_ = prepared.Close()
			_ = tx.Rollback()
			t.Fatalf("ExecContext(second chunk): %v", err)
		}
		if _, err := prepared.ExecContext(ctx); err != nil {
			_ = prepared.Close()
			_ = tx.Rollback()
			t.Fatalf("finishing ExecContext: %v", err)
		}
		if err := prepared.Close(); err != nil {
			_ = tx.Rollback()
			t.Fatalf("Close: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}

	text := readCopyOut(ctx, t, conn, "COPY (SELECT id, name FROM live_raw_src ORDER BY id) TO STDOUT")
	importPayload(kingbase.CopyInRaw("live_raw_dst", "id", "name"), text)

	binary := readCopyOut(ctx, t, conn, "COPY (SELECT id, name FROM live_raw_src ORDER BY id) TO STDOUT WITH (FORMAT binary)")
	importPayload(kingbase.CopyInRawBinary("live_raw_dst", "id", "name"), binary)

	var rows, ones int64
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM live_raw_dst").Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 4 {
		t.Errorf("imported rows = %d, want 4", rows)
	}
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM live_raw_dst WHERE name = 'one'").Scan(&ones); err != nil {
		t.Fatalf("count names: %v", err)
	}
	if ones != 2 {
		t.Errorf("rows named one = %d, want 2", ones)
	}

	// A payload the server cannot parse must abort the stream instead of
	// importing a partial table.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	bad, err := tx.PrepareContext(ctx, kingbase.CopyInRawBinary("live_raw_dst", "id", "name"))
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := bad.ExecContext(ctx, []byte("not a binary copy payload")); err != nil {
		_ = bad.Close()
		_ = tx.Rollback()
		t.Fatalf("ExecContext: %v", err)
	}
	_, err = bad.ExecContext(ctx)
	_ = bad.Close()
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("a malformed binary payload completed successfully")
	}
	// The failed import retires its connection; a fresh one must still see the
	// table unchanged, because nothing from the aborted stream was committed.
	_, fresh, _ := liveDB(t)
	var after int64
	if err := fresh.QueryRowContext(ctx, "SELECT count(*) FROM live_raw_dst").Scan(&after); err != nil {
		t.Fatalf("count after the failed import: %v", err)
	}
	if after != rows {
		t.Errorf("rows after the failed import = %d, want %d", after, rows)
	}
}
