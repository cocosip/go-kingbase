package kingbase

import (
	"database/sql/driver"
	"encoding/binary"
	"strconv"
	"strings"
)

// PostgreSQL numeric sign words (src/backend/utils/adt/numeric.c).
const (
	numericPositive = 0x0000
	numericNegative = 0x4000
	numericNaN      = 0xC000
	numericPosInf   = 0xD000
	numericNegInf   = 0xF000
)

// maxNumericDisplayScale is PostgreSQL's NUMERIC_DSCALE_MAX.
const maxNumericDisplayScale = 16383

// numericValue is the driver.Value adapter for binaryValue.
func numericValue(data []byte) (driver.Value, bool) {
	value, ok := numericBinaryValue(data)
	if !ok {
		return nil, false
	}
	return value, true
}

// numericBinaryValue decodes PostgreSQL's numeric binary layout into the same
// exact decimal text the text-format codec preserves. It returns false for a
// malformed or unsupported layout so the caller can report a conversion error.
func numericBinaryValue(data []byte) (string, bool) {
	if len(data) < 8 {
		return "", false
	}
	ndigits := int(binary.BigEndian.Uint16(data[0:2]))
	weight := int(int16(binary.BigEndian.Uint16(data[2:4])))
	sign := binary.BigEndian.Uint16(data[4:6])
	dscale := int(binary.BigEndian.Uint16(data[6:8]))
	if ndigits < 0 || ndigits > (len(data)-8)/2 || len(data) != 8+2*ndigits {
		return "", false
	}
	if dscale > maxNumericDisplayScale {
		return "", false
	}
	switch sign {
	case numericNaN:
		return "NaN", true
	case numericPosInf:
		return "Infinity", true
	case numericNegInf:
		return "-Infinity", true
	case numericPositive, numericNegative:
	default:
		return "", false
	}
	if ndigits == 0 {
		return zeroNumericText(dscale), true
	}
	digits := make([]int, ndigits)
	nonzero := false
	for i := range digits {
		digit := int(binary.BigEndian.Uint16(data[8+2*i : 10+2*i]))
		if digit > 9999 {
			return "", false
		}
		digits[i] = digit
		if digit != 0 {
			nonzero = true
		}
	}
	var out strings.Builder
	if sign == numericNegative && nonzero {
		out.WriteByte('-')
	}
	if weight >= 0 {
		out.WriteString(strconv.Itoa(numericGroup(digits, weight, weight)))
		for exponent := weight - 1; exponent >= 0; exponent-- {
			writeNumericGroup(&out, numericGroup(digits, weight, exponent))
		}
	} else {
		out.WriteByte('0')
	}
	if dscale > 0 {
		out.WriteByte('.')
		groups := (dscale + 3) / 4
		fraction := make([]byte, 0, groups*4)
		for exponent := -1; exponent >= -groups; exponent-- {
			group := numericGroup(digits, weight, exponent)
			fraction = append(fraction,
				byte('0'+group/1000),
				byte('0'+group/100%10),
				byte('0'+group/10%10),
				byte('0'+group%10))
		}
		out.Write(fraction[:dscale])
	}
	return out.String(), true
}

// numericGroup returns the base-10000 group at a decimal-group exponent,
// padding omitted trailing groups with zero.
func numericGroup(digits []int, weight, exponent int) int {
	index := weight - exponent
	if index < 0 || index >= len(digits) {
		return 0
	}
	return digits[index]
}

func writeNumericGroup(out *strings.Builder, group int) {
	out.WriteByte(byte('0' + group/1000%10))
	out.WriteByte(byte('0' + group/100%10))
	out.WriteByte(byte('0' + group/10%10))
	out.WriteByte(byte('0' + group%10))
}

func zeroNumericText(scale int) string {
	if scale == 0 {
		return "0"
	}
	return "0." + strings.Repeat("0", scale)
}
