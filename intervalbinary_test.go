package kingbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func intervalPayload(micros int64, days, months int32) []byte {
	data := make([]byte, 16)
	binary.BigEndian.PutUint64(data[:8], uint64(micros))
	binary.BigEndian.PutUint32(data[8:12], uint32(days))
	binary.BigEndian.PutUint32(data[12:16], uint32(months))
	return data
}

func TestIntervalBinaryMatchesText(t *testing.T) {
	fourHoursFiveMinutesSix := int64(4*60*60+5*60+6) * microsPerSecond
	cases := []struct {
		name   string
		micros int64
		days   int32
		months int32
		want   string
	}{
		{"mixed fields", fourHoursFiveMinutesSix, 3, 14, "1 year 2 mons 3 days 04:05:06"},
		{"negative components", -int64(1*60*60+2*60+3) * microsPerSecond, -2, -1, "-1 mon -2 days -01:02:03"},
		{"zero", 0, 0, 0, "00:00:00"},
		{"days only", 0, 1, 0, "1 day"},
		{"large hours", 25 * microsPerHour, 0, 0, "25:00:00"},
		{"fraction", 1*microsPerSecond + 123_400, 0, 0, "00:00:01.1234"},
		{"year only", 0, 0, 12, "1 year"},
		{"negative time only", -microsPerSecond, 0, 0, "-00:00:01"},
	}
	for _, testCase := range cases {
		got, ok := intervalBinaryValue(intervalPayload(testCase.micros, testCase.days, testCase.months))
		if !ok || got != testCase.want {
			t.Errorf("%s: intervalBinaryValue = %q, %v, want %q", testCase.name, got, ok, testCase.want)
		}
	}
}

func TestIntervalBinaryRejectsMalformedLayout(t *testing.T) {
	if _, ok := intervalBinaryValue(make([]byte, 15)); ok {
		t.Error("short interval accepted")
	}
	if _, ok := intervalBinaryValue(intervalPayload(math.MinInt64, 0, 0)); ok {
		t.Error("minimum int64 time accepted")
	}
}

func TestIntervalBinaryMetadata(t *testing.T) {
	if !binaryLayoutKnown(pgTypeInterval) {
		t.Fatal("interval binary layout is not known")
	}
	if got := scanType(schemaPGCatalog, pgTypeInterval, 1); got != reflect.TypeOf("") {
		t.Errorf("interval binary scan type = %v, want string", got)
	}
}

func TestBinaryIntervalThroughRows(t *testing.T) {
	data := intervalPayload(int64(4*60*60+5*60+6)*microsPerSecond, 3, 14)
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		column := wire.ColumnDescription{Name: "i", TypeOID: 1186, TypeLen: 16, TypeMod: -1, Format: 1}
		if !serveUserSelect(peer, "SELECT i", column, data) {
			return
		}
		waitTerminate(peer)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got string
	if err := db.QueryRowContext(ctx, "SELECT i").Scan(&got); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got != "1 year 2 mons 3 days 04:05:06" {
		t.Errorf("interval value = %q, want 1 year 2 mons 3 days 04:05:06", got)
	}
}
