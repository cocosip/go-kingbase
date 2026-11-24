package gokb

import (
	"fmt"
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
