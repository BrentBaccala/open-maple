package main

import (
	"os"
	"testing"
)

// TestNonlinearTailLeaderFixes pins the root-cause fixes for the
// "no differential variable as leader" assert and the blockers it unmasked
// (task open-maple-fix-nonlinear-tail-leader-assert):
//
//  1. table([k=v]) constructor must resolve a table/proc Name-alias on each
//     entry value (biTable -> resolveRefForStore). CreateJanetTreesObject does
//     `table(['Ranking'=ranking])` with a proc-local `ranking` table; without
//     the deref the treeobject's Ranking is a dangling name and
//     treeobject['Ranking']['IsDifferentialVariable'](...) evaluates to an inert
//     Func instead of a boolean.
//
//  2. List-element assignment `L[i] := v` (RemoveMultiplicativeVariableInSubtree
//     does node['MultiplicativeVariables'][indexofvar] := 0 on a list).
//
//  3. Extended-real arithmetic: a sum containing infinity collapses to infinity
//     ([infinity,infinity] - [0,1] = [infinity,infinity]).
//
//  4. type(p,`+`)/`*`/`^` — the operator type names arrive backtick-stripped, so
//     the type cases must be the bare strings; otherwise PartialDerivativeInternal
//     skips the sum/product rules and d/dy(u[0,0]-u[1,0]) wrongly comes back 0.
//
//  5. Sage diff of a constant (R(f).derivative) — diff(-1, y) must give 0, not
//     raise on the Integer's missing .derivative.
//
// Requires OPENMAPLE_CAS=sage.
func TestNonlinearTailLeaderFixes(t *testing.T) {
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

	// Fix 1: a table([... = <table-valued name> ...]) keeps the table reference,
	// so indexing the stored table resolves the proc, not an inert Func.
	mustExec("R := `DifferentialThomas/GlobalRanking`:")
	mustExec("tr := `DifferentialThomas/CreateJanetTreesObject`(R):")
	eq("type(eval(tr['Ranking']), table)", "true")
	eq("tr['Ranking']['IsDifferentialVariable'](u[0,1])", "true")

	// Fix 3: extended-real arithmetic on infinity.
	eq("infinity - 1", "infinity")
	eq("[infinity, infinity] - [0, 1]", "[infinity, infinity]")
	// -infinity is represented as the product (-1)*infinity but PRINTS as the
	// atom "-infinity" (Phase-4 printer-fidelity fix, task 416 — previously the
	// printer leaked "-1*infinity").
	eq("-infinity + 5", "-infinity")

	// Fix 4: structural operator type checks (backtick-stripped names).
	eq("type(u[0,0]-u[1,0], `+`)", "true")
	eq("type(-u[1,0], `*`)", "true")
	eq("type(u[0,0]^2, `^`)", "true")
	eq("type(u[0,0], `+`)", "false")

	// Fix 4+5: differentiation of a difference of jet variables (was 0 before).
	eq("`DifferentialThomas/PartialDerivativeInternal`(u[0,0]-u[1,0], y, 2, false, R)",
		"u[0, 1] - 1*u[1, 1]")

	// Fix 2: list-element assignment in a table slot (Maple value semantics).
	mustExec("nt := table(['MultiplicativeVariables' = [infinity, infinity]]):")
	mustExec("nt['MultiplicativeVariables'][1] := 0:")
	eq("nt['MultiplicativeVariables']", "[0, infinity]")
}
