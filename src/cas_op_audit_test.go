package main

import (
	"os"
	"testing"
)

// These tests pin the fixes from the Maple-help-page CAS-op audit. Each op was
// either ignoring its variable argument or using the wrong definition (the same
// class of bug as the earlier content(p, V) fix). The check values come from the
// corresponding ~/open-maple/maple-help/<op>.md help page. Sage-gated: these
// forms route to the Sage backend (cas/sage_server.py).

func sageGate(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage to run the CAS-op audit tests")
	}
}

// evalEq runs expr and compares (by value, order-incidental) to want.
func evalEq(t *testing.T, it *Interp, expr, want string) {
	t.Helper()
	v, err := it.Exec(expr)
	if err != nil {
		t.Fatalf("%s err: %v", expr, err)
	}
	w, err := it.Exec(want + ";")
	if err != nil {
		t.Fatalf("want %q err: %v", want, err)
	}
	if compareValues(v, w) != 0 {
		t.Fatalf("%s: got %q, want %q", expr, printValue(v), want)
	}
}

// rem(a, b, x): remainder of a/b as polynomials in x. a = b*q + r,
// degree(r, x) < degree(b, x). (Help: rem.md / quo.md.)
func TestRemInX(t *testing.T) {
	sageGate(t)
	it := NewInterp()
	evalEq(t, it, "rem(x^2+y, x+y, x);", "y^2+y")
	evalEq(t, it, "rem(x^3+x+1, x^2+x+1, x);", "x+2")
}

// quo(a, b, x): quotient of a/b as polynomials in x. (Help: quo.md.)
func TestQuoInX(t *testing.T) {
	sageGate(t)
	it := NewInterp()
	evalEq(t, it, "quo(x^2+y, x+y, x);", "x-y")
	evalEq(t, it, "quo(x^3+x+1, x^2+x+1, x);", "x-1")
}

// pquo(a, b, x): pseudo-quotient w.r.t. x. m*a = b*q + r with
// m = lcoeff(b,x)^(deg a - deg b + 1). (Help: pquo.md / prem.md.)
func TestPquoInX(t *testing.T) {
	sageGate(t)
	it := NewInterp()
	// lcoeff(x+y, x) = 1, so m = 1 and pquo == quo here.
	evalEq(t, it, "pquo(x^2+y, x+y, x);", "x-y")
	// non-monic in x: b = 2*x + y, m = lcoeff(b,x)^(deg a - deg b + 1) = 2^2 = 4.
	// Pseudo-division: 4*(x^2+y) = (2*x+y)*(2*x-y) + (y^2+4*y), so pquo = 2*x-y
	// and prem = y^2+4*y (see TestPremInX).
	evalEq(t, it, "pquo(x^2+y, 2*x+y, x);", "2*x-y")
}

// prem(a, b, x): pseudo-remainder w.r.t. x. (Help: prem.md.)
func TestPremInX(t *testing.T) {
	sageGate(t)
	it := NewInterp()
	evalEq(t, it, "prem(x^2+y, x+y, x);", "y^2+y")
	// non-monic: m = 4, prem(x^2+y, 2*x+y, x) = y^2 + 4*y.
	evalEq(t, it, "prem(x^2+y, 2*x+y, x);", "y^2+4*y")
}

// tcoeff(p, x): TRAILING coefficient = coeff(p, x, ldegree(p, x)), the
// coefficient of the LOWEST power of x present — not the constant term.
// (Help: tcoeff.md.)
func TestTcoeffInX(t *testing.T) {
	sageGate(t)
	it := NewInterp()
	evalEq(t, it, "tcoeff(x^2+x, x);", "1")
	evalEq(t, it, "tcoeff(3*x^3+5*x^2, x);", "5")
	// multivariate: lowest power of x is x^1, coeff = 2*y.
	evalEq(t, it, "tcoeff(x^2*y + 2*x*y, x);", "2*y")
}

