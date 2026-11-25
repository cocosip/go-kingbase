package kingbase

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// describePrepared reads Parse/Describe/Sync and answers the prepare exchange.
func describePrepared(peer *testpeer.Conn, wantSQL string, paramOIDs []uint32) bool {
	payload, ok := expectMessage(peer, wire.MsgParse)
	if !ok {
		return false
	}
	name, sql, _, err := wire.DecodeParse(payload)
	if err != nil || sql != wantSQL {
		peer.Errorf("Parse = %q (%q), err %v", name, sql, err)
		return false
	}
	describePayload, ok := expectMessage(peer, wire.MsgDescribe)
	if !ok {
		return false
	}
	kind, described, err := wire.DecodeDescribe(describePayload)
	if err != nil || kind != 'S' || described != name {
		peer.Errorf("Describe = %q/%q, err %v, want statement %q", kind, described, err, name)
		return false
	}
	if _, ok := expectMessage(peer, wire.MsgSync); !ok {
		return false
	}
	if err := peer.WriteMessage(wire.MsgParseComplete, nil); err != nil {
		peer.Errorf("ParseComplete: %v", err)
		return false
	}
	if err := peer.WriteMessage(wire.MsgParameterDescription, wire.EncodeParameterDescription(paramOIDs)); err != nil {
		peer.Errorf("ParameterDescription: %v", err)
		return false
	}
	if err := peer.WriteMessage(wire.MsgNoData, nil); err != nil {
		peer.Errorf("NoData: %v", err)
		return false
	}
	if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
		peer.Errorf("ReadyForQuery: %v", err)
		return false
	}
	return true
}

// readBindRaw asserts the Bind/Describe/Execute/Sync batch with raw parameter
// payloads and explicit per-parameter formats (used by binary parameters).
func readBindRaw(peer *testpeer.Conn, statementName string, wantParams [][]byte, wantFormats []int16, describe bool, wantResultFormats []int16) bool {
	payload, ok := expectMessage(peer, wire.MsgBind)
	if !ok {
		return false
	}
	portal, statement, paramFormats, params, resultFormats, err := wire.DecodeBind(payload)
	if err != nil || statement != statementName {
		peer.Errorf("Bind = %q/%q, err %v, want statement %q", portal, statement, err, statementName)
		return false
	}
	if !slices.Equal(paramFormats, wantFormats) {
		peer.Errorf("Bind parameter formats = %v, want %v", paramFormats, wantFormats)
		return false
	}
	if len(params) != len(wantParams) {
		peer.Errorf("Bind has %d parameters, want %d", len(params), len(wantParams))
		return false
	}
	for i, want := range wantParams {
		if !bytes.Equal(params[i], want) {
			peer.Errorf("Bind parameter %d = %v, want %v", i, params[i], want)
			return false
		}
	}
	if !slices.Equal(resultFormats, wantResultFormats) {
		peer.Errorf("Bind result formats = %v, want %v", resultFormats, wantResultFormats)
		return false
	}
	if describe {
		describePayload, ok := expectMessage(peer, wire.MsgDescribe)
		if !ok {
			return false
		}
		kind, name, err := wire.DecodeDescribe(describePayload)
		if err != nil || kind != 'P' || name != "" {
			peer.Errorf("Describe portal = %q/%q, err %v", kind, name, err)
			return false
		}
	}
	if _, ok := expectMessage(peer, wire.MsgExecute); !ok {
		return false
	}
	_, ok = expectMessage(peer, wire.MsgSync)
	return ok
}

// readBind asserts the Bind/Describe/Execute/Sync batch for a statement.
func readBind(peer *testpeer.Conn, statementName string, wantParams []string, describe bool, wantResultFormats []int16) bool {
	raw := make([][]byte, len(wantParams))
	for i, param := range wantParams {
		raw[i] = []byte(param)
	}
	return readBindRaw(peer, statementName, raw, nil, describe, wantResultFormats)
}

