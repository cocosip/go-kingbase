package kingbase

import (
	"context"
	"strings"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

const testStandardConformingStrings = "standard_conforming_strings"

// TestConnectRejectsLegacyStringLiterals keeps the unverified legacy quoting
// mode out of the session instead of risking a wrong rewrite or a stale lexical
// context.
func TestConnectRejectsLegacyStringLiterals(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			peer.Errorf("ReadStartup: %v", err)
			return
		}
		if err := peer.StartupOK([][2]string{{testStandardConformingStrings, "off"}}); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		waitTerminate(peer)
	})

	_, err := connectPeer(t, srv, "")
	if err == nil || !strings.Contains(err.Error(), testStandardConformingStrings) {
		t.Fatalf("Connect error = %v, want an unsupported %s diagnostic", err, testStandardConformingStrings)
	}
}

// TestConnectAcceptsStandardStringLiterals keeps the verified mode working.
func TestConnectAcceptsStandardStringLiterals(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if _, err := peer.ReadStartup(); err != nil {
			peer.Errorf("ReadStartup: %v", err)
			return
		}
		if err := peer.StartupOK([][2]string{{testStandardConformingStrings, "on"}}); err != nil {
			peer.Errorf("StartupOK: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_ = physical.Close()
}

// TestLexicalSettingChangeRetiresConnection covers a settings change reported
// between commands: the session context is no longer verifiable, so the
// connection is retired instead of reused.
func TestLexicalSettingChangeRetiresConnection(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.WriteMessage(wire.MsgParameterStatus,
			wire.EncodeParameterStatus(testStandardConformingStrings, "off")); err != nil {
			peer.Errorf("ParameterStatus: %v", err)
			return
		}
		_ = peer.CommandComplete("INSERT 0 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.ExecContext(context.Background(), "INSERT INTO t VALUES ('x')", nil); err == nil ||
		!strings.Contains(err.Error(), testStandardConformingStrings) {
		t.Fatalf("Exec error = %v, want an unsupported %s diagnostic", err, testStandardConformingStrings)
	}
	if physical.netConn != nil {
		t.Error("connection was not retired after a lexical setting change")
	}
}
