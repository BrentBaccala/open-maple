package main

import (
	"os"
	"testing"
)

// TestSecondOrderDerivatives pins that higher-order jet variables render with
// the right derivative multiplicity. u[2,0] is the second x-derivative, which
// JetList2Diff emits as diff(u(x,t), x, x); the Sage op_diff used to read only
// the first differentiation variable and drop the rest, so the heat and Laplace
// equations came out FIRST order (u_x instead of u_xx). Now diff folds over all
// of its variable arguments.
//
// Skipped unless OPENMAPLE_CAS=sage.
func TestSecondOrderDerivatives(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage")
	}
	cases := []struct{ name, ranking, eqs, want string }{
		{"heat", "[x,t],[u]", "[u[2,0]-u[0,1]]",
			"[[diff(u(x, t), t) - diff(u(x, t), x, x) = 0]]"},
		{"laplace", "[x,y],[u]", "[u[2,0]+u[0,2]]",
			"[[diff(u(x, y), y, y) + diff(u(x, y), x, x) = 0]]"},
	}
	for _, c := range cases {
		it := NewInterp()
		if err := it.LoadDifferentialThomas(dtSrcDir()); err != nil {
			t.Fatalf("%s load: %v", c.name, err)
		}
		if _, err := it.Exec("`DifferentialThomas/ComputeRanking`(" + c.ranking + ");"); err != nil {
			t.Fatalf("%s ranking: %v", c.name, err)
		}
		pp, err := it.Exec("`DifferentialThomas/PrettyPrintDifferentialSystem`(" +
			"`DifferentialThomas/DifferentialThomasDecomposition`(" + c.eqs + ", []));")
		if err != nil {
			t.Fatalf("%s decomp: %v", c.name, err)
		}
		if got := printValue(pp); got != c.want {
			t.Fatalf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
