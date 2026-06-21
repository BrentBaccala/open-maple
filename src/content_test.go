package main

import (
	"os"
	"testing"
)

// TestNativeContent pins the ONE-ARGUMENT content(p) = the positive rational gcd
// of p's numeric coefficients (gcd of numerators / lcm of denominators). This is
// the form nativeContent computes; the two-argument content(p, V) is a different
// (polynomial) computation handled by Sage — see TestContentWrtVariables.
// content is the single most frequent CAS op on multi-variable systems; computing
// the one-arg form natively from the polyNF coefficients avoids a Sage round-trip.
// Includes division-form inputs (x/2 = x*2^-1), exercising constant-power toPolyNF.
func TestNativeContent(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"content(2*x + 4);", "2"},
		{"content(x/2 + 1/3);", "1/6"},
		{"content(6*x*y + 9*y);", "3"},
		{"content(-2*x - 4);", "2"},     // always positive
		{"content(0);", "0"},
		{"content(u[1,0]*5 - 10);", "5"},
		{"content(x/4 + x/6);", "5/12"}, // x/4 + x/6 = 5x/12
		{"content(3*x);", "3"},
		{"content(x + y);", "1"},
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
}

// TestContentWrtVariables pins the TWO-ARGUMENT content(p, V): p viewed as a
// polynomial in the variables V, the gcd of its coefficients (polynomials in the
// remaining variables). This is what DT's SimplifyPolynom calls — content(p,
// Leader(p)) and content(p, DiffVarList(p)) — and then divides p by. The bug it
// regresses: nativeContent and op_content both ignored V and returned the
// rational content, so on the hydrogen ansatz (the first system with explicit
// independent variables x,y,z as polynomial factors) the x / x^2+y^2 common
// factors were never divided out, corrupting the reduction. The corrupted leaders
// made consistent systems reduce to a nonzero field element, which DT declares
// Inconsistent — pruning ~40-50 components (decomposition collapsed 70-80 -> 29).
// Sage-gated (the two-arg form routes to the Sage backend).
func TestContentWrtVariables(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage to run content(p, vars)")
	}
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"content(x*u[0,0] + x*u[1,0], [u[0,0], u[1,0]]);", "x"},
		{"content((x^2+y^2)*u[0,0]^2 - (x^2+y^2)*u[1,0], u[0,0]);", "x^2 + y^2"},
		{"content(6*u[0,0] + 9*u[1,0], [u[0,0], u[1,0]]);", "3"},
		{"content(x*y*u[0,0] + x*y^2*u[1,0], [u[0,0], u[1,0]]);", "x*y"},
	}
	for _, c := range cases {
		v, err := it.Exec(c.expr)
		if err != nil {
			t.Fatalf("%s err: %v", c.expr, err)
		}
		// compare by value (term order of the polynomial content is incidental)
		want, _ := it.Exec(c.want + ";")
		if compareValues(v, want) != 0 {
			t.Fatalf("%s: got %q, want %q", c.expr, printValue(v), c.want)
		}
	}
}
