package main

import "testing"

// TestIndexAssignAutoVivPlaceholder pins Maple's auto-vivification: index-
// assigning into a slot (or name) that holds an unevaluated name auto-creates a
// table. DT's PartialDerivative clears a slot with the self-name idiom
// t['k'] := 't['k']' then does t['k'][x] := false.
func TestIndexAssignAutoVivPlaceholder(t *testing.T) {
	it := NewInterp()
	it.Exec("result := table([]):")
	it.Exec("result['CP'] := 'result['CP']':")
	if _, err := it.Exec("result['CP'][xx] := false:"); err != nil {
		t.Fatalf("nested auto-viv: %v", err)
	}
	v, err := it.Exec("result['CP'][xx];")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := printValue(v); got != "false" {
		t.Fatalf("got %q, want false", got)
	}
	// plain-name placeholder auto-vivifies too
	it2 := NewInterp()
	it2.Exec("foo := 'foo':")
	if _, err := it2.Exec("foo[k] := 5:"); err != nil {
		t.Fatalf("name auto-viv: %v", err)
	}
	if v, _ := it2.Exec("foo[k];"); printValue(v) != "5" {
		t.Fatalf("name auto-viv read: got %q", printValue(v))
	}
}
