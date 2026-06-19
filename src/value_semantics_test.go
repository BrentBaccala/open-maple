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