// coeffs(p, x): coefficients of p w.r.t. x, each a polynomial in the other vars.
// Returns an expression sequence (so {coeffs(...)} forms a set). (Help: coeffs.md.)
func TestCoeffsInX(t *testing.T) {
	sageGate(t)
	it := NewInterp()
	evalEq(t, it,
		"{coeffs(-6*x+3*y+23*x^2-4*x*y*z+7*z^2, x)};",
		"{23, -4*y*z-6, 7*z^2+3*y}")
	evalEq(t, it, "{coeffs(3*v^2*y^2 + 2*v*y^3, v)};", "{3*y^2, 2*y^3}")
}

// ldegree(0, x) must be +infinity (Maple convention), and the set/total form
// over a list of variables must not crash. (Help: ldegree.md / degree.md.)
func TestLdegreeZeroAndSet(t *testing.T) {
	sageGate(t)
	it := NewInterp()
	evalEq(t, it, "ldegree(0, x);", "infinity")
	// total low degree over {x, y}: min over monomials of (deg_x + deg_y).
	evalEq(t, it, "ldegree(x^2*y^3 + x^3*y^2, [x, y]);", "5")
}

// lcm is variadic: lcm(x, y, z) = x*y*z. (Help: lcm.md.)
func TestLcmVariadic(t *testing.T) {
	sageGate(t)
	it := NewInterp()
	evalEq(t, it, "lcm(x, y, z);", "x*y*z")
	evalEq(t, it, "lcm(x^2-1, x-1, x+1);", "x^2-1")
}

// sqrfree(p, x): square-free factorization treating x as the main variable. The
// product reconstructs p and the multiplicities give the square-free structure;
// unit/content placement may differ from Maple's exact text. (Help: sqrfree.md.)
func TestSqrfreeMainVar(t *testing.T) {
	sageGate(t)
	it := NewInterp()
	// sqrfree returns [unit, [[f1,m1],[f2,m2],...]]. Reconstruct the product and
	// check it equals the input — the square-free decomposition is correct iff
	// the product reconstructs and each fi is square-free. (x-1)^2*(x+2) over x
	// has exactly two square-free factors with multiplicities 1 and 2.
	if _, err := it.Exec("F := sqrfree((x-1)^2*(x+2), x):"); err != nil {
		t.Fatalf("sqrfree err: %v", err)
	}
	evalEq(t, it,
		"expand(F[1] * F[2][1][1]^F[2][1][2] * F[2][2][1]^F[2][2][2]);",
		"x^3-3*x+2")
	// The two factors carry multiplicities {1, 2} (square-free structure).
	evalEq(t, it, "{F[2][1][2], F[2][2][2]};", "{1, 2}")
	// A different main variable gives a different result: f = x^3*y - x^3 -
	// x^2*y^2 + x^2*y is square-free in y (single factor, multiplicity 1).
	if _, err := it.Exec("G := sqrfree(x^3*y - x^3 - x^2*y^2 + x^2*y, y):"); err != nil {
		t.Fatalf("sqrfree(.,y) err: %v", err)
	}
	evalEq(t, it, "nops(G[2]);", "1")
	evalEq(t, it, "G[2][1][2];", "1")
}

// lcoeff(p, [x,y]): nested-lexicographic leading coefficient
// lcoeff(...lcoeff(p, x)..., y); also the single-var and univariate forms honor
// the explicit variable. (Help: lcoeff.md.)
func TestLcoeffVarAndList(t *testing.T) {
	sageGate(t)
	it := NewInterp()
	evalEq(t, it, "lcoeff(3*x^2*y + x, x);", "3*y")
	// lcoeff(p, [x, y]) = lcoeff(lcoeff(p, x), y) = lcoeff(3*y, y) = 3.
	evalEq(t, it, "lcoeff(3*x^2*y + x*y^2 + x, [x, y]);", "3")
}
