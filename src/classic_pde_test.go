package main

import (
	"os"
	"testing"
)

// TestClassicPDEs exercises a spread of standard-ranking differential systems
// end to end, pinning the pretty-printed first component. Covers nonlinear
// (Burgers), second-order in both directions (wave — validates that diff folds
// over repeated variables), a factoring multi-component split, and an
// overdetermined system. Skipped unless OPENMAPLE_CAS=sage.
func TestClassicPDEs(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage")
	}
	cases := []struct{ name, ranking, eqs string; ncomp int; firstPP string }{
		{"burgers", "[x,t],[u]", "[u[0,1]+u[0,0]*u[1,0]-u[2,0]]", 1,
			"[[-u(x, t)*diff(u(x, t), x) - diff(u(x, t), t) + diff(u(x, t), x, x) = 0]]"},
		{"wave", "[x,t],[u]", "[u[0,2]-u[2,0]]", 1,
			"[[diff(u(x, t), t, t) - diff(u(x, t), x, x) = 0]]"},
		{"overdetermined", "[x,y],[u]", "[u[1,0], u[0,1]-1]", 1,
			"[[diff(u(x, y), x) = 0, diff(u(x, y), y) - 1 = 0]]"},
	}
	for _, c := range cases {
		it := NewInterp()
		if err := it.LoadDifferentialThomas(dtSrcDir()); err != nil {
			t.Fatalf("%s load: %v", c.name, err)
		}
		if _, err := it.Exec("`DifferentialThomas/ComputeRanking`(" + c.ranking + ");"); err != nil {
			t.Fatalf("%s ranking: %v", c.name, err)
		}
		r, err := it.Exec("`DifferentialThomas/DifferentialThomasDecomposition`(" + c.eqs + ", []);")
		if err != nil {
			t.Fatalf("%s decomp: %v", c.name, err)
		}
		lst, ok := r.(List)
		if !ok || len(lst.Items) != c.ncomp {
			t.Fatalf("%s: expected %d components, got %s", c.name, c.ncomp, printValue(r))
		}
		pp, err := it.Exec("`DifferentialThomas/PrettyPrintDifferentialSystem`(" +
			"`DifferentialThomas/DifferentialThomasDecomposition`(" + c.eqs + ", []));")
		if err != nil {
			t.Fatalf("%s PrettyPrint: %v", c.name, err)
		}
		if got := printValue(pp); got != c.firstPP {
			t.Fatalf("%s: got %q, want %q", c.name, got, c.firstPP)
		}
	}
}
