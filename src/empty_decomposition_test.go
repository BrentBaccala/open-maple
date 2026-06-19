package main

import "testing"

// TestEmptyDecompositionFixes pins the three interpreter fixes from task 414
// that turned the canonical smoke decomposition from "runs but returns []" into
// "returns the correct single u=0 component". Each was a distinct masking layer:
//
//  1. Reference-parameter write-through. A table/proc passed to a proc as a bare
//     name, then *reassigned* inside the callee (`q := PseudoRemainder(...)`),
//     must write through to the caller's name — Maple's reference-parameter
//     semantics. DifferentialThomas's `ReduceWithSideEffects` relies on this: it
//     reduces its poly-object parameter by rebinding it and is called purely for
//     that side effect (the caller ignores the return value). Without the
//     write-through the reduction was lost, the equation mis-reduced in the
//     later tail pass, the leader changed, and the system was *falsely* declared
//     Inconsistent — dropping the surviving u=0 component (decomposition -> []).
//
//  2. ListTools:-FindMaximalElement (with the `position` option), used by
//     FactorModuleBasisFromTreeRecursive as
//     `[ListTools[FindMaximalElement](subivar,position)][2]` — was unimplemented,
//     so the index `[2]` raised "index 2 out of range 1..1".
//
//  3. max/min flatten list/set arguments. `max([0])` must be 0, not the list
//     [0]; DT does `max(map(a->a[i],leafs))` (a one-element list) and then
//     `maxdeg+1`, which otherwise became an unsimplified list+int "$ sequence".
//
// Pure-Go (no Sage); part of the default suite.
func TestEmptyDecompositionFixes(t *testing.T) {
	t.Run("param write-through (DT ReduceWithSideEffects pattern)", func(t *testing.T) {
		it := NewInterp()
		// inner rebinds its parameter to a fresh table; the caller passes its own
		// table-valued name and ignores the return value (exactly the DT pattern).
		code := `
inner := proc(p) p := table(['Polynom'=42]); return 0; end proc:
outer := proc()
  local q;
  q := table(['Polynom'=1]);
  inner(q);
  return q['Polynom'];
end proc:
outer();
`
		v, err := it.Exec(code)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got := printValue(v); got != "42" {
			t.Fatalf("param write-through: got %q, want 42 (caller q must see the callee's rebind)", got)
		}
	})

	t.Run("write-through chains through forwarded parameters", func(t *testing.T) {
		it := NewInterp()
		// middle forwards its own parameter onward; the write must propagate two
		// scopes up to outer's q.
		code := `
inner := proc(p) p := table(['X'=7]); return 0; end proc:
middle := proc(m) inner(m); return 0; end proc:
outer := proc() local q; q := table(['X'=0]); middle(q); return q['X']; end proc:
outer();
`
		v, err := it.Exec(code)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got := printValue(v); got != "7" {
			t.Fatalf("chained write-through: got %q, want 7", got)
		}
	})

	t.Run("in-place mutation still propagates (no regression)", func(t *testing.T) {
		it := NewInterp()
		code := `
inner := proc(p) p['X'] := 9; return 0; end proc:
outer := proc() local q; q := table(['X'=0]); inner(q); return q['X']; end proc:
outer();
`
		v, err := it.Exec(code)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got := printValue(v); got != "9" {
			t.Fatalf("in-place mutation: got %q, want 9", got)
		}
	})

	t.Run("plain (non-name) argument does NOT get write-through", func(t *testing.T) {
		it := NewInterp()
		// A literal table argument has no caller name to write back to; the
		// callee's rebind must stay local (here it simply has no observable
		// effect). This guards against over-broad write-through.
		code := `
inner := proc(p) p := table(['X'=5]); return p['X']; end proc:
inner(table(['X'=1]));
`
		v, err := it.Exec(code)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got := printValue(v); got != "5" {
			t.Fatalf("literal-arg rebind: got %q, want 5 (callee-local)", got)
		}
	})

	t.Run("ListTools FindMaximalElement with position", func(t *testing.T) {
		it := NewInterp()
		// [FindMaximalElement([1,1],position)][2] -> index of the max (1-based).
		v, err := it.Exec("[ListTools[FindMaximalElement]([1,1],position)][2];")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got := printValue(v); got != "1" {
			t.Fatalf("FindMaximalElement position: got %q, want 1", got)
		}
		// non-position form returns the element itself
		v2, err := it.Exec("ListTools[FindMaximalElement]([3,7,5]);")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got := printValue(v2); got != "7" {
			t.Fatalf("FindMaximalElement value: got %q, want 7", got)
		}
		// position of a later max
		v3, err := it.Exec("[ListTools[FindMaximalElement]([3,7,5],position)][2];")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got := printValue(v3); got != "2" {
			t.Fatalf("FindMaximalElement position-2: got %q, want 2", got)
		}
	})

	t.Run("max/min flatten a list argument", func(t *testing.T) {
		it := NewInterp()
		cases := []struct{ expr, want string }{
			{"max([0]);", "0"},
			{"max([3,1,2]);", "3"},
			{"min([3,1,2]);", "1"},
			{"max([0])+1;", "1"}, // the DT maxdeg+1 form must simplify to an int
			{"max([2,5],3);", "5"},
		}
		for _, c := range cases {
			v, err := it.Exec(c.expr)
			if err != nil {
				t.Fatalf("%s err: %v", c.expr, err)
			}
			if got := printValue(v); got != c.want {
				t.Fatalf("%s: got %q, want %q", c.expr, got, c.want)
			}
		}
	})
}
