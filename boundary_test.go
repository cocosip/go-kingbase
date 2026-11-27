package kingbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestTextBooleanRejectsUnexpectedValue(t *testing.T) {
	for _, value := range []string{"t", "f"} {
		got, ok, err := textValue(ModePG, schemaPGCatalog, "bool", []byte(value), nil)
		if err != nil || !ok {
			t.Fatalf("textValue(%q) = %#v, %v, %v", value, got, ok, err)
		}
		want := value == "t"
		if got != want {
			t.Errorf("textValue(%q) = %#v, want %v", value, got, want)
		}
	}
	got, ok, err := textValue(ModePG, schemaPGCatalog, "bool", []byte("not-a-bool"), nil)
	if !ok || got != nil || !errors.Is(err, ErrValueConversion) {
		t.Errorf("malformed boolean = %#v, %v, %v, want a conversion error", got, ok, err)
	}
}

func TestBinaryBooleanRejectsNonCanonicalValue(t *testing.T) {
	if got, ok := binaryValue(ModePG, schemaPGCatalog, "bool", []byte{1}); !ok || got != true {
		t.Errorf("binary true = %#v, %v", got, ok)
	}
	if got, ok := binaryValue(ModePG, schemaPGCatalog, "bool", []byte{0}); !ok || got != false {
		t.Errorf("binary false = %#v, %v", got, ok)
	}
	if _, ok := binaryValue(ModePG, schemaPGCatalog, "bool", []byte{2}); ok {
		t.Error("non-canonical binary boolean was decoded")
	}
	if _, err := typeDecoder(ModePG, schemaPGCatalog, "bool", 1, nil)([]byte{2}); !errors.Is(err, ErrValueConversion) {
		t.Errorf("malformed binary bool error = %v, want ErrValueConversion", err)
	}
}

func TestBinaryTemporalRejectsOutOfRange(t *testing.T) {
	validTime := make([]byte, 8)
	binary.BigEndian.PutUint64(validTime, uint64(int64(time.Second/time.Microsecond)))
	if got, ok := binaryValue(ModePG, schemaPGCatalog, "timestamp", validTime); !ok {
		t.Fatal("valid binary timestamp was rejected")
	} else if !got.(time.Time).Equal(pgEpoch.Add(time.Second)) {
		t.Errorf("binary timestamp = %v", got)
	}

	infinity := make([]byte, 8)
	binary.BigEndian.PutUint64(infinity, uint64(math.MaxInt64))
	if _, ok := binaryValue(ModePG, schemaPGCatalog, "timestamp", infinity); ok {
		t.Error("binary timestamp infinity was decoded as an instant")
	}
	if _, err := typeDecoder(ModePG, schemaPGCatalog, "timestamp", 1, nil)(infinity); !errors.Is(err, ErrValueConversion) {
		t.Errorf("binary timestamp infinity error = %v, want ErrValueConversion", err)
	}

	validDate := make([]byte, 4)
	if got, ok := binaryValue(ModePG, schemaPGCatalog, "date", validDate); !ok || !got.(time.Time).Equal(pgEpoch) {
		t.Errorf("binary date oid = %#v, %v", got, ok)
	}
	overRange := make([]byte, 4)
	binary.BigEndian.PutUint32(overRange, uint32(math.MaxInt32))
	if _, ok := binaryValue(ModePG, schemaPGCatalog, "date", overRange); ok {
		t.Error("out-of-range binary date was decoded")
	}
	if _, err := typeDecoder(ModePG, schemaPGCatalog, "date", 1, nil)(overRange); !errors.Is(err, ErrValueConversion) {
		t.Errorf("out-of-range binary date error = %v, want ErrValueConversion", err)
	}
}

