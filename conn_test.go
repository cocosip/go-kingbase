package gokb

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func peerConfig(t *testing.T, srv *testpeer.Server, extra string) *Config {
	t.Helper()
	host, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("split peer address: %v", err)
	}
	dsn := "host=" + host + " port=" + port + " user=alice dbname=tenant sslmode=disable " + extra
	cfg, err := ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig(%q): %v", dsn, err)
	}
	return cfg
}

func connectPeer(t *testing.T, srv *testpeer.Server, extra string) (*conn, error) {
	t.Helper()
	connector := &Connector{cfg: peerConfig(t, srv, extra)}
	physical, err := connector.Connect(context.Background())
	if err != nil {
		return nil, err
	}
	physicalConn, ok := physical.(*conn)
	if !ok {
		t.Fatalf("Connect returned %T, want *conn", physical)
	}
	return physicalConn, nil
}

func startupParam(params [][2]string, name string) string {
	for _, param := range params {
		if param[0] == name {
			return param[1]
		}
	}
	return ""
}

func TestConnectCleartextAuth(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		params, err := peer.ReadStartup()
		if err != nil {
			peer.Errorf("ReadStartup: %v", err)
			return
		}
		if user := startupParam(params, "user"); user != testUser {
			peer.Errorf("startup user = %q, want alice", user)
		}
		if database := startupParam(params, "database"); database != testDatabase {
			peer.Errorf("startup database = %q, want tenant", database)
		}
		if encoding := startupParam(params, "client_encoding"); encoding != encodingUTF8 {
			peer.Errorf("startup client_encoding = %q, want UTF8", encoding)
		}
		if err := peer.AuthenticateCleartext("secret"); err != nil {
			peer.Errorf("AuthenticateCleartext: %v", err)
			return
		}
		if err := peer.StartupOK([][2]string{
			{"client_encoding", "UTF8"},
			{"server_version", "12.1"},
			{"database_mode", "oracle"},
		}); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		typ, _, err := peer.ReadMessage()
		if err != nil {
			peer.Errorf("expected Terminate, got error: %v", err)
			return
		}
		if typ != wire.MsgTerminate {
			peer.Errorf("message after startup = %q, want Terminate", typ)
		}
	})

	physical, err := connectPeer(t, srv, "password=secret")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if physical.cfg.User != testUser || physical.cfg.Database != testDatabase {
		t.Errorf("connected as %s/%s", physical.cfg.User, physical.cfg.Database)
	}
	if physical.params["server_version"] != "12.1" || physical.params["database_mode"] != "oracle" {
		t.Errorf("startup parameters = %v, want server_version and database_mode", physical.params)
	}
	if physical.backendPID != 4242 || physical.backendSecret != 9876 {
		t.Errorf("backend key data = %d/%d, want 4242/9876", physical.backendPID, physical.backendSecret)
	}
	if physical.txStatus != wire.StatusIdle {
		t.Errorf("txStatus = %q, want idle", physical.txStatus)
	}
	if err := physical.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestConnectMD5Auth(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			peer.Errorf("ReadStartup: %v", err)
			return
		}
		if err := peer.AuthenticateMD5("alice", "secret"); err != nil {
			peer.Errorf("AuthenticateMD5: %v", err)
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			peer.Errorf("StartupOK: %v", err)
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "password=secret")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_ = physical.Close()
}

func TestConnectStartupParameters(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		params, err := peer.ReadStartup()
		if err != nil {
			peer.Errorf("ReadStartup: %v", err)
			return
		}
		if name := startupParam(params, "application_name"); name != "svc" {
			peer.Errorf("application_name = %q, want svc", name)
		}
		if options := startupParam(params, "options"); options != "-c search_path=x" {
			peer.Errorf("options = %q, want -c search_path=x", options)
		}
		if searchPath := startupParam(params, "search_path"); searchPath != testSearchPath {
			peer.Errorf("search_path = %q, want public", searchPath)
		}
		if err := peer.StartupOK(nil); err != nil {
			peer.Errorf("StartupOK: %v", err)
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "application_name=svc options='-c search_path=x' search_path=public")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_ = physical.Close()
}

func TestConnectErrorResponse(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverityFatal},
			wire.ErrorField{Code: wire.ErrorCode, Value: testSQLStateAuth},
			wire.ErrorField{Code: wire.ErrorMessage, Value: testAuthFailed},
		)
	})

	_, err := connectPeer(t, srv, "password=wrong")
	if err == nil {
		t.Fatal("Connect succeeded, want authentication error")
	}
	var serverErr *Error
	if !errors.As(err, &serverErr) {
		t.Fatalf("error %v is not a *gokb.Error", err)
	}
	if serverErr.Code != testSQLStateAuth || serverErr.Severity != testSeverityFatal {
		t.Errorf("server error = %+v, want SQLSTATE 28P01 severity FATAL", serverErr)
	}
	if !strings.Contains(err.Error(), testAuthFailed) {
		t.Errorf("error = %v, want server message", err)
	}
}

