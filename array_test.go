package kingbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestArrayValueEncodesTextLiteral(t *testing.T) {
	one, two := int64(1), int64(2)
	cases := []struct {
		name  string
		value driver.Valuer
		want  driver.Value
	}{
		{"null", Array[int64]{}, nil},
		{"empty", Array[int64]{Valid: true}, "{}"},
		{"scalars", Array[int64]{Valid: true, Elements: []int64{one, two}}, `{"1","2"}`},
		{
			"quoting",
			Array[string]{Valid: true, Elements: []string{"a,b", `c"d`, "NULL", "", `a\b`}},
			`{"a,b","c\"d","NULL","","a\\b"}`,
		},
		{
			"null elements",
			Array[*int64]{Valid: true, Elements: []*int64{&one, nil, &two}},
			`{"1",NULL,"2"}`,
		},
		{
			"null-valuer elements",
			Array[sql.NullString]{
				Valid:    true,
				Elements: []sql.NullString{{String: "a", Valid: true}, {}, {String: "b", Valid: true}},
			},
			`{"a",NULL,"b"}`,
		},
		{
			"multidimensional",
			Array[int64]{
				Valid:      true,
				Dimensions: []ArrayDimension{{Length: 2, LowerBound: 1}, {Length: 3, LowerBound: 1}},
				Elements:   []int64{1, 2, 3, 4, 5, 6},
			},
			`{{"1","2","3"},{"4","5","6"}}`,
		},
		{
			"lower bounds",
			Array[int64]{
				Valid:      true,
				Dimensions: []ArrayDimension{{Length: 2, LowerBound: 2}},
				Elements:   []int64{10, 20},
			},
			`[2:3]={"10","20"}`,
		},
		{
			"delimiter",
			Array[string]{Valid: true, Delimiter: ';', Elements: []string{"a", "b"}},
			`{"a";"b"}`,
		},
		{
			"bytes",
			Array[[]byte]{Valid: true, Elements: [][]byte{{1, 2}}},
			`{"\\x0102"}`,
		},
		{
			"null bytes",
			Array[[]byte]{Valid: true, Elements: [][]byte{nil}},
			`{NULL}`,
		},
		{
			"time",
			Array[time.Time]{Valid: true, Elements: []time.Time{time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}},
			`{"2020-01-02 03:04:05Z"}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := testCase.value.Value()
			if err != nil {
				t.Fatalf("Value: %v", err)
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("Value = %#v, want %#v", got, testCase.want)
			}
		})
	}
}

func TestArrayValueRejectsLossyShapes(t *testing.T) {
	cases := []struct {
		name  string
		value driver.Valuer
	}{
		{"count mismatch", Array[int64]{Valid: true, Dimensions: []ArrayDimension{{2, 1}, {2, 1}}, Elements: []int64{1, 2, 3}}},
		{"zero dimension", Array[int64]{Valid: true, Dimensions: []ArrayDimension{{0, 1}}}},
		{"negative dimension", Array[int64]{Valid: true, Dimensions: []ArrayDimension{{-1, 1}}}},
		{"too many dimensions", Array[int64]{
			Valid:      true,
			Dimensions: []ArrayDimension{{1, 1}, {1, 1}, {1, 1}, {1, 1}, {1, 1}, {1, 1}, {1, 1}},
			Elements:   []int64{1},
		}},
		{"unsupported element", Array[struct{}]{Valid: true, Elements: []struct{}{{}}}},
		{"invalid delimiter", Array[int64]{Valid: true, Delimiter: '{', Elements: []int64{1}}},
		{"inferred size cap", Array[int64]{Valid: true, Elements: make([]int64, maxArrayElements+1)}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := testCase.value.Value(); err == nil {
				t.Error("Value succeeded, want an error")
			}
		})
	}
}

func TestArrayScanParsesTextLiteral(t *testing.T) {
	var got Array[int64]
	if err := got.Scan(`{1,2,3}`); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	wantDimensions := []ArrayDimension{{Length: 3, LowerBound: 1}}
	if !got.Valid || !reflect.DeepEqual(got.Elements, []int64{1, 2, 3}) || !reflect.DeepEqual(got.Dimensions, wantDimensions) {
		t.Errorf("Array = %+v, want three int64 elements", got)
	}

	var empty Array[int64]
	if err := empty.Scan(`{}`); err != nil {
		t.Fatalf("Scan(empty): %v", err)
	}
	if !empty.Valid || len(empty.Elements) != 0 || len(empty.Dimensions) != 0 {
		t.Errorf("Array = %+v, want a valid empty array", empty)
	}

	var null Array[int64]
	if err := null.Scan(nil); err != nil {
		t.Fatalf("Scan(NULL): %v", err)
	}
	if null.Valid || null.Elements != nil {
		t.Errorf("Array = %+v, want SQL NULL", null)
	}

	var bytesSource Array[int64]
	if err := bytesSource.Scan([]byte(`{7}`)); err != nil {
		t.Fatalf("Scan(bytes): %v", err)
	}
	if !reflect.DeepEqual(bytesSource.Elements, []int64{7}) {
		t.Errorf("Elements = %#v, want [7]", bytesSource.Elements)
	}
}

func TestArrayScanQuotedElements(t *testing.T) {
	var got Array[string]
	if err := got.Scan(`{"a,b","c\"d","NULL","","a\\b"}`); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []string{"a,b", `c"d`, "NULL", "", `a\b`}
	if !reflect.DeepEqual(got.Elements, want) {
		t.Errorf("Elements = %#v, want %#v", got.Elements, want)
	}

	delimited := Array[string]{Delimiter: ';'}
	if err := delimited.Scan(`{"a";"b,c"}`); err != nil {
		t.Fatalf("Scan(semicolon): %v", err)
	}
	if !reflect.DeepEqual(delimited.Elements, []string{"a", "b,c"}) {
		t.Errorf("Elements = %#v, want a semicolon-delimited pair", delimited.Elements)
	}
}

func TestArrayScanNullElements(t *testing.T) {
	var got Array[*int64]
	if err := got.Scan(`{1,NULL,3}`); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Elements) != 3 || got.Elements[0] == nil || got.Elements[1] != nil || got.Elements[2] == nil {
		t.Errorf("Elements = %#v, want a value, NULL and a value", got.Elements)
	}
	if *got.Elements[0] != 1 || *got.Elements[2] != 3 {
		t.Errorf("Elements = %d/%d, want 1/3", *got.Elements[0], *got.Elements[2])
	}

	var scannerElements Array[sql.NullString]
	if err := scannerElements.Scan(`{a,NULL}`); err != nil {
		t.Fatalf("Scan(NullString): %v", err)
	}
	want := []sql.NullString{{String: "a", Valid: true}, {}}
	if !reflect.DeepEqual(scannerElements.Elements, want) {
		t.Errorf("Elements = %#v, want %#v", scannerElements.Elements, want)
	}

	var anyElements Array[any]
	if err := anyElements.Scan(`{1,NULL,t}`); err != nil {
		t.Fatalf("Scan(any): %v", err)
	}
	wantAny := []any{"1", nil, "t"}
	if !reflect.DeepEqual(anyElements.Elements, wantAny) {
		t.Errorf("Elements = %#v, want %#v", anyElements.Elements, wantAny)
	}
}

func TestArrayScanPreservesShape(t *testing.T) {
	var got Array[int64]
	if err := got.Scan(`{{1,2,3},{4,5,6}}`); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []ArrayDimension{{Length: 2, LowerBound: 1}, {Length: 3, LowerBound: 1}}
	if !reflect.DeepEqual(got.Dimensions, want) || !reflect.DeepEqual(got.Elements, []int64{1, 2, 3, 4, 5, 6}) {
		t.Errorf("Array = %+v, want a 2x3 shape", got)
	}

	var lowered Array[int64]
	if err := lowered.Scan(`[0:1]={10,20}`); err != nil {
		t.Fatalf("Scan(lower bounds): %v", err)
	}
	wantLowered := []ArrayDimension{{Length: 2, LowerBound: 0}}
	if !reflect.DeepEqual(lowered.Dimensions, wantLowered) {
		t.Errorf("Dimensions = %#v, want %#v", lowered.Dimensions, wantLowered)
	}

	var deep Array[int64]
	if err := deep.Scan(`{{{{{{1}}}}}}`); err != nil {
		t.Fatalf("Scan(six dimensions): %v", err)
	}
	wantDeep := []ArrayDimension{{1, 1}, {1, 1}, {1, 1}, {1, 1}, {1, 1}, {1, 1}}
	if !reflect.DeepEqual(deep.Dimensions, wantDeep) {
		t.Errorf("Dimensions = %#v, want six dimensions", deep.Dimensions)
	}
}

func TestArrayByteaElementsRoundTrip(t *testing.T) {
	var got Array[[]byte]
	if err := got.Scan(`{"\\x0102",NULL,""}`); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got.Elements) != 3 || !reflect.DeepEqual(got.Elements[0], []byte{1, 2}) ||
		got.Elements[1] != nil || len(got.Elements[2]) != 0 {
		t.Fatalf("Elements = %#v, want decoded bytes, NULL and empty bytea", got.Elements)
	}
	text, err := got.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if text != `{"\\x0102",NULL,"\\x"}` {
		t.Errorf("Value = %q, want the bytea round trip", text)
	}
}

