package gokb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestIdentityMetadata(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK([][2]string{
			{"server_version", testServerVersionText},
			{"server_version_num", "120100"},
		}); err != nil {
			return
		}
		waitTerminate(peer)
	})
	connection, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = connection.Close() }()
	meta := connection.serverMetadata()
	if meta.ConfiguredMode != ModeAuto || meta.Mode != ModePG || meta.ModeSource != ModeSourceQuery || !meta.ModeVerified {
		t.Errorf("mode metadata = %+v, want auto -> verified query pg", meta)
	}
	if meta.WireProtocol != WirePG || meta.Profile != "pg" {
		t.Errorf("protocol/profile = %v/%q", meta.WireProtocol, meta.Profile)
	}
	if meta.IdentityStatus != IdentityCaptured || meta.IdentityCause != "" || meta.ReleaseTruncated {
		t.Errorf("identity metadata = %+v, want a complete captured report", meta)
	}
	if meta.ServerVersion != testServerVersionText || meta.ServerVersionNum != "120100" {
		t.Errorf("startup versions = %q/%q", meta.ServerVersion, meta.ServerVersionNum)
	}
	if !strings.HasPrefix(meta.Release, "KingbaseES ") {
		t.Errorf("Release = %q, want the raw product report", meta.Release)
	}
}

func TestConnectionMetadataAccessor(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK([][2]string{{"server_version", testServerVersionText}}); err != nil {
			return
		}
		waitTerminate(peer)
	})
	host, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("split address: %v", err)
	}
	dsn := fmt.Sprintf("host=%s port=%s user=alice dbname=tenant sslmode=disable", host, port)
	db, err := sql.Open("kingbase", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	meta, err := ConnectionMetadata(sc)
	if err != nil {
		t.Fatalf("ConnectionMetadata: %v", err)
	}
	if meta.Mode != ModePG || meta.Release == "" || meta.ServerVersion != testServerVersionText {
		t.Errorf("metadata = %+v, want the connected pg physical diagnostics", meta)
	}
	if err := sc.Close(); err != nil {
		t.Errorf("conn.Close: %v", err)
	}
	if _, err := ConnectionMetadata(nil); err == nil {
		t.Error("ConnectionMetadata(nil) succeeded")
	}
}

func TestConnectionMetadataRejectsForeignConnection(t *testing.T) {
	connector := &foreignConnector{}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	defer func() { _ = sc.Close() }()
	if _, err := ConnectionMetadata(sc); err == nil || !strings.Contains(err.Error(), "not a kingbase") {
		t.Fatalf("ConnectionMetadata = %v, want a foreign-connection error", err)
	}
}

// foreignConnector provides a driver connection that is not a kingbase one, so
// the accessor's rejection path is exercised.
type foreignConnector struct{}

func (*foreignConnector) Connect(context.Context) (driver.Conn, error) { return &foreignConn{}, nil }
func (*foreignConnector) Driver() driver.Driver                        { return foreignDriver{} }

type foreignDriver struct{}

func (foreignDriver) Open(string) (driver.Conn, error) { return &foreignConn{}, nil }

type foreignConn struct{}

func (*foreignConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*foreignConn) Close() error                        { return nil }
func (*foreignConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func TestIdentityBoundedRelease(t *testing.T) {
	long := strings.Repeat("€", 2000) // 6000 bytes; the bound may split only between runes
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetVersionReport(long)
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			return
		}
		waitTerminate(peer)
	})
	connection, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = connection.Close() }()
	meta := connection.serverMetadata()
	if !meta.ReleaseTruncated {
		t.Error("ReleaseTruncated = false, want a truncated report")
	}
	if len(meta.Release) > maxReleaseBytes || len(meta.Release) <= maxReleaseBytes-3 {
		t.Errorf("Release length = %d, want a rune-boundary cut near %d", len(meta.Release), maxReleaseBytes)
	}
	if !strings.HasPrefix(long, meta.Release) {
		t.Error("Release is not a prefix of the raw report")
	}
}

func TestIdentityUnavailableContinues(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetVersionReport("")
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
		if sql, err := wire.DecodeQuery(payload); err != nil || sql != identityQuery {
			peer.Errorf("identity sql = %q err %v, want %q", sql, err, identityQuery)
			return
		}
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "42501"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "permission denied for function version"},
		)
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	connection, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v, want a usable session with unavailable identity", err)
	}
	defer func() { _ = connection.Close() }()
	meta := connection.serverMetadata()
	if meta.IdentityStatus != IdentityUnavailable || !strings.Contains(meta.IdentityCause, "permission denied") {
		t.Fatalf("identity = %d cause %q, want a retained permission cause", meta.IdentityStatus, meta.IdentityCause)
	}
	if meta.Release != "" || meta.ReleaseTruncated {
		t.Errorf("Release = %q truncated %v, want no report", meta.Release, meta.ReleaseTruncated)
	}
}

func TestIdentityEmptyReportUnavailable(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetVersionReport("")
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
			{Name: "version", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		})
		_ = peer.DataRow([][]byte{{}})
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	connection, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if meta := connection.serverMetadata(); meta.IdentityStatus != IdentityUnavailable ||
		!strings.Contains(meta.IdentityCause, "empty") {
		t.Fatalf("identity = %d cause %q, want an empty-report cause", meta.IdentityStatus, meta.IdentityCause)
	}
}

func TestIdentityNoRowUnavailable(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetVersionReport("")
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
			{Name: "version", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		})
		_ = peer.CommandComplete("SELECT 0")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	connection, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if meta := connection.serverMetadata(); meta.IdentityStatus != IdentityUnavailable {
		t.Fatalf("identity = %d, want unavailable for a zero-row report", meta.IdentityStatus)
	}
}

func TestIdentityNonStringUnavailable(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetVersionReport("")
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
			{Name: "version", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 1},
		})
		_ = peer.DataRow([][]byte{{0, 0, 0, 1}})
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	connection, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if meta := connection.serverMetadata(); meta.IdentityStatus != IdentityUnavailable {
		t.Fatalf("identity = %d, want unavailable for a non-string report", meta.IdentityStatus)
	}
}

func TestIdentityTransportFailureFails(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.SetVersionReport("")
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		// Closing the socket mid-probe is a transport failure, not absence.
		_ = peer.Close()
	})
	_, err := connectPeer(t, srv, "")
	if err == nil || !strings.Contains(err.Error(), "capture server identity") {
		t.Fatalf("Connect = %v, want an identity transport failure", err)
	}
}

// testServerVersionText is the startup version string shared by these tests.
const testServerVersionText = "12.1"
