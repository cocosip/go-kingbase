package kingbase

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestModeDiscoveryFromStartup(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			peer.Errorf("ReadStartup: %v", err)
			return
		}
		if err := peer.StartupOK([][2]string{{testDatabaseMode, "mysql"}}); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		payload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		// The discovered mysql mode has no verified profile, so the statement and
		// both arguments reach the server untouched.
		_, sql, _, err := wire.DecodeParse(payload)
		if err != nil || sql != "SELECT ?, ?" {
			peer.Errorf("parse sql = %q err %v, want the untouched statement", sql, err)
			return
		}
		payload, ok = expectMessage(peer, wire.MsgBind)
		if !ok {
			return
		}
		if _, _, _, params, _, err := wire.DecodeBind(payload); err != nil || len(params) != 2 {
			peer.Errorf("bind params = %q err %v", params, err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgDescribe); !ok {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgExecute); !ok {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgParseComplete, nil)
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		_ = peer.SendResult([]wire.ColumnDescription{
			{Name: "n", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 1},
		}, [][][]byte{{[]byte("1")}}, "SELECT 1", wire.StatusIdle)
		waitTerminate(peer)
	})
	connection, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if connection.mode != ModeMySQL || connection.modeSource != ModeSourceStartup || !connection.modeVerified {
		t.Fatalf("mode = %s source %s verified %v, want startup mysql",
			connection.mode, connection.modeSource, connection.modeVerified)
	}
	rows, err := connection.QueryContext(context.Background(), "SELECT ?, ?", []driver.NamedValue{
		{Ordinal: 1, Value: int64(7)},
		{Ordinal: 2, Value: int64(8)},
	})
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	_ = rows.Close()
}

func TestModeDiscoveryStartupConflictsWithExplicitDeclaration(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_ = peer.StartupOK([][2]string{{testDatabaseMode, "mysql"}})
		_, _, _ = peer.ReadMessage()
	})
	cfg := peerConfig(t, srv, "")
	cfg.CompatibilityMode = ModePG
	_, err := (&Connector{cfg: cfg}).Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "conflicts") ||
		!strings.Contains(err.Error(), "mysql") || !strings.Contains(err.Error(), "pg") {
		t.Fatalf("Connect = %v, want an expected/actual mode conflict", err)
	}
}

func TestModeDiscoveryFromQueryChain(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetModeReport("")
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		sql, err := wire.DecodeQuery(payload)
		if err != nil || !strings.Contains(sql, "pg_settings") {
			peer.Errorf("discovery sql = %q err %v, want the pg_settings candidate", sql, err)
			return
		}
		// A missing relation is an unsupported candidate, not an absence proof.
		if err := peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverityFatal},
			wire.ErrorField{Code: wire.ErrorCode, Value: "42P01"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "relation \"pg_settings\" does not exist"},
		); err != nil {
			peer.Errorf("ErrorResponse: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			return
		}
		payload, ok = expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		sql, err = wire.DecodeQuery(payload)
		if err != nil || !strings.EqualFold(strings.TrimSpace(sql), "show database_mode") {
			peer.Errorf("fallback sql = %q err %v, want SHOW database_mode", sql, err)
			return
		}
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: testDatabaseMode, TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		}); err != nil {
			return
		}
		_ = peer.DataRow([][]byte{[]byte("oracle")})
		_ = peer.CommandComplete("SHOW")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	connection, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if connection.mode != ModeOracle || connection.modeSource != ModeSourceQuery || !connection.modeVerified {
		t.Fatalf("mode = %s source %s verified %v, want query oracle",
			connection.mode, connection.modeSource, connection.modeVerified)
	}
}

func TestModeDiscoveryUnavailableAutoFails(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetModeReport("")
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: testColumnName, TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
			{Name: "setting", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		}); err != nil {
			return
		}
		_ = peer.CommandComplete("SELECT 0")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverityFatal},
			wire.ErrorField{Code: wire.ErrorCode, Value: "42704"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "unrecognized configuration parameter \"database_mode\""},
		)
		_ = peer.ReadyForQuery(wire.StatusIdle)
		if _, _, err := peer.ReadMessage(); err == nil {
			peer.Errorf("connection stayed open after a failed mode probe")
		}
	})
	_, err := connectPeer(t, srv, "")
	if err == nil || !strings.Contains(err.Error(), "compatibilityMode") {
		t.Fatalf("Connect = %v, want a request for an explicit mode", err)
	}
	if !strings.Contains(err.Error(), "SQLSTATE 42704") || !strings.Contains(err.Error(), "pg_settings") {
		t.Fatalf("Connect = %v, want per-candidate probe evidence", err)
	}
}