// closeStatement asserts the Close/Sync batch and answers it.
func closeStatement(peer *testpeer.Conn, statementName string) bool {
	payload, ok := expectMessage(peer, wire.MsgClose)
	if !ok {
		return false
	}
	kind, name, err := wire.DecodeClose(payload)
	if err != nil || kind != 'S' || name != statementName {
		peer.Errorf("Close = %q/%q, err %v, want statement %q", kind, name, err, statementName)
		return false
	}
	if _, ok := expectMessage(peer, wire.MsgSync); !ok {
		return false
	}
	if err := peer.WriteMessage(wire.MsgCloseComplete, nil); err != nil {
		peer.Errorf("CloseComplete: %v", err)
		return false
	}
	return peer.ReadyForQuery(wire.StatusIdle) == nil
}

func TestPreparedStatementRoundTrip(t *testing.T) {
	const query = "SELECT id, name FROM t WHERE id = $1"
	var statementName string
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		name, sql, _, err := wire.DecodeParse(payload)
		if err != nil || sql != query {
			peer.Errorf("Parse = %q, err %v", sql, err)
			return
		}
		statementName = name
		describePayload, ok := expectMessage(peer, wire.MsgDescribe)
		if !ok {
			return
		}
		if kind, described, err := wire.DecodeDescribe(describePayload); err != nil || kind != 'S' || described != name {
			peer.Errorf("Describe = %q/%q, err %v", kind, described, err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		if err := peer.WriteMessage(wire.MsgParseComplete, nil); err != nil {
			peer.Errorf("ParseComplete: %v", err)
			return
		}
		if err := peer.WriteMessage(wire.MsgParameterDescription, wire.EncodeParameterDescription([]uint32{23})); err != nil {
			peer.Errorf("ParameterDescription: %v", err)
			return
		}
		if err := peer.RowDescription(selectColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}

		if !readBind(peer, name, []string{"7"}, true, []int16{1}) {
			return
		}
		if err := peer.WriteMessage(wire.MsgBindComplete, nil); err != nil {
			peer.Errorf("BindComplete: %v", err)
			return
		}
		if err := peer.RowDescription(binarySelectColumns()); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{{0, 0, 0, 7}, []byte("alice")}); err != nil {
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
		if !closeStatement(peer, name) {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	statement := prepared.(*stmt)
	if statement.NumInput() != 1 {
		t.Errorf("NumInput = %d, want 1", statement.NumInput())
	}
	rows, err := statement.QueryContext(context.Background(), []driver.NamedValue{{Ordinal: 1, Value: int64(7)}})
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 2)
	if err := rows.Next(dest); err != nil || dest[0] != int64(7) {
		t.Fatalf("row = %v, err %v", dest, err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("rows.Close: %v", err)
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("statement.Close: %v", err)
	}
	if statementName == "" {
		t.Error("statement name was empty")
	}
	_ = physical.Close()
}

func TestPreparedStatementExec(t *testing.T) {
	const query = "UPDATE t SET name = $1 WHERE id = $2"
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		name, _, _, err := wire.DecodeParse(payload)
		if err != nil {
			peer.Errorf("DecodeParse: %v", err)
			return
		}
		_, _ = expectMessage(peer, wire.MsgDescribe)
		_, _ = expectMessage(peer, wire.MsgSync)
		_ = peer.WriteMessage(wire.MsgParseComplete, nil)
		_ = peer.WriteMessage(wire.MsgParameterDescription, wire.EncodeParameterDescription([]uint32{25, 23}))
		_ = peer.WriteMessage(wire.MsgNoData, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)

		if !readBind(peer, name, []string{testBob, "7"}, false, nil) {
			return
		}
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		_ = peer.CommandComplete("UPDATE 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		if !closeStatement(peer, name) {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	statement := prepared.(*stmt)
	execResult, err := statement.ExecContext(context.Background(), []driver.NamedValue{
		{Ordinal: 1, Value: testBob},
		{Ordinal: 2, Value: int64(7)},
	})
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if affected, err := execResult.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("RowsAffected = %d, %v", affected, err)
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("statement.Close: %v", err)
	}
	_ = physical.Close()
}

func TestPrepareServerErrorKeepsConnection(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		_, _ = expectMessage(peer, wire.MsgParse)
		_, _ = expectMessage(peer, wire.MsgDescribe)
		_, _ = expectMessage(peer, wire.MsgSync)
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "42601"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "syntax error"},
		)
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_, err = physical.PrepareContext(context.Background(), "SELECT broken FROM")
	var serverErr *Error
	if !errors.As(err, &serverErr) || serverErr.Code != "42601" {
		t.Fatalf("error = %v, want SQLSTATE 42601", err)
	}
	if physical.netConn == nil {
		t.Error("prepare error retired a reusable connection")
	}
	_ = physical.Close()
}

func TestStatementArgumentMismatchRejectedBeforeSend(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !describePrepared(peer, "SELECT $1, $2", []uint32{25, 25}) {
			return
		}
		if !closeStatement(peer, "kb_stmt_0") {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), "SELECT $1, $2")
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	statement := prepared.(*stmt)
	if _, err := statement.QueryContext(context.Background(), []driver.NamedValue{{Ordinal: 1, Value: "a"}}); err == nil ||
		!strings.Contains(err.Error(), "expects 2 parameters") {
		t.Fatalf("error = %v, want parameter-count error", err)
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("statement.Close: %v", err)
	}
	_ = physical.Close()
}

