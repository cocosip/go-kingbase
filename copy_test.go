package kingbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
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

func TestCopyInRejectsCopyOut(t *testing.T) {
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
	if _, err := physical.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := physical.PrepareContext(context.Background(), "COPY t TO STDOUT"); !errors.Is(err, errCopyOutNotSupported) {
		t.Fatalf("Prepare error = %v, want %v", err, errCopyOutNotSupported)
	}
	if physical.netConn != nil {
		t.Error("connection was not retired after a COPY TO response")
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
		"COPY t TO STDOUT",
	} {
		if _, err := physical.ExecContext(context.Background(), query, nil); !errors.Is(err, errCopyThroughExec) {
			t.Fatalf("ExecContext(%q) error = %v, want %v", query, err, errCopyThroughExec)
		}
		if _, err := physical.QueryContext(context.Background(), query, nil); !errors.Is(err, errCopyThroughExec) {
			t.Fatalf("QueryContext(%q) error = %v, want %v", query, err, errCopyThroughExec)
		}
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
