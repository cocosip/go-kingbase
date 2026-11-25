// Command cursor shows an explicit refcursor on its owning transaction.
package main

import (
	"context"
	"database/sql"
	"fmt"
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "DECLARE example_cursor CURSOR FOR SELECT 41, 42"); err != nil {
		_ = tx.Rollback()
		log.Fatal(err)
	}
	cursor, err := kingbase.NewCursor(tx, "example_cursor")
	if err != nil {
		_ = tx.Rollback()
		log.Fatal(err)
	}
	rows, err := cursor.FetchAll(ctx)
	if err != nil {
		_ = tx.Rollback()
		log.Fatal(err)
	}
	for rows.Next() {
		var first, second int64
		if err := rows.Scan(&first, &second); err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			log.Fatal(err)
		}
		fmt.Printf("fetched %d, %d\n", first, second)
	}
	if err := rows.Close(); err != nil {
		_ = tx.Rollback()
		log.Fatal(err)
	}
	if err := cursor.Close(ctx); err != nil {
		_ = tx.Rollback()
		log.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
}
