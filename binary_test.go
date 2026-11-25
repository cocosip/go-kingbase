package kingbase

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// binarySelectColumns is the binary form of selectColumns.
func binarySelectColumns() []wire.ColumnDescription {
	return []wire.ColumnDescription{
		{Name: "id", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 1},
		{Name: testColumnName, TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 1},
	}
}

func TestBinaryValueCodecs(t *testing.T) {
	encodeInt4 := func(v int32) []byte {
		data := make([]byte, 4)
		binary.BigEndian.PutUint32(data, uint32(v))
		return data
	}
	encodeInt8 := func(v int64) []byte {
		data := make([]byte, 8)
		binary.BigEndian.PutUint64(data, uint64(v))
		return data
	}
	timestamp := time.Date(2026, 11, 24, 10, 30, 0, 123000000, time.UTC)

	cases := []struct {
		typ    string
		data   []byte
		want   driver.Value
		exists bool
	}{
		{typ: testTypeBool, data: []byte{1}, want: true, exists: true},
		{typ: testTypeBool, data: []byte{0}, want: false, exists: true},
		{typ: "int2", data: []byte{0xff, 0xfe}, want: int64(-2), exists: true},
		{typ: testTypeInt4, data: encodeInt4(7), want: int64(7), exists: true},
		{typ: "int8", data: encodeInt8(1 << 40), want: int64(1 << 40), exists: true},
		{typ: "float4", data: encodeInt4(int32(math.Float32bits(1.5))), want: 1.5, exists: true},
		{typ: "float8", data: encodeInt8(int64(math.Float64bits(2.5))), want: 2.5, exists: true},
		{typ: "text", data: []byte(testHello), want: testHello, exists: true},
		{typ: "bytea", data: []byte{1, 2}, want: []byte{1, 2}, exists: true},
		{typ: "jsonb", data: append([]byte{1}, []byte(testJSONBody)...), want: testJSONBody, exists: true},
		{typ: "date", data: encodeInt4(0), want: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), exists: true},
		{
			typ:    "timestamptz",
			data:   encodeInt8(timestamp.Sub(pgEpoch).Microseconds()),
			want:   timestamp,
			exists: true,
		},
		{
			typ:    "uuid",
			data:   []byte{0xde, 0xad, 0xbe, 0xef, 0, 1, 0, 2, 0, 3, 0, 4, 0, 5, 6, 7},
			want:   "deadbeef-0001-0002-0003-000400050607",
			exists: true,
		},
		{typ: pgTypeNumeric, data: encodeInt4(42), exists: false},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			got, ok := binaryValue(ModePG, schemaPGCatalog, tc.typ, tc.data)
			if ok != tc.exists {
				t.Fatalf("binaryValue(%s) exists = %v, want %v", tc.typ, ok, tc.exists)
			}
			if !tc.exists {
				return
			}
			if !valuesEqual(got, tc.want) {
				t.Fatalf("binaryValue(%s) = %#v, want %#v", tc.typ, got, tc.want)
			}
		})
	}
}

func valuesEqual(got, want driver.Value) bool {
	switch want := want.(type) {
	case []byte:
		gotBytes, ok := got.([]byte)
		return ok && string(gotBytes) == string(want)
	case time.Time:
		gotTime, ok := got.(time.Time)
		return ok && gotTime.Equal(want)
	default:
		return got == want
	}
}

func TestPreparedStatementBinaryResultsDisabled(t *testing.T) {
	const query = "SELECT id, name FROM t WHERE id = $1"
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !describePrepared(peer, query, []uint32{23}) {
			return
		}
		if !readBind(peer, "kb_stmt_0", []string{"7"}, true, nil) {
			return
		}
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		_ = peer.RowDescription(selectColumns())
		_ = peer.DataRow([][]byte{[]byte("7"), []byte("alice")})
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		if !closeStatement(peer, "kb_stmt_0") {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "disable_prepared_binary_result=true")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	statement := prepared.(*stmt)
	rows, err := statement.QueryContext(context.Background(), []driver.NamedValue{{Ordinal: 1, Value: int64(7)}})
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 2)
	if err := rows.Next(dest); err != nil || dest[0] != int64(7) || dest[1] != "alice" {
		t.Fatalf("row = %v, err %v", dest, err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("rows.Close: %v", err)
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("statement.Close: %v", err)
	}
	_ = physical.Close()
}

func TestBinaryParameters(t *testing.T) {
	const query = "UPDATE t SET name = $1, id = $2, at = $3"
	moment := time.Date(2026, 11, 24, 10, 30, 0, 0, time.UTC)
	wantMicros := make([]byte, 8)
	binary.BigEndian.PutUint64(wantMicros, uint64(moment.Sub(pgEpoch).Microseconds()))
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !describePrepared(peer, query, []uint32{25, 23, 1184}) {
			return
		}
		if !readBindRaw(peer, "kb_stmt_0",
			[][]byte{[]byte(testBob), {0, 0, 0, 7}, wantMicros},
			[]int16{1, 1, 1}, false, nil) {
			return
		}
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		_ = peer.CommandComplete("UPDATE 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		if !closeStatement(peer, "kb_stmt_0") {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "binary_parameters=true")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	statement := prepared.(*stmt)
	result, err := statement.ExecContext(context.Background(), []driver.NamedValue{
		{Ordinal: 1, Value: testBob},
		{Ordinal: 2, Value: int64(7)},
		{Ordinal: 3, Value: moment},
	})
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("RowsAffected = %d, %v", affected, err)
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("statement.Close: %v", err)
	}
	_ = physical.Close()
}

func TestBinaryParameterOverflow(t *testing.T) {
	const query = "SELECT $1"
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !describePrepared(peer, query, []uint32{23}) {
			return
		}
		if !closeStatement(peer, "kb_stmt_0") {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "binary_parameters=true")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	statement := prepared.(*stmt)
	_, err = statement.ExecContext(context.Background(), []driver.NamedValue{{Ordinal: 1, Value: int64(1 << 40)}})
	if err == nil || !strings.Contains(err.Error(), "overflows int4") {
		t.Fatalf("error = %v, want int4 overflow error", err)
	}
	if physical.netConn == nil {
		t.Error("overflow retired the connection before sending")
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("statement.Close: %v", err)
	}
	_ = physical.Close()
}
