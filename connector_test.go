package kingbase

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestNewConnectorPublicPath(t *testing.T) {
	if _, err := NewConnector(nil); err == nil {
		t.Error("NewConnector(nil) succeeded, want an error")
	}
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		noticeScript(peer)
	})
	cfg := peerConfig(t, srv, "")
	var notices []Notice
	cfg.NoticeHandler = func(notice Notice) { notices = append(notices, notice) }
	connector, err := NewConnector(cfg)
	if err != nil {
		t.Fatalf("NewConnector: %v", err)
	}
	// The public path must be usable with sql.OpenDB, and the handler captured
	// before construction must survive the snapshot.
	cfg.NoticeHandler = nil
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), "SELECT 0"); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if len(notices) != 1 || notices[0].Message != testNoticeMessage || notices[0].Code != testNoticeCode {
		t.Fatalf("notices = %+v, want one %q notice", notices, testNoticeMessage)
	}
}

func TestConnectorPinsConfigurationSnapshot(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	cfg := peerConfig(t, srv, "compatibilityMode=pg")
	connector := newConnector(cfg)

	// Mutating the caller's configuration after construction must not switch
	// the pinned mode or transport.
	cfg.CompatibilityMode = ModeMySQL
	cfg.WireProtocol = WireMySQL
	physical, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	connection := physical.(*conn)
	if connection.mode != ModePG || connection.cfg.WireProtocol != WirePG {
		t.Errorf("connection mode/wire = %s/%s, want pg/pg", connection.mode, connection.cfg.WireProtocol)
	}
	_ = connection.Close()

	// The accessor is a copy, not a mutable window into the snapshot.
	snapshot := connector.Config()
	snapshot.CompatibilityMode = ModeMySQL
	snapshot.WireProtocol = WireMySQL
	if connector.Config().CompatibilityMode != ModePG || connector.Config().WireProtocol != WirePG {
		t.Fatalf("Config() mutation changed the connector snapshot")
	}
}

func TestConnectorPinsEndpointSlices(t *testing.T) {
	cfg := &Config{
		User:  "alice",
		Hosts: []string{"first"},
		Ports: []int{1111},
	}
	connector := newConnector(cfg)
	cfg.Hosts[0] = "changed"
	cfg.Ports[0] = 2222
	got := connector.Config()
	if got.Hosts[0] != "first" || got.Ports[0] != 1111 {
		t.Fatalf("caller slices were aliased: %v/%v", got.Hosts, got.Ports)
	}
	got.Hosts[0] = "clone"
	got.Ports[0] = 3333
	if connector.Config().Hosts[0] != "first" || connector.Config().Ports[0] != 1111 {
		t.Fatalf("Config() slices were aliased")
	}
}

func TestConnectorRejectsNonPGWireBeforeDial(t *testing.T) {
	for _, testCase := range []struct {
		protocol WireProtocol
		want     string
	}{
		{WireMySQL, modeMySQLName},
		{WireProtocol(2), "unknown(2)"},
	} {
		connector := newConnector(&Config{
			User:         "alice",
			Host:         "127.0.0.1",
			Port:         1,
			WireProtocol: testCase.protocol,
		})
		_, err := connector.Connect(context.Background())
		if err == nil || !strings.Contains(err.Error(), "wireProtocol") ||
			!strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("Connect(%s) error = %v, want an unsupported %s error", testCase.protocol, err, testCase.want)
		}
	}
}

func TestConnectorRejectsModeChangeOnLaterConnection(t *testing.T) {
	var connections atomic.Int32
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		mode := "pg"
		if connections.Add(1) > 1 {
			mode = "mysql"
		}
		if _, err := peer.ReadStartup(); err != nil {
			peer.Errorf("ReadStartup: %v", err)
			return
		}
		if err := peer.StartupOK([][2]string{{"database_mode", mode}}); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		waitTerminate(peer)
	})
	connector := newConnector(peerConfig(t, srv, "compatibilityMode=pg"))
	first, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	_ = first.Close()

	_, err = connector.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "conflicts") || !strings.Contains(err.Error(), "mysql") {
		t.Fatalf("second Connect error = %v, want an authoritative mode conflict", err)
	}
}

