package main

import (
	"os"
	"testing"
)

// TestOverviewCauchyRiemann runs the two-dependent-variable nonlinear system
// from DifferentialThomas's doc/DifferentialThomasOverview worksheet:
//
//	ComputeRanking([x,y],[u,v]):
//	DifferentialThomasDecomposition(
//	  [u[1,0]^2-2*u[1,0]*v[0,1]+v[0,1]^2, u[0,1]^2+2*u[0,1]*v[1,0]+v[1,0]^2], [])
//
// The input is [(u_x - v_y)^2, (u_y + v_x)^2], so the (single) finished system
// is the Cauchy-Riemann equations u_x = v_y, u_y = -v_x. This is the first
// multi-dependent-variable, nonlinear (squared-leader) system exercised end to
// end, past the single-equation path; it depends on the product/sum index
// binder, JetList2Diff over several derivative leaders, and the printer's -1
// fold and + joining.
//
// Skipped unless OPENMAPLE_CAS=sage.
func TestOverviewCauchyRiemann(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage to run the overview Cauchy-Riemann system")
	}
	it := NewInterp()
	if err := it.LoadDifferentialThomas(dtSrcDir()); err != nil {
		t.Fatalf("loading DifferentialThomas failed: %v", err)
	}
	if _, err := it.Exec("`DifferentialThomas/ComputeRanking`([x,y],[u,v]);"); err != nil {
		t.Fatalf("ComputeRanking errored: %v", err)
	}
	pp, err := it.Exec("`DifferentialThomas/PrettyPrintDifferentialSystem`(" +
		"`DifferentialThomas/DifferentialThomasDecomposition`(" +
		"[u[1,0]^2-2*u[1,0]*v[0,1]+v[0,1]^2, u[0,1]^2+2*u[0,1]*v[1,0]+v[1,0]^2], []));")
	if err != nil {
		t.Fatalf("decomposition / PrettyPrint errored: %v", err)
	}
	want := "[[diff(u(x, y), x) - diff(v(x, y), y) = 0, diff(u(x, y), y) + diff(v(x, y), x) = 0]]"
	if got := printValue(pp); got != want {
		t.Fatalf("PrettyPrint:\n got %q\nwant %q", got, want)
	}
}
