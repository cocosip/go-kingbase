package testpeer

import (
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/wire"
)

// TestCloseDrainsPendingMessage pins the teardown order: a handler that has not
// read the client's final Terminate yet still receives it, so Close cannot turn
// a message the peer already holds into a spurious EOF that a script reports as
// a missing Terminate.
func TestCloseDrainsPendingMessage(t *testing.T) {
	parked := make(chan struct{})
	release := make(chan struct{})
	srv := Start(t, func(peer *Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			peer.Errorf("ReadStartup: %v", err)
			return
		}
		if err := peer.AuthenticateCleartext("secret"); err != nil {
			peer.Errorf("AuthenticateCleartext: %v", err)
			return
		}
		if err := peer.StartupOK([][2]string{{"client_encoding", "UTF8"}}); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		close(parked)
		<-release
		typ, _, err := peer.ReadMessage()
		if err != nil {
			peer.Errorf("pending message was discarded by teardown: %v", err)
			return
		}
		if typ != wire.MsgTerminate {
			peer.Errorf("pending message = %q, want Terminate", typ)
		}
	})

	netConn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("dial peer: %v", err)
	}
	writer := wire.NewWriter(netConn)
	payload := wire.EncodeStartupPayload([][2]string{{"user", "alice"}, {"database", "tenant"}})
	if err := writer.WriteStartup(payload); err != nil {
		t.Fatalf("write startup: %v", err)
	}
	if err := writer.WriteMessage(wire.MsgPassword, []byte("secret\x00")); err != nil {
		t.Fatalf("write password: %v", err)
	}
	// Drain the startup response the way a real client does, so closing the
	// socket sends a clean FIN instead of a reset that would discard the bytes
	// the peer already holds.
	reader := wire.NewReader(netConn, 0)
	for {
		typ, _, err := reader.ReadMessage()
		if err != nil {
			t.Fatalf("read startup response: %v", err)
		}
		if typ == wire.MsgReadyForQuery {
			break
		}
	}
	<-parked
	if err := writer.WriteMessage(wire.MsgTerminate, nil); err != nil {
		t.Fatalf("write terminate: %v", err)
	}
	if err := netConn.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	// Release the handler inside the grace window: Close has started, the
	// socket still holds the unread Terminate, and the handler resumes before
	// the grace expires.
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(release)
	}()
	srv.Close()
}
