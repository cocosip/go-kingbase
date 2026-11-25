package kingbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func numericPayload(weight int, sign uint16, dscale int, digits ...int) []byte {
	data := make([]byte, 8+2*len(digits))
	binary.BigEndian.PutUint16(data[0:2], uint16(len(digits)))
	binary.BigEndian.PutUint16(data[2:4], uint16(int16(weight)))
	binary.BigEndian.PutUint16(data[4:6], sign)
	binary.BigEndian.PutUint16(data[6:8], uint16(dscale))
	for index, digit := range digits {
		binary.BigEndian.PutUint16(data[8+2*index:10+2*index], uint16(digit))
	}
	return data
}

func TestNumericBinaryMatchesText(t *testing.T) {
	cases := []struct {
		name   string
		weight int
		sign   uint16
		dscale int
		digits []int
		want   string
	}{
		{"one point two three", 0, numericPositive, 2, []int{1, 2300}, "1.23"},
		{"negative with scale", 0, numericNegative, 4, []int{1, 2300}, "-1.2300"},
		{"zero with scale", 0, numericPositive, 2, nil, "0.00"},
		{"negative zero", 0, numericNegative, 2, nil, "0.00"},
		{"ten thousand", 1, numericPositive, 0, []int{1, 0}, "10000"},
		{"small fraction", -1, numericPositive, 4, []int{12}, "0.0012"},
		{"large", 2, numericPositive, 0, []int{1, 2, 3}, "100020003"},
		{"nan", 0, numericNaN, 0, nil, "NaN"},
		{"positive infinity", 0, numericPosInf, 0, nil, "Infinity"},
		{"negative infinity", 0, numericNegInf, 0, nil, "-Infinity"},
	}
	for _, testCase := range cases {
		data := numericPayload(testCase.weight, testCase.sign, testCase.dscale, testCase.digits...)
		got, ok := numericBinaryValue(data)
		if !ok || got != testCase.want {
			t.Errorf("%s: numericBinaryValue = %q, %v, want %q", testCase.name, got, ok, testCase.want)
		}
		value, ok := binaryValue(ModePG, schemaPGCatalog, pgTypeNumeric, data)
		if !ok || value != testCase.want {
			t.Errorf("%s: binaryValue = %#v, %v, want %q", testCase.name, value, ok, testCase.want)
		}
	}
}

func TestNumericBinaryRejectsMalformedLayout(t *testing.T) {
	valid := numericPayload(0, numericPositive, 2, 1, 2300)
	cases := map[string][]byte{
		"short header":       valid[:7],
		"truncated digits":   valid[:len(valid)-1],
		"extra digits":       append(append([]byte(nil), valid...), 0, 0),
		"bad sign":           numericPayload(0, 0x1234, 0, 1),
		"digit overflow":     numericPayload(0, numericPositive, 0, 10000),
		"display scale high": numericPayload(0, numericPositive, maxNumericDisplayScale+1, 1),
	}
	for name, data := range cases {
		if value, ok := numericBinaryValue(data); ok {
			t.Errorf("%s: numericBinaryValue = %q, true, want false", name, value)
		}
		decoder := typeDecoder(ModePG, schemaPGCatalog, pgTypeNumeric, 1, nil)
		if _, err := decoder(data); !errors.Is(err, ErrValueConversion) {
			t.Errorf("%s: typeDecoder error = %v, want ErrValueConversion", name, err)
		}
	}
}

func TestNumericBinaryMetadata(t *testing.T) {
	if !binaryLayoutKnown(ModePG, schemaPGCatalog, pgTypeNumeric) {
		t.Fatal("numeric binary layout is not known")
	}
	if got := scanType(ModePG, schemaPGCatalog, pgTypeNumeric, 1); got != reflect.TypeOf("") {
		t.Errorf("numeric binary scan type = %v, want string", got)
	}
}

func TestBinaryNumericThroughRows(t *testing.T) {
	data := numericPayload(0, numericNegative, 4, 1, 2300)
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		column := wire.ColumnDescription{Name: "n", TypeOID: 1700, TypeLen: -1, TypeMod: -1, Format: 1}
		if !serveUserSelect(peer, "SELECT n", column, data) {
			return
		}
		waitTerminate(peer)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got string
	if err := db.QueryRowContext(ctx, "SELECT n").Scan(&got); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got != "-1.2300" {
		t.Errorf("numeric value = %q, want -1.2300", got)
	}
}
