package kingbase

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestParseGeneratedInsertAcceptsSafeForms(t *testing.T) {
	cases := []struct {
		name  string
		query string
		table string
	}{
		{"simple", "INSERT INTO t VALUES (1)", "t"},
		{"qualified", "insert into public.t (a, b) values ($1, $2)", "public.t"},
		{"quoted", `INSERT INTO "S"."T" DEFAULT VALUES`, `"S"."T"`},
		{"nested tuple", `INSERT INTO t VALUES ((SELECT 1))`, "t"},
		{"semicolon", `INSERT INTO t VALUES (1);`, "t"},
		{"trailing comment", `INSERT INTO t VALUES (1) -- note`, "t"},
		{"keyword in string", `INSERT INTO t VALUES ('RETURNING id')`, "t"},
		{"plain trailing backslash", `INSERT INTO t VALUES ('\')`, "t"},
		{"escape string quote", `INSERT INTO t VALUES (E'a\'b')`, "t"},
		{"unicode escape backslash", `INSERT INTO t VALUES (U&'C:\' UESCAPE '!')`, "t"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			target, insert, err := parseGeneratedInsert(testCase.query)
			if err != nil || !insert {
				t.Fatalf("parseGeneratedInsert(%q) = %v/%v, want an INSERT", testCase.query, insert, err)
			}
			if got := target.regclass(); got != testCase.table {
				t.Errorf("regclass = %q, want %q", got, testCase.table)
			}
		})
	}
}

func TestParseGeneratedInsertRejectsUnsafeForms(t *testing.T) {
	cases := []string{
		`INSERT INTO t VALUES (1), (2)`,
		`INSERT INTO t VALUES (1) RETURNING id`,
		`INSERT INTO t VALUES (1) ON CONFLICT DO NOTHING`,
		`INSERT INTO t SELECT 1`,
		`WITH x AS (SELECT 1) INSERT INTO t VALUES (1)`,
		`INSERT INTO t VALUES (1); SELECT 1`,
		`INSERT t VALUES (1)`,
		`INSERT INTO a.b.c VALUES (1)`,
		`INSERT INTO t OVERRIDING SYSTEM VALUE VALUES (1)`,
		`INSERT INTO t VALUES ('\'), (2)`,
		`INSERT INTO t VALUES ('\') ON CONFLICT DO NOTHING`,
		`INSERT INTO t VALUES ('unterminated)`,
	}
	for _, query := range cases {
		t.Run(query, func(t *testing.T) {
			if _, _, err := parseGeneratedInsert(query); err == nil {
				t.Errorf("parseGeneratedInsert(%q) succeeded, want an unsupported-form error", query)
			}
		})
	}
}

func TestParseGeneratedInsertIgnoresOtherStatements(t *testing.T) {
	for _, query := range []string{
		"SELECT 1",
		"UPDATE t SET a = 1",
		"WITH x AS (SELECT 1) SELECT * FROM x",
	} {
		if _, insert, err := parseGeneratedInsert(query); err != nil || insert {
			t.Errorf("parseGeneratedInsert(%q) = %v/%v, want a non-INSERT", query, insert, err)
		}
	}
	target, insert, err := parseGeneratedInsert("INSERT INTO Ä VALUES (1)")
	if err != nil || !insert || target.regclass() != "Ä" {
		t.Errorf("non-ASCII unquoted relation = %q/%v/%v, want Ä", target.regclass(), insert, err)
	}
	target, insert, err = parseGeneratedInsert(`INSERT INTO t VALUES (1) /* trailing */`)
	if err != nil || !insert {
		t.Fatalf("comment form = %v/%v, want an INSERT", insert, err)
	}
	rewritten := `INSERT INTO t VALUES (1) RETURNING "id" /* trailing */`
	got := `INSERT INTO t VALUES (1) /* trailing */`
	at := target.insertAt
	if at <= 0 || at >= len(got) {
		t.Fatalf("insertAt = %d, want the end of the VALUES group", at)
	}
	if want := got[:at] + ` RETURNING "id"` + got[at:]; want != rewritten {
		t.Errorf("rewrite = %q, want %q", want, rewritten)
	}
}

