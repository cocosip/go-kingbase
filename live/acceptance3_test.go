//go:build live

package live_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func TestLiveGeneratedID(t *testing.T) {
	_, _, meta := liveDB(t)
	if meta.Mode != kingbase.ModePG {
		t.Skip("generated-ID capture is scoped to the PG profile")
	}
	generatedDB, err := sql.Open("kingbase", os.Getenv("KINGBASE_TEST_DSN")+" get_last_insert_id=yes")
	if err != nil {
		t.Fatalf("Open generated-ID config: %v", err)
	}
	defer func() { _ = generatedDB.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	generatedConn, err := generatedDB.Conn(ctx)
	if err != nil {
		t.Fatalf("generated-ID Conn: %v", err)
	}
	defer func() { _ = generatedConn.Close() }()
	if _, err := generatedConn.ExecContext(ctx, "CREATE TEMP TABLE live_serial (id serial PRIMARY KEY, name text)"); err != nil {
		t.Fatalf("CREATE TEMP TABLE: %v", err)
	}
	result, err := generatedConn.ExecContext(ctx, "INSERT INTO live_serial (name) VALUES ($1)", "alpha")
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	if id != 1 {
		t.Errorf("LastInsertId = %d, want 1", id)
	}
}

func TestLiveBinaryParameters(t *testing.T) {
	generatedDB, err := sql.Open("kingbase", os.Getenv("KINGBASE_TEST_DSN")+" binary_parameters=yes")
	if err != nil {
		t.Fatalf("Open binary config: %v", err)
	}
	defer func() { _ = generatedDB.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := generatedDB.Conn(ctx)
	if err != nil {
		t.Fatalf("binary Conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	statement, err := conn.PrepareContext(ctx, `SELECT $1::pg_catalog.bytea, $2::pg_catalog.int4, $3::pg_catalog.timestamp`)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = statement.Close() }()
	var blob []byte
	var number int64
	var moment time.Time
	want := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := statement.QueryRowContext(ctx, []byte{0, 1, 2}, int64(9), want).Scan(&blob, &number, &moment); err != nil {
		t.Fatalf("binary parameter scan: %v", err)
	}
	if len(blob) != 3 || blob[2] != 2 || number != 9 || !moment.Equal(want) {
		t.Errorf("binary parameter values = %v/%d/%s", blob, number, moment)
	}
}

func TestLiveArraysAndBinaryPrepared(t *testing.T) {
	_, conn, meta := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if meta.Mode != kingbase.ModeSQLServer {
		var array kingbase.Array[int64]
		if err := conn.QueryRowContext(ctx, `SELECT ARRAY[1,2,3]::pg_catalog.int4[]`).Scan(&array); err != nil {
			t.Fatalf("array scan: %v", err)
		}
		if !array.Valid || len(array.Elements) != 3 || array.Elements[0] != 1 || array.Elements[2] != 3 {
			t.Errorf("array = %+v", array)
		}
	} else {
		// The documented SQLServer-mode limitation is measured here instead of
		// asserted by a comment: the probe must be a server rejection, and a
		// listener that starts accepting the literal must satisfy the values.
		var array kingbase.Array[int64]
		err := conn.QueryRowContext(ctx, `SELECT ARRAY[1,2,3]::pg_catalog.int4[]`).Scan(&array)
		var serverErr *kingbase.Error
		switch {
		case errors.As(err, &serverErr):
			t.Logf("SQLServer-mode listener rejects the PG ARRAY[] literal: %v", serverErr)
		case err == nil:
			if !array.Valid || len(array.Elements) != 3 || array.Elements[0] != 1 || array.Elements[2] != 3 {
				t.Errorf("SQLServer-mode array = %+v, want the ARRAY[1,2,3] values", array)
			}
		default:
			t.Fatalf("SQLServer-mode array probe = %v, want a server error or a decoded array", err)
		}
	}
	statement, err := conn.PrepareContext(ctx, `SELECT $1::pg_catalog.int4, $2::pg_catalog.text, $3::pg_catalog.bytea`)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = statement.Close() }()
	var number int64
	var text string
	var blob []byte
	if err := statement.QueryRowContext(ctx, int64(9), "payload", []byte("abc")).Scan(&number, &text, &blob); err != nil {
		t.Fatalf("binary prepared scan: %v", err)
	}
	if number != 9 || text != "payload" || string(blob) != "abc" {
		t.Errorf("binary prepared values = %d/%q/%v", number, text, blob)
	}
}
