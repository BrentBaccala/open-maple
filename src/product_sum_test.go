package main

import "testing"

// TestProductSumBinder pins product(f, k=m..n) / sum(f, k=m..n) as explicit
// finite product/sum over a concrete integer range, BINDING the index k so the
// body can index a collection by k (DT: product(ivar[j]^currentpos[j], j=1..n)).
// Pure-Go (no Sage); part of the default suite.
func TestProductSumBinder(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		// index-binding over a list — the DT PrincipalDerivativesFromTree shape
		{"product(L[j], j=1..3);", "24"}, // 2*3*4
		{"sum(L[j], j=1..3);", "9"},      // 2+3+4
		// empty range: product=1, sum=0
		{"product(j, j=1..0);", "1"},
		{"sum(j, j=1..0);", "0"},
		// plain integer bounds, no collection
		{"product(j, j=1..4);", "24"},
		{"sum(j^2, j=1..3);", "14"}, // 1+4+9
	}
	mustExec := func(code string) {
		if _, err := it.Exec(code); err != nil {
			t.Fatalf("setup %q: %v", code, err)
		}
	}
	mustExec("L := [2, 3, 4]:")
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

// TestPrintProdNegOneFold pins printValue folding a leading -1 coefficient to a
// unary minus, so a subtracted term renders as a-b (Maple surface), not a-1*b.
func TestPrintProdNegOneFold(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"a - b;", "a - b"},
		{"a - 2*b;", "a - 2*b"},
		{"-b;", "-b"},
		{"x - y - z;", "x - y - z"},
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
