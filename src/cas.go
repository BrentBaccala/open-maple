package main

import "fmt"

// CAS is the computer-algebra backend interface. Phase 2 ships a stub that
// errors clearly; Phase 3 implements it with a Sage subprocess. Every delegated
// computer-algebra op flows through Call(op, args) so Phase 3 is a clean
// drop-in at a single dispatch point.
type CAS interface {
	Call(op string, args []Value) (Value, error)
}

// cacheClearer is implemented by a CAS backend that keeps an expression-handle
// cache (the Sage backend). The interpreter driver calls ClearCache at coarse
// boundaries (between top-level decomposition statements) so the no-eviction
// ref cache cannot grow without bound on a long run. Backends without a cache
// don't implement it and the clear is simply skipped.
type cacheClearer interface {
	ClearCache() error
}

// errCASUnimplemented is the explicit signal that a computer-algebra op was
// reached but the backend is a stub.
type errCASUnimplemented struct{ Op string }

func (e *errCASUnimplemented) Error() string {
	return "errCASUnimplemented: " + e.Op
}

func errCAS(op string) error { return &errCASUnimplemented{Op: op} }

// stubCAS errors on every CAS op (Phase 2). It records nothing; the dispatch
// point is CAS.Call.
type stubCAS struct{}

func (*stubCAS) Call(op string, args []Value) (Value, error) {
	return nil, &errCASUnimplemented{Op: fmt.Sprintf("%s/%d", op, len(args))}
}

// errCASBackend is returned when the requested backend (e.g. Sage) could not be
// constructed; it reports the construction error on every call.
type errCASBackend struct{ err error }

func (e *errCASBackend) Call(op string, args []Value) (Value, error) {
	return nil, fmt.Errorf("CAS backend unavailable: %v (op %s)", e.err, op)
}

// casOps is the exact set of computer-algebra operations that Phase 3 must
// implement in the Sage bridge. Derived from the Phase-1 audit table. Anything
// in this set, when called as an unbound name, is routed to CAS.Call rather
// than becoming an inert function application.
var casOps = map[string]bool{
	// factorization / polynomial structure
	"factors": true, "factor": true, "AFactors": true,
	"expand": true, "normal": true, "simplify": true,
	"degree": true, "ldegree": true, "coeff": true, "coeffs": true,
	"lcoeff": true, "tcoeff": true, "collect": true,
	"indets": true, "gcd": true, "lcm": true, "gcdex": true,
	"denom": true, "numer": true,
	"divide": true, "rem": true, "quo": true, "prem": true, "pquo": true,
	"primpart": true, "content": true, "sqrfree": true,
	"resultant": true, "discrim": true, "subresultant": true,
	// calculus
	"diff": true, "Diff": true, "int": true, "mtaylor": true, "taylor": true,
	"series": true, "product": true, "sum": true,
	// algebraic numbers
	"RootOf": true, "evala": true, "radnormal": true, "minpoly": true,
	"solve": true, "fsolve": true, "isolve": true,
	// linear algebra (Matrix-valued)
	"Matrix": true, "Vector": true, "Array": true,
	"LinearSolve": true, "LUDecomposition": true,
	// misc numeric/special
	"binomial": true, "GAMMA": true,
}

func isCASOp(name string) bool {
	if casOps[name] {
		return true
	}
	// LinearAlgebra[...] and PolynomialTools[...] resolved as "Pkg:-member"
	if i := indexColon(name); i >= 0 {
		pkg := name[:i]
		switch pkg {
		case "LinearAlgebra", "PolynomialTools", "RootFinding":
			return true
		}
	}
	return false
}

func indexColon(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == ':' && s[i+1] == '-' {
			return i
		}
	}
	return -1
}