func TestStatementOnRetiredConnection(t *testing.T) {
	statement := &stmt{conn: &conn{}}
	if _, err := statement.QueryContext(context.Background(), nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("error = %v, want driver.ErrBadConn", err)
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("Close on retired connection: %v", err)
	}
}

type countingValuer struct {
	calls int
}

func (v *countingValuer) Value() (driver.Value, error) {
	v.calls++
	return "resolved", nil
}

func TestCheckNamedValue(t *testing.T) {
	physical := &conn{}
	valuer := &countingValuer{}
	cases := []struct {
		name      string
		argument  driver.NamedValue
		want      driver.Value
		wantErr   string
		wantCalls int
	}{
		{
			name:     "named parameter rejected",
			argument: driver.NamedValue{Ordinal: 1, Name: "id", Value: int64(1)},
			wantErr:  "named parameters are not supported",
		},
		{
			name:     "uint64 above int64 preserved",
			argument: driver.NamedValue{Ordinal: 1, Value: uint64(1 << 63)},
			want:     uint64(1 << 63),
		},
		{
			name:     "uint64 full range preserved",
			argument: driver.NamedValue{Ordinal: 1, Value: ^uint64(0)},
			want:     ^uint64(0),
		},
		{
			name:     "varchar wrapper preserved",
			argument: driver.NamedValue{Ordinal: 1, Value: VarChar("alice")},
			want:     VarChar("alice"),
		},
		{
			name:     "datetime wrapper preserved",
			argument: driver.NamedValue{Ordinal: 1, Value: DateTime1(time.Unix(1, 0).UTC())},
			want:     DateTime1(time.Unix(1, 0).UTC()),
		},
		{
			name:      "valuer resolved once",
			argument:  driver.NamedValue{Ordinal: 2, Value: valuer},
			want:      "resolved",
			wantCalls: 1,
		},
		{
			name:     "nil stays nil",
			argument: driver.NamedValue{Ordinal: 3, Value: nil},
			want:     nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			valuer.calls = 0
			argument := tc.argument
			err := physical.CheckNamedValue(&argument)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckNamedValue: %v", err)
			}
			if argument.Value != tc.want {
				t.Errorf("value = %#v, want %#v", argument.Value, tc.want)
			}
			if valuer.calls != tc.wantCalls {
				t.Errorf("Valuer calls = %d, want %d", valuer.calls, tc.wantCalls)
			}
		})
	}
}
