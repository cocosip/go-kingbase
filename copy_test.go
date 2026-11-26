package kingbase

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// peerBegin answers BEGIN and leaves the session in a transaction.
func peerBegin(peer *testpeer.Conn) bool {
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return false
	}
	if err := peer.CommandComplete(testBeginSQL); err != nil {
		peer.Errorf("CommandComplete: %v", err)
		return false
	}
	if err := peer.ReadyForQuery(wire.StatusInTransaction); err != nil {
		peer.Errorf("ReadyForQuery: %v", err)
		return false
	}
	return true
}

// peerCopyIn answers the COPY statement with a text CopyInResponse.
func peerCopyIn(peer *testpeer.Conn, columns int) bool {
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return false
	}
	formats := make([]int16, columns)
	if err := peer.WriteMessage(wire.MsgCopyInResponse, wire.EncodeCopyInResponse(0, formats)); err != nil {
		peer.Errorf("CopyInResponse: %v", err)
		return false
	}
	return true
}

// readCopyStream consumes CopyData until the client completes or aborts the
// stream, returning the concatenated data, the chunk count and any CopyFail
// reason.
func readCopyStream(peer *testpeer.Conn) (data string, chunks int, failed string, err error) {
	var b strings.Builder
	for {
		typ, payload, rerr := peer.ReadMessage()
		if rerr != nil {
			return "", 0, "", rerr
		}
		switch typ {
		case wire.MsgCopyData:
			b.Write(payload)
			chunks++
		case wire.MsgCopyDone:
			return b.String(), chunks, "", nil
		case wire.MsgCopyFail:
			reason, derr := wire.DecodeCopyFail(payload)
			if derr != nil {
				return "", 0, "", derr
			}
			return b.String(), chunks, reason, nil
		default:
			return "", 0, "", fmt.Errorf("unexpected message %q during COPY", typ)
		}
	}
}

// finishCopyStream answers the terminal message of a copy stream with the given
// outcome: a completion tag and status for CopyDone, or an error for CopyFail.
func finishCopyStream(peer *testpeer.Conn, tag string) bool {
	_, _, failed, err := readCopyStream(peer)
	if err != nil {
		peer.Errorf("copy stream: %v", err)
		return false
	}
	if failed != "" {
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "57014"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "COPY aborted"},
		)
		return peer.ReadyForQuery(wire.StatusFailedTransaction) == nil
	}
	if err := peer.CommandComplete(tag); err != nil {
		peer.Errorf("CommandComplete: %v", err)
		return false
	}
	return peer.ReadyForQuery(wire.StatusInTransaction) == nil
}

// drainUntilTerminate ignores late CopyData that the client had already
// buffered when the server failed the stream.
func drainUntilTerminate(peer *testpeer.Conn) {
	for {
		typ, _, err := peer.ReadMessage()
		if err != nil || typ == wire.MsgTerminate {
			return
		}
	}
}

func TestCopyInStatementHelpers(t *testing.T) {
	if got := CopyIn("t", "a", "b"); got != `COPY "t" ("a", "b") FROM STDIN` {
		t.Errorf("CopyIn = %q", got)
	}
	if got := CopyIn("t"); got != `COPY "t" FROM STDIN` {
		t.Errorf("CopyIn without columns = %q", got)
	}
	if got := CopyInSchema("s", `we"ird`, "c"); got != `COPY "s"."we""ird" ("c") FROM STDIN` {
		t.Errorf("CopyInSchema = %q", got)
	}
	if got := QuoteIdentifier("a\x00b"); got != `"a"` {
		t.Errorf("QuoteIdentifier with NUL = %q", got)
	}
}

