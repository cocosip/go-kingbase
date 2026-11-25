package kingbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// outArgument is the driver-internal form of sql.Out. It keeps the destination
// outside the wire encoder while preserving the input for an INOUT argument.
type outArgument struct {
	dest  reflect.Value
	input driver.Value
}

func newOutArgument(out sql.Out) (outArgument, error) {
	if out.Dest == nil {
		return outArgument{}, errors.New("sql.Out.Dest is nil")
	}
	pointer := reflect.ValueOf(out.Dest)
	if pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return outArgument{}, errors.New("sql.Out.Dest must be a non-nil pointer")
	}
	dest := pointer.Elem()
	if !dest.CanSet() {
		return outArgument{}, errors.New("sql.Out.Dest is not settable")
	}
	var input driver.Value
	if out.In {
		converted, err := driver.DefaultParameterConverter.ConvertValue(dest.Interface())
		if err != nil {
			return outArgument{}, fmt.Errorf("sql.Out input: %w", err)
		}
		input = converted
	}
	return outArgument{dest: dest, input: input}, nil
}

// extractOutArguments removes sql.Out wrappers before parameter planning. Only
// ExecContext consumes the returned destinations; Query rejects them because a
// caller cannot receive an output value through sql.Rows.
func extractOutArguments(args []driver.NamedValue) ([]driver.NamedValue, []outArgument) {
	var normalized []driver.NamedValue
	outputs := make([]outArgument, 0)
	for index, argument := range args {
		out, ok := argument.Value.(outArgument)
		if !ok {
			continue
		}
		if normalized == nil {
			normalized = append([]driver.NamedValue(nil), args...)
		}
		normalized[index].Value = out.input
		outputs = append(outputs, out)
	}
	if normalized == nil {
		return args, nil
	}
	return normalized, outputs
}

func isCallStatement(query string) bool {
	trimmed := strings.TrimSpace(query)
	if len(trimmed) < 5 || !strings.EqualFold(trimmed[:4], "CALL") || !isSpaceByte(trimmed[4]) {
		return false
	}
	return true
}

// execRoutineWithOutputs runs a PG CALL through the normal query path, reads
// its single output row, and assigns each value only after the command has
// completed. The connection is drained before the result is returned.
func (c *conn) execRoutineWithOutputs(ctx context.Context, query string, args []driver.NamedValue,
	outputs []outArgument) (driver.Result, error) {
	if c.mode != ModePG {
		return nil, fmt.Errorf("kingbase: sql.Out is supported only by the verified PG profile")
	}
	if !isCallStatement(query) {
		return nil, fmt.Errorf("kingbase: sql.Out requires a CALL statement")
	}
	rows, err := c.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return assignRoutineRows(rows, outputs)
}

func assignRoutineRows(rows driver.Rows, outputs []outArgument) (driver.Result, error) {
	defer func() { _ = rows.Close() }()
	values := make([]driver.Value, len(rows.Columns()))
	if len(values) != len(outputs) {
		return nil, fmt.Errorf("kingbase: CALL returned %d output values for %d sql.Out arguments",
			len(values), len(outputs))
	}
	if err := rows.Next(values); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("kingbase: CALL returned no output row")
		}
		return nil, err
	}
	if err := rows.Next(make([]driver.Value, len(values))); err == nil {
		return nil, fmt.Errorf("kingbase: CALL returned more than one output row")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	for index, output := range outputs {
		if err := assignOutValue(output.dest, values[index]); err != nil {
			return nil, fmt.Errorf("kingbase: assign sql.Out argument %d: %w", index+1, err)
		}
	}
	return &result{rowsAffected: -1}, nil
}

func (s *stmt) execRoutineWithOutputs(ctx context.Context, args []driver.NamedValue,
	outputs []outArgument) (driver.Result, error) {
	if s.conn.mode != ModePG {
		return nil, fmt.Errorf("kingbase: sql.Out is supported only by the verified PG profile")
	}
	if !isCallStatement(s.query) {
		return nil, fmt.Errorf("kingbase: sql.Out requires a CALL statement")
	}
	rows, err := s.QueryContext(ctx, args)
	if err != nil {
		return nil, err
	}
	return assignRoutineRows(rows, outputs)
}

func assignOutValue(dest reflect.Value, value driver.Value) error {
	if dest.CanAddr() {
		if scanner, ok := dest.Addr().Interface().(sql.Scanner); ok {
			return scanner.Scan(value)
		}
	}
	if value == nil {
		return errors.New("cannot assign NULL to a non-scanner destination")
	}
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(dest.Type()) {
		dest.Set(source)
		return nil
	}
	switch dest.Kind() {
	case reflect.String:
		switch typed := value.(type) {
		case string:
			dest.SetString(typed)
		case []byte:
			dest.SetString(string(typed))
		default:
			return fmt.Errorf("cannot assign %T to string", value)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number, ok := value.(int64)
		if !ok || dest.OverflowInt(number) {
			return fmt.Errorf("cannot assign %T to %s", value, dest.Type())
		}
		dest.SetInt(number)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		number, ok := value.(int64)
		if !ok || number < 0 || dest.OverflowUint(uint64(number)) {
			return fmt.Errorf("cannot assign %T to %s", value, dest.Type())
		}
		dest.SetUint(uint64(number))
	case reflect.Float32, reflect.Float64:
		number, ok := value.(float64)
		if !ok || dest.OverflowFloat(number) {
			return fmt.Errorf("cannot assign %T to %s", value, dest.Type())
		}
		dest.SetFloat(number)
	case reflect.Bool:
		boolean, ok := value.(bool)
		if !ok {
			return fmt.Errorf("cannot assign %T to bool", value)
		}
		dest.SetBool(boolean)
	case reflect.Slice:
		bytes, ok := value.([]byte)
		if !ok || dest.Type().Elem().Kind() != reflect.Uint8 {
			return fmt.Errorf("cannot assign %T to %s", value, dest.Type())
		}
		dest.SetBytes(bytes)
	default:
		return fmt.Errorf("cannot assign %T to %s", value, dest.Type())
	}
	return nil
}