func TestConnectorPinsAutoResolvedMode(t *testing.T) {
	var connections atomic.Int32
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		mode := "pg"
		if connections.Add(1) > 1 {
			mode = "mysql"
		}
		if _, err := peer.ReadStartup(); err != nil {
			peer.Errorf("ReadStartup: %v", err)
			return
		}
		if err := peer.StartupOK([][2]string{{"database_mode", mode}}); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		waitTerminate(peer)
	})
	connector := newConnector(peerConfig(t, srv, ""))
	first, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	if mode := first.(*conn).mode; mode != ModePG {
		t.Fatalf("first mode = %s, want pg", mode)
	}
	_ = first.Close()

	_, err = connector.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "connector mode") ||
		!strings.Contains(err.Error(), "mysql") {
		t.Fatalf("second Connect error = %v, want a connector resolved-mode conflict", err)
	}
}

func TestConcurrentPGAndMySQLQueries(t *testing.T) {
	serve := func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		for {
			typ, payload, err := peer.ReadMessage()
			if err != nil {
				return
			}
			if typ == wire.MsgTerminate {
				return
			}
			if typ != wire.MsgQuery {
				peer.Errorf("unexpected message %q", typ)
				return
			}
			query, err := wire.DecodeQuery(payload)
			if err != nil || !strings.Contains(query, "SELECT 42") {
				peer.Errorf("unexpected query %q: %v", query, err)
				return
			}
			_ = peer.RowDescription([]wire.ColumnDescription{
				{Name: "n", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
			})
			_ = peer.DataRow([][]byte{[]byte("42")})
			_ = peer.CommandComplete("SELECT 1")
			if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
				return
			}
		}
	}
	pgDB := sql.OpenDB(newConnector(peerConfig(t, testpeer.Start(t, serve), "compatibilityMode=pg")))
	mysqlDB := sql.OpenDB(newConnector(peerConfig(t, testpeer.Start(t, serve), "compatibilityMode=mysql")))
	defer func() { _ = pgDB.Close() }()
	defer func() { _ = mysqlDB.Close() }()
	for _, db := range []*sql.DB{pgDB, mysqlDB} {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(4)
	}

	var wait sync.WaitGroup
	errors := make(chan error, 16)
	run := func(db *sql.DB) {
		defer wait.Done()
		for i := 0; i < 10; i++ {
			var got int64
			if err := db.QueryRowContext(context.Background(), "SELECT 42").Scan(&got); err != nil {
				errors <- err
				return
			}
			if got != 42 {
				errors <- fmt.Errorf("query result = %d, want 42", got)
				return
			}
		}
	}
	for i := 0; i < 8; i++ {
		wait.Add(2)
		go run(pgDB)
		go run(mysqlDB)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestConcurrentConnectorModeIsolation(t *testing.T) {
	pgServer := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	mysqlServer := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	pgConnector := newConnector(peerConfig(t, pgServer, "compatibilityMode=pg"))
	mysqlConnector := newConnector(peerConfig(t, mysqlServer, "compatibilityMode=mysql"))

	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(2)
		go func() {
			defer wait.Done()
			physical, err := pgConnector.Connect(context.Background())
			if err != nil {
				t.Errorf("pg Connect: %v", err)
				return
			}
			connection := physical.(*conn)
			if connection.mode != ModePG {
				t.Errorf("pg connection mode = %s, want pg", connection.mode)
			}
			_ = connection.Close()
		}()
		go func() {
			defer wait.Done()
			physical, err := mysqlConnector.Connect(context.Background())
			if err != nil {
				t.Errorf("mysql Connect: %v", err)
				return
			}
			connection := physical.(*conn)
			if connection.mode != ModeMySQL {
				t.Errorf("mysql connection mode = %s, want mysql", connection.mode)
			}
			_ = connection.Close()
		}()
	}
	wait.Wait()
}