// generatedMetadataColumns is the five-column shape of the resolver query.
func generatedMetadataColumns() []wire.ColumnDescription {
	return []wire.ColumnDescription{
		{Name: "attname", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		{Name: "atttypid", TypeOID: 26, TypeLen: 4, TypeMod: -1, Format: 0},
		{Name: "attidentity", TypeOID: 18, TypeLen: 1, TypeMod: -1, Format: 0},
		{Name: "attgenerated", TypeOID: 18, TypeLen: 1, TypeMod: -1, Format: 0},
		{Name: "default", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
	}
}

// serveGeneratedMetadata answers the generated-column resolver query.
func serveGeneratedMetadata(peer *testpeer.Conn, rows [][5]string) bool {
	payload, ok := expectMessage(peer, wire.MsgQuery)
	if !ok {
		return false
	}
	statement, err := wire.DecodeQuery(payload)
	if err != nil || !strings.Contains(statement, "pg_catalog.pg_attribute") ||
		!strings.Contains(statement, "to_regclass") {
		peer.Errorf("generated metadata SQL = %q err %v", statement, err)
		return false
	}
	if err := peer.RowDescription(generatedMetadataColumns()); err != nil {
		peer.Errorf("RowDescription: %v", err)
		return false
	}
	for _, row := range rows {
		if err := peer.DataRow([][]byte{[]byte(row[0]), []byte(row[1]), []byte(row[2]), []byte(row[3]), []byte(row[4])}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return false
		}
	}
	if err := peer.CommandComplete("SELECT " + strconv.Itoa(len(rows))); err != nil {
		peer.Errorf("CommandComplete: %v", err)
		return false
	}
	return peer.ReadyForQuery(wire.StatusIdle) == nil
}

func serveRelationExists(peer *testpeer.Conn, exists bool) bool {
	payload, ok := expectMessage(peer, wire.MsgQuery)
	if !ok {
		return false
	}
	statement, err := wire.DecodeQuery(payload)
	if err != nil || !strings.Contains(statement, "to_regclass") {
		peer.Errorf("relation-existence SQL = %q err %v", statement, err)
		return false
	}
	if err := peer.RowDescription([]wire.ColumnDescription{
		{Name: "exists", TypeOID: 16, TypeLen: 1, TypeMod: -1, Format: 0},
	}); err != nil {
		peer.Errorf("RowDescription: %v", err)
		return false
	}
	value := "f"
	if exists {
		value = "t"
	}
	if err := peer.DataRow([][]byte{[]byte(value)}); err != nil {
		peer.Errorf("DataRow: %v", err)
		return false
	}
	if err := peer.CommandComplete("SELECT 1"); err != nil {
		peer.Errorf("CommandComplete: %v", err)
		return false
	}
	return peer.ReadyForQuery(wire.StatusIdle) == nil
}

// sendGeneratedResult answers the rewritten INSERT with one row.
func sendGeneratedResult(peer *testpeer.Conn, value, tag string) bool {
	if err := peer.RowDescription([]wire.ColumnDescription{
		{Name: "id", TypeOID: 20, TypeLen: 8, TypeMod: -1, Format: 0},
	}); err != nil {
		peer.Errorf("RowDescription: %v", err)
		return false
	}
	if err := peer.DataRow([][]byte{[]byte(value)}); err != nil {
		peer.Errorf("DataRow: %v", err)
		return false
	}
	if err := peer.CommandComplete(tag); err != nil {
		peer.Errorf("CommandComplete: %v", err)
		return false
	}
	return peer.ReadyForQuery(wire.StatusIdle) == nil
}

// readExtendedInsert asserts the unnamed Parse/Bind/Execute/Sync of a direct
// Exec and answers ParseComplete/BindComplete.
func readExtendedInsert(peer *testpeer.Conn, wantSQL string, wantParams [][]byte, wantFormats []int16) bool {
	parsePayload, ok := expectMessage(peer, wire.MsgParse)
	if !ok {
		return false
	}
	name, statement, _, err := wire.DecodeParse(parsePayload)
	if err != nil || name != "" || statement != wantSQL {
		peer.Errorf("Parse = %q (%q), err %v, want %q", name, statement, err, wantSQL)
		return false
	}
	bindPayload, ok := expectMessage(peer, wire.MsgBind)
	if !ok {
		return false
	}
	_, _, formats, params, resultFormats, err := wire.DecodeBind(bindPayload)
	if err != nil || !slices.Equal(formats, wantFormats) || len(resultFormats) != 0 ||
		len(params) != len(wantParams) {
		peer.Errorf("Bind = %v/%v, err %v, want formats %v", formats, resultFormats, err, wantFormats)
		return false
	}
	for i, want := range wantParams {
		if !bytes.Equal(params[i], want) {
			peer.Errorf("Bind parameter %d = %q, want %q", i, params[i], want)
			return false
		}
	}
	if _, ok := expectMessage(peer, wire.MsgExecute); !ok {
		return false
	}
	if _, ok := expectMessage(peer, wire.MsgSync); !ok {
		return false
	}
	_ = peer.WriteMessage(wire.MsgParseComplete, nil)
	_ = peer.WriteMessage(wire.MsgBindComplete, nil)
	return true
}

func TestLastInsertIDTenantBeforeGeneratedColumn(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !serveGeneratedMetadata(peer, [][5]string{{"order_id", "20", "a", "", ""}}) {
			return
		}
		if !readExtendedInsert(peer,
			`INSERT INTO orders (tenant_id, name) VALUES ($1, $2) RETURNING "order_id"`,
			[][]byte{[]byte("tenant-a"), []byte("first")}, nil) {
			return
		}
		if !sendGeneratedResult(peer, "77", "INSERT 0 1") {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "get_last_insert_id=yes")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	result, err := physical.ExecContext(context.Background(),
		"INSERT INTO orders (tenant_id, name) VALUES ($1, $2)", []driver.NamedValue{
			{Ordinal: 1, Value: "tenant-a"},
			{Ordinal: 2, Value: "first"},
		})
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if id, err := result.LastInsertId(); err != nil || id != 77 {
		t.Errorf("LastInsertId = %d, %v, want 77", id, err)
	}
}

func TestLastInsertIDPlainBackslashString(t *testing.T) {
	const query = `INSERT INTO t VALUES ('\')`
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !serveGeneratedMetadata(peer, [][5]string{{"id", "20", "a", "", ""}}) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		statement, err := wire.DecodeQuery(payload)
		if err != nil || statement != query+` RETURNING "id"` {
			peer.Errorf("INSERT = %q err %v", statement, err)
			return
		}
		if !sendGeneratedResult(peer, "12", "INSERT 0 1") {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "get_last_insert_id=yes")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	result, err := physical.ExecContext(context.Background(), query, nil)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if id, err := result.LastInsertId(); err != nil || id != 12 {
		t.Errorf("LastInsertId = %d, %v, want 12", id, err)
	}
}

func TestLastInsertIDRejectsUnsafeDirectFormsBeforeSend(t *testing.T) {
	cases := []string{
		`INSERT INTO t VALUES (1) RETURNING id`,
		`INSERT INTO t VALUES (1), (2)`,
		`INSERT INTO t VALUES (1) ON CONFLICT DO NOTHING`,
		`WITH x AS (SELECT 1) INSERT INTO t VALUES (1)`,
		`INSERT INTO t VALUES ('\'), (2)`,
		`INSERT INTO t VALUES ('\') ON CONFLICT DO NOTHING`,
	}
	for _, query := range cases {
		t.Run(query, func(t *testing.T) {
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				if !startupOK(peer) {
					return
				}
				waitTerminate(peer)
			})
			physical, err := connectPeer(t, srv, "get_last_insert_id=yes")
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err = physical.ExecContext(ctx, query, nil)
			if err == nil || !errors.Is(err, ErrLastInsertIDUnavailable) {
				t.Errorf("ExecContext error = %v, want ErrLastInsertIDUnavailable", err)
			}
			_ = physical.Close()
		})
	}
}

