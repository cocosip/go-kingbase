package kingbase

import (
	"database/sql/driver"
	"encoding/binary"
	"math"
	"strconv"
)

// intervalBinaryValue decodes PostgreSQL's interval layout (int64 microseconds,
// int32 days, int32 months) into the default postgres-style text. The binary
// form carries no IntervalStyle, so this is the canonical component order and
// spelling rather than a copy of an arbitrary server style.
func intervalBinaryValue(data []byte) (string, bool) {
	if len(data) != 16 {
		return "", false
	}
	micros := int64(binary.BigEndian.Uint64(data[0:8]))
	if micros == math.MinInt64 {
		return "", false
	}
	days := int32(binary.BigEndian.Uint32(data[8:12]))
	months := int32(binary.BigEndian.Uint32(data[12:16]))

	years := months / 12
	remainingMonths := months % 12
	out := make([]byte, 0, 40)
	out = appendIntervalUnit(out, years, "year", "years")
	out = appendIntervalUnit(out, remainingMonths, "mon", "mons")
	out = appendIntervalUnit(out, days, "day", "days")
	if micros != 0 {
		if len(out) != 0 {
			out = append(out, ' ')
		}
		out = appendIntervalTime(out, micros)
	} else if len(out) == 0 {
		return "00:00:00", true
	}
	return string(out), true
}

func intervalValue(data []byte) (driver.Value, bool) {
	value, ok := intervalBinaryValue(data)
	if !ok {
		return nil, false
	}
	return value, true
}

func appendIntervalUnit(out []byte, value int32, singular, plural string) []byte {
	if value == 0 {
		return out
	}
	if len(out) != 0 {
		out = append(out, ' ')
	}
	out = strconv.AppendInt(out, int64(value), 10)
	out = append(out, ' ')
	if value == 1 || value == -1 {
		return append(out, singular...)
	}
	return append(out, plural...)
}

func appendIntervalTime(out []byte, micros int64) []byte {
	if micros < 0 {
		out = append(out, '-')
		micros = -micros
	}
	hours := micros / microsPerHour
	minutes := (micros % microsPerHour) / microsPerMinute
	seconds := (micros % microsPerMinute) / microsPerSecond
	if hours < 10 {
		out = append(out, byte('0'+hours/10), byte('0'+hours%10))
	} else {
		out = strconv.AppendInt(out, hours, 10)
	}
	out = append(out, ':')
	out = appendTwoDigits(out, minutes)
	out = append(out, ':')
	out = appendTwoDigits(out, seconds)
	fraction := micros % microsPerSecond
	if fraction != 0 {
		out = append(out, '.')
		out = append(out,
			byte('0'+fraction/100000),
			byte('0'+fraction/10000%10),
			byte('0'+fraction/1000%10),
			byte('0'+fraction/100%10),
			byte('0'+fraction/10%10),
			byte('0'+fraction%10))
		for out[len(out)-1] == '0' {
			out = out[:len(out)-1]
		}
	}
	return out
}
