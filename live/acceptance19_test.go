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