func TestLastInsertIDDirectExec(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !serveGeneratedMetadata(peer, [][5]string{{"id", "20", "a", "", ""}}) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		statement, err := wire.DecodeQuery(payload)
		if err != nil || statement != `INSERT INTO t VALUES (1) RETURNING "id"` {
			peer.Errorf("INSERT = %q err %v", statement, err)
			return
		}
		if !sendGeneratedResult(peer, "42", "INSERT 0 1") {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "get_last_insert_id=yes")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	result, err := physical.ExecContext(context.Background(), "INSERT INTO t VALUES (1)", nil)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil || id != 42 {
		t.Errorf("LastInsertId = %d, %v, want 42", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		t.Errorf("RowsAffected = %d, %v, want 1", affected, err)
	}
}

func TestLastInsertIDDirectExecWithArgs(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !serveGeneratedMetadata(peer, [][5]string{{"id", "20", "d", "", ""}}) {
			return
		}
		if !readExtendedInsert(peer, `INSERT INTO t (a) VALUES ($1) RETURNING "id"`, [][]byte{[]byte("7")}, nil) {
			return
		}
		if !sendGeneratedResult(peer, "43", "INSERT 0 1") {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "get_last_insert_id=yes")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	result, err := physical.ExecContext(context.Background(), "INSERT INTO t (a) VALUES ($1)", []driver.NamedValue{
		{Ordinal: 1, Value: int64(7)},
	})
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if id, err := result.LastInsertId(); err != nil || id != 43 {
		t.Errorf("LastInsertId = %d, %v, want 43", id, err)
	}
}

func TestLastInsertIDOverflowPreservesRows(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !serveGeneratedMetadata(peer, [][5]string{{"id", "20", "a", "", ""}}) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		if !sendGeneratedResult(peer, "9223372036854775808", "INSERT 0 2") {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "get_last_insert_id=yes")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	result, err := physical.ExecContext(context.Background(), "INSERT INTO t VALUES (1)", nil)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 2 {
		t.Errorf("RowsAffected = %d, %v, want 2", affected, err)
	}
	if _, err := result.LastInsertId(); !errors.Is(err, ErrLastInsertIDOverflow) {
		t.Errorf("LastInsertId error = %v, want ErrLastInsertIDOverflow", err)
	}
}

func TestLastInsertIDRejectsMissingOrAmbiguousColumn(t *testing.T) {
	cases := []struct {
		name          string
		rows          [][5]string
		relationCheck bool
		relation      bool
		want          string
	}{
		{"missing column", [][5]string{{testColumnName, "25", "", "", ""}}, true, true, "no generated"},
		{"missing relation", nil, true, false, "does not exist"},
		{"ambiguous", [][5]string{{"id", "20", "a", "", ""}, {"other", "23", "d", "", ""}}, false, false, "ambiguous"},
		{"non integral", [][5]string{{testColumnName, "25", "", "s", ""}}, false, false, "no integral"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				if !startupOK(peer) {
					return
				}
				if !serveGeneratedMetadata(peer, testCase.rows) {
					return
				}
				if testCase.relationCheck && !serveRelationExists(peer, testCase.relation) {
					return
				}
				waitTerminate(peer)
			})
			physical, err := connectPeer(t, srv, "get_last_insert_id=yes")
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			_, err = physical.ExecContext(context.Background(), "INSERT INTO t VALUES (1)", nil)
			if err == nil || !errors.Is(err, ErrLastInsertIDUnavailable) || !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("ExecContext error = %v, want %q wrapped in ErrLastInsertIDUnavailable", err, testCase.want)
			}
			_ = physical.Close()
		})
	}
}

