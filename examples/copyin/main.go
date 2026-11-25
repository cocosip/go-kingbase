// Command copyin shows the statement-shaped COPY FROM STDIN helper on a
// pinned transaction. Call Exec without rows to flush and finish the import.
package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"time"

	"github.com/cocosip/go-kingbase"
)

func main() {
	dsn := os.Getenv("KINGBASE_DSN")
	if dsn == "" {
		log.Fatal("set KINGBASE_DSN to a KingbaseES DSN")
	}
	db, err := sql.Open("kingbase", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	statement, err := tx.PrepareContext(ctx, kingbase.CopyIn("orders", "id", "name"))
	if err != nil {
		_ = tx.Rollback()
		log.Fatal(err)
	}
	for index, name := range []string{"first", "second"} {
		if _, err := statement.ExecContext(ctx, int64(index+1), name); err != nil {
			_ = statement.Close()
			_ = tx.Rollback()
			log.Fatal(err)
		}
	}
	if _, err := statement.ExecContext(ctx); err != nil {
		_ = statement.Close()
		_ = tx.Rollback()
		log.Fatal(err)
	}
	if err := statement.Close(); err != nil {
		_ = tx.Rollback()
		log.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
}
