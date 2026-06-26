package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestEvalAddChainFlatten pins evalAddChain: a '+'/'-' operator chain is
// flattened and simplified in a single linear pass instead of folded pairwise
// (which is O(N^2) — each '+' node re-flattens and re-simplifies the whole
// growing partial sum — and recurses N-deep down the operator spine). The
// hydrogen-ansatz + Schrodinger-PDE reduction built a ~10^5-term intermediate
// sum that ran for hours on the pairwise fold; this is the regression guard.
func TestEvalAddChainFlatten(t *testing.T) {
	// drop spaces so the comparison doesn't depend on pretty-print spacing
	norm := func(s string) string { return strings.ReplaceAll(s, " ", "") }
	cases := []struct{ expr, want string }{
		{"a+b+c", "a+b+c"},          // flatten, order preserved
		{"x+2+y+3", "x+y+5"},        // rationals folded into one trailing term
		{"x-3", "x-3"},              // subtraction sign
		{"x-y+z", "x-y+z"},          // mixed signs, order
		{"a-b-c", "a-b-c"},          // chained subtraction
		{"1.5+2+3", "6.5"},          // a Float forces the exact pairwise fallback
		{"x+1.5+y", "x+1.5+y"},      // Float mixed with symbols (fallback)
	}
	for _, c := range cases {
		it := NewInterp()
		v, err := it.Exec(c.expr + ";")
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := norm(printValue(v)); got != c.want {
			t.Fatalf("%s: got %q want %q", c.expr, got, c.want)
		}
	}

	// nops of a flattened chain == number of terms
	it := NewInterp()
	if v, _ := it.Exec("nops(a+b+c+d+e);"); printValue(v) != "5" {
		t.Fatalf("nops flatten: got %q want 5", printValue(v))
	}

	// Large chain of distinct symbols: the pairwise fold is O(N^2) and would
	// crawl here (the unfixed engine takes >120 s at this N); the flattened path
	// is linear. Correctness is checked via nops; the test merely completing
	// quickly is the performance guard.
	const N = 50000
	var sb strings.Builder
	for i := 1; i <= N; i++ {
		if i > 1 {
			sb.WriteByte('+')
		}
		fmt.Fprintf(&sb, "x%d", i)
	}
	it2 := NewInterp()
	v, err := it2.Exec("nops(" + sb.String() + ");")
	if err != nil {
		t.Fatalf("large sum: %v", err)
	}
	if got, want := printValue(v), fmt.Sprintf("%d", N); got != want {
		t.Fatalf("large sum nops: got %q want %q", got, want)
	}
}
