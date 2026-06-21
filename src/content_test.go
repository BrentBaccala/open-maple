package main

import "testing"

// TestNativeContent pins content(p) = the positive rational gcd of p's numeric
// coefficients (gcd of numerators / lcm of denominators), matching Sage's
// op_content — which ignores the variable argument (numeric content over the
// whole ring). content is the single most frequent CAS op on multi-variable
// systems; computing it natively from the polyNF coefficients avoids a Sage
// round-trip. Includes division-form inputs (x/2 = x*2^-1), which exercise the
// constant-power handling in toPolyNF.
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
