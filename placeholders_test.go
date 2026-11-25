package kingbase

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestPlanPlaceholders(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
		args  int
	}{
		{"question marks", "SELECT ?, ? FROM t WHERE a = ?", "SELECT $1, $2 FROM t WHERE a = $3", 3},
		{"explicit", "SELECT $2, $1", "SELECT $2, $1", 2},
		{"none", "SELECT 1", "SELECT 1", 0},
		{"cast", "SELECT a::text FROM t", "SELECT a::text FROM t", 0},
		{"operators", "SELECT data ?| array['a'], data ?& array['b'], data::jsonb @> '{}'", "SELECT data ?| array['a'], data ?& array['b'], data::jsonb @> '{}'", 0},
		{"jsonpath operator", "SELECT data @? '$.a' FROM t", "SELECT data @? '$.a' FROM t", 0},
		{"array slices", "SELECT arr[1:n], payload[:length(payload)] FROM t", "SELECT arr[1:n], payload[:length(payload)] FROM t", 0},
		{"dollar in identifier", "SELECT foo$1, bar FROM t", "SELECT foo$1, bar FROM t", 0},
		{"dollar in identifier with placeholder", "SELECT foo$1, ? FROM t", "SELECT foo$1, $1 FROM t", 1},
		{"unterminated quote", "SELECT 'a?b", "SELECT 'a?b", 0},
		{"doubled quotes", "SELECT '', '' , ?", "SELECT '', '' , $1", 1},
		{
			"literals and comments",
			"SELECT 'a?b', \"c?\", E'e\\'?', $$?$$, $tag$?$tag$ -- ?\n/* ? /* ? */ */, ?",
			"SELECT 'a?b', \"c?\", E'e\\'?', $$?$$, $tag$?$tag$ -- ?\n/* ? /* ? */ */, $1",
			1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planPlaceholders(pgModeProfile, tc.query)
			if err != nil {
				t.Fatalf("planPlaceholders: %v", err)
			}
			if plan.query != tc.want {
				t.Errorf("query = %q, want %q", plan.query, tc.want)
			}
			if plan.args != tc.args {
				t.Errorf("args = %d, want %d", plan.args, tc.args)
			}
		})
	}
}

func TestPlanPlaceholdersRejects(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"mixed", "SELECT ? WHERE a = $1", "mixed"},
		{"mixed reverse", "SELECT $1 WHERE a = ?", "mixed"},
		{"named colon", "SELECT * FROM t WHERE id = :id", "named placeholder"},
		{"named at", "SELECT * FROM t WHERE id = @id", "named placeholder"},
		{"zero index", "SELECT $0", "invalid placeholder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := planPlaceholders(pgModeProfile, tc.query); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("planPlaceholders(%q) = %v, want %q", tc.query, err, tc.want)
			}
		})
	}
}

