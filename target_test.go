package kingbase

import (
	"database/sql"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// targetQueryServer answers one direct extended query with one result column.
// It asserts that Parse carries no parameter OIDs and that the Bind formats and
// parameters are exactly the expected explicit-target encoding.
func targetQueryServer(t *testing.T, wantSQL string, wantFormats []int16, wantParams [][]byte,
	resultOID uint32, resultFormat int16, result []byte) *testpeer.Server {
	t.Helper()
	return testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		_, query, oids, err := wire.DecodeParse(payload)
		if err != nil || query != wantSQL || len(oids) != 0 {
			peer.Errorf("parse = %q oids %v err %v, want %q without OIDs", query, oids, err, wantSQL)
			return
		}
		payload, ok = expectMessage(peer, wire.MsgBind)
		if !ok {
			return
		}
		_, _, formats, params, _, err := wire.DecodeBind(payload)
		if err != nil || !slices.Equal(formats, wantFormats) || len(params) != len(wantParams) {
			peer.Errorf("bind formats %v params %q err %v", formats, params, err)
			return
		}
		for i := range wantParams {
			if !slices.Equal(params[i], wantParams[i]) {
				peer.Errorf("bind param %d = %q, want %q", i, params[i], wantParams[i])
				return
			}
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
			{Name: testColumnName, TypeOID: resultOID, TypeLen: -1, TypeMod: -1, Format: resultFormat},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{result}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
}

func TestExplicitCastSelectsBinaryParameter(t *testing.T) {
	const query = "SELECT $1::pg_catalog.int4"
	number := make([]byte, 4)
	binary.BigEndian.PutUint32(number, 7)
	srv := targetQueryServer(t, query, []int16{1}, [][]byte{number}, 23, 1, number)
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "binary_parameters=yes")))
	defer func() { _ = db.Close() }()
	var got int64
	if err := db.QueryRow(query, int64(7)).Scan(&got); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if got != 7 {
		t.Errorf("value = %d, want 7", got)
	}
}

func TestUnqualifiedCastKeepsTextParameter(t *testing.T) {
	const query = "SELECT $1::int4"
	srv := targetQueryServer(t, query, nil, [][]byte{[]byte("7")}, 23, 0, []byte("7"))
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "binary_parameters=yes")))
	defer func() { _ = db.Close() }()
	var got int64
	if err := db.QueryRow(query, int64(7)).Scan(&got); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if got != 7 {
		t.Errorf("value = %d, want 7", got)
	}
}

func TestExplicitByteaTargetSelectsBinaryParameter(t *testing.T) {
	const query = "SELECT $1::pg_catalog.bytea"
	raw := []byte{0xde, 0xad}
	srv := targetQueryServer(t, query, []int16{1}, [][]byte{raw}, 17, 1, raw)
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "binary_parameters=yes")))
	defer func() { _ = db.Close() }()
	var got []byte
	if err := db.QueryRow(query, raw).Scan(&got); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if !slices.Equal(got, raw) {
		t.Errorf("value = %q, want %q", got, raw)
	}
}

func TestRawBytesDoNotSelectByteaTarget(t *testing.T) {
	const query = "SELECT $1"
	raw := []byte{0xde, 0xad}
	srv := targetQueryServer(t, query, nil, [][]byte{raw}, 17, 1, raw)
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "binary_parameters=yes")))
	defer func() { _ = db.Close() }()
	var got []byte
	if err := db.QueryRow(query, raw).Scan(&got); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if !slices.Equal(got, raw) {
		t.Errorf("value = %q, want %q", got, raw)
	}
}