func TestConnectUnsupportedAuth(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_ = peer.WriteMessage(wire.MsgAuthentication, wire.AppendInt32(nil, 99))
	})

	_, err := connectPeer(t, srv, "password=secret")
	if err == nil || !strings.Contains(err.Error(), "unsupported authentication request") {
		t.Fatalf("error = %v, want unsupported authentication error", err)
	}
}

func TestConnectTLSNotSupported(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		code, _, err := peer.ReadStartupPacket()
		if err != nil {
			peer.Errorf("ReadStartupPacket: %v", err)
			return
		}
		if code != wire.SSLRequestCode {
			peer.Errorf("startup code = %d, want SSLRequest", code)
			return
		}
		_ = peer.WriteRaw([]byte{'N'})
	})

	connector := &Connector{cfg: peerConfig(t, srv, "sslmode=require")}
	_, err := connector.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not support TLS") {
		t.Fatalf("error = %v, want TLS unsupported error", err)
	}
}

func TestConnectRejectsNonUTF8Encoding(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_ = peer.StartupOK([][2]string{{"client_encoding", "LATIN1"}})
	})

	_, err := connectPeer(t, srv, "")
	if err == nil || !strings.Contains(err.Error(), "client_encoding") {
		t.Fatalf("error = %v, want unsupported client_encoding error", err)
	}
}

func TestConnectContextDeadline(t *testing.T) {
	// Auth succeeds, then initialization stalls: the caller deadline must
	// bound the whole connect and close the partial connection.
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_ = peer.WriteMessage(wire.MsgAuthentication, wire.EncodeAuthenticationOk())
		_, _, _ = peer.ReadMessage() // block until the client gives up
	})

	connector := &Connector{cfg: peerConfig(t, srv, "password=secret")}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := connector.Connect(ctx)
	if err == nil {
		t.Fatal("Connect succeeded, want deadline error")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("Connect took %v, want bounded by the context deadline", elapsed)
	}
}

func TestConnectRequiresUser(t *testing.T) {
	connector := &Connector{cfg: &Config{}}
	if _, err := connector.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "user is required") {
		t.Fatalf("error = %v, want user required", err)
	}
}

func TestConnectCancelInterruptsHandshake(t *testing.T) {
	reading := make(chan struct{})
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		close(reading)
		// Block without ever answering the startup exchange.
		_, _, _ = peer.ReadMessage()
	})

	connector := &Connector{cfg: peerConfig(t, srv, "")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-reading
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	_, err := connector.Connect(ctx)
	if err == nil {
		t.Fatal("Connect succeeded, want cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("Connect took %v, want cancellation to interrupt the handshake", elapsed)
	}
}

func TestConnectTimeoutBudget(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_, _, _ = peer.ReadMessage()
	})

	connector := &Connector{cfg: peerConfig(t, srv, "connect_timeout=1")}
	started := time.Now()
	_, err := connector.Connect(context.Background())
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Connect succeeded, want budget error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed < 800*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("Connect took %v, want close to the 1s budget", elapsed)
	}
}

func TestConnectEarlierContextDeadlineWins(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_, _, _ = peer.ReadMessage()
	})

	connector := &Connector{cfg: peerConfig(t, srv, "connect_timeout=30")}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := connector.Connect(ctx)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("Connect took %v, want the context deadline to win", elapsed)
	}
}

func TestConnectClearsHandshakeDeadline(t *testing.T) {
	const responseDelay = 1200 * time.Millisecond
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		time.Sleep(responseDelay)
		if err := peer.CommandComplete("SELECT 0"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "connect_timeout=1")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// The one-second handshake budget must not leak into command deadlines.
	if _, err := physical.ExecContext(context.Background(), "SELECT 1", nil); err != nil {
		t.Fatalf("Exec after connect: %v", err)
	}
	_ = physical.Close()
}

func TestConnectRejectsNonIdleStartup(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_ = peer.WriteMessage(wire.MsgAuthentication, wire.EncodeAuthenticationOk())
		_ = peer.ReadyForQuery(wire.StatusInTransaction)
	})

	_, err := connectPeer(t, srv, "")
	if err == nil || !strings.Contains(err.Error(), "transaction state") {
		t.Fatalf("error = %v, want non-idle transaction state error", err)
	}
}