func TestArrayBareElementEscapes(t *testing.T) {
	var escaped Array[string]
	if err := escaped.Scan(`{a\,b,c\\d,N\ULL}`); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	wantEscaped := []string{"a,b", `c\d`, "NULL"}
	if !reflect.DeepEqual(escaped.Elements, wantEscaped) {
		t.Errorf("Elements = %#v, want %#v", escaped.Elements, wantEscaped)
	}

	var quoted Array[string]
	if err := quoted.Scan(`{"a\b"}`); err != nil {
		t.Fatalf("Scan(quoted): %v", err)
	}
	if !reflect.DeepEqual(quoted.Elements, []string{"ab"}) {
		t.Errorf("Elements = %#v, want PostgreSQL's escaped byte", quoted.Elements)
	}

	var spaces Array[string]
	if err := spaces.Scan("{\u00a0x,\u3000y}"); err != nil {
		t.Fatalf("Scan(unicode spaces): %v", err)
	}
	if !reflect.DeepEqual(spaces.Elements, []string{"\u00a0x", "\u3000y"}) {
		t.Errorf("Elements = %#v, want non-ASCII spaces preserved", spaces.Elements)
	}

	var trimmed Array[int64]
	if err := trimmed.Scan(`{ 1 , 2 }`); err != nil {
		t.Fatalf("Scan(ASCII spaces): %v", err)
	}
	if !reflect.DeepEqual(trimmed.Elements, []int64{1, 2}) {
		t.Errorf("Elements = %#v, want ASCII whitespace trimmed", trimmed.Elements)
	}
}

