package kingbase

import (
	"context"
	"database/sql"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func inetPayload(family, bits, isCIDR byte, address net.IP) []byte {
	data := []byte{family, bits, isCIDR, byte(len(address))}
	return append(data, address...)
}

func TestInetBinaryMatchesText(t *testing.T) {
	ipv4 := net.ParseIP("192.0.2.1").To4()
	ipv6 := net.ParseIP("2001:db8::1").To16()
	cases := []struct {
		name  string
		data  []byte
		want  string
		field string
	}{
		{"inet v4 prefix", inetPayload(inetFamilyIPv4, 24, 0, ipv4), "192.0.2.1/24", pgTypeInet},
		{"inet v4 full", inetPayload(inetFamilyIPv4, 32, 0, ipv4), "192.0.2.1", pgTypeInet},
		{"cidr v4 full", inetPayload(inetFamilyIPv4, 32, 1, ipv4), "192.0.2.1/32", pgTypeCidr},
		{"inet v6 prefix", inetPayload(inetFamilyIPv6, 64, 0, ipv6), "2001:db8::1/64", pgTypeInet},
		{"inet v6 full", inetPayload(inetFamilyIPv6, 128, 0, ipv6), "2001:db8::1", pgTypeInet},
		{"cidr v6 full", inetPayload(inetFamilyIPv6, 128, 1, ipv6), "2001:db8::1/128", pgTypeCidr},
	}
	for _, testCase := range cases {
		got, ok := inetBinaryValue(testCase.field, testCase.data)
		if !ok || got != testCase.want {
			t.Errorf("%s: inetBinaryValue = %q, %v, want %q", testCase.name, got, ok, testCase.want)
		}
		if value, ok := inetValue(testCase.field, testCase.data); !ok || value != testCase.want {
			t.Errorf("%s: inetValue = %#v, %v, want %q", testCase.name, value, ok, testCase.want)
		}
	}
}

func TestInetBinaryRejectsMalformedLayout(t *testing.T) {
	ipv4 := net.ParseIP("192.0.2.1").To4()
	valid := inetPayload(inetFamilyIPv4, 24, 0, ipv4)
	cases := map[string][]byte{
		"short":          valid[:3],
		"bad family":     inetPayload(1, 24, 0, ipv4),
		"bits overflow":  inetPayload(inetFamilyIPv4, 33, 0, ipv4),
		"bad flag":       inetPayload(inetFamilyIPv4, 24, 2, ipv4),
		"bad addr len":   append([]byte{inetFamilyIPv4, 24, 0, 5}, ipv4...),
		"truncated data": valid[:len(valid)-1],
	}
	for name, data := range cases {
		if value, ok := inetBinaryValue(pgTypeInet, data); ok {
			t.Errorf("%s: inetBinaryValue = %q, true, want false", name, value)
		}
	}
	if _, ok := inetBinaryValue(pgTypeInet, inetPayload(inetFamilyIPv6, 64, 0, ipv4)); ok {
		t.Error("IPv4 address accepted for IPv6 family")
	}
}

func TestInetBinaryMetadata(t *testing.T) {
	for _, name := range []string{pgTypeInet, pgTypeCidr} {
		if !binaryLayoutKnown(name) {
			t.Errorf("%s binary layout is not known", name)
		}
		if got := scanType(schemaPGCatalog, name, 1); got != reflect.TypeOf("") {
			t.Errorf("%s binary scan type = %v, want string", name, got)
		}
	}
}

func TestBinaryInetThroughRows(t *testing.T) {
	data := inetPayload(inetFamilyIPv4, 24, 0, net.ParseIP("192.0.2.1").To4())
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		column := wire.ColumnDescription{Name: "a", TypeOID: 869, TypeLen: -1, TypeMod: -1, Format: 1}
		if !serveUserSelect(peer, "SELECT a", column, data) {
			return
		}
		waitTerminate(peer)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got string
	if err := db.QueryRowContext(ctx, "SELECT a").Scan(&got); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got != "192.0.2.1/24" {
		t.Errorf("inet value = %q, want 192.0.2.1/24", got)
	}
}