func TestCopyInRoundTrip(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 3) {
			return
		}
		data, _, failed, err := readCopyStream(peer)
		if err != nil {
			peer.Errorf("copy stream: %v", err)
			return
		}
		if failed != "" {
			peer.Errorf("copy failed: %q", failed)
			return
		}
		want := "a\\tb\\nc\\\\d\\re\t\\N\t42\nx\t\t\\N\n\\N\n"
		if data != want {
			peer.Errorf("copy data = %q, want %q", data, want)
		}
		_ = peer.CommandComplete("COPY 3")
		_ = peer.ReadyForQuery(wire.StatusInTransaction)
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.CommandComplete("COMMIT")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	tx, err := physical.BeginTx(context.Background(), driver.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), CopyIn("t", "a", "b", "c"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	ci, ok := prepared.(*copyIn)
	if !ok {
		t.Fatalf("prepared statement is %T, want *copyIn", prepared)
	}
	if _, err := ci.Exec([]driver.Value{"a\tb\nc\\d\re", nil, int64(42)}); err != nil {
		t.Fatalf("Exec row 1: %v", err)
	}
	if _, err := ci.Exec([]driver.Value{"x", "", nil}); err != nil {
		t.Fatalf("Exec row 2: %v", err)
	}
	if _, err := ci.Exec([]driver.Value{nil}); err != nil {
		t.Fatalf("Exec row 3: %v", err)
	}
	result, err := ci.Exec(nil)
	if err != nil {
		t.Fatalf("Exec finish: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 3 {
		t.Errorf("RowsAffected = %d, %v, want 3", affected, err)
	}
	if err := ci.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestCopyInRequiresTransaction(t *testing.T) {
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
	if _, err := physical.PrepareContext(context.Background(), CopyIn("t", "a")); !errors.Is(err, errCopyOutsideTx) {
		t.Fatalf("PrepareContext error = %v, want %v", err, errCopyOutsideTx)
	}
	if physical.netConn == nil {
		t.Error("connection was retired without sending anything")
	}
	_ = physical.Close()
}

func TestCopyOutCannotBePrepared(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		// The rejected Prepare must not touch the wire; the next message is the
		// probe statement that proves the session is still usable.
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
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
	if _, err := physical.PrepareContext(context.Background(), "COPY t TO STDOUT"); !errors.Is(err, errCopyOutPrepared) {
		t.Fatalf("Prepare(COPY TO STDOUT) error = %v, want %v", err, errCopyOutPrepared)
	}
	if physical.netConn == nil {
		t.Fatal("a rejected COPY TO statement retired the connection")
	}
	if _, err := physical.ExecContext(context.Background(), "SELECT 1", nil); err != nil {
		t.Fatalf("ExecContext(SELECT 1) after the rejection: %v", err)
	}
}

// TestCopyOutStatementDetection pins which statements the driver treats as a
// COPY TO STDOUT stream, including the shapes that must stay ordinary SQL.
func TestCopyOutStatementDetection(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"COPY t TO STDOUT", true},
		{"copy t to stdout", true},
		{"COPY t (a, b) TO STDOUT WITH (FORMAT binary)", true},
		{"COPY (SELECT 1) TO STDOUT", true},
		{"COPY (SELECT 'TO STDOUT') TO STDOUT", true},
		{"COPY t FROM STDIN", false},
		{"COPY t TO '/tmp/out.csv'", false},
		{"COPY t TO PROGRAM 'gzip'", false},
		{"COPY t FROM '/tmp/in.csv'", false},
		{"SELECT * FROM t", false},
		{"/* COPY t TO STDOUT */ SELECT 1", false},
		{"COPY t (a) FROM STDIN; SELECT 1", false},
	}
	for _, tc := range cases {
		if got := isCopyOutStatement(tc.query); got != tc.want {
			t.Errorf("isCopyOutStatement(%q) = %t, want %t", tc.query, got, tc.want)
		}
	}
	if !isCopyOutStatement("COPY t (to, other) TO STDOUT") {
		t.Error("a column named to inside the column list was mistaken for the direction keyword")
	}
	if isCopyOutStatement(`COPY "to" FROM STDIN`) {
		t.Error("a quoted identifier named to changed the direction")
	}
}

// TestCopyInRawStatementHelpers pins the raw statement shapes: an explicit
// FORMAT selects raw mode, and the schema variants stay qualified.
func TestCopyInRawStatementHelpers(t *testing.T) {
	if got, want := CopyInRaw("orders", "id", "total"), `COPY "orders" ("id", "total") FROM STDIN WITH (FORMAT text)`; got != want {
		t.Errorf("CopyInRaw = %q, want %q", got, want)
	}
	if got, want := CopyInRawSchema("sales", "orders"), `COPY "sales"."orders" FROM STDIN WITH (FORMAT text)`; got != want {
		t.Errorf("CopyInRawSchema = %q, want %q", got, want)
	}
	if got, want := CopyInRawBinary("orders", "id"), `COPY "orders" ("id") FROM STDIN WITH (FORMAT binary)`; got != want {
		t.Errorf("CopyInRawBinary = %q, want %q", got, want)
	}
	if got, want := CopyInRawBinarySchema("sales", "orders"), `COPY "sales"."orders" FROM STDIN WITH (FORMAT binary)`; got != want {
		t.Errorf("CopyInRawBinarySchema = %q, want %q", got, want)
	}
	for query, want := range map[string]bool{
		CopyIn("t", "a"):                             false,
		CopyInRaw("t", "a"):                          true,
		CopyInRawBinary("t", "a"):                    true,
		`COPY t (format) FROM STDIN`:                 false,
		`COPY t (a) FROM STDIN WITH (FORMAT csv)`:    true,
		`COPY t (a) FROM STDIN WITH (FORMAT text)`:   true,
		`COPY t (a) FROM STDIN WITH (DELIMITER ';')`: false,
	} {
		if got := copyInIsRaw(query); got != want {
			t.Errorf("copyInIsRaw(%q) = %t, want %t", query, got, want)
		}
	}
}

// TestCopyInRawWritesPayloadVerbatim proves raw mode forwards caller bytes
// without adding row framing, in text and binary form.
func TestCopyInRawWritesPayloadVerbatim(t *testing.T) {
	for _, tc := range []struct {
		name      string
		statement string
		format    int8
		chunks    []string
		want      string
	}{
		{
			name:      pgTypeText,
			statement: CopyInRaw("t", "a"),
			format:    0,
			chunks:    []string{"1\ta\n", "2\tb\n"},
			want:      "1\ta\n2\tb\n",
		},
		{
			name:      "binary",
			statement: CopyInRawBinary("t", "a"),
			format:    1,
			chunks:    []string{"KBCOPY\n\xff\r\n\x00", "\x00\x01payload", "\xff\xff"},
			want:      "KBCOPY\n\xff\r\n\x00\x00\x01payload\xff\xff",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				if !startupOK(peer) {
					return
				}
				if !peerBegin(peer) {
					return
				}
				if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
					return
				}
				formats := []int16{int16(tc.format)}
				if err := peer.WriteMessage(wire.MsgCopyInResponse, wire.EncodeCopyInResponse(tc.format, formats)); err != nil {
					peer.Errorf("CopyInResponse: %v", err)
					return
				}
				data, chunks, failed, err := readCopyStream(peer)
				if err != nil {
					peer.Errorf("copy stream: %v", err)
					return
				}
				if failed != "" {
					peer.Errorf("copy failed: %q", failed)
					return
				}
				if data != tc.want {
					peer.Errorf("copy data = %q, want %q", data, tc.want)
				}
				if chunks != len(tc.chunks) {
					peer.Errorf("stream had %d chunks, want %d", chunks, len(tc.chunks))
				}
				_ = peer.CommandComplete("COPY 2")
				_ = peer.ReadyForQuery(wire.StatusInTransaction)
				waitTerminate(peer)
			})
			physical, err := connectPeer(t, srv, "")
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			prepared, err := physical.PrepareContext(context.Background(), tc.statement)
			if err != nil {
				t.Fatalf("PrepareContext: %v", err)
			}
			ci, ok := prepared.(*copyIn)
			if !ok {
				t.Fatalf("prepared statement is %T, want *copyIn", prepared)
			}
			for _, chunk := range tc.chunks {
				if _, err := ci.Exec([]driver.Value{chunk}); err != nil {
					t.Fatalf("Exec(%q): %v", chunk, err)
				}
			}
			result, err := ci.Exec(nil)
			if err != nil {
				t.Fatalf("finish Exec: %v", err)
			}
			if affected, err := result.RowsAffected(); err != nil || affected != 2 {
				t.Errorf("RowsAffected = %d, %v, want 2", affected, err)
			}
		})
	}
}

