package gokb

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func startupOK(peer *testpeer.Conn) bool {
	if _, err := peer.ReadStartup(); err != nil {
		peer.Errorf("ReadStartup: %v", err)
		return false
	}
	if err := peer.StartupOK(nil); err != nil {
		peer.Errorf("StartupOK: %v", err)
		return false
	}
	return true
}

func expectMessage(peer *testpeer.Conn, want wire.MsgType) ([]byte, bool) {
	typ, payload, err := peer.ReadMessage()
	if err != nil {
		peer.Errorf("read message: %v", err)
		return nil, false
	}
	if typ != want {
		peer.Errorf("message %q, want %q", typ, want)
		return nil, false
	}
	return payload, true
}

func waitTerminate(peer *testpeer.Conn) {
	typ, _, err := peer.ReadMessage()
	if err != nil {
		return
	}
	if typ != wire.MsgTerminate {
		peer.Errorf("final message %q, want Terminate", typ)
	}
}

func selectColumns() []wire.ColumnDescription {
	return []wire.ColumnDescription{
		{Name: "id", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "name", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
	}
}

// drainUntilSync consumes the extended-protocol batch through Sync.
func drainUntilSync(peer *testpeer.Conn) bool {
	for {
		typ, _, err := peer.ReadMessage()
		if err != nil {
			peer.Errorf("read extended batch: %v", err)
			return false
		}
		if typ == wire.MsgSync {
			return true
		}
	}
}

// expectSimpleQuery answers one simple query with tag.
func expectSimpleQuery(peer *testpeer.Conn) bool {
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return false
	}
	if err := peer.CommandComplete("UPDATE 1"); err != nil {
		peer.Errorf("CommandComplete: %v", err)
		return false
	}
	if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
		peer.Errorf("ReadyForQuery: %v", err)
		return false
	}
	return true
}

func TestQuerySimpleRows(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		sql, err := wire.DecodeQuery(payload)
		if err != nil || sql != "SELECT id, name FROM t" {
			peer.Errorf("query = %q err %v", sql, err)
			return
		}
		if err := peer.RowDescription(selectColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("1"), []byte("alice")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("2"), nil}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.CommandComplete("SELECT 2"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		// The second command proves that Close drained the first response.
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.CommandComplete("UPDATE 3"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SELECT id, name FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	if names := result.Columns(); len(names) != 2 || names[0] != "id" || names[1] != "name" {
		t.Errorf("Columns = %v, want [id name]", names)
	}
	dest := make([]driver.Value, 2)
	if err := result.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if dest[0] != int64(1) || dest[1] != "alice" {
		t.Errorf("row 1 = %v, want [1 alice]", dest)
	}
	if err := result.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if dest[0] != int64(2) || dest[1] != nil {
		t.Errorf("row 2 = %v, want [2 <nil>]", dest)
	}
	if err := result.Next(dest); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after last row = %v, want EOF", err)
	}
	if err := result.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	execResult, err := physical.ExecContext(context.Background(), "UPDATE t SET x = 1", nil)
	if err != nil {
		t.Fatalf("ExecContext after Close: %v", err)
	}
	affected, err := execResult.RowsAffected()
	if err != nil || affected != 3 {
		t.Errorf("RowsAffected = %d err %v, want 3", affected, err)
	}
	if err := physical.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestQueryExtendedBinding(t *testing.T) {
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
		payload, ok = expectMessage(peer, wire.MsgDescribe)
		if !ok {
			return
		}
		kind, _, err := wire.DecodeDescribe(payload)
		if err != nil || kind != 'P' {
			peer.Errorf("describe kind = %q err %v", kind, err)
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
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: "n", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 1},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte{0, 0, 0, 0, 0, 0, 0, 42}}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.CommandComplete("SELECT 1"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SELECT $1, $2", []driver.NamedValue{
		{Ordinal: 1, Value: int64(42)},
		{Ordinal: 2, Value: testHello},
	})
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 1)
	if err := result.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	// A binary-format unknown column is bytes, per the unknown-type fallback.
	raw, ok := dest[0].([]byte)
	if !ok || len(raw) != 8 || raw[7] != 42 {
		t.Errorf("value = %#v, want binary bytes ending in 42", dest[0])
	}
	if err := result.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = physical.Close()
}

func TestExecExtended(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgParse); !ok {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgBind)
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
		if err := peer.CommandComplete("INSERT 0 1"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	execResult, err := physical.ExecContext(context.Background(), "INSERT INTO t VALUES ($1)", []driver.NamedValue{
		{Ordinal: 1, Value: int64(7)},
	})
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	affected, err := execResult.RowsAffected()
	if err != nil || affected != 1 {
		t.Errorf("RowsAffected = %d err %v, want 1", affected, err)
	}
	if _, err := execResult.LastInsertId(); err == nil {
		t.Error("LastInsertId succeeded, want unavailable")
	}
	_ = physical.Close()
}

