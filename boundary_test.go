package kingbase

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

func TestTextBooleanPreservesUnexpectedValue(t *testing.T) {
	for _, value := range []string{"t", "f"} {
		got, ok := textValue(schemaPGCatalog, "bool", []byte(value))
		if !ok {
			t.Fatalf("textValue(%q) had no codec", value)
		}
		want := value == "t"
		if got != want {
			t.Errorf("textValue(%q) = %#v, want %v", value, got, want)
		}
	}
	got, ok := textValue(schemaPGCatalog, "bool", []byte("not-a-bool"))
	if !ok || got != "not-a-bool" {
		t.Errorf("malformed boolean = %#v, %v, want opaque text", got, ok)
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

	validDate := make([]byte, 4)
	if got, ok := binaryValue(schemaPGCatalog, "date", validDate); !ok || !got.(time.Time).Equal(pgEpoch) {
		t.Errorf("binary date oid = %#v, %v", got, ok)
	}
	overRange := make([]byte, 4)
	binary.BigEndian.PutUint32(overRange, uint32(math.MaxInt32))
	if _, ok := binaryValue(schemaPGCatalog, "date", overRange); ok {
		t.Error("out-of-range binary date was decoded")
	}
}

func TestTextTemporalPolicy(t *testing.T) {
	if got := parseDate("2024-02-29"); got == "2024-02-29" {
		t.Error("valid date was left as text")
	}
	for _, value := range []string{"2023-02-29", "0000-01-01", "2024-02-29 BC", "infinity"} {
		if got := parseDate(value); got != value {
			t.Errorf("parseDate(%q) = %#v, want text", value, got)
		}
	}
	if got := parseTimestamp("2024-01-02 03:04:05"); got == "2024-01-02 03:04:05" {
		t.Error("valid timestamp was left as text")
	}
	for _, value := range []string{"infinity", "-infinity", "2024-01-02 03:04:05 BC"} {
		if got := parseTimestamp(value); got != value {
			t.Errorf("parseTimestamp(%q) = %#v, want text", value, got)
		}
	}
	moment := parseTimestamptz("2024-01-02 03:04:05+08")
	parsed, ok := moment.(time.Time)
	if !ok {
		t.Fatalf("timestamptz = %T, want time.Time", moment)
	}
	_, offset := parsed.Zone()
	if offset != 8*60*60 || parsed.Hour() != 3 {
		t.Errorf("timestamptz = %v offset %d, want 03:04:05+08:00", parsed, offset)
	}
}