// TestCopyInRawRejectsBadPayload proves a non-payload argument aborts the
// stream instead of importing half a table.
func TestCopyInRawRejectsBadPayload(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []driver.Value
	}{
		{name: "two arguments", args: []driver.Value{"a", "b"}},
		{name: "non payload", args: []driver.Value{int64(7)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				if !startupOK(peer) {
					return
				}
				if !peerBegin(peer) {
					return
				}
				if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
					return
				}
				if err := peer.WriteMessage(wire.MsgCopyInResponse, wire.EncodeCopyInResponse(0, []int16{0})); err != nil {
					peer.Errorf("CopyInResponse: %v", err)
					return
				}
				_, _, failed, err := readCopyStream(peer)
				if err != nil {
					peer.Errorf("copy stream: %v", err)
					return
				}
				if failed == "" {
					peer.Errorf("stream was not aborted with CopyFail")
				}
				_ = peer.ErrorResponse(
					wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
					wire.ErrorField{Code: wire.ErrorCode, Value: "22P04"},
					wire.ErrorField{Code: wire.ErrorMessage, Value: "COPY aborted by the client"},
				)
				_ = peer.ReadyForQuery(wire.StatusFailedTransaction)
				drainUntilTerminate(peer)
			})
			physical, err := connectPeer(t, srv, "")
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			prepared, err := physical.PrepareContext(context.Background(), CopyInRaw("t", "a"))
			if err != nil {
				t.Fatalf("PrepareContext: %v", err)
			}
			if _, err := prepared.(driver.StmtExecContext).ExecContext(context.Background(), legacyNamedValues(tc.args)); err == nil {
				t.Fatal("raw COPY accepted an invalid payload")
			}
		})
	}
}

// TestCopyInBinaryStatement proves the helper marks the statement and that the
// parser recovers the relation and column list, or refuses the statement.
func TestCopyInBinaryStatement(t *testing.T) {
	statement := CopyInBinary("orders", "id", "note")
	want := `COPY "orders" ("id", "note") FROM STDIN WITH (FORMAT binary) /* kingbase:binary-rows */`
	if statement != want {
		t.Fatalf("CopyInBinary = %q, want %q", statement, want)
	}
	relation, columns, marked, err := copyBinaryStatement(statement)
	if err != nil || !marked || relation != `"orders"` || len(columns) != 2 ||
		columns[0] != "id" || columns[1] != "note" {
		t.Fatalf("copyBinaryStatement = %q, %v, %t, %v", relation, columns, marked, err)
	}
	if relation, _, marked, err := copyBinaryStatement(`COPY "sales"."orders" ("id") FROM STDIN WITH (FORMAT binary, DELIMITER ',') ` + copyBinaryMarker); err != nil ||
		!marked || relation != `"sales"."orders"` {
		t.Fatalf("qualified statement = %q, %t, %v", relation, marked, err)
	}
	if _, _, marked, err := copyBinaryStatement(CopyInRawBinary("orders", "id")); marked || err != nil {
		t.Fatalf("raw statement parsed as typed: %t, %v", marked, err)
	}
	for _, bad := range []string{
		`COPY "t" FROM STDIN WITH (FORMAT binary) ` + copyBinaryMarker,
		`COPY "t" ("a") FROM STDIN WITH (FORMAT text) ` + copyBinaryMarker,
		`COPY "t" ("a") FROM STDIN ` + copyBinaryMarker,
		`INSERT INTO "t" ("a") VALUES ($1) ` + copyBinaryMarker,
		`COPY "t" ("a") FROM '/tmp/in.csv' WITH (FORMAT binary) ` + copyBinaryMarker,
	} {
		if _, _, _, err := copyBinaryStatement(bad); err == nil {
			t.Errorf("copyBinaryStatement(%q) accepted a malformed statement", bad)
		}
	}
}

