package kingbase

import (
	"database/sql/driver"
	"encoding/binary"
)

// bitBinaryValue decodes PostgreSQL bit/varbit binary data (int32 bit count,
// then ceil(bits/8) big-endian bytes) into the same 0/1 text contract.
func bitBinaryValue(data []byte) (driver.Value, bool) {
	if len(data) < 4 {
		return nil, false
	}
	bits := binary.BigEndian.Uint32(data[:4])
	byteCount := int64(bits+7) / 8
	if byteCount > int64(len(data)-4) || int64(len(data)) != 4+byteCount {
		return nil, false
	}
	if remainder := bits % 8; remainder != 0 {
		mask := byte(1<<(8-remainder)) - 1
		if data[len(data)-1]&mask != 0 {
			return nil, false
		}
	}
	out := make([]byte, 0, bits)
	for index := uint32(0); index < bits; index++ {
		bit := (data[4+index/8] >> (7 - index%8)) & 1
		out = append(out, '0'+bit)
	}
	return string(out), true
}