func TestQueryServerErrorKeepsConnection(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "42P01"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "relation does not exist"},
		)
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.CommandComplete("SET"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_, err = physical.QueryContext(context.Background(), "SELECT * FROM missing", nil)
	var serverErr *Error
	if !errors.As(err, &serverErr) || serverErr.Code != "42P01" {
		t.Fatalf("error = %v, want SQLSTATE 42P01", err)
	}
	if _, err := physical.ExecContext(context.Background(), "SET x = 1", nil); err != nil {
		t.Fatalf("Exec after server error: %v", err)
	}
	_ = physical.Close()
}

func TestQueryEarlyCloseDrains(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription(selectColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		for _, id := range []string{"1", "2", "3"} {
			if err := peer.DataRow([][]byte{[]byte(id), []byte("x")}); err != nil {
				peer.Errorf("DataRow: %v", err)
				return
			}
		}
		if err := peer.CommandComplete("SELECT 3"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.CommandComplete("DELETE 0"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SELECT id, name FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 2)
	if err := result.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if err := result.Close(); err != nil {
		t.Fatalf("early Close: %v", err)
	}
	if _, err := physical.ExecContext(context.Background(), "DELETE FROM t", nil); err != nil {
		t.Fatalf("Exec after early Close: %v", err)
	}
	_ = physical.Close()
}

func TestQueryMultipleResultSetsRejected(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.RowDescription(selectColumns()[:1])
		_ = peer.DataRow([][]byte{[]byte("1")})
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.RowDescription(selectColumns()[:1])
		_ = peer.DataRow([][]byte{[]byte("2")})
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SELECT 1; SELECT 2", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 1)
	if err := result.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if err := result.Next(dest); !errors.Is(err, io.EOF) {
		t.Fatalf("second Next = %v, want EOF for the first result set", err)
	}
	// The second result set is not consumed, so Close must report it instead of
	// silently reusing a connection with unread results.
	if err := result.Close(); err == nil || !strings.Contains(err.Error(), "multiple result sets") {
		t.Fatalf("Close = %v, want multiple result sets error", err)
	}
	_ = physical.Close()
	if physical.netConn != nil {
		t.Error("connection was not retired after an unconsumed result set")
	}
}

func TestQueryContextDeadlineRetires(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.RowDescription(selectColumns()[:1])
		// Stall before the first row so the client deadline expires.
		_, _, _ = peer.ReadMessage()
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result, err := physical.QueryContext(ctx, "SELECT id FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 1)
	if err := result.Next(dest); err == nil {
		t.Fatal("Next succeeded, want deadline error")
	}
	_ = result.Close()
	if physical.netConn != nil {
		t.Error("connection was not retired after a read failure")
	}
	if _, err := physical.ExecContext(context.Background(), "SELECT 1", nil); !errors.Is(err, driver.ErrBadConn) {
		t.Errorf("Exec on retired connection = %v, want ErrBadConn", err)
	}
}

func TestQueryNoRows(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.CommandComplete("INSERT 0 1"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "INSERT INTO t VALUES (1)", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	if names := result.Columns(); len(names) != 0 {
		t.Errorf("Columns = %v, want none", names)
	}
	if err := result.Next(nil); !errors.Is(err, io.EOF) {
		t.Errorf("Next = %v, want EOF", err)
	}
	if err := result.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	_ = physical.Close()
}

func TestQuerySimpleAsyncMessages(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.NoticeResponse(wire.ErrorField{Code: wire.ErrorSeverity, Value: "NOTICE"}); err != nil {
			peer.Errorf("NoticeResponse: %v", err)
			return
		}
		if err := peer.WriteMessage(wire.MsgParameterStatus,
			wire.EncodeParameterStatus("application_name", "async")); err != nil {
			peer.Errorf("ParameterStatus: %v", err)
			return
		}
		if err := peer.RowDescription(selectColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("1"), []byte("alice")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.NotificationResponse(1234, "channel", "payload"); err != nil {
			peer.Errorf("NotificationResponse: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("2"), []byte("bob")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.CommandComplete("SELECT 2"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if !expectSimpleQuery(peer) {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SELECT id, name FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 2)
	if err := result.Next(dest); err != nil || dest[0] != int64(1) {
		t.Fatalf("first row = %v, err %v", dest, err)
	}
	if err := result.Next(dest); err != nil || dest[0] != int64(2) {
		t.Fatalf("second row = %v, err %v", dest, err)
	}
	if err := result.Next(dest); !errors.Is(err, io.EOF) {
		t.Fatalf("Next = %v, want EOF", err)
	}
	if err := result.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := physical.params["application_name"]; got != "async" {
		t.Errorf("application_name = %q, want async from async ParameterStatus", got)
	}
	if _, err := physical.ExecContext(context.Background(), "UPDATE t SET x = 1", nil); err != nil {
		t.Fatalf("Exec after async messages: %v", err)
	}
	_ = physical.Close()
}

func TestQueryExtendedErrorKeepsConnection(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !drainUntilSync(peer) {
			return
		}
		_ = peer.WriteMessage(wire.MsgParseComplete, nil)
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		if err := peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "23505"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "duplicate key value"},
		); err != nil {
			peer.Errorf("ErrorResponse: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if !expectSimpleQuery(peer) {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_, err = physical.QueryContext(context.Background(), "INSERT INTO t VALUES ($1)", []driver.NamedValue{
		{Ordinal: 1, Value: int64(1)},
	})
	var serverErr *Error
	if !errors.As(err, &serverErr) || serverErr.Code != "23505" {
		t.Fatalf("error = %v, want SQLSTATE 23505", err)
	}
	if _, err := physical.ExecContext(context.Background(), "UPDATE t SET x = 1", nil); err != nil {
		t.Fatalf("Exec after extended error: %v", err)
	}
	_ = physical.Close()
}

func TestRowsCloseTrailingError(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription(selectColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("1"), []byte("alice")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "22012"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "division by zero"},
		); err != nil {
			peer.Errorf("ErrorResponse: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if !expectSimpleQuery(peer) {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SELECT 1/0 FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 2)
	if err := result.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	closeErr := result.Close()
	var serverErr *Error
	if !errors.As(closeErr, &serverErr) || serverErr.Code != "22012" {
		t.Fatalf("Close error = %v, want SQLSTATE 22012", closeErr)
	}
	if _, err := physical.ExecContext(context.Background(), "UPDATE t SET x = 1", nil); err != nil {
		t.Fatalf("Exec after trailing error: %v", err)
	}
	_ = physical.Close()
}

func TestExecDiscardsReturningRows(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription(selectColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("1"), []byte("alice")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.CommandComplete("INSERT 0 1"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	execResult, err := physical.ExecContext(context.Background(), "INSERT INTO t VALUES (1) RETURNING id, name", nil)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if affected, err := execResult.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("RowsAffected = %d, %v, want 1", affected, err)
	}
	_ = physical.Close()
}

func TestQueryExtendedNoData(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !drainUntilSync(peer) {
			return
		}
		_ = peer.WriteMessage(wire.MsgParseComplete, nil)
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		_ = peer.WriteMessage(wire.MsgNoData, nil)
		if err := peer.CommandComplete("UPDATE 2"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if !expectSimpleQuery(peer) {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "UPDATE t SET x = $1", []driver.NamedValue{
		{Ordinal: 1, Value: int64(1)},
	})
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	if columns := result.Columns(); len(columns) != 0 {
		t.Errorf("Columns = %v, want none", columns)
	}
	if err := result.Next(nil); !errors.Is(err, io.EOF) {
		t.Errorf("Next = %v, want EOF", err)
	}
	if err := result.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := physical.ExecContext(context.Background(), "UPDATE t SET x = 1", nil); err != nil {
		t.Fatalf("Exec after NoData: %v", err)
	}
	_ = physical.Close()
}

func TestQueryExtendedReadyBeforeDescription(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !drainUntilSync(peer) {
			return
		}
		_ = peer.WriteMessage(wire.MsgParseComplete, nil)
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		if err := peer.CommandComplete("UPDATE 0"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if !expectSimpleQuery(peer) {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SET x = $1", []driver.NamedValue{
		{Ordinal: 1, Value: int64(1)},
	})
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	if err := result.Next(nil); !errors.Is(err, io.EOF) {
		t.Errorf("Next = %v, want EOF", err)
	}
	if err := result.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := physical.ExecContext(context.Background(), "UPDATE t SET x = 1", nil); err != nil {
		t.Fatalf("Exec after early completion: %v", err)
	}
	_ = physical.Close()
}

func TestQueryCancelOnlyContextInterruptsRead(t *testing.T) {
	reading := make(chan struct{})
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription(selectColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		close(reading)
		_, _, _ = peer.ReadMessage()
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result, err := physical.QueryContext(ctx, "SELECT id, name FROM t", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	go func() {
		<-reading
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	if err := result.Next(make([]driver.Value, 2)); err == nil {
		t.Fatal("Next succeeded, want cancellation error")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Next took %v, want cancellation to interrupt the read", elapsed)
	}
	_ = result.Close()
	if physical.netConn != nil {
		t.Error("connection was not retired after cancellation")
	}
}

func TestQueryEmptySQL(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgEmptyQueryResponse, nil)
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if !expectSimpleQuery(peer) {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	if err := result.Next(nil); !errors.Is(err, io.EOF) {
		t.Errorf("Next = %v, want EOF", err)
	}
	if err := result.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := physical.ExecContext(context.Background(), "UPDATE t SET x = 1", nil); err != nil {
		t.Fatalf("Exec after empty query: %v", err)
	}
	_ = physical.Close()
}

func TestQueryMalformedParameterStatusRetires(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgParameterStatus, []byte{0x01, 0x02, 0x03})
		// The client must retire instead of reading another message here.
		_, _, _ = peer.ReadMessage()
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.QueryContext(context.Background(), "SELECT 1", nil); err == nil ||
		!strings.Contains(err.Error(), "ParameterStatus") {
		t.Fatalf("error = %v, want malformed ParameterStatus error", err)
	}
	if physical.netConn != nil {
		t.Error("connection was not retired after a malformed async message")
	}
}