// TestCopyInBinaryKinds pins the catalog mapping, including the two storage forms
// of a date and the types that stay unsupported.
func TestCopyInBinaryKinds(t *testing.T) {
	for _, tc := range []struct {
		typname string
		typlen  int64
		want    copyKind
	}{
		{"int2", 2, copyInt2},
		{pgTypeInt4, 4, copyInt4},
		{"int8", 8, copyInt8},
		{"float4", 4, copyFloat4},
		{"float8", 8, copyFloat8},
		{"bool", 1, copyBool},
		{pgTypeText, -1, copyText},
		{"varcharbyte", -1, copyText},
		{"bytea", -1, copyBytea},
		{"date", 4, copyDateDays},
		{"date", 8, copyDateMicros},
		{"timestamp", 8, copyTimestamp},
		{"timestamptz", 8, copyTimestamptz},
		{"time", 8, copyTime},
		{"uuid", 16, copyUUID},
		{"json", -1, copyJSON},
		{"jsonb", -1, copyJSONB},
	} {
		kind, ok := copyKindForType(tc.typname, tc.typlen)
		if !ok || kind != tc.want {
			t.Errorf("copyKindForType(%q, %d) = %v, %t, want %v", tc.typname, tc.typlen, kind, ok, tc.want)
		}
	}
	for _, typname := range []string{"numeric", "interval", "rowversion", "int4range", "_int4"} {
		if _, ok := copyKindForType(typname, -1); ok {
			t.Errorf("copyKindForType(%q) accepted a type without an encoder", typname)
		}
	}
}

// catalogResponse describes the three columns of the binary COPY resolution query.
func catalogResponse(rows ...[]string) ([]wire.ColumnDescription, [][][]byte) {
	columns := []wire.ColumnDescription{
		{Name: "attname", TypeOID: 25, TypeLen: -1},
		{Name: "typname", TypeOID: 19, TypeLen: -1},
		{Name: "typlen", TypeOID: 21, TypeLen: 2},
	}
	data := make([][][]byte, len(rows))
	for i, row := range rows {
		data[i] = [][]byte{[]byte(row[0]), []byte(row[1]), []byte(row[2])}
	}
	return columns, data
}

// peerBinaryCatalog answers the column resolution query and then the COPY
// statement that follows it.
func peerBinaryCatalog(peer *testpeer.Conn, rows [][]string, stream func(*testpeer.Conn)) bool {
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return false
	}
	columns, data := catalogResponse(rows...)
	if err := peer.SendResult(columns, data, "SELECT 0", wire.StatusInTransaction); err != nil {
		peer.Errorf("catalog result: %v", err)
		return false
	}
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return false
	}
	if err := peer.WriteMessage(wire.MsgCopyInResponse, wire.EncodeCopyInResponse(1, []int16{1})); err != nil {
		peer.Errorf("CopyInResponse: %v", err)
		return false
	}
	stream(peer)
	return true
}

// TestCopyInBinaryStream proves the wire shape after the catalog lookup: one
// header, length-prefixed fields in declared order, NULL as -1 and the trailer
// before CopyDone.
func TestCopyInBinaryStream(t *testing.T) {
	want := []byte("KBCOPY\n\xff\r\n\x00")
	want = append(want, 0, 0, 0, 0, 0, 0, 0, 0) // flags, header extension length
	want = append(want, 0, 4)                   // four fields
	want = append(want, 0, 0, 0, 4, 0, 0, 0, 7) // integer 7
	want = append(want, 0, 0, 0, 2, 'h', 'i')   // text "hi"
	want = append(want, 0, 0, 0, 1, 1)          // boolean true
	want = append(want, 0xff, 0xff, 0xff, 0xff) // NULL
	want = append(want, 0xff, 0xff)             // trailer

	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerBinaryCatalog(peer, [][]string{
			{"a", pgTypeInt4, "4"},
			{"b", pgTypeText, "-1"},
			{"c", "bool", "1"},
			{"d", pgTypeText, "-1"},
		}, func(peer *testpeer.Conn) {
			data, _, failed, err := readCopyStream(peer)
			if err != nil {
				peer.Errorf("copy stream: %v", err)
				return
			}
			if failed != "" {
				peer.Errorf("copy failed: %q", failed)
				return
			}
			if !bytes.Equal([]byte(data), want) {
				peer.Errorf("copy payload = %x, want %x", data, want)
			}
			_ = peer.CommandComplete("COPY 1")
			_ = peer.ReadyForQuery(wire.StatusInTransaction)
			drainUntilTerminate(peer)
		}) {
			return
		}
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), CopyInBinary("t", "a", "b", "c", "d"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	ci := prepared.(*copyIn)
	if _, err := ci.Exec([]driver.Value{int64(7), "hi", true, nil}); err != nil {
		t.Fatalf("Exec row: %v", err)
	}
	if _, err := ci.Exec(nil); err != nil {
		t.Fatalf("finish Exec: %v", err)
	}
}

// TestCopyInBinaryTemporal proves the date and timestamp encodings against
// literal epoch counts, so an off-by-one day or a microsecond slip fails.
func TestCopyInBinaryTemporal(t *testing.T) {
	const (
		wantDays = 9498                                 // 2000-01-01 → 2026-01-02
		wantTime = int64(820627200000000 + 11045000000) // 03:04:05 since the epoch
	)
	days, err := encodeCopyBinaryValue(copyDateDays, time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatalf("encode date: %v", err)
	}
	if got := int64(binary.BigEndian.Uint32(days)); got != wantDays {
		t.Errorf("date days = %d, want %d", got, wantDays)
	}
	stamp, err := encodeCopyBinaryValue(copyTimestamp, time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600)))
	if err != nil {
		t.Fatalf("encode timestamp: %v", err)
	}
	if got := int64(binary.BigEndian.Uint64(stamp)); got != wantTime {
		t.Errorf("timestamp micros = %d, want %d", got, wantTime)
	}
	// The eight-byte date form keeps the instant, not just the day.
	zoned, err := encodeCopyBinaryValue(copyDateMicros, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatalf("encode eight-byte date: %v", err)
	}
	if got := int64(binary.BigEndian.Uint64(zoned)); got != wantTime {
		t.Errorf("eight-byte date micros = %d, want %d", got, wantTime)
	}
}

