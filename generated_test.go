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
