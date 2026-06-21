package main

import "testing"

// TestLambdaIndexHead pins that a bound name used as the HEAD of an index
// expression resolves to its value: (a -> a[0])(c1) = c1[0]. evalIndex was
// returning an inert Indexed with the *literal* parameter name, so DT's
// SubstituteDVar — subs(map(a->a=a[0$nops(ivar)], dvar), p), which builds
// d = d[0] for each dvar — produced d = a[0] for every dvar, leaking a phantom
// jet variable `a[0]` into the polynomials (seen on the EliminateFunction
// Reduce-worksheet system, where a bare dvar `c1` became `a[0]`).
func TestLambdaIndexHead(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"(a -> a[0])(c1);", "c1[0]"},
		{"map(a -> a[0], [F, c1]);", "[F[0], c1[0]]"},
		{"map(a -> a = a[0], [F, c1]);", "[F = F[0], c1 = c1[0]]"},
		{"(a -> a[0,0])(u);", "u[0, 0]"},
		{"(a -> a+1)(5);", "6"},
		// an unbound jet head is still kept inert (u not bound -> u[1,0])
		{"u[1,0];", "u[1, 0]"},
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
