//go:build live

package live_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func TestLiveReadOnlyTransactionEnforcement(t *testing.T) {
	_, conn, _ := liveDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	table := fmt.Sprintf("live_ro_%d", time.Now().UnixNano())
	quoted := kingbase.QuoteIdentifier(table)
	if _, err := conn.ExecContext(ctx, `CREATE TABLE `+quoted+` (id int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.ExecContext(cleanupCtx, `DROP TABLE IF EXISTS `+quoted)
	})
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("BeginTx read-only: %v", err)
	}
	_, insertErr := tx.ExecContext(ctx, `INSERT INTO `+quoted+` (id) VALUES (1)`)
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if insertErr == nil {
		t.Fatal("a READ ONLY transaction accepted a write; read-only enforcement is not a profile guarantee")
	}
	var serverErr *kingbase.Error
	if !errors.As(insertErr, &serverErr) || serverErr.Code != "25006" {
		t.Fatalf("read-only write error = %v, want a server rejection with SQLSTATE 25006", insertErr)
	}
}
