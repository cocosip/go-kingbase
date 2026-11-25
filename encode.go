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
	case float64:
		return strconv.AppendFloat(nil, typed, 'g', -1, 64), nil
	case bool:
		if typed {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		return []byte(typed), nil
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
	case "bool":
		flag, ok := value.(bool)
		if !ok {
			return nil, false, nil
		}
		if flag {
			return []byte{1}, true, nil
		}
		return []byte{0}, true, nil
	case pgTypeInt2, "int4", pgTypeInt8, "oid", "xid", "cid":
		number, ok := value.(int64)
		if !ok {
			return nil, false, nil
		}
		switch name {
		case "int2":
			if number < math.MinInt16 || number > math.MaxInt16 {
				return nil, false, fmt.Errorf("kingbase: parameter %d overflows int2", number)
			}
			return binary.BigEndian.AppendUint16(nil, uint16(number)), true, nil
		case "int8":
			return binary.BigEndian.AppendUint64(nil, uint64(number)), true, nil
		default:
			if number < math.MinInt32 || number > math.MaxInt32 {
				return nil, false, fmt.Errorf("kingbase: parameter %d overflows int4", number)
			}
			return binary.BigEndian.AppendUint32(nil, uint32(number)), true, nil
		}
	case "float4":
		number, ok := value.(float64)
		if !ok {
			return nil, false, nil
		}
		return binary.BigEndian.AppendUint32(nil, math.Float32bits(float32(number))), true, nil
	case "float8":
		number, ok := value.(float64)
		if !ok {
			return nil, false, nil
		}
		return binary.BigEndian.AppendUint64(nil, math.Float64bits(number)), true, nil
	case "text", "varchar", "bpchar", "char", "name", "json":
		text, ok := value.(string)
		if !ok {
			return nil, false, nil
		}
		return []byte(text), true, nil
	case "jsonb":
		text, ok := value.(string)
		if !ok {
			return nil, false, nil
		}
		return append([]byte{1}, text...), true, nil
	case "bytea":
		bytes, ok := value.([]byte)
		if !ok {
			return nil, false, nil
		}
		return bytes, true, nil
	case "timestamp", "timestamptz":
		moment, ok := value.(time.Time)
		if !ok {
			return nil, false, nil
		}
		micros := moment.UTC().UnixMicro() - pgEpoch.UnixMicro()
		return binary.BigEndian.AppendUint64(nil, uint64(micros)), true, nil
	case "date":
		moment, ok := value.(time.Time)
		if !ok {
			return nil, false, nil
		}
		year, month, day := moment.UTC().Date()
		midnight := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
		days := (midnight.Unix() - pgEpoch.Unix()) / 86400
		if days < math.MinInt32 || days > math.MaxInt32 {
			return nil, false, fmt.Errorf("kingbase: date parameter %s overflows date", moment)
		}
		return binary.BigEndian.AppendUint32(nil, uint32(int32(days))), true, nil
	}
	return nil, false, nil
}