func TestQueryRewritesQuestionPlaceholders(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		_, sql, _, err := wire.DecodeParse(payload)
		if err != nil || sql != "SELECT $1, $2" {
			peer.Errorf("parse sql = %q err %v", sql, err)
			return
		}
		payload, ok = expectMessage(peer, wire.MsgBind)
		if !ok {
			return
		}
		_, _, _, params, _, err := wire.DecodeBind(payload)
		if err != nil || len(params) != 2 || string(params[0]) != "42" || string(params[1]) != testHello {
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
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	rows, err := physical.QueryContext(context.Background(), "SELECT ?, ?", []driver.NamedValue{
		{Ordinal: 1, Value: int64(42)},
		{Ordinal: 2, Value: testHello},
	})
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	_ = rows.Close()
}

func TestQueryArgumentCountMismatchFailsBeforeSend(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	_, err = physical.QueryContext(context.Background(), "SELECT ?", []driver.NamedValue{
		{Ordinal: 1, Value: int64(1)},
		{Ordinal: 2, Value: int64(2)},
	})
	if err == nil || !strings.Contains(err.Error(), "expects 1 arguments, got 2") {
		t.Fatalf("QueryContext = %v, want an argument-count error", err)
	}
	if physical.netConn == nil {
		t.Error("connection was retired for a pre-send count error")
	}
}

func TestPrepareRewritesQuestionPlaceholders(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		_, sql, _, err := wire.DecodeParse(payload)
		if err != nil || sql != "SELECT $1" {
			peer.Errorf("parse sql = %q err %v", sql, err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgDescribe); !ok {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgParseComplete, nil)
		_ = peer.WriteMessage(wire.MsgParameterDescription, wire.EncodeParameterDescription([]uint32{23}))
		_ = peer.RowDescription([]wire.ColumnDescription{
			{Name: "n", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 1},
		})
		_ = peer.ReadyForQuery(wire.StatusIdle)
		if _, ok := expectMessage(peer, wire.MsgClose); !ok {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgCloseComplete, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), "SELECT ?")
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	if prepared.NumInput() != 1 {
		t.Errorf("NumInput = %d, want 1", prepared.NumInput())
	}
	if err := prepared.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	_ = physical.Close()
}

func TestNonPGModeLeavesPlaceholdersUntouched(t *testing.T) {
	for _, mode := range []CompatibilityMode{ModeOracle, ModeMySQL, ModeSQLServer} {
		t.Run(mode.String(), func(t *testing.T) {
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				if !startupOK(peer) {
					return
				}
				payload, ok := expectMessage(peer, wire.MsgParse)
				if !ok {
					return
				}
				_, sql, _, err := wire.DecodeParse(payload)
				if err != nil || sql != "SELECT ?, ?" {
					peer.Errorf("parse sql = %q err %v, want the statement unchanged", sql, err)
					return
				}
				payload, ok = expectMessage(peer, wire.MsgBind)
				if !ok {
					return
				}
				// An unverified mode skips the client-side count check too.
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
			cfg := peerConfig(t, srv, "")
			cfg.CompatibilityMode = mode
			physical, err := (&Connector{cfg: cfg}).Connect(context.Background())
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			connection := physical.(*conn)
			defer func() { _ = connection.Close() }()
			rows, err := connection.QueryContext(context.Background(), "SELECT ?, ?", []driver.NamedValue{
				{Ordinal: 1, Value: int64(7)},
				{Ordinal: 2, Value: int64(8)},
			})
			if err != nil {
				t.Fatalf("QueryContext: %v", err)
			}
			_ = rows.Close()
		})
	}
}

func TestExecRewritesQuestionPlaceholders(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		_, sql, _, err := wire.DecodeParse(payload)
		if err != nil || sql != "SELECT $1" {
			peer.Errorf("parse sql = %q err %v", sql, err)
			return
		}
		payload, ok = expectMessage(peer, wire.MsgBind)
		if !ok {
			return
		}
		_, _, _, params, _, err := wire.DecodeBind(payload)
		if err != nil || len(params) != 1 || string(params[0]) != "7" {
			peer.Errorf("bind params = %q err %v", params, err)
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
		_ = peer.CommandComplete("SELECT 0")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	if _, err := physical.ExecContext(context.Background(), "SELECT ?", []driver.NamedValue{
		{Ordinal: 1, Value: int64(7)},
	}); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
}

func TestExecZeroArgumentCountMismatchFailsBeforeSend(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	_, err = physical.ExecContext(context.Background(), "DELETE FROM t WHERE id = ?", nil)
	if err == nil || !strings.Contains(err.Error(), "expects 1 arguments, got 0") {
		t.Fatalf("ExecContext = %v, want a pre-send argument-count error", err)
	}
	if physical.netConn == nil {
		t.Error("connection was retired for a pre-send count error")
	}
}

func TestPrepareRejectsNamedPlaceholdersBeforeSend(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	if _, err := physical.PrepareContext(context.Background(), "SELECT * FROM t WHERE id = :id"); err == nil ||
		!strings.Contains(err.Error(), "named placeholder") {
		t.Fatalf("PrepareContext = %v, want a named-placeholder error", err)
	}
	if physical.netConn == nil {
		t.Error("connection was retired for a pre-send lexer error")
	}
}