func TestModeDiscoveryExplicitModeIsUnverifiedFallback(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetModeReport("")
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			return
		}
		waitTerminate(peer)
	})
	cfg := peerConfig(t, srv, "")
	cfg.CompatibilityMode = ModeMySQL
	physical, err := (&Connector{cfg: cfg}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	connection := physical.(*conn)
	defer func() { _ = connection.Close() }()
	if connection.mode != ModeMySQL || connection.modeSource != ModeSourceExplicit || connection.modeVerified {
		t.Fatalf("mode = %s source %s verified %v, want an unverified explicit mysql",
			connection.mode, connection.modeSource, connection.modeVerified)
	}
	meta := connection.serverMetadata()
	if meta.ConfiguredMode != ModeMySQL || meta.ModeSource != ModeSourceExplicit || meta.ModeVerified ||
		meta.IdentityStatus != IdentityCaptured || meta.Profile != "" {
		t.Fatalf("metadata = %+v, want an unverified explicit mysql with a captured identity", meta)
	}
}

func TestModeDiscoveryRetainsPermissionFailure(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetModeReport("")
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverityFatal},
			wire.ErrorField{Code: wire.ErrorCode, Value: "42501"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "permission denied for table pg_settings"},
		)
		_ = peer.ReadyForQuery(wire.StatusIdle)
	})
	_, err := connectPeer(t, srv, "")
	if err == nil || !strings.Contains(err.Error(), "mode discovery") ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Connect = %v, want the retained permission cause", err)
	}
}

func TestModeDiscoveryMalformedStartupReportFails(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_ = peer.StartupOK([][2]string{{testDatabaseMode, "weird"}})
		// The identity query still runs before mode resolution; the auto
		// responder answers it while this script waits for the close.
		waitTerminate(peer)
	})
	_, err := connectPeer(t, srv, "")
	if err == nil || !strings.Contains(err.Error(), "unknown database_mode") {
		t.Fatalf("Connect = %v, want a malformed-report error", err)
	}
}

func TestModeDiscoveryVersionOnlyFallsThrough(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetModeReport("")
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: testColumnName, TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
			{Name: "setting", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		}); err != nil {
			return
		}
		_ = peer.DataRow([][]byte{[]byte("server_version"), []byte(testServerVersionText)})
		_ = peer.DataRow([][]byte{[]byte("server_version_num"), []byte("120100")})
		_ = peer.CommandComplete("SELECT 2")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.RowDescription([]wire.ColumnDescription{
			{Name: testDatabaseMode, TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		})
		_ = peer.DataRow([][]byte{[]byte("mysql")})
		_ = peer.CommandComplete("SHOW")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	connection, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if connection.mode != ModeMySQL || connection.modeSource != ModeSourceQuery {
		t.Fatalf("mode = %s source %s, want the SHOW fallback report", connection.mode, connection.modeSource)
	}
}

func TestModeDiscoveryQueryProvenancePassthrough(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetModeReport("")
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.RowDescription([]wire.ColumnDescription{
			{Name: testColumnName, TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
			{Name: "setting", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		})
		_ = peer.DataRow([][]byte{[]byte(testDatabaseMode), []byte("mysql")})
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		payload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		if _, sql, _, err := wire.DecodeParse(payload); err != nil || sql != "SELECT ?" {
			peer.Errorf("parse sql = %q err %v, want the untouched statement", sql, err)
			return
		}
		payload, ok = expectMessage(peer, wire.MsgBind)
		if !ok {
			return
		}
		// Two arguments for one placeholder: an unverified mode does not
		// enforce the client-side count.
		if _, _, _, params, _, err := wire.DecodeBind(payload); err != nil || len(params) != 2 {
			peer.Errorf("bind params = %q err %v", params, err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgDescribe); !ok {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgExecute); !ok {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgParseComplete, nil)
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		_ = peer.SendResult([]wire.ColumnDescription{
			{Name: "n", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 1},
		}, [][][]byte{{[]byte("1")}}, "SELECT 1", wire.StatusIdle)
		waitTerminate(peer)
	})
	connection, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if connection.mode != ModeMySQL || !connection.modeVerified {
		t.Fatalf("mode = %s verified %v, want a verified query discovery", connection.mode, connection.modeVerified)
	}
	rows, err := connection.QueryContext(context.Background(), "SELECT ?", []driver.NamedValue{
		{Ordinal: 1, Value: int64(7)},
		{Ordinal: 2, Value: int64(8)},
	})
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	_ = rows.Close()
}

// testDatabaseMode is the startup/discovery setting name shared by these tests.
const testDatabaseMode = "database_mode"