// TestCopyInBinaryRejectsBadRow proves a value that does not fit its column
// aborts the stream instead of importing a corrupt row.
func TestCopyInBinaryRejectsBadRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []driver.Value
	}{
		{name: "out of range", args: []driver.Value{int64(1 << 40)}},
		{name: "wrong type", args: []driver.Value{"abc"}},
		{name: "wrong count", args: []driver.Value{int64(1), int64(2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				if !startupOK(peer) {
					return
				}
				if !peerBegin(peer) {
					return
				}
				peerBinaryCatalog(peer, [][]string{{"a", pgTypeInt4, "4"}}, func(peer *testpeer.Conn) {
					_, _, failed, err := readCopyStream(peer)
					if err != nil {
						peer.Errorf("copy stream: %v", err)
						return
					}
					if failed == "" {
						peer.Errorf("stream was not aborted with CopyFail")
					}
					_ = peer.ErrorResponse(
						wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
						wire.ErrorField{Code: wire.ErrorCode, Value: "22P04"},
						wire.ErrorField{Code: wire.ErrorMessage, Value: "COPY aborted by the client"},
					)
					_ = peer.ReadyForQuery(wire.StatusFailedTransaction)
					drainUntilTerminate(peer)
				})
			})
			physical, err := connectPeer(t, srv, "")
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			prepared, err := physical.PrepareContext(context.Background(), CopyInBinary("t", "a"))
			if err != nil {
				t.Fatalf("PrepareContext: %v", err)
			}
			if _, err := prepared.(driver.StmtExecContext).ExecContext(context.Background(), legacyNamedValues(tc.args)); err == nil {
				t.Fatal("typed binary COPY accepted a row that does not match its column")
			}
		})
	}
}

// TestCopyInBinaryRejectsUnsupportedType proves a column without a binary
// encoder fails at prepare, before any COPY byte is sent.
func TestCopyInBinaryRejectsUnsupportedType(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		columns, data := catalogResponse([]string{"a", "numeric", "-1"})
		_ = peer.SendResult(columns, data, "SELECT 0", wire.StatusInTransaction)
		drainUntilTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	_, err = physical.PrepareContext(context.Background(), CopyInBinary("t", "a"))
	if err == nil || !strings.Contains(err.Error(), "no binary encoder") {
		t.Fatalf("PrepareContext error = %v, want a missing encoder", err)
	}
}

// TestCopyInBinaryRejectsTextResponse proves a typed statement never writes
// binary rows into a text stream.
func TestCopyInBinaryRejectsTextResponse(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		columns, data := catalogResponse([]string{"a", pgTypeInt4, "4"})
		_ = peer.SendResult(columns, data, "SELECT 0", wire.StatusInTransaction)
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgCopyInResponse, wire.EncodeCopyInResponse(0, []int16{0}))
		drainUntilTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := physical.PrepareContext(context.Background(), CopyInBinary("t", "a")); !errors.Is(err, errCopyTypedFormat) {
		t.Fatalf("PrepareContext error = %v, want %v", err, errCopyTypedFormat)
	}
}

// TestCopyInBinaryEmptyStream proves a typed statement that imports no row still
// sends the header and the trailer.
func TestCopyInBinaryEmptyStream(t *testing.T) {
	want := append(append([]byte{}, copyBinaryHeader...), 0xff, 0xff)
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		peerBinaryCatalog(peer, [][]string{{"a", pgTypeInt4, "4"}}, func(peer *testpeer.Conn) {
			data, _, failed, err := readCopyStream(peer)
			if err != nil {
				peer.Errorf("copy stream: %v", err)
				return
			}
			if failed != "" {
				peer.Errorf("copy failed: %q", failed)
				return
			}
			if !bytes.Equal([]byte(data), want) {
				peer.Errorf("copy payload = %x, want %x", data, want)
			}
			_ = peer.CommandComplete("COPY 0")
			_ = peer.ReadyForQuery(wire.StatusInTransaction)
			drainUntilTerminate(peer)
		})
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), CopyInBinary("t", "a"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	if _, err := prepared.(driver.StmtExecContext).ExecContext(context.Background(), nil); err != nil {
		t.Fatalf("finish Exec: %v", err)
	}
}

// peerCopyOutChunks answers one COPY TO STDOUT statement with the given chunks,
// then leaves the session idle and answers one probe query.
func peerCopyOutChunks(peer *testpeer.Conn, format int8, chunks ...string) bool {
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return false
	}
	if err := peer.WriteMessage(wire.MsgCopyOutResponse, wire.EncodeCopyInResponse(format, nil)); err != nil {
		peer.Errorf("CopyOutResponse: %v", err)
		return false
	}
	for _, chunk := range chunks {
		if err := peer.WriteMessage(wire.MsgCopyData, wire.EncodeCopyData([]byte(chunk))); err != nil {
			peer.Errorf("CopyData: %v", err)
			return false
		}
	}
	if err := peer.WriteMessage(wire.MsgCopyDone, nil); err != nil {
		peer.Errorf("CopyDone: %v", err)
		return false
	}
	if err := peer.CommandComplete("COPY 2"); err != nil {
		peer.Errorf("CommandComplete: %v", err)
		return false
	}
	return peer.ReadyForQuery(wire.StatusIdle) == nil
}

