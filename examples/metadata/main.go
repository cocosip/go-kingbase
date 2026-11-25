// Command metadata shows the per-physical-connection diagnostic report.
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
	connection, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	metadata, err := kingbase.ConnectionMetadata(connection)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("mode=%s source=%s verified=%t wire=%s release=%q\n",
		metadata.Mode, metadata.ModeSource, metadata.ModeVerified, metadata.WireProtocol, metadata.Release)
}
