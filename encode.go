package kingbase

import (
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"time"
)

// encodeParameter encodes one logical argument for a text-format Bind. The
// target type is inferred by the server because no parameter OIDs are sent;
// binary parameters and wrapper/default semantics are handled by the binding
// task.
func encodeParameter(value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case int64:
		return strconv.AppendInt(nil, typed, 10), nil
	case uint64:
		// Full-range unsigned values stay exact decimal text; no int64 cast.
		return strconv.AppendUint(nil, typed, 10), nil
	case float64:
		return strconv.AppendFloat(nil, typed, 'g', -1, 64), nil
	case bool:
		if typed {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		return []byte(typed), nil
	case VarChar:
		return []byte(typed), nil
	case VarCharMax:
		return []byte(typed), nil
	case NVarCharMax:
		return []byte(typed), nil
	case NChar:
		return []byte(typed), nil
	case DateTime1:
		return []byte(time.Time(typed).Format("2006-01-02 15:04:05.999999999Z07:00")), nil
	case []byte:
		// ponytail: raw bytes go as text; bytea (\x…) and binary formats belong
		// to the codec task (T-009). Targets that reject the text raise a server
		// error instead of storing silent corruption.
		return typed, nil
	case time.Time:
		// ponytail: always a zone-qualified timestamp; date/time targets are a
		// codec concern (T-009).
		return []byte(typed.Format("2006-01-02 15:04:05.999999999Z07:00")), nil
	default:
		return nil, fmt.Errorf("kingbase: unsupported parameter type %T", value)
	}
}

// encodePlanParameters encodes direct-statement parameters. An explicit
// qualified pg_catalog cast that resolves to one catalog identity may select
// the verified binary layout; every other parameter keeps text encoding and
// server inference. No OID is sent or assigned by type name alone.
func (c *conn) encodePlanParameters(values []driver.Value, targets map[int]targetRef) ([][]byte, []int16, error) {
	useBinary := c.cfg != nil && c.cfg.BinaryParameters
	if useBinary && c.catalog != nil && len(targets) != 0 {
		oids := make([]uint32, len(values))
		found := false
		for index, ref := range targets {
			if index < 1 || index > len(values) || ref.schema != schemaPGCatalog {
				continue
			}
			if oid, ok := c.catalog.lookup(ref.schema, ref.name); ok {
				oids[index-1] = oid
				found = true
			}
		}
		if found {
			return c.encodeParametersWithTypes(values, oids)
		}
	}
	params, err := encodeParameters(values)
	return params, nil, err
}

// encodeParametersWithTypes encodes a Bind batch for a prepared statement whose
// parameter OIDs are known. With binary parameters enabled, values whose target
// type has a verified binary layout are sent as binary and the rest stay text;
// the per-parameter format list makes the mixed batch explicit.
func (c *conn) encodeParametersWithTypes(values []driver.Value, oids []uint32) ([][]byte, []int16, error) {
	params := make([][]byte, len(values))
	formats := make([]int16, len(values))
	useBinary := c.cfg != nil && c.cfg.BinaryParameters
	for i, value := range values {
		if useBinary && i < len(oids) && value != nil {
			data, ok, err := c.binaryParameter(oids[i], value)
			if err != nil {
				return nil, nil, err
			}
			if ok {
				params[i] = data
				formats[i] = 1
				continue
			}
		}
		data, err := encodeParameter(value)
		if err != nil {
			return nil, nil, err
		}
		params[i] = data
	}
	allText := true
	for _, format := range formats {
		if format != 0 {
			allText = false
			break
		}
	}
	if allText {
		return params, nil, nil
	}
	return params, formats, nil
}

// binaryParameter encodes one value in its target type's verified binary
// layout. A false boolean means "not handled here; use the text encoding".
func (c *conn) binaryParameter(oid uint32, value driver.Value) ([]byte, bool, error) {
	schema, name, ok := c.codecName(oid)
	if !ok || schema != schemaPGCatalog {
		return nil, false, nil
	}
	switch name {
	case pgTypeBool:
		return boolParameter(value)
	case pgTypeInt2, pgTypeInt4, pgTypeInt8, pgTypeOid, pgTypeXid, pgTypeCid:
		return integerParameter(name, value)
	case pgTypeFloat4, pgTypeFloat8:
		return floatParameter(name, value)
	case pgTypeText, pgTypeVarchar, pgTypeBpchar, pgTypeChar, pgTypeName, pgTypeJSON, pgTypeJSONB:
		return textParameter(name, value)
	case pgTypeBytea:
		return bytesParameter(value)
	case pgTypeTimestamp, pgTypeTimestamptz:
		return timestampParameter(name, value)
	case pgTypeDate:
		return dateParameter(value)
	}
	return nil, false, nil
}

