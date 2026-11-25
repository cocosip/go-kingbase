package kingbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestWrapperEncoding(t *testing.T) {
	moment := time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC)
	cases := []struct {
		value any
		want  string
	}{
		{VarChar("varchar"), "varchar"},
		{VarCharMax("varchar-max"), "varchar-max"},
		{NVarCharMax("nvarchar-max"), "nvarchar-max"},
		{NChar("nchar"), "nchar"},
		{DateTime1(moment), "2024-01-02 03:04:05.123456Z"},
		{uint64(1 << 63), "9223372036854775808"},
		{^uint64(0), "18446744073709551615"},
	}
	for _, testCase := range cases {
		got, err := encodeParameter(testCase.value)
		if err != nil {
			t.Errorf("encodeParameter(%T): %v", testCase.value, err)
			continue
		}
		if string(got) != testCase.want {
			t.Errorf("encodeParameter(%T) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

func TestWrapperScan(t *testing.T) {
	var text VarChar
	if err := text.Scan([]byte(testUser)); err != nil || text != testUser {
		t.Errorf("VarChar.Scan([]byte) = %q, %v", text, err)
	}
	var fixed NChar
	if err := fixed.Scan("x"); err != nil || fixed != "x" {
		t.Errorf("NChar.Scan(string) = %q, %v", fixed, err)
	}
	var moment DateTime1
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := moment.Scan(now); err != nil || !time.Time(moment).Equal(now) {
		t.Errorf("DateTime1.Scan = %v, %v", time.Time(moment), err)
	}
	if err := moment.Scan(nil); err == nil {
		t.Error("DateTime1.Scan(nil) succeeded")
	}
	var nchar NVarCharMax
	if err := nchar.Scan(nil); err == nil {
		t.Error("NVarCharMax.Scan(nil) succeeded")
	}
}

func TestWrapperThroughDatabaseSQL(t *testing.T) {
	moment := time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC)
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		_, query, paramOIDs, err := wire.DecodeParse(payload)
		if err != nil || query != "SELECT $1, $2" || len(paramOIDs) != 0 {
			peer.Errorf("parse = %q oids %v err %v", query, paramOIDs, err)
			return
		}
		payload, ok = expectMessage(peer, wire.MsgBind)
		if !ok {
			return
		}
		_, _, formats, params, _, err := wire.DecodeBind(payload)
		wantMoment := moment.Format("2006-01-02 15:04:05.999999999Z07:00")
		if err != nil || len(formats) != 0 || len(params) != 2 ||
			string(params[0]) != testUser || string(params[1]) != wantMoment {
			peer.Errorf("bind formats %v params %q err %v", formats, params, err)
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
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: testColumnName, TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
			{Name: "at", TypeOID: 1184, TypeLen: 8, TypeMod: -1, Format: 0},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte(testUser), []byte(wantMoment)}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
	})

	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	var name string
	var at time.Time
	if err := db.QueryRowContext(context.Background(), "SELECT $1, $2",
		VarChar(testUser), DateTime1(moment)).Scan(&name, &at); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if name != testUser || !at.Equal(moment) {
		t.Errorf("row = %q, %v, want alice, %v", name, at, moment)
	}
}

func TestUnsignedParameterThroughDatabaseSQL(t *testing.T) {
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
		if err != nil || len(params) != 1 || string(params[0]) != "18446744073709551615" {
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
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
	})

	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), "SELECT $1", ^uint64(0)); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
}

func TestWrapperValuers(t *testing.T) {
	cases := []struct {
		value driver.Valuer
		want  driver.Value
	}{
		{VarChar("v"), "v"},
		{VarCharMax("vm"), "vm"},
		{NVarCharMax("nvm"), "nvm"},
		{NChar("n"), "n"},
		{DateTime1(time.Unix(0, 0).UTC()), time.Unix(0, 0).UTC()},
	}
	for _, testCase := range cases {
		got, err := testCase.value.Value()
		if err != nil || got != testCase.want {
			t.Errorf("Value() = %#v, %v, want %#v", got, err, testCase.want)
		}
	}
}
