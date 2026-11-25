package kingbase

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func endpointPort(t *testing.T, address string) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split %q: %v", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("port %q: %v", portText, err)
	}
	return port
}

func TestParseConfigEndpointLists(t *testing.T) {
	cfg := mustParse(t, "host=a,b port=1111,2222 user=u")
	if !reflect.DeepEqual(cfg.Hosts, []string{"a", "b"}) {
		t.Errorf("Hosts = %v", cfg.Hosts)
	}
	if !reflect.DeepEqual(cfg.Ports, []int{1111, 2222}) {
		t.Errorf("Ports = %v", cfg.Ports)
	}
	if cfg.Host != "a" || cfg.Port != 1111 {
		t.Errorf("first endpoint = %q:%d", cfg.Host, cfg.Port)
	}
	endpoints := cfg.endpoints()
	if len(endpoints) != 2 || endpoints[0].address != "a:1111" || endpoints[1].address != "b:2222" {
		t.Errorf("endpoints = %+v", endpoints)
	}

	// A shorter list repeats its last entry.
	short := mustParse(t, "host=a,b port=1111 user=u")
	endpoints = short.endpoints()
	if len(endpoints) != 2 || endpoints[1].address != "b:1111" {
		t.Errorf("short port list endpoints = %+v", endpoints)
	}
}

func TestTLSConfigUsesEndpointHost(t *testing.T) {
	config, err := tlsConfigForHost(&Config{SSLMode: SSLRequire}, "second.example")
	if err != nil {
		t.Fatalf("tlsConfigForHost: %v", err)
	}
	if config.ServerName != "second.example" {
		t.Errorf("ServerName = %q, want the endpoint host", config.ServerName)
	}
}

func TestConnectSkipsUnreachableEndpoint(t *testing.T) {
	// Reserve and release a loopback port so the first candidate refuses.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	livePort := endpointPort(t, srv.Addr())
	dsn := fmt.Sprintf("host=127.0.0.1,127.0.0.1 port=%d,%d user=alice dbname=tenant sslmode=disable",
		deadPort, livePort)
	cfg, err := ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	physical, err := (&Connector{cfg: cfg}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn := physical.(*conn)
	if want := fmt.Sprintf("127.0.0.1:%d", livePort); conn.netEndpoint.address != want {
		t.Errorf("connected endpoint = %q, want %q", conn.netEndpoint.address, want)
	}
	_ = conn.Close()
}

func TestConnectTargetSessionReadWrite(t *testing.T) {
	srv := startTargetPeer(t, "off")
	physical, err := connectPeer(t, srv, "target_session_attrs=read-write")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_ = physical.Close()
}

func TestConnectTargetSessionReadOnly(t *testing.T) {
	srv := startTargetPeer(t, "on")
	physical, err := connectPeer(t, srv, "target_session_attrs=read-only")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_ = physical.Close()
}

func TestConnectTargetSessionMismatchFails(t *testing.T) {
	srv := startTargetPeer(t, "on")
	_, err := connectPeer(t, srv, "target_session_attrs=read-write")
	if err == nil || !strings.Contains(err.Error(), "read-write is not satisfied") {
		t.Fatalf("Connect error = %v, want a read-write mismatch", err)
	}
}

func TestConnectTargetSessionRejectsUnknownReport(t *testing.T) {
	srv := startTargetPeer(t, "maybe")
	_, err := connectPeer(t, srv, "target_session_attrs=read-only")
	if err == nil || !strings.Contains(err.Error(), "unexpected transaction_read_only") {
		t.Fatalf("Connect error = %v, want an unverified report error", err)
	}
}

func TestConnectTargetSessionSkipsMismatchedHost(t *testing.T) {
	readOnlyHost := startTargetPeer(t, "on")
	readWriteHost := startTargetPeer(t, "off")
	dsn := fmt.Sprintf("host=127.0.0.1,127.0.0.1 port=%d,%d user=alice dbname=tenant sslmode=disable target_session_attrs=read-write",
		endpointPort(t, readOnlyHost.Addr()), endpointPort(t, readWriteHost.Addr()))
	cfg, err := ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	physical, err := (&Connector{cfg: cfg}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn := physical.(*conn)
	if want := fmt.Sprintf("127.0.0.1:%d", endpointPort(t, readWriteHost.Addr())); conn.netEndpoint.address != want {
		t.Errorf("connected endpoint = %q, want the read-write host %q", conn.netEndpoint.address, want)
	}
	_ = conn.Close()
}

// startTargetPeer serves one session whose transaction_read_only report is
// readOnlyValue.
func startTargetPeer(t *testing.T, readOnlyValue string) *testpeer.Server {
	t.Helper()
	return testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		sql, err := wire.DecodeQuery(payload)
		if err != nil || sql != "SHOW transaction_read_only" {
			peer.Errorf("first query = %q, %v, want SHOW transaction_read_only", sql, err)
			return
		}
		column := wire.ColumnDescription{Name: "transaction_read_only", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0}
		if err := peer.SendResult([]wire.ColumnDescription{column},
			[][][]byte{{[]byte(readOnlyValue)}}, "SHOW", wire.StatusIdle); err != nil {
			peer.Errorf("SendResult: %v", err)
			return
		}
		waitTerminate(peer)
	})
}
