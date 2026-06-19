package main

import "testing"

// These are pure-Go regression tests for Maple value-semantics fixes found
// while driving the DifferentialThomas decomposition through the Sage backend.
// They need no CAS backend, so they run in the default `go test ./...` suite.

// TestSubsopNullDeletion: subsop(i=NULL, expr) deletes the i-th operand
// (Maple semantics), rather than leaving a NULL/empty slot. The stray slot
// was flowing into DifferentialThomas/SetMaxOrder and tripping its
// "unknown input" guard.
func TestSubsopNullDeletion(t *testing.T) {
	it := NewInterp()
	cases := []struct{ code, want string }{
		{"subsop(2=NULL, [a,b,c]);", "[a, c]"},
		{"nops(subsop(2=NULL,[a,b,c]));", "2"},
		{"subsop(1=NULL, [a,b,c]);", "[b, c]"},
		{"subsop(3=NULL, [a,b,c]);", "[a, b]"},
		{"subsop(2=z, [a,b,c]);", "[a, z, c]"},
	}
	for _, c := range cases {
		v, err := it.Exec(c.code)
		if err != nil {
			t.Fatalf("%s -> error %v", c.code, err)
		}
		if got := printValue(v); got != c.want {
			t.Errorf("%s = %q, want %q", c.code, got, c.want)
		}
	}
}

// TestListArithmetic: Maple does element-wise arithmetic on equal-length
// lists and scalar*list. DifferentialThomas relies on
// LeadingDerivation(p)-LeadingDerivation(q) yielding a list (a multi-index),
// which the Compare* ranking procs then type-check as list(extended_numeric).
func TestListArithmetic(t *testing.T) {
	it := NewInterp()
	cases := []struct{ code, want string }{
		{"[1,2] - [0,1];", "[1, 1]"},
		{"[1,2] + [3,4];", "[4, 6]"},
		{"[0,0] - [0,0];", "[0, 0]"},
		{"2*[1,2];", "[2, 4]"},
		{"[1,2,3] - [1,1,1];", "[0, 1, 2]"},
	}
	for _, c := range cases {
		v, err := it.Exec(c.code)
		if err != nil {
			t.Fatalf("%s -> error %v", c.code, err)
		}
		if got := printValue(v); got != c.want {
			t.Errorf("%s = %q, want %q", c.code, got, c.want)
		}
	}
}

// TestNestedTableAssignment: Maple table auto-vivification for nested indexed
// assignment (t[i][j] := v), plus the rule that an unassigned entry of an
// existing table reads as NULL. DifferentialThomas/ProlongationConsidered does
//
//	if p['ConsideredProlongations']=NULL then p['ConsideredProlongations']:=table([]) fi;
//	if p['ConsideredProlongations'][x]<>true then p['ConsideredProlongations'][x]:=true; ...
//
// Without the NULL-read the sub-table never materialises; without nested
// auto-vivification the `[x]:=true` write errored ("indexed assignment to
// non-name base").
func TestNestedTableAssignment(t *testing.T) {
	it := NewInterp()
	mustExec := func(code string) Value {
		v, err := it.Exec(code)
		if err != nil {
			t.Fatalf("%s -> error %v", code, err)
		}
		return v
	}
	eq := func(code, want string) {
		if got := printValue(mustExec(code)); got != want {
			t.Errorf("%s = %q, want %q", code, got, want)
		}
	}

	// unassigned entry of an existing table reads as NULL, while assigned() is
	// still false (it inspects the table directly).
	mustExec("p := table([]):")
	eq("evalb(p['ConsideredProlongations']=NULL);", "true")
	eq("assigned(p['ConsideredProlongations']);", "false")

	// full auto-vivification of a nested indexed assignment.
	mustExec("p['ConsideredProlongations'][x] := true:")
	eq("p['ConsideredProlongations'][x];", "true")
	eq("assigned(p['ConsideredProlongations']);", "true")
	// the just-written nested key reads back; a sibling unassigned key is NULL.
	eq("evalb(p['ConsideredProlongations'][y]=NULL);", "true")

	// one-shot auto-vivification (no pre-created intermediate table).
	mustExec("q := table([]):")
	mustExec("q['A'][z] := 5:")
	eq("q['A'][z];", "5")
}

// TestExtendedNumericType: type(_, extended_numeric) recognizes numeric values
// plus infinity / -infinity / undefined. DifferentialThomas uses
// type(v, list(extended_numeric)) to validate exponent/multi-index vectors,
// where the "infinity" sentinel appears for unbounded multiplicative variables.
func TestExtendedNumericType(t *testing.T) {
	it := NewInterp()
	cases := []struct{ code, want string }{
		{"type(1, extended_numeric);", "true"},
		{"type(infinity, extended_numeric);", "true"},
		{"type(-infinity, extended_numeric);", "true"},
		{"type(infinity, numeric);", "false"},
		{"type([1,2], list(extended_numeric));", "true"},
		{"type([infinity,infinity], list(extended_numeric));", "true"},
		{"type([1,x], list(extended_numeric));", "false"},
		{"type(infinity, infinity);", "true"},
	}
	for _, c := range cases {
		v, err := it.Exec(c.code)
		if err != nil {
			t.Fatalf("%s -> error %v", c.code, err)
		}
		if got := printValue(v); got != c.want {
			t.Errorf("%s = %q, want %q", c.code, got, c.want)
		}
	}
}
