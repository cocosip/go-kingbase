package kingbase

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

// modeTypeOID is the scripted catalog OID used by one mode-codec case.
type modeTypeCase struct {
	mode     CompatibilityMode
	oid      uint32
	schema   string
	name     string
	length   int16
	format   int16
	row      []byte
	want     driver.Value
	wantType reflect.Type
	wantErr  bool
}

// serveModeTypeResult answers the catalog query with the scripted rows plus one
// mode type, then one result column in the requested format.
func serveModeTypeResult(t *testing.T, mode CompatibilityMode, extra [][][]byte,
	columns []wire.ColumnDescription, row []byte) *conn {
	t.Helper()
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		peer.DisableAutoCatalog()
		if _, err := peer.ReadStartup(); err != nil {
			return
		}
		if err := peer.StartupOK(nil); err != nil {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.RowDescription(testpeer.CatalogColumns())
		rows := append(testpeer.CatalogRows(), extra...)
		for _, catalogRow := range rows {
			_ = peer.DataRow(catalogRow)
		}
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		_ = peer.RowDescription(columns)
		if row != nil {
			_ = peer.DataRow([][]byte{row})
		}
		_ = peer.CommandComplete("SELECT 1")
		_ = peer.ReadyForQuery(wire.StatusIdle)
		waitTerminate(peer)
	})
	cfg := peerConfig(t, srv, "")
	cfg.CompatibilityMode = mode
	physical, err := (&Connector{cfg: cfg}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = physical.Close() })
	return physical.(*conn)
}

