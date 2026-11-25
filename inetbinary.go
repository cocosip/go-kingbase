package kingbase

import (
	"database/sql/driver"
	"net"
	"strconv"
)

// PostgreSQL inet/cidr binary families.
const (
	inetFamilyIPv4 = 2
	inetFamilyIPv6 = 3
)

// inetBinaryValue decodes PostgreSQL's inet/cidr layout into canonical text.
// The binary form has no field order to preserve: family, prefix bits, the
// is-cidr flag and the raw address completely determine the value.
func inetBinaryValue(name string, data []byte) (string, bool) {
	if len(data) < 4 {
		return "", false
	}
	family := data[0]
	bits := data[1]
	isCIDR := data[2] == 1
	if data[2] > 1 {
		return "", false
	}
	addressLen := int(data[3])
	if len(data) != 4+addressLen {
		return "", false
	}
	var maxBits int
	switch family {
	case inetFamilyIPv4:
		maxBits = 32
	case inetFamilyIPv6:
		maxBits = 128
	default:
		return "", false
	}
	if addressLen != maxBits/8 || int(bits) > maxBits {
		return "", false
	}
	address := net.IP(data[4:]).String()
	if name == pgTypeCidr || isCIDR || int(bits) != maxBits {
		return address + "/" + strconv.Itoa(int(bits)), true
	}
	return address, true
}

func inetValue(name string, data []byte) (driver.Value, bool) {
	value, ok := inetBinaryValue(name, data)
	if !ok {
		return nil, false
	}
	return value, true
}
