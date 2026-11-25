// Command listener shows pull-driven LISTEN/NOTIFY delivery. The Listener owns
// one dedicated connection; it must not run ordinary commands.
package main

import (
	"context"
	"errors"
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
	cfg, err := kingbase.ParseConfig(dsn)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	listener, err := kingbase.NewListener(ctx, cfg, "orders")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	if _, err := listener.Next(ctx); err != nil {
		if errors.Is(err, kingbase.ErrNotificationGap) {
			log.Print("notification gap; reconnect with NewListener")
			return
		}
		log.Fatal(err)
	}
	fmt.Println("received one notification")
}
