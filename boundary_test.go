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
		got, ok, err := textValue(schemaPGCatalog, "bool", []byte(value))
		if err != nil || !ok {
			t.Fatalf("textValue(%q) = %#v, %v, %v", value, got, ok, err)
		}
		want := value == "t"
		if got != want {
			t.Errorf("textValue(%q) = %#v, want %v", value, got, want)
		}
	}
	got, ok, err := textValue(schemaPGCatalog, "bool", []byte("not-a-bool"))
	if !ok || got != nil || !errors.Is(err, ErrValueConversion) {
		t.Errorf("malformed boolean = %#v, %v, %v, want a conversion error", got, ok, err)
	}
}

func TestBinaryBooleanRejectsNonCanonicalValue(t *testing.T) {
	if got, ok := binaryValue(schemaPGCatalog, "bool", []byte{1}); !ok || got != true {
		t.Errorf("binary true = %#v, %v", got, ok)
	}
	if got, ok := binaryValue(schemaPGCatalog, "bool", []byte{0}); !ok || got != false {
		t.Errorf("binary false = %#v, %v", got, ok)
	}
	if _, ok := binaryValue(schemaPGCatalog, "bool", []byte{2}); ok {
		t.Error("non-canonical binary boolean was decoded")
	}
	if _, err := typeDecoder(schemaPGCatalog, "bool", 1)([]byte{2}); !errors.Is(err, ErrValueConversion) {
		t.Errorf("malformed binary bool error = %v, want ErrValueConversion", err)
	}
}

func TestBinaryTemporalRejectsOutOfRange(t *testing.T) {
	validTime := make([]byte, 8)
	binary.BigEndian.PutUint64(validTime, uint64(int64(time.Second/time.Microsecond)))
	if got, ok := binaryValue(schemaPGCatalog, "timestamp", validTime); !ok {
		t.Fatal("valid binary timestamp was rejected")
	} else if !got.(time.Time).Equal(pgEpoch.Add(time.Second)) {
		t.Errorf("binary timestamp = %v", got)
	}

	infinity := make([]byte, 8)
	binary.BigEndian.PutUint64(infinity, uint64(math.MaxInt64))
	if _, ok := binaryValue(schemaPGCatalog, "timestamp", infinity); ok {
		t.Error("binary timestamp infinity was decoded as an instant")
	}
	if _, err := typeDecoder(schemaPGCatalog, "timestamp", 1)(infinity); !errors.Is(err, ErrValueConversion) {
		t.Errorf("binary timestamp infinity error = %v, want ErrValueConversion", err)
	}

	validDate := make([]byte, 4)
	if got, ok := binaryValue(schemaPGCatalog, "date", validDate); !ok || !got.(time.Time).Equal(pgEpoch) {
		t.Errorf("binary date oid = %#v, %v", got, ok)
	}
	overRange := make([]byte, 4)
	binary.BigEndian.PutUint32(overRange, uint32(math.MaxInt32))
	if _, ok := binaryValue(schemaPGCatalog, "date", overRange); ok {
		t.Error("out-of-range binary date was decoded")
	}
	if _, err := typeDecoder(schemaPGCatalog, "date", 1)(overRange); !errors.Is(err, ErrValueConversion) {
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
	parsed, err := parseTimestamptz("2024-01-02 03:04:05+08")
	if err != nil {
		t.Fatalf("timestamptz: %v", err)
	}
	_, offset := parsed.Zone()
	if offset != 8*60*60 || parsed.Hour() != 3 {
		t.Errorf("timestamptz = %v offset %d, want 03:04:05+08:00", parsed, offset)
	}
}
