//go:build live

package live_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func TestLiveColumnMetadata(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, `SELECT 1::pg_catalog.int4 AS n, 'x'::pg_catalog.varchar(7) AS label, '2024-01-02'::pg_catalog.date AS day`)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.ColumnTypes()
	if err != nil {
		t.Fatalf("ColumnTypes: %v", err)
	}
	if len(columns) != 3 {
		t.Fatalf("columns = %d, want 3", len(columns))
	}
	for _, column := range columns {
		length, lengthOK := column.Length()
		nullable, nullableOK := column.Nullable()
		t.Logf("column=%s database=%s scan=%v length=%d/%t nullable=%t/%t",
			column.Name(), column.DatabaseTypeName(), column.ScanType(), length,
			lengthOK, nullable, nullableOK)
	}
	if columns[0].DatabaseTypeName() != "INT4" || columns[1].DatabaseTypeName() != "VARCHAR" ||
		columns[2].DatabaseTypeName() != "DATE" {
		t.Errorf("database type names = %q/%q/%q", columns[0].DatabaseTypeName(),
			columns[1].DatabaseTypeName(), columns[2].DatabaseTypeName())
	}
	if length, ok := columns[1].Length(); !ok || length != 7 {
		t.Errorf("varchar length = %d/%t, want 7", length, ok)
	}
	var number int64
	var label string
	var day time.Time
	if !rows.Next() {
		t.Fatalf("metadata rows: %v", rows.Err())
	}
	if err := rows.Scan(&number, &label, &day); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if number != 1 || label != "x" || day.Format("2006-01-02") != "2024-01-02" {
		t.Errorf("metadata values = %d/%q/%s", number, label, day)
	}
}

func TestLiveCatalogLifecycle(t *testing.T) {
	_, conn, meta := liveDB(t)
	if meta.Mode != kingbase.ModePG {
		t.Skip("type lifecycle probe is scoped to the PG profile")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	typeName := fmt.Sprintf("live_catalog_%d", time.Now().UnixNano())
	quoted := kingbase.QuoteIdentifier(typeName)
	if _, err := conn.ExecContext(ctx, `CREATE TYPE `+quoted+` AS ENUM ('a', 'b')`); err != nil {
		t.Fatalf("CREATE TYPE: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.ExecContext(cleanupCtx, `DROP TYPE IF EXISTS `+quoted)
	})
	var value string
	if err := conn.QueryRowContext(ctx, `SELECT 'a'::`+quoted).Scan(&value); err != nil {
		t.Fatalf("enum value on the creating connection: %v", err)
	}
	if value != "a" {
		t.Errorf("enum value = %q", value)
	}
	secondDB, err := sql.Open("kingbase", os.Getenv("KINGBASE_TEST_DSN"))
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = secondDB.Close() }()
	secondConn, err := secondDB.Conn(ctx)
	if err != nil {
		t.Fatalf("second Conn: %v", err)
	}
	defer func() { _ = secondConn.Close() }()
	var secondValue string
	if err := secondConn.QueryRowContext(ctx, `SELECT 'b'::`+quoted).Scan(&secondValue); err != nil {
		t.Fatalf("enum value on a fresh catalog: %v", err)
	}
	if secondValue != "b" {
		t.Errorf("fresh enum value = %q", secondValue)
	}
	if _, err := conn.ExecContext(ctx, `DROP TYPE `+quoted); err != nil {
		t.Fatalf("DROP TYPE: %v", err)
	}
	if err := secondConn.QueryRowContext(ctx, `SELECT 'a'::`+quoted).Scan(&value); err == nil {
		t.Errorf("stale catalog accepted a dropped type")
	}
}

func TestLiveBoundaryValues(t *testing.T) {
	_, conn, meta := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if meta.Mode == kingbase.ModeSQLServer {
		t.Skip("the shared numeric edge query uses PG numeric syntax")
	}
	var nan string
	if err := conn.QueryRowContext(ctx, `SELECT 'NaN'::pg_catalog.numeric`).Scan(&nan); err != nil {
		t.Fatalf("numeric NaN: %v", err)
	}
	if nan != "NaN" {
		t.Errorf("numeric NaN = %q", nan)
	}
	if err := conn.QueryRowContext(ctx, `SELECT 'infinity'::pg_catalog.timestamp`).Scan(new(time.Time)); !errors.Is(err, kingbase.ErrValueConversion) {
		t.Errorf("timestamp infinity error = %v, want ErrValueConversion", err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT '0001-01-01 BC'::pg_catalog.date`).Scan(new(time.Time)); !errors.Is(err, kingbase.ErrValueConversion) {
		t.Errorf("BC date error = %v, want ErrValueConversion", err)
	}
	var exact string
	if err := conn.QueryRowContext(ctx, `SELECT 999999999999999999999999.123456789::pg_catalog.numeric`).Scan(&exact); err != nil {
		t.Fatalf("high-precision numeric: %v", err)
	}
	if exact != "999999999999999999999999.123456789" {
		t.Errorf("high-precision numeric = %q", exact)
	}
}
