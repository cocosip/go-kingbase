package gokb

import "database/sql/driver"

// decodeValue converts one result column to a driver value. Without a
// catalog-verified codec the documented fallback applies: text is a string and
// binary is []byte; type-specific decoding is added by the codec task.
func decodeValue(format int16, data []byte) driver.Value {
	if format == 1 {
		return append([]byte(nil), data...)
	}
	return string(data)
}