func boolParameter(value driver.Value) ([]byte, bool, error) {
	flag, ok := value.(bool)
	if !ok {
		return nil, false, nil
	}
	if flag {
		return []byte{1}, true, nil
	}
	return []byte{0}, true, nil
}

func integerParameter(name string, value driver.Value) ([]byte, bool, error) {
	number, ok := value.(int64)
	if !ok {
		return nil, false, nil
	}
	switch name {
	case pgTypeInt2:
		if number < math.MinInt16 || number > math.MaxInt16 {
			return nil, false, fmt.Errorf("kingbase: parameter %d overflows int2", number)
		}
		return binary.BigEndian.AppendUint16(nil, uint16(number)), true, nil
	case pgTypeInt8:
		return binary.BigEndian.AppendUint64(nil, uint64(number)), true, nil
	case pgTypeOid, pgTypeXid, pgTypeCid:
		// System identifiers are unsigned 32-bit values.
		if number < 0 || number > math.MaxUint32 {
			return nil, false, fmt.Errorf("kingbase: parameter %d overflows oid", number)
		}
		return binary.BigEndian.AppendUint32(nil, uint32(number)), true, nil
	default:
		if number < math.MinInt32 || number > math.MaxInt32 {
			return nil, false, fmt.Errorf("kingbase: parameter %d overflows int4", number)
		}
		return binary.BigEndian.AppendUint32(nil, uint32(number)), true, nil
	}
}

func floatParameter(name string, value driver.Value) ([]byte, bool, error) {
	number, ok := value.(float64)
	if !ok {
		return nil, false, nil
	}
	if name == pgTypeFloat8 {
		return binary.BigEndian.AppendUint64(nil, math.Float64bits(number)), true, nil
	}
	converted := float32(number)
	if !math.IsInf(number, 0) && math.IsInf(float64(converted), 0) {
		return nil, false, fmt.Errorf("kingbase: parameter %v overflows real", number)
	}
	return binary.BigEndian.AppendUint32(nil, math.Float32bits(converted)), true, nil
}

func textParameter(name string, value driver.Value) ([]byte, bool, error) {
	text, ok := value.(string)
	if !ok {
		return nil, false, nil
	}
	if name == pgTypeJSONB {
		return append([]byte{1}, text...), true, nil
	}
	return []byte(text), true, nil
}

func bytesParameter(value driver.Value) ([]byte, bool, error) {
	bytes, ok := value.([]byte)
	if !ok {
		return nil, false, nil
	}
	return bytes, true, nil
}

func timestampParameter(name string, value driver.Value) ([]byte, bool, error) {
	moment, ok := value.(time.Time)
	if !ok {
		return nil, false, nil
	}
	micros := timestampParameterMicros(moment, name == pgTypeTimestamptz)
	return binary.BigEndian.AppendUint64(nil, uint64(micros)), true, nil
}

func dateParameter(value driver.Value) ([]byte, bool, error) {
	moment, ok := value.(time.Time)
	if !ok {
		return nil, false, nil
	}
	// A date is a calendar day, so the caller's wall-clock fields are kept.
	year, month, day := moment.Date()
	midnight := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	days := (midnight.Unix() - pgEpoch.Unix()) / 86400
	if days < math.MinInt32 || days > math.MaxInt32 {
		return nil, false, fmt.Errorf("kingbase: date parameter %s overflows date", moment)
	}
	return binary.BigEndian.AppendUint32(nil, uint32(int32(days))), true, nil
}

// timestampParameterMicros encodes a timestamp parameter as microseconds since
// the PG epoch. A timestamp without time zone carries the caller's wall-clock
// fields, so it is not converted through UTC; a timestamptz carries the instant.
func timestampParameterMicros(moment time.Time, withTimeZone bool) int64 {
	if !withTimeZone {
		moment = time.Date(moment.Year(), moment.Month(), moment.Day(),
			moment.Hour(), moment.Minute(), moment.Second(), moment.Nanosecond(), time.UTC)
	}
	return moment.UTC().UnixMicro() - pgEpoch.UnixMicro()
}
