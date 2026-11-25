package kingbase

import "testing"

func TestBuiltinShapeMismatchDisablesCodec(t *testing.T) {
	cases := []struct {
		name  string
		info  typeInfo
		valid bool
	}{
		{"int4 correct", typeInfo{schema: schemaPGCatalog, name: "int4", kind: 'b', length: 4}, true},
		{"int4 wrong length", typeInfo{schema: schemaPGCatalog, name: "int4", kind: 'b', length: 8}, false},
		{"int4 wrong kind", typeInfo{schema: schemaPGCatalog, name: "int4", kind: 'd', length: 4}, false},
		{"unknown name", typeInfo{schema: schemaPGCatalog, name: "not_builtin", kind: 'b', length: 4}, false},
		{"varchar variable length", typeInfo{schema: schemaPGCatalog, name: "varchar", kind: 'b', length: -1}, true},
		{"varchar fixed length", typeInfo{schema: schemaPGCatalog, name: "varchar", kind: 'b', length: 4}, false},
		{"bit variable length", typeInfo{schema: schemaPGCatalog, name: pgTypeBit, kind: 'b', length: -1}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			catalog := &typeCatalog{byOID: map[uint32]typeInfo{23: testCase.info}}
			connection := &conn{catalog: catalog}
			_, _, ok := connection.codecName(23)
			if ok != testCase.valid {
				t.Errorf("codecName = %t, want %t", ok, testCase.valid)
			}
		})
	}
}