// peerProbeQuery answers one ordinary query, so a test can prove the session is
// reusable after a COPY stream.
func peerProbeQuery(peer *testpeer.Conn) bool {
	if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
		return false
	}
	if err := peer.RowDescription([]wire.ColumnDescription{
		{Name: "?column?", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
	}); err != nil {
		peer.Errorf("RowDescription: %v", err)
		return false
	}
	if err := peer.DataRow([][]byte{[]byte("1")}); err != nil {
		peer.Errorf("DataRow: %v", err)
		return false
	}
	if err := peer.CommandComplete("SELECT 1"); err != nil {
		peer.Errorf("CommandComplete: %v", err)
		return false
	}
	return peer.ReadyForQuery(wire.StatusIdle) == nil
}

// TestCopyOutStreamsChunks proves COPY TO STDOUT surfaces the server payload as
// protocol chunks, in text and binary form, and ends on the completion tag.
func TestCopyOutStreamsChunks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		format  int8
		chunks  []string
		payload string
	}{
		{name: "text", format: 0, chunks: []string{"1\n", "2\n"}, payload: "1\n2\n"},
		{name: "binary", format: 1, chunks: []string{"PGCOPY\n", "\xff\r\n"}, payload: "PGCOPY\n\xff\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				if !startupOK(peer) {
					return
				}
				if !peerCopyOutChunks(peer, tc.format, tc.chunks...) {
					return
				}
				if !peerProbeQuery(peer) {
					return
				}
				waitTerminate(peer)
			})
			physical, err := connectPeer(t, srv, "")
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			rows, err := physical.QueryContext(context.Background(), "COPY (SELECT 1) TO STDOUT", nil)
			if err != nil {
				t.Fatalf("QueryContext: %v", err)
			}
			if names := rows.Columns(); len(names) != 1 || names[0] != "copy" {
				t.Fatalf("Columns() = %v, want [copy]", names)
			}
			dest := make([]driver.Value, 1)
			var collected strings.Builder
			for {
				err := rows.Next(dest)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("Next: %v", err)
				}
				chunk, ok := dest[0].([]byte)
				if !ok {
					t.Fatalf("chunk type = %T, want []byte", dest[0])
				}
				collected.Write(chunk)
			}
			if got := collected.String(); got != tc.payload {
				t.Errorf("payload = %q, want %q", got, tc.payload)
			}
			if err := rows.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			probe, err := physical.QueryContext(context.Background(), "SELECT 1", nil)
			if err != nil {
				t.Fatalf("QueryContext after COPY: %v", err)
			}
			if err := probe.Next(dest); err != nil {
				t.Fatalf("probe Next: %v", err)
			}
			if dest[0] != int64(1) {
				t.Errorf("probe value = %v, want 1", dest[0])
			}
			if err := probe.Close(); err != nil {
				t.Fatalf("probe Close: %v", err)
			}
		})
	}
}

