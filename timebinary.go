package kingbase

import (
	"database/sql/driver"
	"encoding/binary"
)

const (
	microsPerSecond      = int64(1_000_000)
	microsPerMinute      = 60 * microsPerSecond
	microsPerHour        = 60 * microsPerMinute
	microsPerDayTime     = 24 * microsPerHour
	maxTimeZoneSeconds   = 16 * 60 * 60
	timetzBinaryDataSize = 12
)

// timeValue and timetzValue adapt the text results to driver.Value.
func timeValue(data []byte) (driver.Value, bool) {
	value, ok := timeBinaryValue(data)
	if !ok {
		return nil, false
	}
	return value, true
}

func timetzValue(data []byte) (driver.Value, bool) {
	value, ok := timetzBinaryValue(data)
	if !ok {
		return nil, false
	}
	return value, true
}

func timeOrTimetzValue(name string, data []byte) (driver.Value, bool) {
	if name == pgTypeTime {
		return timeValue(data)
	}
	return timetzValue(data)
}

// timeBinaryValue decodes PostgreSQL's time binary value (int64 microseconds
// since midnight) into the default text representation. PostgreSQL permits
// exactly 24:00:00; negative and longer values are malformed.
func timeBinaryValue(data []byte) (string, bool) {
	if len(data) != 8 {
		return "", false
	}
	return formatTimeMicros(int64(binary.BigEndian.Uint64(data)))
}

// timetzBinaryValue decodes time + a signed seconds-west-of-UTC zone. The text
// form uses the inverse east-of-UTC sign and omits zero minutes/seconds.
func timetzBinaryValue(data []byte) (string, bool) {
	if len(data) != timetzBinaryDataSize {
		return "", false
	}
	timeText, ok := timeBinaryValue(data[:8])
	if !ok {
		return "", false
	}
	zone := int32(binary.BigEndian.Uint32(data[8:]))
	if zone > maxTimeZoneSeconds || zone < -maxTimeZoneSeconds {
		return "", false
	}
	offset := -int64(zone)
	out := []byte(timeText)
	if offset == 0 {
		return timeText + "+00", true
	}
	sign := byte('+')
	if offset < 0 {
		sign = '-'
		offset = -offset
	}
	hours := offset / 3600
	minutes := (offset % 3600) / 60
	seconds := offset % 60
	out = append(out, sign)
	out = appendTwoDigits(out, hours)
	if minutes != 0 || seconds != 0 {
		out = append(out, ':')
		out = appendTwoDigits(out, minutes)
	}
	if seconds != 0 {
		out = append(out, ':')
		out = appendTwoDigits(out, seconds)
	}
	return string(out), true
}

func formatTimeMicros(micros int64) (string, bool) {
	if micros < 0 || micros > microsPerDayTime {
		return "", false
	}
	hours := micros / microsPerHour
	minutes := (micros % microsPerHour) / microsPerMinute
	seconds := (micros % microsPerMinute) / microsPerSecond
	out := make([]byte, 0, 18)
	out = appendTwoDigits(out, hours)
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
	return string(out), true
}

func appendTwoDigits(out []byte, value int64) []byte {
	return append(out, byte('0'+value/10%10), byte('0'+value%10))
}