func TestLastInsertIDPartialCatalogUnavailable(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.DisableAutoCatalog()
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			return
		}
		if !serveCatalogProbe(peer, "pg_catalog.pg_type", "42703") ||
			!serveCatalogProbe(peer, "sys_catalog.sys_type", "42P01") {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		statement, err := wire.DecodeQuery(payload)
		if err != nil || !strings.Contains(statement, "typname") {
			peer.Errorf("names catalog SQL = %q err %v", statement, err)
			return
		}
		_ = peer.RowDescription([]wire.ColumnDescription{
			{Name: "oid", TypeOID: 26, TypeLen: 4, TypeMod: -1, Format: 0},
			{Name: "typname", TypeOID: 25, TypeLen: -1, TypeMod: -1, Format: 0},
		})
		_ = peer.DataRow([][]byte{[]byte("20"), []byte("int8")})
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)

		if !serveGeneratedMetadata(peer, [][5]string{{"id", "20", "a", "", ""}}) {
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "get_last_insert_id=yes")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	_, err = physical.ExecContext(context.Background(), "INSERT INTO t VALUES (1)", nil)
	if err == nil || !errors.Is(err, ErrLastInsertIDUnavailable) || !strings.Contains(err.Error(), "partial type catalog") {
		t.Errorf("ExecContext error = %v, want a partial-catalog unavailable error", err)
	}
}