func TestConnectClosesSocketOnFailedHandshake(t *testing.T) {
	closed := make(chan struct{})
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_ = peer.ErrorResponse(wire.ErrorField{Code: wire.ErrorCode, Value: "28000"})
		// The client must not leave the failed physical connection open.
		_, _, _ = peer.ReadMessage()
		close(closed)
	})

	if _, err := connectPeer(t, srv, ""); err == nil {
		t.Fatal("Connect succeeded, want startup error")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not close the failed connection")
	}
}

func TestServerErrorFullFieldMapping(t *testing.T) {
	fields := []wire.ErrorField{
		{Code: wire.ErrorSeverity, Value: testSeverity},
		{Code: wire.ErrorSeverityNonLocal, Value: testSeverity},
		{Code: wire.ErrorCode, Value: "23505"},
		{Code: wire.ErrorMessage, Value: testDuplicateKey},
		{Code: wire.ErrorDetail, Value: "Key (id)=(1) already exists."},
		{Code: wire.ErrorHint, Value: "choose another key"},
		{Code: wire.ErrorPosition, Value: "42"},
		{Code: wire.ErrorInternalPosition, Value: "7"},
		{Code: wire.ErrorInternalQuery, Value: "INSERT INTO t"},
		{Code: wire.ErrorWhere, Value: "PL/pgSQL function f"},
		{Code: wire.ErrorSchemaName, Value: testSearchPath},
		{Code: wire.ErrorTableName, Value: "t"},
		{Code: wire.ErrorColumnName, Value: "id"},
		{Code: wire.ErrorDataTypeName, Value: "integer"},
		{Code: wire.ErrorConstraintName, Value: "t_pkey"},
		{Code: wire.ErrorFile, Value: "nbtinsert.c"},
		{Code: wire.ErrorLine, Value: "563"},
		{Code: wire.ErrorRoutine, Value: "_bt_check_unique"},
		{Code: 'Z', Value: "extension-value"},
	}
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_ = peer.ErrorResponse(fields...)
	})

	_, err := connectPeer(t, srv, "")
	var serverErr *Error
	if !errors.As(err, &serverErr) {
		t.Fatalf("error %v is not a *gokb.Error", err)
	}
	if serverErr.Severity != testSeverity || serverErr.SeverityNonLocal != testSeverity {
		t.Errorf("severity = %q/%q", serverErr.Severity, serverErr.SeverityNonLocal)
	}
	if serverErr.Code != "23505" || serverErr.Detail == "" || serverErr.Hint == "" {
		t.Errorf("code/detail/hint = %q/%q/%q", serverErr.Code, serverErr.Detail, serverErr.Hint)
	}
	if serverErr.Position != 42 || serverErr.InternalPosition != 7 {
		t.Errorf("positions = %d/%d, want 42/7", serverErr.Position, serverErr.InternalPosition)
	}
	if serverErr.SchemaName != testSearchPath || serverErr.TableName != "t" || serverErr.ColumnName != "id" ||
		serverErr.DataTypeName != "integer" || serverErr.ConstraintName != "t_pkey" {
		t.Errorf("object fields = %+v", serverErr)
	}
	if serverErr.File != "nbtinsert.c" || serverErr.Line != "563" || serverErr.Routine != "_bt_check_unique" {
		t.Errorf("source fields = %q/%q/%q", serverErr.File, serverErr.Line, serverErr.Routine)
	}
	if serverErr.Extra['Z'] != "extension-value" {
		t.Errorf("extension field = %q, want extension-value", serverErr.Extra['Z'])
	}
	if !strings.Contains(err.Error(), "23505") {
		t.Errorf("error text = %v, want SQLSTATE", err)
	}
}

func TestConnectUnixSocket(t *testing.T) {
	if runtime.GOOS == testWindows {
		t.Skip("unix sockets are rejected on windows")
	}
	dir := t.TempDir()
	socketPath := filepath.Join(dir, ".s.PGSQL.6543")
	testpeer.StartAt(t, "unix", socketPath, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	dsn := "host=" + dsnValue(dir) + " port=6543 user=" + testUser + " dbname=" + testDatabase + " sslmode=disable"
	cfg, err := ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Network != networkUnix {
		t.Fatalf("network = %q, want unix", cfg.Network)
	}
	connector := &Connector{cfg: cfg}
	physical, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect over unix socket: %v", err)
	}
	_ = physical.Close()
}

