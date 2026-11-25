package kingbase

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"

	"github.com/cocosip/go-kingbase/internal/testpeer"
	"github.com/cocosip/go-kingbase/internal/wire"
)

func TestColumnMetadata(t *testing.T) {
	srv := testpeer.Start(t, func(peer *testpeer.Conn) {
		if !startupOK(peer) {
			return
		}
		if _, ok := expectMessage(peer, wire.MsgQuery); !ok {
			return
		}
		numericTypmod := int32(10<<16|2) + 4
		negativeScaleTypmod := int32(10<<16|0x7fe) + 4
		columns := []wire.ColumnDescription{
			{Name: "v", TypeOID: 1043, TypeLen: -1, TypeMod: 54, Format: 0},
			{Name: "n", TypeOID: 1700, TypeLen: -1, TypeMod: numericTypmod, Format: 0},
			{Name: "nn", TypeOID: 1700, TypeLen: -1, TypeMod: negativeScaleTypmod, Format: 0},
			{Name: "b", TypeOID: 17, TypeLen: -1, TypeMod: -1, Format: 0},
			{Name: "i", TypeOID: 23, TypeLen: 4, TypeMod: -1, Format: 0},
		}
		if err := peer.RowDescription(columns); err != nil {
			peer.Errorf("RowDescription: %v", err)
			return
		}
		if err := peer.DataRow([][]byte{[]byte("x"), []byte("1.00"), []byte("1.00"), []byte("\\x01"), []byte("1")}); err != nil {
			peer.Errorf("DataRow: %v", err)
			return
		}
		if err := peer.CommandComplete("SELECT 1"); err != nil {
			peer.Errorf("CommandComplete: %v", err)
			return
		}
		if err := peer.ReadyForQuery(wire.StatusIdle); err != nil {
			peer.Errorf("ReadyForQuery: %v", err)
			return
		}
		waitTerminate(peer)
	})

	physical, err := connectPeer(t, srv, "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := physical.QueryContext(context.Background(), "SELECT v, n, b, i", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	lengths := result.(driver.RowsColumnTypeLength)
	precisions := result.(driver.RowsColumnTypePrecisionScale)
	nullable := result.(driver.RowsColumnTypeNullable)

	if length, ok := lengths.ColumnTypeLength(0); length != 50 || !ok {
		t.Errorf("varchar length = %d, %v, want 50, true", length, ok)
	}
	if precision, scale, ok := precisions.ColumnTypePrecisionScale(1); precision != 10 || scale != 2 || !ok {
		t.Errorf("numeric precision = %d,%d,%v, want 10,2,true", precision, scale, ok)
	}
	if precision, scale, ok := precisions.ColumnTypePrecisionScale(2); precision != 10 || scale != -2 || !ok {
		t.Errorf("negative-scale precision = %d,%d,%v, want 10,-2,true", precision, scale, ok)
	}
	if _, _, ok := precisions.ColumnTypePrecisionScale(0); ok {
		t.Error("varchar reported a numeric precision")
	}
	if length, ok := lengths.ColumnTypeLength(3); length != 0 || ok {
		t.Errorf("bytea length = %d, %v, want unknown", length, ok)
	}
	if length, ok := lengths.ColumnTypeLength(4); length != 4 || !ok {
		t.Errorf("int4 length = %d, %v, want 4, true", length, ok)
	}
	if possible, ok := nullable.ColumnTypeNullable(0); !possible || ok {
		t.Errorf("nullable = %v, %v, want true,false (unknown)", possible, ok)
	}
	if name := result.(driver.RowsColumnTypeDatabaseTypeName).ColumnTypeDatabaseTypeName(0); name != "VARCHAR" {
		t.Errorf("DatabaseTypeName = %q, want VARCHAR", name)
	}
	if scan := result.(driver.RowsColumnTypeScanType).ColumnTypeScanType(0); scan != reflect.TypeOf("") {
		t.Errorf("ScanType = %v, want string", scan)
	}
	if err := result.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = physical.Close()
}
