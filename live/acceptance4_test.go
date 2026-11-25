//go:build live

package live_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase"
)

func TestLiveListenerNotification(t *testing.T) {
	_, conn, meta := liveDB(t)
	if meta.Mode != kingbase.ModePG {
		t.Skip("LISTEN/NOTIFY live check is scoped to the PG profile")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := kingbase.ParseConfig(os.Getenv("KINGBASE_TEST_DSN"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	listener, err := kingbase.NewListener(ctx, cfg, "live_channel")
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	defer func() { _ = listener.Close() }()
	if _, err := conn.ExecContext(ctx, "NOTIFY live_channel, 'hello'"); err != nil {
		t.Fatalf("NOTIFY: %v", err)
	}
	notification, err := listener.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if notification.Channel != "live_channel" || notification.Payload != "hello" {
		t.Errorf("notification = %+v", notification)
	}
}