func TestConnectCancelledContext(t *testing.T) {
	connector := &Connector{cfg: &Config{User: "u", Host: "127.0.0.1", Port: 1, Network: networkTCP}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := connector.Connect(ctx)
	if err == nil {
		t.Fatal("Connect succeeded with a cancelled context")
	}
}

const testPassword = "s3cret"

func TestConnectSCRAM(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if _, err := peer.AuthenticateSCRAM(testPassword, testpeer.SCRAMOptions{}); err != nil {
			peer.Errorf("AuthenticateSCRAM: %v", err)
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "password="+testPassword)
	if err != nil {
		t.Fatalf("Connect with SCRAM: %v", err)
	}
	_ = physical.Close()
}

func TestConnectSCRAMWrongPassword(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if _, err := peer.AuthenticateSCRAM(testPassword, testpeer.SCRAMOptions{}); err != nil {
			if testpeer.IsInvalidProof(err) {
				_ = peer.ErrorResponse(
					wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverityFatal},
					wire.ErrorField{Code: wire.ErrorCode, Value: testSQLStateAuth},
					wire.ErrorField{Code: wire.ErrorMessage, Value: testAuthFailed},
				)
				return
			}
			peer.Errorf("AuthenticateSCRAM: %v", err)
		}
	})

	_, err := connectPeer(t, srv, "password=wrong")
	if err == nil || !strings.Contains(err.Error(), testSQLStateAuth) {
		t.Fatalf("error = %v, want SQLSTATE 28P01", err)
	}
	var serverErr *Error
	if !errors.As(err, &serverErr) || serverErr.Code != testSQLStateAuth {
		t.Fatalf("error %v is not a 28P01 *gokb.Error", err)
	}
}

func TestConnectSCRAMRejectsBadServerSignature(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if _, err := peer.AuthenticateSCRAM(testPassword, testpeer.SCRAMOptions{CorruptServerSignature: true}); err != nil {
			peer.Errorf("AuthenticateSCRAM: %v", err)
			return
		}
		// The client must abort; ignore the write failure.
		_ = peer.StartupOK(nil)
	})

	_, err := connectPeer(t, srv, "password="+testPassword)
	if err == nil || !strings.Contains(err.Error(), "server signature mismatch") {
		t.Fatalf("error = %v, want SCRAM server signature mismatch", err)
	}
}

func TestConnectSCRAMRejectsUnsupportedMechanism(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		_ = peer.WriteMessage(wire.MsgAuthentication, wire.EncodeAuthenticationSASL("SCRAM-SHA-256-PLUS"))
	})

	_, err := connectPeer(t, srv, "password="+testPassword)
	if err == nil || !strings.Contains(err.Error(), "SCRAM-SHA-256") {
		t.Fatalf("error = %v, want unsupported mechanism error", err)
	}
}

func TestConnectSM3(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.AuthenticateSM3(testUser, testPassword); err != nil {
			peer.Errorf("AuthenticateSM3: %v", err)
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "password="+testPassword)
	if err != nil {
		t.Fatalf("Connect with SM3: %v", err)
	}
	_ = physical.Close()
}

func TestConnectSM3WrongPassword(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.AuthenticateSM3(testUser, testPassword); err != nil {
			_ = peer.ErrorResponse(
				wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverityFatal},
				wire.ErrorField{Code: wire.ErrorCode, Value: testSQLStateAuth},
				wire.ErrorField{Code: wire.ErrorMessage, Value: testAuthFailed},
			)
			return
		}
	})

	_, err := connectPeer(t, srv, "password=wrong")
	if err == nil || !strings.Contains(err.Error(), testSQLStateAuth) {
		t.Fatalf("error = %v, want SQLSTATE 28P01", err)
	}
}

func TestConnectRequiresConfiguredPassword(t *testing.T) {
	challenges := []struct {
		name    string
		payload []byte
	}{
		{name: "cleartext", payload: wire.EncodeAuthenticationCleartext()},
		{name: "md5", payload: wire.EncodeAuthenticationMD5([4]byte{1, 2, 3, 4})},
		{name: "sasl", payload: wire.EncodeAuthenticationSASL("SCRAM-SHA-256")},
		{name: "sm3", payload: wire.EncodeAuthenticationSM3([4]byte{1, 2, 3, 4})},
	}
	for _, challenge := range challenges {
		t.Run(challenge.name, func(t *testing.T) {
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				if _, err := peer.ReadStartup(); err != nil {
					return
				}
				_ = peer.WriteMessage(wire.MsgAuthentication, challenge.payload)
			})
			_, err := connectPeer(t, srv, "")
			if err == nil || !strings.Contains(err.Error(), "password") {
				t.Fatalf("error = %v, want missing-password error", err)
			}
		})
	}
}

func TestConnectWithKeepAlivesDisabled(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "keepalives=0 keepalives_idle=5")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_ = physical.Close()
}
