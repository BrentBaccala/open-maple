package main

import (
	"os"
	"testing"
)

// TestDecompositionSmoke is the Phase-3/4 milestone: run the canonical
// readme.txt smoke test end-to-end through the real Sage backend.
//
//	ComputeRanking([x,y],[u]):
//	DifferentialThomasDecomposition([u[1,0]-u[0,0], u[0,1]-u[0,0]^2], [])
//
// Phase-3 success = the decomposition runs to completion WITHOUT hitting
// errCASUnimplemented (the landed CAS ops carry it). Producing the exact
// PrettyPrint output [[u(x, y) = 0]] is the Phase-4 goal.
//
// Skipped unless OPENMAPLE_CAS=sage.
func TestDecompositionSmoke(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage to run the decomposition smoke test")
	}
	it := NewInterp()
	if err := it.LoadDifferentialThomas(dtSrcDir()); err != nil {
		t.Fatalf("loading DifferentialThomas failed: %v", err)
	}

	if _, err := it.Exec("`DifferentialThomas/ComputeRanking`([x,y],[u]);"); err != nil {
		t.Fatalf("ComputeRanking errored: %v", err)
	}

	r, err := it.Exec("`DifferentialThomas/DifferentialThomasDecomposition`([u[1,0]-u[0,0], u[0,1]-u[0,0]^2], []);")
	if err != nil {
		t.Fatalf("DifferentialThomasDecomposition errored: %v", err)
	}
	t.Logf("decomposition returned: %s", printValue(r))

	// Try PrettyPrint too (Phase-4 target output).
	pp, err := it.Exec("`DifferentialThomas/PrettyPrintDifferentialSystem`(`DifferentialThomas/DifferentialThomasDecomposition`([u[1,0]-u[0,0], u[0,1]-u[0,0]^2], []));")
	if err != nil {
		t.Logf("PrettyPrint errored (Phase-4 work): %v", err)
	} else {
		t.Logf("PrettyPrint returned: %s", printValue(pp))
		if printValue(pp) == "[[u(x, y) = 0]]" {
			t.Logf("EXACT TARGET MATCH: [[u(x, y) = 0]] (Phase 4 folded in)")
		}
	}
}
