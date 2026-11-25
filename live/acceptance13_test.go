//go:build live

package live_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func TestLiveMySQLGeneratedID(t *testing.T) {
	db, conn, meta := liveDB(t)
	if meta.Mode != kingbase.ModeMySQL {
		t.Skip("MySQL LAST_INSERT_ID capture is scoped to MySQL mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tableName := fmt.Sprintf("live_myid_%d", time.Now().UnixNano())
	table := kingbase.QuoteIdentifier(tableName)
	if _, err := conn.ExecContext(ctx, "CREATE TABLE "+table+" (id serial PRIMARY KEY, name varchar(20))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP TABLE IF EXISTS "+table)
	}()
	idDB, err := sql.Open("kingbase", os.Getenv("KINGBASE_TEST_DSN")+" get_last_insert_id=yes")
	if err != nil {
		t.Fatalf("Open with generated IDs: %v", err)
	}
	defer func() { _ = idDB.Close() }()
	result, err := idDB.ExecContext(ctx, "INSERT INTO "+table+" (name) VALUES ('first')")
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
	// A tinyint auto-increment column exercises the generated tinyint
	// conversion: the listener types LAST_INSERT_ID() as UINT8, so the value is
	// converted from exact decimal text to int64.
	tinyName := fmt.Sprintf("live_tinyid_%d", time.Now().UnixNano())
	tiny := kingbase.QuoteIdentifier(tinyName)
	if _, err := conn.ExecContext(ctx, "CREATE TABLE "+tiny+" (id tinyint NOT NULL AUTO_INCREMENT PRIMARY KEY, v int)"); err != nil {
		t.Fatalf("CREATE TABLE tinyint: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.ExecContext(cleanupCtx, "DROP TABLE IF EXISTS "+tiny)
	}()
	for want := int64(1); want <= 2; want++ {
		result, err := idDB.ExecContext(ctx, "INSERT INTO "+tiny+" (v) VALUES ("+strconv.FormatInt(want, 10)+")")
		if err != nil {
			t.Fatalf("tinyint INSERT: %v", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("tinyint LastInsertId: %v", err)
		}
		if id != want {
			t.Errorf("tinyint LastInsertId = %d, want %d", id, want)
		}
	}
}
