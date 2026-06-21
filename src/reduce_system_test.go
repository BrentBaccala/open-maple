package main

import (
	"os"
	"strings"
	"testing"
)

// TestReduceWorksheetSystem runs the system from DifferentialThomas's
// DifferentialThomasDifferentialSystemReduce worksheet — the first exercised
// case that uses INEQUATIONS (a non-empty second argument) and the
// "EliminateFunction" ranking option:
//
//	ComputeRanking([t],[F,c1,sV,c],"EliminateFunction"):
//	DifferentialThomasDecomposition(
//	  [2*sV[1]*sV[0]-F[0]+k*sV[0],
//	   c[1]*sV[0]^2+2*sV[1]*sV[0]*c[0]-c1*F[0]+c[0]*k*sV[0],
//	   c1[1]],
//	  [sV[0], F[0]])
//
// Note the bare dvar `c1` in `-c1*F[0]` (no index): SubstituteDVar must turn it
// into c1[0] = c1(t). A bug in indexing a bound name as a head (see
// TestLambdaIndexHead) used to leak a phantom jet variable `a[0]` here; this
// test guards that regression — no `a(t)` / `a[0]` may appear in any component.
//
// Skipped unless OPENMAPLE_CAS=sage.
func TestReduceWorksheetSystem(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage to run the Reduce worksheet system")
	}
	it := NewInterp()
	if err := it.LoadDifferentialThomas(dtSrcDir()); err != nil {
		t.Fatalf("loading DifferentialThomas failed: %v", err)
	}
	if _, err := it.Exec("`DifferentialThomas/ComputeRanking`([t],[F,c1,sV,c],\"EliminateFunction\");"); err != nil {
		t.Fatalf("ComputeRanking errored: %v", err)
	}
	r, err := it.Exec("`DifferentialThomas/DifferentialThomasDecomposition`(" +
		"[2*sV[1]*sV[0]-F[0]+k*sV[0], " +
		"c[1]*sV[0]^2+2*sV[1]*sV[0]*c[0]-c1*F[0]+c[0]*k*sV[0], " +
		"c1[1]], [sV[0], F[0]]);")
	if err != nil {
		t.Fatalf("decomposition errored: %v", err)
	}
	lst, ok := r.(List)
	if !ok || len(lst.Items) != 4 {
		t.Fatalf("expected a 4-component decomposition, got %s", printValue(r))
	}
	for i := range lst.Items {
		it.globals["__rc"] = lst.Items[i]
		pp, err := it.Exec("`DifferentialThomas/PrettyPrintDifferentialSystem`(__rc);")
		if err != nil {
			t.Fatalf("component %d PrettyPrint errored: %v", i+1, err)
		}
		s := printValue(pp)
		// the phantom-`a` regression guard: only F, c1, sV, c, t, k may appear
		if strings.Contains(s, "a(t)") || strings.Contains(s, "a[0]") {
			t.Fatalf("phantom variable a leaked into component %d: %s", i+1, s)
		}
	}
}