func TestArrayScanTemporalElements(t *testing.T) {
	var got Array[time.Time]
	if err := got.Scan(`{"2020-01-02 03:04:05"}`); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if len(got.Elements) != 1 || !got.Elements[0].Equal(want) {
		t.Errorf("Elements = %v, want %v", got.Elements, want)
	}
}

func TestArrayScanRejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{"ragged", `{{1,2},{3}}`},
		{"mixed nesting", `{1,{2}}`},
		{"trailing delimiter", `{1,}`},
		{"empty bare element", `{,}`},
		{"unterminated quote", `{"a}`},
		{"unterminated brace", `{1,2`},
		{"declared mismatch", `[1:3]={1,2}`},
		{"dimensions without body", `[1:2]`},
		{"trailing data", `{1}extra`},
		{"empty dimension", `{{},{}}`},
		{"too deep", strings.Repeat(`{`, 7) + strings.Repeat(`}`, 7)},
		{"binary layout", "\x00\x00\x00\x01"},
		{"wrong delimiter", `{"a";"b"}`},
		{"trailing backslash", `{a\}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var got Array[int64]
			if err := got.Scan(testCase.source); err == nil {
				t.Errorf("Scan(%q) succeeded, want an error", testCase.source)
			}
		})
	}
	t.Run("null element into scalar", func(t *testing.T) {
		var got Array[int64]
		if err := got.Scan(`{1,NULL}`); err == nil {
			t.Error("Scan succeeded, want a NULL conversion error")
		}
	})
	t.Run("invalid integer", func(t *testing.T) {
		var got Array[int64]
		if err := got.Scan(`{x}`); err == nil {
			t.Error("Scan succeeded, want an integer conversion error")
		}
	})
	t.Run("state unchanged on error", func(t *testing.T) {
		got := Array[int64]{Valid: true, Elements: []int64{9}}
		if err := got.Scan(`{1,oops}`); err == nil {
			t.Fatal("Scan succeeded, want a conversion error")
		}
		if !got.Valid || !reflect.DeepEqual(got.Elements, []int64{9}) {
			t.Errorf("Array = %+v, want the previous value preserved", got)
		}
	})
	t.Run("invalid boolean", func(t *testing.T) {
		var got Array[bool]
		if err := got.Scan(`{maybe}`); err == nil {
			t.Error("Scan succeeded, want a boolean conversion error")
		}
	})
	t.Run("unsupported element type", func(t *testing.T) {
		var got Array[struct{}]
		if err := got.Scan(`{1}`); err == nil {
			t.Error("Scan succeeded, want an unsupported element error")
		}
	})
	t.Run("invalid delimiter", func(t *testing.T) {
		got := Array[int64]{Delimiter: '{'}
		if err := got.Scan(`{1}`); err == nil {
			t.Error("Scan succeeded, want an invalid delimiter error")
		}
	})
}

func TestArrayRoundTripPreservesShape(t *testing.T) {
	original := Array[int64]{
		Valid:      true,
		Delimiter:  ';',
		Dimensions: []ArrayDimension{{Length: 2, LowerBound: 2}, {Length: 3, LowerBound: 0}},
		Elements:   []int64{1, 2, 3, 4, 5, 6},
	}
	text, err := original.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	var scanned Array[int64]
	scanned.Delimiter = original.Delimiter
	if err := scanned.Scan(text); err != nil {
		t.Fatalf("Scan(%q): %v", text, err)
	}
	if !scanned.Valid || !reflect.DeepEqual(scanned.Dimensions, original.Dimensions) ||
		!reflect.DeepEqual(scanned.Elements, original.Elements) {
		t.Errorf("round trip = %+v, want %+v", scanned, original)
	}
}

// TestArrayPreparedResultFormatContract pins both prepared-statement result
// modes: the default binary response cannot be parsed as text, while the
// documented text option scans normally.
func TestArrayPreparedResultFormatContract(t *testing.T) {
	binaryPayload := []byte{
		0, 0, 0, 1, // one dimension
		0, 0, 0, 0, // no NULLs
		0, 0, 0, 23, // int4 elements
		0, 0, 0, 2, 0, 0, 0, 1, // two elements, lower bound 1
		0, 0, 0, 4, 0, 0, 0, 1,
		0, 0, 0, 4, 0, 0, 0, 2,
	}
	cases := []struct {
		name    string
		config  string
		format  int16
		payload []byte
		wantErr string
	}{
		{"binary rejected", "", 1, binaryPayload, "binary array values"},
		{"text option", "disable_prepared_binary_result=true", 0, []byte(`{1,2}`), ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var statementName string
			srv := testpeer.Start(t, func(peer *testpeer.Conn) {
				servePreparedArrayResult(peer, &statementName, testCase.format, testCase.payload)
			})

			db := sql.OpenDB(&Connector{cfg: peerConfig(t, srv, testCase.config)})
			ctx := context.Background()
			statement, err := db.PrepareContext(ctx, "SELECT $1")
			if err != nil {
				t.Fatalf("PrepareContext: %v", err)
			}
			var got Array[int64]
			err = statement.QueryRowContext(ctx, Array[int64]{Valid: true, Elements: []int64{1, 2}}).Scan(&got)
			if testCase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("Scan error = %v, want %q", err, testCase.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Scan: %v", err)
			} else if !reflect.DeepEqual(got.Elements, []int64{1, 2}) {
				t.Errorf("Elements = %#v, want [1 2]", got.Elements)
			}
			if err := statement.Close(); err != nil {
				t.Errorf("statement.Close: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Errorf("db.Close: %v", err)
			}
		})
	}
}

// servePreparedArrayResult scripts one prepared array select, its configured
// result format, the statement Close and the final Terminate.
func servePreparedArrayResult(peer *testpeer.Conn, statementName *string, format int16, payload []byte) {
	if !startupOK(peer) {
		return
	}
	parsePayload, ok := expectMessage(peer, wire.MsgParse)
	if !ok {
		return
	}
	name, statement, _, err := wire.DecodeParse(parsePayload)
	*statementName = name
	if err != nil || statement != "SELECT $1" {
		peer.Errorf("Parse = %q err %v, want SELECT $1", statement, err)
		return
	}
	if _, ok := expectMessage(peer, wire.MsgDescribe); !ok {
		return
	}
	if _, ok := expectMessage(peer, wire.MsgSync); !ok {
		return
	}
	_ = peer.WriteMessage(wire.MsgParseComplete, nil)
	_ = peer.WriteMessage(wire.MsgParameterDescription, wire.EncodeParameterDescription([]uint32{1007}))
	_ = peer.WriteMessage(wire.MsgNoData, nil)
	_ = peer.ReadyForQuery(wire.StatusIdle)

	bindPayload, ok := expectMessage(peer, wire.MsgBind)
	if !ok {
		return
	}
	_, bound, paramFormats, params, resultFormats, err := wire.DecodeBind(bindPayload)
	if err != nil || bound != name || len(paramFormats) != 0 ||
		len(params) != 1 || string(params[0]) != `{"1","2"}` {
		peer.Errorf("Bind = %q/%q formats %v params %q err %v", bound, name, paramFormats, params, err)
		return
	}
	if format == 1 {
		if len(resultFormats) != 1 || resultFormats[0] != 1 {
			peer.Errorf("Bind result formats = %v, want binary", resultFormats)
			return
		}
	} else if len(resultFormats) != 0 {
		peer.Errorf("Bind result formats = %v, want text", resultFormats)
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
	_ = peer.RowDescription([]wire.ColumnDescription{
		{Name: "values", TypeOID: 1007, TypeLen: -1, TypeMod: -1, Format: format},
	})
	_ = peer.DataRow([][]byte{payload})
	_ = peer.CommandComplete("SELECT 1")
	_ = peer.ReadyForQuery(wire.StatusIdle)

	closePayload, ok := expectMessage(peer, wire.MsgClose)
	if !ok {
		return
	}
	kind, closed, err := wire.DecodeClose(closePayload)
	if err != nil || kind != 'S' || closed != name {
		peer.Errorf("Close = %q/%q err %v", kind, closed, err)
		return
	}
	if _, ok := expectMessage(peer, wire.MsgSync); !ok {
		return
	}
	_ = peer.WriteMessage(wire.MsgCloseComplete, nil)
	_ = peer.ReadyForQuery(wire.StatusIdle)
	waitTerminate(peer)
}

// TestArrayThroughDatabaseSQL pins the Valuer/Scanner path: database/sql
// converts the argument once, the Bind carries a text array literal, and the
// opaque array result scans through Array while metadata stays honest.
func TestArrayThroughDatabaseSQL(t *testing.T) {
	const wantParam = `{"1","2"}`
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		parsePayload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		_, statement, _, err := wire.DecodeParse(parsePayload)
		if err != nil || statement != "SELECT $1" {
			peer.Errorf("Parse = %q err %v, want SELECT $1", statement, err)
			return
		}
		bindPayload, ok := expectMessage(peer, wire.MsgBind)
		if !ok {
			return
		}
		_, _, formats, params, resultFormats, err := wire.DecodeBind(bindPayload)
		if err != nil || len(formats) != 0 || len(resultFormats) != 0 {
			peer.Errorf("Bind formats = %v/%v err %v, want text", formats, resultFormats, err)
			return
		}
		if len(params) != 1 || string(params[0]) != wantParam {
			peer.Errorf("Bind parameters = %q, want %q", params, wantParam)
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
		_ = peer.RowDescription([]wire.ColumnDescription{
			{Name: "values", TypeOID: 70010, TypeLen: -1, TypeMod: -1, Format: 0},
		})
		_ = peer.DataRow([][]byte{[]byte(`{1,2}`)})
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})

	db := sql.OpenDB(&Connector{cfg: peerConfig(t, srv, "")})
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), "SELECT $1", Array[int64]{Valid: true, Elements: []int64{1, 2}})
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = rows.Close() }()
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		t.Fatalf("ColumnTypes: %v", err)
	}
	if name := columnTypes[0].DatabaseTypeName(); name != "_int4" {
		t.Errorf("DatabaseTypeName = %q, want _int4", name)
	}
	var got Array[int64]
	if !rows.Next() {
		t.Fatalf("Next: %v", rows.Err())
	}
	if err := rows.Scan(&got); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !reflect.DeepEqual(got.Elements, []int64{1, 2}) {
		t.Errorf("scanned elements = %#v, want [1 2]", got.Elements)
	}
}
