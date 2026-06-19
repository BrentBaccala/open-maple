package main

import (
	"os"
	"testing"
)

// TestReductionLeaderRepro pins the root-cause fixes for the reduction-engine
// non-termination (task open-maple-fix-reduction-nontermination):
//
//   1. Indexed assignment must resolve a table/proc Name-alias on the RHS
//      (interp.go assignIndexed -> resolveRefForStore), so that
//      `rankingtable['Compare'] := Compare2` stores the proc object, not the
//      bare name. Before the fix, `R['Compare'](a,b)` returned an inert
//      Func{Compare2,...} instead of a boolean, BiggestDiffVar never updated its
//      candidate, and Leader(u[0,1]-u[0,0]^2) came back as u[0,0] (rank 2)
//      instead of u[0,1] (rank 1) — feeding the oscillating reduction loop.
//
//   2. Nested indexed assignment (t[i][j] := v) auto-vivifies intermediate
//      tables, and an unassigned entry of an existing table reads as NULL, so
//      ProlongationConsidered's `if p['ConsideredProlongations']=NULL` branch
//      materialises its sub-table.
//
// Requires OPENMAPLE_CAS=sage (the Compare proc calls into the CAS layer).
func TestReductionLeaderRepro(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage")
	}
	it := NewInterp()
	if err := it.LoadDifferentialThomas(dtSrcDir()); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := it.Exec("`DifferentialThomas/ComputeRanking`([x,y],[u]);"); err != nil {
		t.Fatalf("ComputeRanking: %v", err)
	}
	mustExec := func(code string) Value {
		v, err := it.Exec(code)
		if err != nil {
			t.Fatalf("exec %q: %v", code, err)
		}
		return v
	}
	eq := func(code, want string) {
		got := printValue(mustExec(code))
		if got != want {
			t.Errorf("%s = %q, want %q", code, got, want)
		}
	}

	mustExec("R := `DifferentialThomas/GlobalRanking`:")

	// Fix #1: the ranking Compare proc actually runs and returns a boolean
	// (DegRevLex: u[0,1] outranks u[0,0]).
	eq("R['Compare'](u[0,1], u[0,0])", "true")
	eq("R['Compare'](u[0,0], u[0,1])", "false")

	// ...so the leader of u[0,1]-u[0,0]^2 is the genuine highest derivative
	// u[0,1] with rank 1 (was wrongly u[0,0] / rank 2 before the fix).
	mustExec("p := `DifferentialThomas/CreatePolynomialObject`(u[0,1]-u[0,0]^2, R):")
	eq("`DifferentialThomas/Leader`(p)", "u[0, 1]")
	eq("`DifferentialThomas/Rank`(p)", "1")

	// Fix #2: nested indexed assignment + NULL-read makes ProlongationConsidered
	// memoise correctly (false on first prolongation of a var, true after).
	mustExec("q := `DifferentialThomas/CreatePolynomialObject`(u[1,0]-u[0,0], R):")
	eq("`DifferentialThomas/ProlongationConsidered`(q,x)", "false")
	eq("`DifferentialThomas/ProlongationConsidered`(q,x)", "true")
}