func TestConversionErrorDrainsAndReusesConnection(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if !serveUserSelect(peer, "SELECT t",
			wire.ColumnDescription{Name: "t", TypeOID: 1114, TypeLen: 8, TypeMod: -1, Format: 0},
			[]byte("infinity")) {
			return
		}
		if !serveUserSelect(peer, "SELECT n",
			wire.ColumnDescription{Name: "n", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
			[]byte("7")) {
			return
		}
		waitTerminate(peer)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	var at time.Time
	err := db.QueryRowContext(context.Background(), "SELECT t").Scan(&at)
	if !errors.Is(err, ErrValueConversion) {
		t.Fatalf("infinity timestamp error = %v, want ErrValueConversion", err)
	}
	var number int64
	if err := db.QueryRowContext(context.Background(), "SELECT n").Scan(&number); err != nil {
		t.Fatalf("reuse query: %v", err)
	}
	if number != 7 {
		t.Errorf("reuse value = %d, want 7", number)
	}
}

func TestExactDecimalAndTextBoundaries(t *testing.T) {
	cases := []struct {
		typ   string
		value string
	}{
		{pgTypeNumeric, "123456789012345678901234567890.12345678901234567890"},
		{pgTypeNumeric, "NaN"},
		{pgTypeNumeric, "Infinity"},
		{pgTypeNumeric, "-Infinity"},
		{"money", "$1,234.56"},
		{"money", "($1,234.56)"},
		{"interval", "1 year 2 mons 3 days 04:05:06.789"},
		{"interval", "-00:00:01"},
		{pgTypeTime, "24:00:00.123456"},
		{pgTypeTime, "-01:02:03"},
		{"timetz", "23:59:59.999999+05:30"},
	}
	for _, testCase := range cases {
		got, ok, err := textValue(ModePG, schemaPGCatalog, testCase.typ, []byte(testCase.value), nil)
		if err != nil || !ok || got != testCase.value {
			t.Errorf("textValue(%s, %q) = %#v, %v, %v, want exact text", testCase.typ, testCase.value, got, ok, err)
		}
	}
	for _, value := range []string{"NaN", "Infinity", "-Infinity"} {
		got, ok, err := textValue(ModePG, schemaPGCatalog, "float8", []byte(value), nil)
		if err != nil || !ok {
			t.Errorf("float8 %q = %#v, %v, %v", value, got, ok, err)
			continue
		}
		number := got.(float64)
		if value == "NaN" && !math.IsNaN(number) {
			t.Errorf("float8 NaN = %v", number)
		}
		if value == "Infinity" && !math.IsInf(number, 1) {
			t.Errorf("float8 Infinity = %v", number)
		}
		if value == "-Infinity" && !math.IsInf(number, -1) {
			t.Errorf("float8 -Infinity = %v", number)
		}
	}
}

func TestTimestamptzOffsetsAndPrecision(t *testing.T) {
	moment, err := parseTimestamptz("2024-03-10 03:30:00.123456789-04:00", nil)
	if err != nil {
		t.Fatalf("timestamptz: %v", err)
	}
	_, offset := moment.Zone()
	if offset != -4*60*60 || moment.Nanosecond() != 123456789 {
		t.Errorf("timestamptz = %v offset %d nanos %d", moment, offset, moment.Nanosecond())
	}

	seconds, err := parseTimestamptz("2024-03-10 03:30:00+05:30:45", nil)
	if err != nil {
		t.Fatalf("seconds-offset timestamptz: %v", err)
	}
	_, secondsOffset := seconds.Zone()
	if secondsOffset != 5*60*60+30*60+45 {
		t.Errorf("seconds offset = %d, want 19845", secondsOffset)
	}

	before, err := parseTimestamptz("2024-03-10 01:30:00-05:00", nil)
	if err != nil {
		t.Fatalf("before DST: %v", err)
	}
	after, err := parseTimestamptz("2024-03-10 03:30:00-04:00", nil)
	if err != nil {
		t.Fatalf("after DST: %v", err)
	}
	if after.Sub(before) != time.Hour {
		t.Errorf("DST instants differ by %v, want one hour", after.Sub(before))
	}
}

func TestNullBoolStandardScan(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		column := wire.ColumnDescription{Name: "b", TypeOID: 16, TypeLen: 1, TypeMod: -1, Format: 0}
		if !serveUserSelect(peer, "SELECT b", column, nil) {
			return
		}
		if !serveUserSelect(peer, "SELECT b", column, []byte("t")) {
			return
		}
		waitTerminate(peer)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var missing sql.NullBool
	if err := db.QueryRowContext(ctx, "SELECT b").Scan(&missing); err != nil {
		t.Fatalf("NULL scan: %v", err)
	}
	if missing.Valid {
		t.Errorf("NULL bool = %+v, want invalid", missing)
	}
	var present sql.NullBool
	if err := db.QueryRowContext(ctx, "SELECT b").Scan(&present); err != nil {
		t.Fatalf("true scan: %v", err)
	}
	if !present.Valid || !present.Bool {
		t.Errorf("true bool = %+v, want valid true", present)
	}
}

func TestTextTemporalPolicy(t *testing.T) {
	if _, err := parseDate("2024-02-29"); err != nil {
		t.Errorf("valid date: %v", err)
	}
	for _, value := range []string{"2023-02-29", "0000-01-01", "2024-02-29 BC", "infinity"} {
		if _, err := parseDate(value); !errors.Is(err, ErrValueConversion) {
			t.Errorf("parseDate(%q) error = %v, want ErrValueConversion", value, err)
		}
	}
	if _, err := parseTimestamp("2024-01-02 03:04:05"); err != nil {
		t.Errorf("valid timestamp: %v", err)
	}
	for _, value := range []string{"infinity", "-infinity", "2024-01-02 03:04:05 BC"} {
		if _, err := parseTimestamp(value); !errors.Is(err, ErrValueConversion) {
			t.Errorf("parseTimestamp(%q) error = %v, want ErrValueConversion", value, err)
		}
	}
	parsed, err := parseTimestamptz("2024-01-02 03:04:05+08", nil)
	if err != nil {
		t.Fatalf("timestamptz: %v", err)
	}
	_, offset := parsed.Zone()
	if offset != 8*60*60 || parsed.Hour() != 3 {
		t.Errorf("timestamptz = %v offset %d, want 03:04:05+08:00", parsed, offset)
	}
}

// TestTimestamptzOffsetlessSessionZone covers the measured MySQL-mode text form:
// an instant is rendered as the session-zone wall clock with no offset, so the
// startup-reported TimeZone recovers the instant instead of assuming UTC.
func TestTimestamptzOffsetlessSessionZone(t *testing.T) {
	zone := time.FixedZone("CST", 8*60*60)
	parsed, err := parseTimestamptz("2024-03-10 14:30:00", zone)
	if err != nil {
		t.Fatalf("parseTimestamptz(offset-less, session zone): %v", err)
	}
	if got := parsed.UTC().Format("2006-01-02 15:04:05"); got != "2024-03-10 06:30:00" {
		t.Errorf("instant = %s, want 2024-03-10 06:30:00", got)
	}
	if _, err := parseTimestamptz("2024-03-10 14:30:00", nil); !errors.Is(err, ErrValueConversion) {
		t.Fatalf("offset-less without a zone error = %v, want conversion error", err)
	}
	physical := &conn{params: map[string]string{"TimeZone": "Asia/Shanghai"}}
	location := physical.sessionLocation()
	if location == nil {
		t.Skip("the platform has no Asia/Shanghai zoneinfo")
	}
	value, ok, err := textValue(ModeMySQL, schemaPGCatalog, pgTypeTimestamptz, []byte("2024-03-10 14:30:00"), location)
	if err != nil || !ok {
		t.Fatalf("textValue timestamptz = %#v, %v, %v", value, ok, err)
	}
	stamp, isTime := value.(time.Time)
	if !isTime || stamp.UTC().Format("15:04:05") != "06:30:00" {
		t.Errorf("decoded timestamptz = %#v, want the 06:30:00Z instant", value)
	}
	if physical.sessionLocation() != location {
		t.Error("sessionLocation did not reuse its loaded zone")
	}
	physical.sessionZoneName = ""
	if other := physical.sessionLocation(); other == nil {
		t.Error("sessionLocation did not reload after invalidation")
	}
}
