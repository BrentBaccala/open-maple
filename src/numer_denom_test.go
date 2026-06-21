package main

import "testing"

// TestNativeNumerDenom pins numer/denom on scalar arguments to the Sage backend's
// polynomial-fraction convention (numer/denom over Frac(QQ[vars])): a constant —
// even a rational like -1/2 — is its own numerator with denominator 1, NOT the
// rational numerator/denominator. The verify harness caught a native version that
// returned the rational numerator (numer(-1/2) = -1) instead of -1/2.
func TestNativeNumerDenom(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"numer(-1/2);", "-1/2"},
		{"denom(-1/2);", "1"},
		{"numer(5);", "5"},
		{"denom(5);", "1"},
		{"numer(3/4);", "3/4"},
		{"denom(3/4);", "1"},
		{"numer(u[1,0]);", "u[1, 0]"},
		{"denom(u[1,0]);", "1"},
		{"numer(x);", "x"},
		{"denom(x);", "1"},
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
