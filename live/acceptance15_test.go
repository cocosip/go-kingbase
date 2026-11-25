//go:build live

package live_test

import (
	"context"
	"testing"
	"time"
)

// TestLiveModeTypeCodecs checks the verified non-PG type codecs on the live
// four-mode deployment, in both the text (direct) and binary (prepared) paths.
func TestLiveModeTypeCodecs(t *testing.T) {
	db, conn, meta := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	queryOne := func(query string, args ...any) any {
		t.Helper()
		var value any
		if err := conn.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return value
	}
	preparedOne := func(query string) any {
		t.Helper()
		stmt, err := conn.PrepareContext(ctx, query)
		if err != nil {
			t.Fatalf("prepare %s: %v", query, err)
		}
		defer func() { _ = stmt.Close() }()
		var value any
		if err := stmt.QueryRowContext(ctx).Scan(&value); err != nil {
			t.Fatalf("prepared %s: %v", query, err)
		}
		return value
	}

	switch meta.Mode.String() {
	case "oracle":
		if got := queryOne("SELECT CAST(5 AS tinyint)"); got != int64(5) {
			t.Errorf("oracle tinyint text = %#v, want int64 5", got)
		}
		if got := preparedOne("SELECT CAST(5 AS tinyint)"); got != int64(5) {
			t.Errorf("oracle tinyint binary = %#v, want int64 5", got)
		}
		if got := preparedOne("SELECT CAST(-1 AS tinyint)"); got != int64(-1) {
			t.Errorf("oracle tinyint binary -1 = %#v, want int64 -1", got)
		}
	case "mysql":
		if got := queryOne("SELECT CAST(127 AS tinyint)"); got != int64(127) {
			t.Errorf("mysql tinyint text = %#v, want int64 127", got)
		}
		if got := preparedOne("SELECT CAST(-1 AS tinyint)"); got != int64(-1) {
			t.Errorf("mysql tinyint binary = %#v, want int64 -1", got)
		}
		if got := queryOne("SELECT CAST(4294967295 AS uint4)"); got != int64(4294967295) {
			t.Errorf("mysql uint4 text = %#v, want int64 4294967295", got)
		}
		if got := preparedOne("SELECT CAST(4294967295 AS uint4)"); got != int64(4294967295) {
			t.Errorf("mysql uint4 binary = %#v, want int64 4294967295", got)
		}
		const maxUint8 = "18446744073709551615"
		if got := queryOne("SELECT CAST(" + maxUint8 + " AS uint8)"); got != maxUint8 {
			t.Errorf("mysql uint8 text = %#v, want %q", got, maxUint8)
		}
		if got := preparedOne("SELECT CAST(" + maxUint8 + " AS uint8)"); got != maxUint8 {
			t.Errorf("mysql uint8 binary = %#v, want %q", got, maxUint8)
		}
		if got := queryOne("SELECT CAST('2024-05-06' AS year)"); got != int64(2024) {
			t.Errorf("mysql year = %#v, want int64 2024", got)
		}
		if got, ok := queryOne("SELECT CAST('2024-01-02 03:04:05' AS datetime)").(time.Time); !ok ||
			got.Format("2006-01-02 15:04:05") != "2024-01-02 03:04:05" {
			t.Errorf("mysql datetime text = %#v, want that wall clock", got)
		}
		if got, ok := queryOne("SELECT CAST('abc' AS varbinary(3))").([]byte); !ok || string(got) != "abc" {
			t.Errorf("mysql varbinary text = %#v, want []byte abc", got)
		}
		if got, ok := preparedOne("SELECT CAST('abc' AS varbinary(3))").([]byte); !ok || string(got) != "abc" {
			t.Errorf("mysql varbinary binary = %#v, want []byte abc", got)
		}
		// The datetime binary payload is session-timezone dependent, so the
		// verified contract keeps it as raw bytes instead of a shifted instant.
		prepared := preparedOne("SELECT CAST('2024-01-02 03:04:05' AS datetime)")
		if _, ok := prepared.([]byte); !ok {
			t.Errorf("mysql datetime binary = %#v, want raw bytes", prepared)
		}
	case "sqlserver":
		if got := queryOne("SELECT CAST(200 AS tinyint)"); got != int64(200) {
			t.Errorf("sqlserver tinyint text = %#v, want int64 200", got)
		}
		if got := preparedOne("SELECT CAST(200 AS tinyint)"); got != int64(200) {
			t.Errorf("sqlserver tinyint binary = %#v, want int64 200", got)
		}
		if got := queryOne("SELECT CAST(1 AS bit)"); got != true {
			t.Errorf("sqlserver bit 1 = %#v, want true", got)
		}
		if got := queryOne("SELECT CAST(0 AS bit)"); got != false {
			t.Errorf("sqlserver bit 0 = %#v, want false", got)
		}
		if got := preparedOne("SELECT CAST(1 AS bit)"); got != true {
			t.Errorf("sqlserver bit binary = %#v, want true", got)
		}
		if got, ok := queryOne("SELECT CAST('ab' AS nchar(3))").(string); !ok || got != "ab " {
			t.Errorf("sqlserver nchar = %#v, want padded string", got)
		}
		if got, ok := preparedOne("SELECT CAST('abc' AS varbinary(3))").([]byte); !ok || string(got) != "abc" {
			t.Errorf("sqlserver varbinary binary = %#v, want []byte abc", got)
		}
		if got, ok := queryOne("SELECT CAST('2024-01-02 03:04:05' AS datetime)").(time.Time); !ok ||
			got.Format("2006-01-02 15:04:05") != "2024-01-02 03:04:05" {
			t.Errorf("sqlserver datetime text = %#v, want that wall clock", got)
		}
		if got := queryOne("SELECT CAST(1234.56 AS money)"); got != "1234.5600" {
			t.Errorf("sqlserver money text = %#v, want 1234.5600", got)
		}
		if got := preparedOne("SELECT CAST(1234.56 AS money)"); got != "1234.5600" {
			t.Errorf("sqlserver money binary = %#v, want 1234.5600", got)
		}
		if got, ok := queryOne("SELECT CAST('2024-01-02 03:04:05' AS smalldatetime)").(time.Time); !ok ||
			got.Format("2006-01-02 15:04:05") != "2024-01-02 03:04:00" {
			t.Errorf("sqlserver smalldatetime = %#v, want minute precision", got)
		}
	case "pg":
		if got := queryOne("SELECT CAST(1 AS int4)"); got != int64(1) {
			t.Errorf("pg int4 = %#v", got)
		}
	}
	_ = db
}
