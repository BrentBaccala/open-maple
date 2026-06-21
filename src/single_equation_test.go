package main

import (
	"os"
	"testing"
)

// TestSingleEquationDecomposition pins the single-equation decomposition path,
// the next system past the canonical two-equation readme smoke. It exercises
// four gaps that the two-equation smoke never hit (each fixed in ~/open-maple/src;
// the LGPL DifferentialThomas source is untouched):
//
//  1. product(f, k=m..n) / sum(f, k=m..n) with concrete integer bounds must
//     expand as an explicit finite product/sum BINDING the index k — DT's
//     PrincipalDerivativesFromTree does product(ivar[j]^currentpos[j], j=1..n).
//     Before, product/sum were routed to the CAS with eager arg evaluation, so
//     ivar[j] (= [x,y][j], j unbound) raised "unsupported index type".
//
//  2. The leader of a first-order equation is a derivative u[1,0] -> the inert
//     diff(u(x, y), x). The Sage backend must declare the unknown function head
//     u as a symbolic function (parse_symbolic) so the derivative stays
//     unevaluated instead of raising "name 'u' is not defined".
//
//  3. Re-parsing already-reduced Sage output must NOT re-dispatch a CAS-op head
//     (parseBack inertParse): diff(u(x, y), x) round-trips to itself, so
//     re-evaluating it would loop forever.
//
//  4. printValue must fold a leading -1 coefficient to a unary minus
//     (u(x, y) - diff(...), not u(x, y) - 1*diff(...)) — Maple's surface form.
//
// The decomposition of the single first-order equation u_x - u = 0 has no
// integrability condition (unlike the two-equation u=0 smoke), so the one
// finished component is the equation itself: u(x, y) - diff(u(x, y), x) = 0.
//
// Skipped unless OPENMAPLE_CAS=sage.
func TestSingleEquationDecomposition(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage to run the single-equation decomposition test")
	}
	it := NewInterp()
	if err := it.LoadDifferentialThomas(dtSrcDir()); err != nil {
		t.Fatalf("loading DifferentialThomas failed: %v", err)
	}
	if _, err := it.Exec("`DifferentialThomas/ComputeRanking`([x,y],[u]);"); err != nil {
		t.Fatalf("ComputeRanking errored: %v", err)
	}

	// Decomposition runs to completion and returns exactly one finished system.
	r, err := it.Exec("`DifferentialThomas/DifferentialThomasDecomposition`([u[1,0]-u[0,0]], []);")
	if err != nil {
		t.Fatalf("single-equation decomposition errored: %v", err)
	}
	sys, ok := r.(List)
	if !ok || len(sys.Items) != 1 {
		t.Fatalf("expected a 1-component decomposition, got: %s", printValue(r))
	}

	// PrettyPrint produces exactly the equation u(x, y) - diff(u(x, y), x) = 0.
	pp, err := it.Exec("`DifferentialThomas/PrettyPrintDifferentialSystem`(`DifferentialThomas/DifferentialThomasDecomposition`([u[1,0]-u[0,0]], []));")
	if err != nil {
		t.Fatalf("PrettyPrint errored: %v", err)
	}
	if got := printValue(pp); got != "[[u(x, y) - diff(u(x, y), x) = 0]]" {
		t.Fatalf("PrettyPrint: got %q, want [[u(x, y) - diff(u(x, y), x) = 0]]", got)
	}
}