// TestCopyOutCloseDrainsStream proves an abandoned COPY stream is drained to
// ReadyForQuery, so the physical connection stays reusable.
func TestCopyOutCloseDrainsStream(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerCopyOutChunks(peer, 0, "1\n", "2\n", "3\n") {
			return
		}
		if !peerProbeQuery(peer) {
			return
		}
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rows, err := physical.QueryContext(context.Background(), "COPY t TO STDOUT", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(dest[0].([]byte)) != "1\n" {
		t.Fatalf("first chunk = %q, want 1\\n", dest[0])
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if physical.netConn == nil {
		t.Fatal("closing a COPY stream retired the connection")
	}
	probe, err := physical.QueryContext(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatalf("QueryContext after drained close: %v", err)
	}
	if err := probe.Next(dest); err != nil {
		t.Fatalf("probe Next: %v", err)
	}
	if dest[0] != int64(1) {
		t.Errorf("probe value = %v, want 1", dest[0])
	}
	if err := probe.Close(); err != nil {
		t.Fatalf("probe Close: %v", err)
	}
}

// TestCopyOutServerError proves a server error in the middle of a COPY stream is
// reported with its SQLSTATE and still leaves a usable session.
func TestCopyOutServerError(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.WriteMessage(wire.MsgCopyOutResponse, wire.EncodeCopyInResponse(0, nil)); err != nil {
			peer.Errorf("CopyOutResponse: %v", err)
			return
		}
		if err := peer.WriteMessage(wire.MsgCopyData, wire.EncodeCopyData([]byte("1\n"))); err != nil {
			peer.Errorf("CopyData: %v", err)
			return
		}
		if err := peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: "ERROR"},
			wire.ErrorField{Code: wire.ErrorCode, Value: "57014"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "canceling statement due to user request"},
		); err != nil {
			peer.Errorf("ErrorResponse: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		if !peerProbeQuery(peer) {
			return
		}
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rows, err := physical.QueryContext(context.Background(), "COPY t TO STDOUT", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		t.Fatalf("first Next: %v", err)
	}
	err = rows.Next(dest)
	if err == nil {
		t.Fatal("Next after a server error returned a chunk")
	}
	var serverErr *Error
	if !errors.As(err, &serverErr) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if serverErr.Message == "" {
		t.Error("server error lost its message")
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("Close after server error: %v", err)
	}
	probe, err := physical.QueryContext(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatalf("QueryContext after the COPY error: %v", err)
	}
	if err := probe.Next(dest); err != nil {
		t.Fatalf("probe Next: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatalf("probe Close: %v", err)
	}
}

// TestCopyOutRequiresFirstStatement pins the boundary: a COPY TO STDOUT that is
// not the first statement of a multi-statement query is rejected, because the
// driver cannot stream it back after a result set.
func TestCopyOutRequiresFirstStatement(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: "?column?", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("1")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.WriteMessage(wire.MsgCopyOutResponse, wire.EncodeCopyInResponse(0, nil)); err != nil {
			peer.Errorf("CopyOutResponse: %v", err)
			return
		}
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rows, err := physical.QueryContext(context.Background(), "SELECT 1; COPY t TO STDOUT", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		t.Fatalf("first result Next: %v", err)
	}
	next, ok := rows.(interface{ NextResultSet() error })
	if !ok {
		t.Fatalf("rows type %T has no NextResultSet", rows)
	}
	err = next.NextResultSet()
	if err == nil {
		t.Fatal("a COPY stream inside a multi-statement query was accepted")
	}
	if !errors.Is(err, errUnexpectedCopyResponse) {
		t.Fatalf("error = %v, want %v", err, errUnexpectedCopyResponse)
	}
	if physical.netConn != nil {
		t.Error("connection was not retired after the unexpected COPY response")
	}
}

// TestCopyStatementThroughQueryExecRequiresHelper pins the pre-send rejection
// of a COPY statement sent through Query/Exec. The CopyIn helper owns the copy
// stream, so the driver answers before the wire instead of misjudging the
// server's COPY response, and the session stays usable.
func TestCopyStatementThroughQueryExecRequiresHelper(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: "?column?", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("1")}); err != nil {
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
	for _, query := range []string{
		"COPY t FROM STDIN",
		"  copy t (a) from stdin",
		"COPY t FROM '/tmp/in.csv'",
	} {
		if _, err := physical.ExecContext(context.Background(), query, nil); !errors.Is(err, errCopyThroughExec) {
			t.Fatalf("ExecContext(%q) error = %v, want %v", query, err, errCopyThroughExec)
		}
		if _, err := physical.QueryContext(context.Background(), query, nil); !errors.Is(err, errCopyThroughExec) {
			t.Fatalf("QueryContext(%q) error = %v, want %v", query, err, errCopyThroughExec)
		}
	}
	if _, err := physical.ExecContext(context.Background(), "COPY t TO STDOUT", nil); !errors.Is(err, errCopyOutThroughExec) {
		t.Fatalf("ExecContext(COPY t TO STDOUT) error = %v, want %v", err, errCopyOutThroughExec)
	}
	if physical.netConn == nil {
		t.Fatal("a rejected COPY statement retired the connection")
	}
	queryRows, err := physical.QueryContext(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatalf("QueryContext after rejection: %v", err)
	}
	values := make([]driver.Value, 1)
	if err := queryRows.Next(values); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if values[0] != int64(1) {
		t.Errorf("decoded value = %#v, want int64(1)", values[0])
	}
	if err := queryRows.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = physical.Close()
}

func TestCopyInRejectsBinaryFormat(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if err := peer.WriteMessage(wire.MsgCopyInResponse, wire.EncodeCopyInResponse(1, []int16{1, 1})); err != nil {
			peer.Errorf("CopyInResponse: %v", err)
			return
		}
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := physical.PrepareContext(context.Background(), CopyIn("t", "a", "b")); !errors.Is(err, errCopyBinaryFormat) {
		t.Fatalf("Prepare error = %v, want %v", err, errCopyBinaryFormat)
	}
	if physical.netConn != nil {
		t.Error("connection was not retired after a binary COPY response")
	}
}

func TestCopyInServerErrorRetires(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 1) {
			return
		}
		// The client completes the stream normally; the server then rejects the
		// copied rows as a whole.
		if _, _, _, err := readCopyStream(peer); err != nil {
			peer.Errorf("copy stream: %v", err)
			return
		}
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "22P02"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "invalid input syntax"},
		)
		_ = peer.ReadyForQuery(wire.StatusFailedTransaction)
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), CopyIn("t", "a"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	ci := prepared.(*copyIn)
	if _, err := ci.Exec([]driver.Value{"1"}); err != nil {
		t.Fatalf("Exec row: %v", err)
	}
	var serverErr *Error
	if err := ci.Close(); !errors.As(err, &serverErr) || serverErr.Code != "22P02" {
		t.Fatalf("Close error = %v, want SQLSTATE 22P02", err)
	}
	if physical.netConn != nil {
		t.Error("connection was not retired after a server error during COPY")
	}
}

func TestCopyInMidStreamServerError(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 1) {
			return
		}
		// Fail the stream before the client finishes sending.
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "23505"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: testDuplicateKey},
		)
		_ = peer.ReadyForQuery(wire.StatusFailedTransaction)
		drainUntilTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), CopyIn("t", "a"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	ci := prepared.(*copyIn)
	_, _ = ci.Exec([]driver.Value{"1"})
	var serverErr *Error
	if err := ci.Close(); !errors.As(err, &serverErr) || serverErr.Code != "23505" {
		t.Fatalf("Close error = %v, want SQLSTATE 23505", err)
	}
	if physical.netConn != nil {
		t.Error("connection was not retired after a mid-stream server error")
	}
}

func TestCopyInRejectsIncompleteCompletion(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 1) {
			return
		}
		if _, _, _, err := readCopyStream(peer); err != nil {
			peer.Errorf("copy stream: %v", err)
			return
		}
		// A terminal ReadyForQuery without CommandComplete is a protocol error.
		_ = peer.ReadyForQuery(wire.StatusInTransaction)
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), CopyIn("t", "a"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	ci := prepared.(*copyIn)
	if err := ci.Close(); err == nil || !strings.Contains(err.Error(), "without a completion tag") {
		t.Fatalf("Close error = %v, want a missing-completion error", err)
	}
	if physical.netConn != nil {
		t.Error("connection was not retired after an incomplete completion")
	}
}

