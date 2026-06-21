package main

import "testing"

// TestNativeGCD pins gcd(a,b) to the Sage QQ[vars] conventions: both constants
// give the positive rational gcd; otherwise the MONIC gcd (content stripped,
// leading coeff 1, so a nonzero constant is a unit → 1). Univariate is a monic
// Euclidean GCD over Q; genuinely multivariate inputs fall back to Sage.
func TestNativeGCD(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"gcd(2, -u[0,0]);", "1"},
		{"gcd(x^2 - 1, x - 1);", "x - 1"},
		{"gcd(2*x - 2, 4*x - 4);", "x - 1"},
		{"gcd(2, 4);", "2"},
		{"gcd(6, 9);", "3"},
		{"gcd(3*x^2 - 3, 6*x + 6);", "x + 1"},
		{"gcd(0, 2*x - 4);", "x - 2"},
		{"gcd(x - 2, 0);", "x - 2"},
		{"gcd(u[1,0]^2 - 1, u[1,0] + 1);", "u[1, 0] + 1"},
		{"gcd(0, 0);", "0"},
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
