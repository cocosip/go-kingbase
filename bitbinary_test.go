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

func bitPayload(bits uint32, data ...byte) []byte {
	payload := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(payload, bits)
	copy(payload[4:], data)
	return payload
}

func TestBitBinaryMatchesText(t *testing.T) {
	cases := []struct {
		name string
		bits uint32
		data []byte
		want string
	}{
		{"five bits", 5, []byte{0b10101000}, "10101"},
		{"one byte", 8, []byte{0xAB}, "10101011"},
		{"empty", 0, nil, ""},
		{"two bytes", 12, []byte{0xFF, 0xF0}, "111111111111"},
	}
	for _, testCase := range cases {
		got, ok := bitBinaryValue(bitPayload(testCase.bits, testCase.data...))
		if !ok || got != testCase.want {
			t.Errorf("%s: bitBinaryValue = %#v, %v, want %q", testCase.name, got, ok, testCase.want)
		}
	}
	for _, name := range []string{pgTypeBit, pgTypeVarbit} {
		if text, ok, err := textValue(ModePG, schemaPGCatalog, name, []byte("10101"), nil); err != nil || !ok || text != "10101" {
			t.Errorf("%s text = %#v, %v, %v", name, text, ok, err)
		}
		if !binaryLayoutKnown(ModePG, schemaPGCatalog, name) {
			t.Errorf("%s binary layout is not known", name)
		}
		if got := scanType(ModePG, schemaPGCatalog, name, 1); got != reflect.TypeOf("") {
			t.Errorf("%s binary scan type = %v, want string", name, got)
		}
	}
}

func TestBitBinaryRejectsMalformedLayout(t *testing.T) {
	valid := bitPayload(5, 0b10101000)
	cases := map[string][]byte{
		"short header":    valid[:3],
		"missing byte":    valid[:4],
		"extra byte":      append(append([]byte(nil), valid...), 0),
		"dirty tail bits": bitPayload(5, 0b10101001),
	}
	for name, data := range cases {
		if value, ok := bitBinaryValue(data); ok {
			t.Errorf("%s: bitBinaryValue = %#v, true, want false", name, value)
		}
	}
}

// TestBitBinaryRejectsWrappedBitCount covers counts whose length calculation
// wraps a 32-bit sum: a four-byte frame must be rejected, never allocated from
// or indexed by the claimed count.
func TestBitBinaryRejectsWrappedBitCount(t *testing.T) {
	for _, bits := range []uint32{0xFFFFFFF9, 0xFFFFFFFE, 0xFFFFFFFF} {
		if value, ok := bitBinaryValue(bitPayload(bits)); ok {
			t.Errorf("bitBinaryValue(count %#x) = %q, true, want a rejection", bits, value)
		}
	}
}

func TestBinaryBitThroughRows(t *testing.T) {
	data := bitPayload(5, 0b10101000)
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		column := wire.ColumnDescription{Name: "b", TypeOID: 1562, TypeLen: -1, TypeMod: -1, Format: 1}
		if !serveUserSelect(peer, "SELECT b", column, data) {
			return
		}
		waitTerminate(peer)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got string
	if err := db.QueryRowContext(ctx, "SELECT b").Scan(&got); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got != "10101" {
		t.Errorf("bit value = %q, want 10101", got)
	}
}
