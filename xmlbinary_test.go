package kingbase

import (
	"reflect"
	"testing"
)

func TestXMLBinaryIsText(t *testing.T) {
	const document = `<root attr="v">text</root>`
	value, ok := binaryValue(schemaPGCatalog, pgTypeXML, []byte(document))
	if !ok || value != document {
		t.Errorf("binaryValue(xml) = %#v, %v, want the exact text", value, ok)
	}
	if !binaryLayoutKnown(pgTypeXML) {
		t.Fatal("xml binary layout is not known")
	}
	if got := scanType(schemaPGCatalog, pgTypeXML, 1); got != reflect.TypeOf("") {
		t.Errorf("xml binary scan type = %v, want string", got)
	}
}
