package kingbase

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestOutArgumentInputAndAssignment(t *testing.T) {
	value := int64(2)
	out, err := newOutArgument(sql.Out{Dest: &value, In: true})
	if err != nil {
		t.Fatalf("newOutArgument: %v", err)
	}
	if out.input != int64(2) || out.dest.Kind() != reflect.Int64 {
		t.Errorf("INOUT argument = %#v, want input 2 and int64 dest", out)
	}
	out, err = newOutArgument(sql.Out{Dest: &value})
	if err != nil {
		t.Fatalf("OUT argument: %v", err)
	}
	if out.input != nil {
		t.Errorf("OUT input = %v, want nil", out.input)
	}
	for name, destination := range map[string]any{
		"nil destination": (*int64)(nil),
		"non-pointer":     int64(0),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newOutArgument(sql.Out{Dest: destination}); err == nil {
				t.Error("newOutArgument accepted an invalid destination")
			}
		})
	}

	if err := assignOutValue(reflect.ValueOf(&value).Elem(), int64(7)); err != nil || value != 7 {
		t.Errorf("int assignment = %d, %v, want 7", value, err)
	}
	var text string
	if err := assignOutValue(reflect.ValueOf(&text).Elem(), []byte("hello")); err != nil || text != "hello" {
		t.Errorf("text assignment = %q, %v", text, err)
	}
	var nullable sql.NullInt64
	if err := assignOutValue(reflect.ValueOf(&nullable).Elem(), nil); err != nil || nullable.Valid {
		t.Errorf("NULL assignment = %#v, %v, want invalid", nullable, err)
	}
	if err := assignOutValue(reflect.ValueOf(new(struct{})).Elem(), int64(1)); err == nil {
		t.Error("assignOutValue accepted a struct destination")
	}
}

func TestExecRoutineWithOutputsRejectsNonCall(t *testing.T) {
	c := &conn{mode: ModePG}
	var value int64
	out, err := newOutArgument(sql.Out{Dest: &value})
	if err != nil {
		t.Fatalf("newOutArgument: %v", err)
	}
	if _, err := c.execRoutineWithOutputs(context.Background(), "SELECT 1", nil, []outArgument{out}); err == nil {
		t.Error("execRoutineWithOutputs accepted a non-CALL statement")
	}
}

func TestExecRoutineWithOutputsAssignsTestpeerRow(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		parsePayload, ok := expectMessage(peer, wire.MsgParse)
		if !ok {
			return
		}
		_, statement, _, err := wire.DecodeParse(parsePayload)
		if err != nil || statement != "CALL p($1)" {
			peer.Errorf("Parse = %q, err %v", statement, err)
			return
		}
		bindPayload, ok := expectMessage(peer, wire.MsgBind)
		if !ok {
			return
		}
		_, _, _, params, _, err := wire.DecodeBind(bindPayload)
		if err != nil || len(params) != 1 || params[0] != nil {
			peer.Errorf("Bind params = %v, err %v, want one NULL", params, err)
			return
		}
		if _, ok := expectMessage(peer, wire.MsgDescribe); !ok {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgExecute); !ok {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgSync); !ok {
			return
		}
		_ = peer.WriteMessage(wire.MsgParseComplete, nil)
		_ = peer.WriteMessage(wire.MsgBindComplete, nil)
		if err := peer.RowDescription([]wire.ColumnDescription{
			{Name: "b", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
		}); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("3")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.CommandComplete("CALL"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})
	db := sql.OpenDB(newConnector(peerConfig(t, srv, "")))
	defer func() { _ = db.Close() }()
	value := int64(0)
	if _, err := db.ExecContext(context.Background(), "CALL p($1)", sql.Out{Dest: &value}); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if value != 3 {
		t.Errorf("output = %d, want 3", value)
	}
}

func TestAssignOutValueTinyintRanges(t *testing.T) {
	var signed int8
	if err := assignOutValue(reflect.ValueOf(&signed).Elem(), int64(127)); err != nil || signed != 127 {
		t.Errorf("int8 127 = %d, %v", signed, err)
	}
	if err := assignOutValue(reflect.ValueOf(&signed).Elem(), int64(128)); err == nil {
		t.Error("int8 accepted 128")
	}
	if err := assignOutValue(reflect.ValueOf(&signed).Elem(), int64(-128)); err != nil || signed != -128 {
		t.Errorf("int8 -128 = %d, %v", signed, err)
	}
	if err := assignOutValue(reflect.ValueOf(&signed).Elem(), int64(-129)); err == nil {
		t.Error("int8 accepted -129")
	}
	var unsigned uint8
	if err := assignOutValue(reflect.ValueOf(&unsigned).Elem(), int64(255)); err != nil || unsigned != 255 {
		t.Errorf("uint8 255 = %d, %v", unsigned, err)
	}
	if err := assignOutValue(reflect.ValueOf(&unsigned).Elem(), int64(256)); err == nil {
		t.Error("uint8 accepted 256")
	}
	if err := assignOutValue(reflect.ValueOf(&unsigned).Elem(), int64(-1)); err == nil {
		t.Error("uint8 accepted -1")
	}
	var nullable sql.NullInt64
	if err := assignOutValue(reflect.ValueOf(&nullable).Elem(), nil); err != nil || nullable.Valid {
		t.Errorf("tinyint NULL = %#v, %v", nullable, err)
	}
	if err := assignOutValue(reflect.ValueOf(&nullable).Elem(), int64(127)); err != nil ||
		!nullable.Valid || nullable.Int64 != 127 {
		t.Errorf("tinyint scanner = %#v, %v", nullable, err)
	}
}
