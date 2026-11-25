package kingbase

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
)

func TestWrongProtocolPeerDiagnosticsBounded(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	var accepts atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		peer, err := listener.Accept()
		if err != nil {
			return
		}
		accepts.Add(1)
		payload := "HTTP/1.1 400 Bad Request\r\n" + strings.Repeat("X", 4096) + "\r\n\r\n"
		_, _ = peer.Write([]byte(payload))
		_ = peer.Close()
	}()

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	connector := &Connector{cfg: &Config{
		Network:        "tcp",
		Host:           host,
		Port:           port,
		User:           testUser,
		Database:       testDatabase,
		SSLMode:        SSLDisable,
		ConnectTimeout: 3 * time.Second,
	}}
	if _, err := connector.Connect(context.Background()); err == nil {
		t.Fatal("Connect succeeded against a non-PG peer")
	} else {
		message := err.Error()
		<-done
		if accepts.Load() != 1 {
			t.Errorf("peer accepted %d connections, want exactly 1 (no protocol fallback)", accepts.Load())
		}
		if len(message) > 512 {
			t.Errorf("diagnostic is %d bytes, want a bounded message", len(message))
		}
		if strings.Contains(message, "HTTP") || strings.Contains(message, strings.Repeat("X", 32)) {
			t.Errorf("diagnostic leaked peer payload: %q", message)
		}
		if !strings.Contains(message, "tcp handshake failed") {
			t.Errorf("diagnostic = %q, want the configured transport named", message)
		}
	}
}

func TestNonUTF8EncodingDiagnosticIsBounded(t *testing.T) {
	encoding := "LATIN" + strings.Repeat("A", 4096) + "\x1b"
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			peer.Errorf("read startup: %v", err)
			return
		}
		if err := peer.StartupOK([][2]string{{"client_encoding", encoding}}); err != nil {
			peer.Errorf("StartupOK: %v", err)
		}
	})

	_, err := connectPeer(t, srv, "")
	if err == nil {
		t.Fatal("Connect accepted a non-UTF8 encoding")
	}
	message := err.Error()
	if !strings.Contains(message, "client_encoding") {
		t.Errorf("diagnostic = %q, want the rejected parameter named", message)
	}
	if len(message) > 512 {
		t.Errorf("diagnostic is %d bytes, want a bounded message", len(message))
	}
	if strings.ContainsRune(message, 0x1b) || strings.ContainsRune(message, 0x00) {
		t.Errorf("diagnostic contains raw control bytes: %q", message)
	}
	if !strings.Contains(message, "LATIN") {
		t.Errorf("diagnostic = %q, want a recognizable prefix of the value", message)
	}
}
