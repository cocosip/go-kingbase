package kingbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func timePayload(micros int64) []byte {
	data := make([]byte, 8)
	binary.BigEndian.PutUint64(data, uint64(micros))
	return data
}

func timetzPayload(micros int64, zone int32) []byte {
	data := make([]byte, 12)
	binary.BigEndian.PutUint64(data[:8], uint64(micros))
	binary.BigEndian.PutUint32(data[8:], uint32(zone))
	return data
}

func TestTimeBinaryMatchesText(t *testing.T) {
	cases := []struct {
		micros int64
		want   string
	}{
		{0, "00:00:00"},
		{1_000_000, "00:00:01"},
		{3_661_123_456, "01:01:01.123456"},
		{1_100_000, "00:00:01.1"},
		{24 * microsPerHour, "24:00:00"},
	}
	for _, testCase := range cases {
		got, ok := timeBinaryValue(timePayload(testCase.micros))
		if !ok || got != testCase.want {
			t.Errorf("timeBinaryValue(%d) = %q, %v, want %q", testCase.micros, got, ok, testCase.want)
		}
	}
	for _, micros := range []int64{-1, microsPerDayTime + 1} {
		if got, ok := timeBinaryValue(timePayload(micros)); ok {
			t.Errorf("timeBinaryValue(%d) = %q, true, want false", micros, got)
		}
	}
}

func TestTimetzBinaryMatchesText(t *testing.T) {
	cases := []struct {
		name   string
		micros int64
		zone   int32
		want   string
	}{
		{"east", 3_723_000_000, -19_800, "01:02:03+05:30"},
		{"west", 0, 14_400, "00:00:00-04"},
		{"utc", 0, 0, "00:00:00+00"},
		{"seconds", 0, -19_845, "00:00:00+05:30:45"},
	}
	for _, testCase := range cases {
		got, ok := timetzBinaryValue(timetzPayload(testCase.micros, testCase.zone))
		if !ok || got != testCase.want {
			t.Errorf("%s: timetzBinaryValue = %q, %v, want %q", testCase.name, got, ok, testCase.want)
		}
	}
	if _, ok := timetzBinaryValue(timetzPayload(0, maxTimeZoneSeconds+1)); ok {
		t.Error("out-of-range timetz zone accepted")
	}
}

func TestTimeBinaryMalformedAndMetadata(t *testing.T) {
	if _, ok := timeBinaryValue([]byte{0, 0, 0}); ok {
		t.Error("short time accepted")
	}
	if _, ok := timetzBinaryValue(timePayload(0)); ok {
		t.Error("short timetz accepted")
	}
	for _, name := range []string{pgTypeTime, pgTypeTimetz} {
		if !binaryLayoutKnown(ModePG, schemaPGCatalog, name) {
			t.Errorf("%s binary layout is not known", name)
		}
		if got := scanType(ModePG, schemaPGCatalog, name, 1); got != reflect.TypeOf("") {
			t.Errorf("%s binary scan type = %v, want string", name, got)
		}
	}
}

func TestBinaryTimeThroughRows(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		column := wire.ColumnDescription{Name: "t", TypeOID: 1083, TypeLen: 8, TypeMod: -1, Format: 1}
		if !serveUserSelect(peer, "SELECT t", column, timePayload(3_723_000_000)) {
			return
		}
		waitTerminate(peer)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got string
	if err := db.QueryRowContext(ctx, "SELECT t").Scan(&got); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got != "01:02:03" {
		t.Errorf("time value = %q, want 01:02:03", got)
	}
}
