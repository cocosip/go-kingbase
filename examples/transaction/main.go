// Command transaction shows an explicit transaction with context and rollback.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/cocosip/go-kingbase"
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "CREATE TEMPORARY TABLE example_items (id int)"); err != nil {
		_ = tx.Rollback()
		log.Fatal(err)
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO example_items (id) VALUES ($1)", int64(1))
	if err != nil {
		_ = tx.Rollback()
		log.Fatal(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		log.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("committed rows=%d\n", affected)
}