func TestCopyInFlushesLargeBatches(t *testing.T) {
	payload := strings.Repeat("x", 30<<10)
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 1) {
			return
		}
		data, chunks, failed, err := readCopyStream(peer)
		if err != nil {
			peer.Errorf("copy stream: %v", err)
			return
		}
		if failed != "" {
			peer.Errorf("copy failed: %q", failed)
			return
		}
		if chunks < 2 {
			peer.Errorf("copy arrived in %d chunks, want a mid-stream flush", chunks)
		}
		if want := strings.Repeat(payload+"\n", 4); data != want {
			peer.Errorf("copy data has %d bytes, want %d", len(data), len(want))
		}
		_ = peer.CommandComplete("COPY 4")
		_ = peer.ReadyForQuery(wire.StatusInTransaction)
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.CommandComplete("COMMIT")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	tx, err := physical.BeginTx(context.Background(), driver.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), CopyIn("t", "a"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	ci := prepared.(*copyIn)
	for i := 0; i < 4; i++ {
		if _, err := ci.Exec([]driver.Value{payload}); err != nil {
			t.Fatalf("Exec row %d: %v", i, err)
		}
	}
	if _, err := ci.Exec(nil); err != nil {
		t.Fatalf("Exec finish: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestCopyInCommandAbortsStream(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 1) {
			return
		}
		if !finishCopyStream(peer, "") {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusFailedTransaction)
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.CommandComplete("ROLLBACK")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	tx, err := physical.BeginTx(context.Background(), driver.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), CopyIn("t", "a"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	ci := prepared.(*copyIn)
	if _, err := ci.Exec([]driver.Value{"1"}); err != nil {
		t.Fatalf("Exec row: %v", err)
	}
	if _, err := physical.ExecContext(context.Background(), "SELECT 1", nil); err != nil {
		t.Fatalf("Exec during COPY: %v", err)
	}
	if err := ci.Close(); err == nil || !strings.Contains(err.Error(), "COPY") {
		t.Fatalf("Close error = %v, want the aborted COPY error", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}

func TestCopyInRowContextTimesOut(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 1) {
			return
		}
		// Ignore the rest of the stream so only the row context can interrupt.
		_, _, _ = peer.ReadMessage()
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), CopyIn("t", "a"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	ci := prepared.(*copyIn)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := ci.ExecContext(ctx, nil); err == nil {
		t.Fatal("Exec finish succeeded, want a timeout")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Exec finish took %v, want the row context to bound the wait", elapsed)
	}
	if physical.netConn != nil {
		t.Error("connection was not retired after a stalled COPY")
	}
}

func TestCopyInViaDatabaseSQL(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 2) {
			return
		}
		if !finishCopyStream(peer, "COPY 2") {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.CommandComplete("COMMIT")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	db := sql.OpenDB(&Connector{cfg: peerConfig(t, srv, "")})
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	statement, err := tx.Prepare(CopyIn("t", "a", "b"))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := statement.Exec("one", 1); err != nil {
		t.Fatalf("Exec row 1: %v", err)
	}
	if _, err := statement.Exec("two", 2); err != nil {
		t.Fatalf("Exec row 2: %v", err)
	}
	result, err := statement.Exec()
	if err != nil {
		t.Fatalf("Exec finish: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 2 {
		t.Errorf("RowsAffected = %d, %v, want 2", affected, err)
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("Stmt.Close: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestCopyInRollbackAfterBadArgument(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 1) {
			return
		}
		if !finishCopyStream(peer, "COPY 0") {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.CommandComplete("ROLLBACK")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	db := sql.OpenDB(&Connector{cfg: peerConfig(t, srv, "")})
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	statement, err := tx.Prepare(CopyIn("t", "a"))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := statement.Exec(make(chan int)); err == nil {
		t.Fatal("Exec with an unsupported argument succeeded")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback after a rejected row: %v", err)
	}
}

func TestCopyInEncodingErrorAbortsStream(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !peerBegin(peer) {
			return
		}
		if !peerCopyIn(peer, 1) {
			return
		}
		_, _, failed, err := readCopyStream(peer)
		if err != nil {
			peer.Errorf("copy stream: %v", err)
			return
		}
		if !strings.Contains(failed, "unsupported parameter type") {
			peer.Errorf("CopyFail reason = %q", failed)
		}
		_ = peer.ErrorResponse(
			wire.ErrorField{Code: wire.ErrorSeverity, Value: testSeverity},
			wire.ErrorField{Code: wire.ErrorCode, Value: "57014"},
			wire.ErrorField{Code: wire.ErrorMessage, Value: "COPY aborted"},
		)
		_ = peer.ReadyForQuery(wire.StatusFailedTransaction)
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.CommandComplete("ROLLBACK")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	tx, err := physical.BeginTx(context.Background(), driver.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	prepared, err := physical.PrepareContext(context.Background(), CopyIn("t", "a"))
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	ci := prepared.(*copyIn)
	if _, err := ci.Exec([]driver.Value{struct{}{}}); err == nil ||
		!strings.Contains(err.Error(), "unsupported parameter type") {
		t.Fatalf("Exec error = %v, want an encoding error", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback after aborted COPY: %v", err)
	}
}