// runModeTypeCase decodes one scripted value and checks the scan type.
func runModeTypeCase(t *testing.T, testCase modeTypeCase) {
	t.Helper()
	extra := [][][]byte{userCatalogRow(testCase.oid, testCase.schema, testCase.name, 'b', testCase.length, 0)}
	columns := []wire.ColumnDescription{
		{Name: "v", TypeOID: testCase.oid, TypeLen: testCase.length, TypeMod: -1, Format: testCase.format},
	}
	connection := serveModeTypeResult(t, testCase.mode, extra, columns, testCase.row)
	result, err := connection.QueryContext(context.Background(), "SELECT v", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = result.Close() }()
	typed, ok := result.(*rows)
	if !ok {
		t.Fatalf("QueryContext returned %T, want *rows", result)
	}
	if got := typed.ColumnTypeScanType(0); got != testCase.wantType {
		t.Errorf("%s.%s scan type = %v, want %v", testCase.schema, testCase.name, got, testCase.wantType)
	}
	dest := make([]driver.Value, 1)
	err = typed.Next(dest)
	if testCase.wantErr {
		if !errors.Is(err, ErrValueConversion) {
			t.Fatalf("%s.%s error = %v, want ErrValueConversion", testCase.schema, testCase.name, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("%s.%s Next: %v", testCase.schema, testCase.name, err)
	}
	if !reflect.DeepEqual(dest[0], testCase.want) {
		t.Errorf("%s.%s value = %#v (%T), want %#v (%T)",
			testCase.schema, testCase.name, dest[0], dest[0], testCase.want, testCase.want)
	}
}

func TestModeTypeCodecs(t *testing.T) {
	when := func(year int, month time.Month, day, hour, minute, second int) time.Time {
		return time.Date(year, month, day, hour, minute, second, 0, time.UTC)
	}
	tiny := reflect.TypeOf(int64(0))
	text := reflect.TypeOf("")
	bytesType := reflect.TypeOf([]byte(nil))
	boolean := reflect.TypeOf(true)
	instant := reflect.TypeOf(time.Time{})
	cases := []modeTypeCase{
		{mode: ModeMySQL, oid: 75001, schema: schemaSys, name: pgTypeTinyint, length: 1, row: []byte("5"), want: int64(5), wantType: tiny},
		{mode: ModeMySQL, oid: 75002, schema: schemaSys, name: pgTypeTinyint, length: 1, row: []byte("128"), wantErr: true, wantType: tiny},
		{mode: ModeSQLServer, oid: 75003, schema: schemaSys, name: pgTypeTinyint, length: 1, row: []byte("200"), want: int64(200), wantType: tiny},
		{mode: ModeSQLServer, oid: 75004, schema: schemaSys, name: pgTypeTinyint, length: 1, row: []byte("-1"), wantErr: true, wantType: tiny},
		{mode: ModeMySQL, oid: 75005, schema: schemaSys, name: pgTypeTinyint, length: 1, format: 1, row: []byte{0xff}, want: int64(-1), wantType: tiny},
		{mode: ModeSQLServer, oid: 75006, schema: schemaSys, name: pgTypeTinyint, length: 1, format: 1, row: []byte{0xc8}, want: int64(200), wantType: tiny},
		{mode: ModeMySQL, oid: 75007, schema: schemaSys, name: testTypeUint4, length: 4, row: []byte("4294967295"), want: int64(4294967295), wantType: tiny},
		{mode: ModeMySQL, oid: 75008, schema: schemaSys, name: testTypeUint4, length: 4, row: []byte("4294967296"), wantErr: true, wantType: tiny},
		{mode: ModeMySQL, oid: 75009, schema: schemaSys, name: testTypeUint4, length: 4, format: 1, row: []byte{0xff, 0xff, 0xff, 0xff}, want: int64(4294967295), wantType: tiny},
		{mode: ModeMySQL, oid: 75010, schema: schemaSys, name: "uint8", length: 8, row: []byte("18446744073709551615"), want: "18446744073709551615", wantType: text},
		{mode: ModeMySQL, oid: 75011, schema: schemaSys, name: "uint8", length: 8, format: 1,
			row: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, want: "18446744073709551615", wantType: text},
		{mode: ModeMySQL, oid: 75012, schema: schemaSys, name: "datetime", length: 8, row: []byte("2024-01-02 03:04:05"),
			want: when(2024, time.January, 2, 3, 4, 5), wantType: instant},
		{mode: ModeSQLServer, oid: 75013, schema: schemaSys, name: "smalldatetime", length: 8, row: []byte("2024-01-02 03:04:00"),
			want: when(2024, time.January, 2, 3, 4, 0), wantType: instant},
		// The mode datetime binary payload is the same plain wall-clock stamp as
		// its text, so it decodes without a zone shift.
		{mode: ModeMySQL, oid: 75014, schema: schemaSys, name: modeTypeDatetime, length: 8, format: 1,
			row:  binary.BigEndian.AppendUint64(nil, uint64(when(2024, time.January, 2, 3, 4, 5).UnixMicro()-pgEpoch.UnixMicro())),
			want: when(2024, time.January, 2, 3, 4, 5), wantType: instant},
		{mode: ModeMySQL, oid: 75015, schema: schemaSys, name: "varbinary", length: -1, row: []byte("0x616263"),
			want: []byte("abc"), wantType: bytesType},
		{mode: ModeMySQL, oid: 75016, schema: schemaSys, name: "binary", length: -1, format: 1, row: []byte("abc"),
			want: []byte("abc"), wantType: bytesType},
		{mode: ModeSQLServer, oid: 75017, schema: schemaSys, name: pgTypeBit, length: 1, row: []byte("1"), want: true, wantType: boolean},
		{mode: ModeSQLServer, oid: 75018, schema: schemaSys, name: pgTypeBit, length: 1, row: []byte("0"), want: false, wantType: boolean},
		{mode: ModeSQLServer, oid: 75019, schema: schemaSys, name: pgTypeBit, length: 1, row: []byte("2"), wantErr: true, wantType: boolean},
		{mode: ModeSQLServer, oid: 75020, schema: schemaSys, name: pgTypeBit, length: 1, format: 1, row: []byte{1}, want: true, wantType: boolean},
		{mode: ModeSQLServer, oid: 75021, schema: schemaSys, name: "nchar", length: -1, row: []byte("ab "), want: "ab ", wantType: text},
		{mode: ModeSQLServer, oid: 75022, schema: schemaSys, name: "nvarchar", length: -1, format: 1, row: []byte("ab "), want: "ab ", wantType: text},
		// MySQL BIT is a bit string, never a boolean: without a verified layout
		// it stays the exact hex text or raw binary value, so NullBool can never
		// silently accept it as a boolean.
		{mode: ModeMySQL, oid: 75040, schema: schemaSys, name: pgTypeBit, length: -1, row: []byte("0x41"), want: "0x41", wantType: text},
		{mode: ModeMySQL, oid: 75041, schema: schemaSys, name: pgTypeBit, length: -1, format: 1, row: []byte{0x41}, want: []byte{0x41}, wantType: bytesType}, // SQLServer money keeps the text scale and renders the binary scale
		// exactly as decimal text.
		{mode: ModeSQLServer, oid: 75030, schema: schemaSys, name: pgTypeMoney, length: 8, row: []byte("1234.5600"), want: "1234.5600", wantType: text},
		{mode: ModeSQLServer, oid: 75031, schema: schemaSys, name: pgTypeMoney, length: 8, format: 1,
			row: []byte{0, 0, 0, 0, 0, 0xbc, 0x61, 0x00}, want: "1234.5600", wantType: text},
		{mode: ModeSQLServer, oid: 75032, schema: schemaSys, name: pgTypeMoney, length: 8, format: 1,
			row: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0x43, 0x9f, 0x00}, want: "-1234.5600", wantType: text},
		{mode: ModeSQLServer, oid: 75033, schema: schemaSys, name: pgTypeMoney, length: 8, format: 1,
			row: []byte{0, 0, 0, 0, 0, 0xbc, 0x61}, wantErr: true, wantType: text}, // A same-named sys type with a different base length never selects the
		// codec: it stays opaque text just like an unknown OID.
		{mode: ModeMySQL, oid: 75023, schema: schemaSys, name: testTypeUint4, length: 8, row: []byte("4294967295"), want: "4294967295", wantType: text},
		{mode: ModeSQLServer, oid: 75024, schema: schemaSys, name: pgTypeTinyint, length: 4, row: []byte("5"), want: "5", wantType: text},
		// Oracle exposes tinyint as a pg_catalog base type.
		{mode: ModeOracle, oid: 75025, schema: schemaPGCatalog, name: pgTypeTinyint, length: 1, row: []byte("5"), want: int64(5), wantType: tiny},
		{mode: ModeOracle, oid: 75026, schema: schemaPGCatalog, name: pgTypeTinyint, length: 1, format: 1, row: []byte{0x7f}, want: int64(127), wantType: tiny},
	}
	for _, testCase := range cases {
		runModeTypeCase(t, testCase)
	}
}

// TestYearDomainDecodesAsInt64 pins the MySQL year domain contract: the server
// validates the YYYY/YYYY-MM-DD syntax and range, and the driver reports the
// validated value as int64 through the int4 base link.
func TestYearDomainDecodesAsInt64(t *testing.T) {
	yearOID := uint32(75030)
	extra := [][][]byte{
		userCatalogRow(yearOID, schemaSys, "year", 'd', 4, 23),
	}
	columns := []wire.ColumnDescription{{Name: "v", TypeOID: yearOID, TypeLen: 4, TypeMod: -1, Format: 0}}
	connection := serveModeTypeResult(t, ModeMySQL, extra, columns, []byte("2024"))
	result, err := connection.QueryContext(context.Background(), "SELECT v", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = result.Close() }()
	typed := result.(*rows)
	if got := typed.ColumnTypeScanType(0); got != reflect.TypeOf(int64(0)) {
		t.Errorf("year scan type = %v, want int64", got)
	}
	dest := make([]driver.Value, 1)
	if err := typed.Next(dest); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if dest[0] != int64(2024) {
		t.Errorf("year value = %#v, want int64 2024", dest[0])
	}
	if name := typed.ColumnTypeDatabaseTypeName(0); name != "INT4" {
		// The documented display identity is the resolved base name of a domain.
		t.Errorf("year type name = %q, want INT4 (domain base)", name)
	}
}