func TestLastInsertIDPreparedExec(t *testing.T) {
	const original = `INSERT INTO t (a) VALUES ($1)`
	const rewritten = `INSERT INTO t (a) VALUES ($1) RETURNING "id"`
	var statementName string
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !serveGeneratedMetadata(peer, [][5]string{{"id", "20", "a", "", ""}}) {
			return
		}
		parsePayload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		name, statement, _, err := wire.DecodeParse(parsePayload)
		statementName = name
		if err != nil || statement != original {
			peer.Errorf("Parse = %q err %v, want %q", statement, err, original)
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
		_ = peer.WriteMessage(wire.MsgNoData, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)

		if !readExtendedInsert(peer, rewritten, [][]byte{[]byte("7")}, nil) {
			return
		}
		if !sendGeneratedResult(peer, "9", "INSERT 0 1") {
			return
		}

		closePayload, ok := expectMessage(peer, wire.MsgClose)
		if !ok {
			return
		}
		kind, closed, err := wire.DecodeClose(closePayload)
		if err != nil || kind != 'S' || closed != statementName {
			peer.Errorf("Close = %q/%q err %v", kind, closed, err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgCloseComplete, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	db := sql.OpenDB(&Connector{cfg: peerConfig(t, srv, "get_last_insert_id=yes")})
	defer func() { _ = db.Close() }()
	statement, err := db.PrepareContext(context.Background(), original)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	result, err := statement.ExecContext(context.Background(), 7)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if id, err := result.LastInsertId(); err != nil || id != 9 {
		t.Errorf("LastInsertId = %d, %v, want 9", id, err)
	}
	if err := statement.Close(); err != nil {
		t.Errorf("statement.Close: %v", err)
	}
}

func TestLastInsertIDPreparedExecKeepsBinaryParameters(t *testing.T) {
	const original = `INSERT INTO t (b) VALUES ($1)`
	const rewritten = `INSERT INTO t (b) VALUES ($1) RETURNING "id"`
	var statementName string
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !serveGeneratedMetadata(peer, [][5]string{{"id", "20", "a", "", ""}}) {
			return
		}
		parsePayload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		name, statement, _, err := wire.DecodeParse(parsePayload)
		statementName = name
		if err != nil || statement != original {
			peer.Errorf("Parse = %q err %v, want %q", statement, err, original)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgDescribe); !ok {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgParseComplete, nil)
		_ = peer.WriteMessage(wire.MsgParameterDescription, wire.EncodeParameterDescription([]uint32{17}))
		_ = peer.WriteMessage(wire.MsgNoData, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)

		if !readExtendedInsert(peer, rewritten, [][]byte{{0xff, 0x00, 0x41}}, []int16{1}) {
			return
		}
		if !sendGeneratedResult(peer, "9", "INSERT 0 1") {
			return
		}
		closePayload, ok := expectMessage(peer, wire.MsgClose)
		if !ok {
			return
		}
		kind, closed, err := wire.DecodeClose(closePayload)
		if err != nil || kind != 'S' || closed != statementName {
			peer.Errorf("Close = %q/%q err %v", kind, closed, err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgCloseComplete, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	db := sql.OpenDB(&Connector{cfg: peerConfig(t, srv, "get_last_insert_id=yes binary_parameters=yes")})
	defer func() { _ = db.Close() }()
	statement, err := db.PrepareContext(context.Background(), original)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	result, err := statement.ExecContext(context.Background(), []byte{0xff, 0x00, 0x41})
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if id, err := result.LastInsertId(); err != nil || id != 9 {
		t.Errorf("LastInsertId = %d, %v, want 9", id, err)
	}
	if err := statement.Close(); err != nil {
		t.Errorf("statement.Close: %v", err)
	}
}

func TestLastInsertIDPreparedQueryKeepsCallerStatement(t *testing.T) {
	const query = `INSERT INTO t (a) VALUES ($1) RETURNING id`
	var statementName string
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		parsePayload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		name, statement, _, err := wire.DecodeParse(parsePayload)
		statementName = name
		if err != nil || statement != query {
			peer.Errorf("Parse = %q err %v, want %q", statement, err, query)
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
		_ = peer.WriteMessage(wire.MsgNoData, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)

		if _, ok := expectMessage(peer, wire.MsgBind); !ok {
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
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		_ = peer.WriteMessage(wire.MsgRowDescription, wire.EncodeRowDescription([]wire.ColumnDescription{
			{Name: "id", TypeOID: 20, TypeLen: 8, TypeMod: -1, Format: 0},
		}))
		_ = peer.WriteMessage(wire.MsgDataRow, wire.EncodeDataRow([][]byte{[]byte("5")}))
		_ = peer.WriteMessage(wire.MsgCommandComplete, wire.EncodeCommandComplete("INSERT 0 1"))
		_ = peer.ReadyForQuery(wire.StatusIdle)

		closePayload, ok := expectMessage(peer, wire.MsgClose)
		if !ok {
			return
		}
		kind, closed, err := wire.DecodeClose(closePayload)
		if err != nil || kind != 'S' || closed != statementName {
			peer.Errorf("Close = %q/%q err %v", kind, closed, err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgCloseComplete, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	db := sql.OpenDB(&Connector{cfg: peerConfig(t, srv, "get_last_insert_id=yes")})
	defer func() { _ = db.Close() }()
	statement, err := db.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	rows, err := statement.QueryContext(context.Background(), 7)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	if !rows.Next() {
		t.Fatalf("QueryContext: no row: %v", rows.Err())
	}
	var id int64
	if err := rows.Scan(&id); err != nil || id != 5 {
		t.Errorf("Scan = %d, %v, want 5", id, err)
	}
	if err := rows.Close(); err != nil {
		t.Errorf("rows.Close: %v", err)
	}
	if err := statement.Close(); err != nil {
		t.Errorf("statement.Close: %v", err)
	}
}

func TestLastInsertIDPreparedReturningRejectsExec(t *testing.T) {
	const query = `INSERT INTO t (a) VALUES ($1) RETURNING id`
	var statementName string
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		parsePayload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		name, statement, _, err := wire.DecodeParse(parsePayload)
		statementName = name
		if err != nil || statement != query {
			peer.Errorf("Parse = %q err %v, want %q", statement, err, query)
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
		_ = peer.WriteMessage(wire.MsgNoData, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)

		closePayload, ok := expectMessage(peer, wire.MsgClose)
		if !ok {
			return
		}
		kind, closed, err := wire.DecodeClose(closePayload)
		if err != nil || kind != 'S' || closed != statementName {
			peer.Errorf("Close = %q/%q err %v", kind, closed, err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgCloseComplete, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	db := sql.OpenDB(&Connector{cfg: peerConfig(t, srv, "get_last_insert_id=yes")})
	defer func() { _ = db.Close() }()
	statement, err := db.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	// Exec of an existing RETURNING form is rejected, while the prepared
	// statement itself remains usable for Query.
	if _, err := statement.ExecContext(context.Background(), 7); !errors.Is(err, ErrLastInsertIDUnavailable) {
		t.Errorf("ExecContext error = %v, want ErrLastInsertIDUnavailable", err)
	}
	if err := statement.Close(); err != nil {
		t.Errorf("statement.Close: %v", err)
	}
}

func TestLastInsertIDMySQLCapturesConnectionValue(t *testing.T) {
	const insertSQL = "INSERT INTO t VALUES (1)"
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		statement, err := wire.DecodeQuery(payload)
		if err != nil || statement != insertSQL {
			peer.Errorf("Query = %q, err %v, want %q", statement, err, insertSQL)
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
		payload, ok = expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		statement, err = wire.DecodeQuery(payload)
		if err != nil || statement != "SELECT LAST_INSERT_ID()" {
			peer.Errorf("identity query = %q, err %v", statement, err)
			return
		}
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: "last_insert_id", TypeOID: 20, TypeLen: 8, TypeMod: -1, Format: 0},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("41")}); err != nil {
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
	physical, err := connectPeer(t, srv, "mode=mysql get_last_insert_id=yes")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	result, err := physical.ExecContext(context.Background(), insertSQL, nil)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if id, err := result.LastInsertId(); err != nil || id != 41 {
		t.Errorf("LastInsertId = %d, %v, want 41", id, err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Errorf("RowsAffected = %d, %v, want 1", affected, err)
	}
}

func TestExactLastInsertIDOverflow(t *testing.T) {
	if _, err := exactLastInsertID("9223372036854775808"); !errors.Is(err, ErrLastInsertIDOverflow) {
		t.Errorf("overflow error = %v, want ErrLastInsertIDOverflow", err)
	}
	if id, err := exactLastInsertID("41"); err != nil || id != 41 {
		t.Errorf("exact ID = %d, %v, want 41", id, err)
	}
	if _, err := exactLastInsertID("not-a-number"); !errors.Is(err, ErrLastInsertIDUnavailable) {
		t.Errorf("malformed error = %v, want ErrLastInsertIDUnavailable", err)
	}
}

func TestLastInsertIDMySQLPreparedExec(t *testing.T) { //nolint:gocyclo // testpeer script reads linearly
	const insertSQL = "INSERT INTO t VALUES ($1)"
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		parsePayload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		name, statement, _, err := wire.DecodeParse(parsePayload)
		if err != nil || name != "kb_stmt_0" || statement != insertSQL {
			peer.Errorf("Parse = %q (%q), err %v", name, statement, err)
			return
		}
		describePayload, ok := expectMessage(peer, wire.MsgDescribe)
		if !ok {
			return
		}
		kind, described, err := wire.DecodeDescribe(describePayload)
		if err != nil || kind != 'S' || described != name {
			peer.Errorf("Describe = %q/%q, err %v", kind, described, err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgParseComplete, nil)
		_ = peer.WriteMessage(wire.MsgParameterDescription, wire.EncodeParameterDescription([]uint32{23}))
		_ = peer.WriteMessage(wire.MsgNoData, nil)
		_ = peer.ReadyForQuery(wire.StatusIdle)

		bindPayload, ok := expectMessage(peer, wire.MsgBind)
		if !ok {
			return
		}
		_, prepared, _, params, _, err := wire.DecodeBind(bindPayload)
		if err != nil || prepared != name || len(params) != 1 {
			peer.Errorf("Bind = %q/%d, err %v", prepared, len(params), err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgExecute); !ok {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		if err := peer.CommandComplete("INSERT 0 1"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}

		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		statement, err = wire.DecodeQuery(payload)
		if err != nil || statement != "SELECT LAST_INSERT_ID()" {
			peer.Errorf("identity query = %q, err %v", statement, err)
			return
		}
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: "last_insert_id", TypeOID: 20, TypeLen: 8, TypeMod: -1, Format: 0},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("42")}); err != nil {
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
	physical, err := connectPeer(t, srv, "mode=mysql get_last_insert_id=yes")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = physical.Close() }()
	prepared, err := physical.PrepareContext(context.Background(), insertSQL)
	if err != nil {
		t.Fatalf("PrepareContext: %v", err)
	}
	execCtx, ok := prepared.(driver.StmtExecContext)
	if !ok {
		t.Fatalf("prepared statement does not implement StmtExecContext")
	}
	result, err := execCtx.ExecContext(context.Background(), []driver.NamedValue{{Ordinal: 1, Value: int64(1)}})
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if id, err := result.LastInsertId(); err != nil || id != 42 {
		t.Errorf("LastInsertId = %d, %v, want 42", id, err)
	}
}

// TestGeneratedTinyintConversion pins the generated tinyint contract measured on
// the live listeners: the MySQL form reads LAST_INSERT_ID() as a UINT8 text
// value (exact decimal to int64), while a tinyint generated column value is
// decoded through the mode tinyint codec, which applies the accepted signed
// (Oracle/MySQL) or unsigned (SQLServer) 8-bit range.
func TestGeneratedTinyintConversion(t *testing.T) {
	for _, text := range []string{"1", "127", "255", "9223372036854775807"} {
		id, err := parseExactLastInsertID(text)
		if err != nil {
			t.Fatalf("parseExactLastInsertID(%q): %v", text, err)
		}
		if got := strconv.FormatInt(id, 10); got != text {
			t.Errorf("parseExactLastInsertID(%q) = %q", text, got)
		}
	}
	if _, err := parseExactLastInsertID("9223372036854775808"); !errors.Is(err, ErrLastInsertIDOverflow) {
		t.Errorf("uint64 above MaxInt64 error = %v, want overflow", err)
	}
	for _, testCase := range []struct {
		mode   CompatibilityMode
		text   string
		want   int64
		reject bool
	}{
		{mode: ModeMySQL, text: "127", want: 127},
		{mode: ModeMySQL, text: "128", reject: true},
		{mode: ModeMySQL, text: "-128", want: -128},
		{mode: ModeSQLServer, text: "255", want: 255},
		{mode: ModeSQLServer, text: "256", reject: true},
		{mode: ModeSQLServer, text: "-1", reject: true},
	} {
		value, _, err := textValue(testCase.mode, schemaSys, pgTypeTinyint, []byte(testCase.text), nil)
		if testCase.reject {
			if !errors.Is(err, ErrValueConversion) {
				t.Errorf("%s tinyint %q error = %v, want conversion error", testCase.mode, testCase.text, err)
			}
			continue
		}
		if err != nil || value != testCase.want {
			t.Errorf("%s tinyint %q = %v, %v; want %d", testCase.mode, testCase.text, value, err, testCase.want)
		}
	}
}

// TestExplicitSequenceAccessPinsConnection pins nextval/currval to one
// database/sql connection and keeps generated-ID capture separate: the
// sequence calls use explicit caller SQL, never a MAX/last-sequence guess.
func TestExplicitSequenceAccessPinsConnection(t *testing.T) {
	const nextSQL = `SELECT nextval('orders_id_seq')`
	const currSQL = `SELECT currval('orders_id_seq')`
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		column := wire.ColumnDescription{Name: "value", TypeOID: 20, TypeLen: 8, TypeMod: -1, Format: 0}
		if !serveUserSelect(peer, nextSQL, column, []byte("42")) {
			return
		}
		if !serveUserSelect(peer, currSQL, column, []byte("42")) {
			return
		}
		payload, ok := expectMessage(peer, wire.MsgQuery)
		if !ok {
			return
		}
		query, err := wire.DecodeQuery(payload)
		if err != nil || query != "SELECT 1" {
			peer.Errorf("query = %q err %v", query, err)
			return
		}
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = connection.Close() }()

	var next, current int64
	if err := connection.QueryRowContext(ctx, nextSQL).Scan(&next); err != nil {
		t.Fatalf("nextval: %v", err)
	}
	if err := connection.QueryRowContext(ctx, currSQL).Scan(&current); err != nil {
		t.Fatalf("currval: %v", err)
	}
	if next != 42 || current != 42 {
		t.Errorf("sequence values = %d/%d, want 42/42", next, current)
	}

	result, err := connection.ExecContext(ctx, "SELECT 1")
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if _, err := result.LastInsertId(); !errors.Is(err, ErrLastInsertIDUnavailable) {
		t.Errorf("LastInsertId error = %v, want ErrLastInsertIDUnavailable", err)
	}
}
