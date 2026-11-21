package daemon

import "testing"

func TestTemplateRowID(t *testing.T) {
	const act = "0123456789abcdef0123456789abcdef"
	first := templateRowID(act)
	if first != templateRowID(act) {
		t.Fatal("same action id produced different row ids")
	}
	if first < templateBase || first >= 2*templateBase {
		t.Fatalf("template row id %d is outside its reserved range", first)
	}
	if first == templateRowID("1123456789abcdef0123456789abcdef") {
		t.Fatal("different action ids produced the same row id")
	}
}
