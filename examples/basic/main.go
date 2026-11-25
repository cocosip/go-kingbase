// Command basic shows opening a KingbaseES connection, querying with a
// qualified binary parameter target, and scanning a text array.
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
	if err := db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}

	var number int64
	if err := db.QueryRowContext(ctx, "SELECT $1::pg_catalog.int8", int64(7)).Scan(&number); err != nil {
		log.Fatal(err)
	}

	var tags kingbase.Array[string]
	if err := db.QueryRowContext(ctx, "SELECT ARRAY['a','b']::text[]").Scan(&tags); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("number=%d tags=%v\n", number, tags.Elements)
}
