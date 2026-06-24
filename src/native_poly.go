package main

import (
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// Native implementations of the cheap polynomial-structure ops that
// DifferentialThomas calls in tight inner loops (degree, indets). Routing each
// of these to the Sage subprocess costs a full JSON round-trip + re-parse; a
// 3-variable decomposition makes tens of thousands of them. These native
// versions operate directly on the Value AST and never touch Sage.
//
// They work on a fully expanded normal form (a monomial -> coefficient map with
// like terms collected and zero coefficients dropped), so they agree with Sage
// even when the input AST has cancelling terms, e.g.
// degree(-u[1,1] - (u[0,1] - u[1,1]), u[1,1]) == 0 (the u[1,1] cancels). The
// normal form is used ONLY to compute the integer/set result; polynomials are
// never re-stored or re-printed through it, so there is no term-ordering /
// string-identity risk (compareValues on Sum/Prod falls back to string compare).
//
// SAFETY PRINCIPLE: each handler returns ok=false (fall back to Sage) for ANY
// input it cannot put into this normal form — a Func/Float, a symbolic or
// negative exponent, anything non-polynomial. Worst case is "no speedup", never
// a wrong answer. OPENMAPLE_VERIFY_NATIVE=1 runs native AND Sage and asserts
// they agree, proving the equivalence across the test corpus.

// tryNativePoly dispatches a CAS op to its native implementation when one exists
// and can handle the given args. Returns ok=false to fall through to the CAS.
func (it *Interp) tryNativePoly(name string, args []Value) (Value, bool) {
	var v Value
	var ok bool
	switch name {
	case "indets":
		if len(args) == 1 {
			v, ok = nativeIndets(args[0])
		}
	case "degree":
		v, ok = nativeDegree(args)
	case "expand":
		if len(args) == 1 {
			v, ok = nativeExpand(args[0])
		}
	case "evala":
		// With no algebraic numbers (RootOf) in play — the only case this port
		// supports — evala(p) just normalizes p to expanded standard form, which
		// is exactly native expand on a polynomial. DT calls it as
		// evala(expand(...)) and evala(StandardForm(p)/c) (polynomial over a
		// rational content), both of which toPolyNF handles. A genuine rational
		// function or any RootOf input falls through to Sage. This is also a large
		// perf/robustness win: evala on a fully-expanded polynomial was the op
		// that round-tripped a huge term-string to Sage, where CPython's compiler
		// overflowed its recursion limit parsing it (the hydrogen ansatz).
		if len(args) == 1 {
			v, ok = nativeExpand(args[0])
		}
	case "coeff":
		v, ok = nativeCoeff(args)
	case "numer":
		v, ok = nativeNumerDenom(args, true)
	case "denom":
		v, ok = nativeNumerDenom(args, false)
	case "content":
		// Only the one-arg form content(p) is the rational content (gcd of numeric
		// coefficients) that nativeContent computes. The two-arg content(p, V) is
		// the polynomial content w.r.t. the variables V (e.g. content(x*Vf+x*rho,
		// [Vf,rho])=x) — a different computation; route it to Sage's op_content.
		if len(args) == 1 {
			v, ok = nativeContent(args[0])
		}
	case "gcd":
		if len(args) == 2 {
			v, ok = nativeGCD(args[0], args[1])
		}
	case "normal", "simplify":
		// On a scalar/atom (constant or a single name/jet variable) normal and
		// simplify are the identity — there is nothing to combine or reduce. A
		// composite polynomial/rational expression returns ok=false → Sage, which
		// owns the canonical form (and its term order, which feeds FactorSorter).
		if len(args) == 1 {
			if _, isAtom := atomOrConst(args[0]); isAtom {
				v, ok = args[0], true
			}
		}
	}
	if !ok {
		return nil, false
	}
	if it.verifyNative {
		// indets on a Func-containing expression: native is the authority (Sage's
		// indets drops applied-function terms and returns {}), so don't compare.
		if name == "indets" {
			if _, isPoly := toPolyNF(args[0]); !isPoly {
				return v, true
			}
		}
		// Compare against Sage only when Sage itself produces a value: the Sage
		// path errors on inputs native handles correctly (e.g. degree(3, x) — the
		// degree of a constant — makes op_degree raise), and there native is the
		// authority, not a mismatch.
		if ref, err := it.cas.Call(name, args); err == nil && compareValues(v, ref) != 0 {
			panic("native " + name + " mismatch: native=" + printValue(v) +
				" sage=" + printValue(ref) + " args=" + printArgs(args))
		}
	}
	return v, true
}

func printValueOrErr(v Value, err error) string {
	if err != nil {
		return "<err:" + err.Error() + ">"
	}
	return printValue(v)
}

// ---------------------------------------------------------------------------
// Expanded normal form
// ---------------------------------------------------------------------------

// nfMono is one monomial: atom canonical-key -> exponent (>0). The empty map is
// the constant monomial.
type nfMono map[string]int64

// polyNF is a multivariate polynomial in normal form: collected monomials with
// nonzero rational coefficients, plus the atom Value for each atom key so indets
// can reconstruct the indeterminate set.
type polyNF struct {
	coeff map[string]*big.Rat // monoKey -> coefficient (nonzero)
	mono  map[string]nfMono   // monoKey -> exponent map
	atoms map[string]Value    // atomKey -> the atom Value (Name / *Indexed)
}

func newPolyNF() *polyNF {
	return &polyNF{coeff: map[string]*big.Rat{}, mono: map[string]nfMono{}, atoms: map[string]Value{}}
}

// monoKey is a canonical string key for a monomial (sorted atomKey^exp).
func monoKey(m nfMono) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(k)
		b.WriteByte('^')
		b.WriteString(strconv.FormatInt(m[k], 10))
	}
	return b.String()
}

// add accumulates c*<monomial m> into the polynomial, dropping the term if the
// coefficient becomes zero.
func (p *polyNF) add(m nfMono, c *big.Rat) {
	if c.Sign() == 0 {
		return
	}
	k := monoKey(m)
	if cur, ok := p.coeff[k]; ok {
		cur.Add(cur, c)
		if cur.Sign() == 0 {
			delete(p.coeff, k)
			delete(p.mono, k)
		}
		return
	}
	p.coeff[k] = new(big.Rat).Set(c)
	p.mono[k] = m
}

// toPolyNF converts a Value AST to expanded normal form. ok=false for anything
// that is not a plain polynomial over the rationals in name/indexed atoms.
func toPolyNF(v Value) (*polyNF, bool) {
	p := newPolyNF()
	switch n := v.(type) {
	case Integer:
		p.add(nfMono{}, new(big.Rat).SetInt(n.Val))
		return p, true
	case Rational:
		p.add(nfMono{}, new(big.Rat).Set(n.Val))
		return p, true
	case Name, *Indexed:
		key := canonicalKey(v)
		p.atoms[key] = v
		p.add(nfMono{key: 1}, big.NewRat(1, 1))
		return p, true
	case *Sum:
		for _, t := range n.Terms {
			tp, ok := toPolyNF(t)
			if !ok {
				return nil, false
			}
			p.mergeInPlace(tp)
		}
		return p, true
	case *Prod:
		acc := newPolyNF()
		acc.add(nfMono{}, big.NewRat(1, 1)) // multiplicative identity
		for _, f := range n.Factors {
			fp, ok := toPolyNF(f)
			if !ok {
				return nil, false
			}
			acc = acc.mul(fp)
		}
		return acc, true
	case *Power:
		// A constant base with ANY integer exponent (incl. negative, e.g. 2^-1
		// from x/2 = x*2^-1) is a rational constant.
		if c, ok := toRat(n.Base); ok {
			if e, ok := signedIntExp(n.Exp); ok {
				if val, ok := ratPow(c, e); ok {
					p.add(nfMono{}, val)
					return p, true
				}
			}
		}
		e, ok := nonNegIntExp(n.Exp)
		if !ok {
			return nil, false
		}
		bp, ok := toPolyNF(n.Base)
		if !ok {
			return nil, false
		}
		res := newPolyNF()
		res.add(nfMono{}, big.NewRat(1, 1))
		for i := int64(0); i < e; i++ {
			res = res.mul(bp)
		}
		return res, true
	}
	// Func, Float, Range, Equation, … — not a plain polynomial.
	return nil, false
}

// mergeInPlace adds every term of q into p (and unions the atom registries).
func (p *polyNF) mergeInPlace(q *polyNF) {
	for k, a := range q.atoms {
		p.atoms[k] = a
	}
	for key, c := range q.coeff {
		p.add(q.mono[key], c)
	}
}

// mul returns p*q.
func (p *polyNF) mul(q *polyNF) *polyNF {
	res := newPolyNF()
	for k, a := range p.atoms {
		res.atoms[k] = a
	}
	for k, a := range q.atoms {
		res.atoms[k] = a
	}
	for pk, pc := range p.coeff {
		for qk, qc := range q.coeff {
			m := nfMono{}
			for a, e := range p.mono[pk] {
				m[a] += e
			}
			for a, e := range q.mono[qk] {
				m[a] += e
			}
			res.add(m, new(big.Rat).Mul(pc, qc))
		}
	}
	return res
}

// ---------------------------------------------------------------------------
// indets / degree on the normal form
// ---------------------------------------------------------------------------

// nativeIndets computes indets(p): the set of indeterminate atoms (names and
// indexed/jet variables) that actually appear (nonzero exponent in some
// surviving monomial). ok=false to fall back to Sage.
func nativeIndets(v Value) (Value, bool) {
	if p, ok := toPolyNF(v); ok {
		// Pure polynomial: read indeterminates off the normal form, so cancelled
		// variables are dropped (matches Sage; verifiable).
		seen := map[string]bool{}
		var atoms []Value
		for key, m := range p.mono {
			_ = key
			for a, e := range m {
				if e > 0 && !seen[a] {
					seen[a] = true
					atoms = append(atoms, p.atoms[a])
				}
			}
		}
		return makeSet(atoms), true
	}
	// Func/transcendental-containing: collect every name / indexed / function
	// subterm. Maple's indets includes applied functions (u(x), diff(u(x),x));
	// the Sage path drops them (its sanitizer keeps only function ARGUMENTS and
	// returns {}), so native is the authority here — DT's Diff2JetList relies on
	// indets surfacing the diff and applied-function terms to convert diff-
	// notation input (diff(u(x),x)) to jet form (u[1]).
	atoms := map[string]Value{}
	if !collectFuncIndets(v, atoms) {
		return nil, false
	}
	items := make([]Value, 0, len(atoms))
	for _, a := range atoms {
		items = append(items, a)
	}
	return makeSet(items), true
}

// collectFuncIndets gathers every name / indexed / function subterm of v into
// out (keyed canonically). It recurses through arithmetic (Sum/Prod/Power) and
// the structural/relational containers indets is routinely called on in a
// Thomas decomposition — Seq/List/Set, Range, Equation, Relation, Uneval —
// unioning indets over their components (matches Maple). Numbers/strings/
// booleans contribute nothing; an unsupported node (Table/Proc/…) returns false.
func collectFuncIndets(v Value, out map[string]Value) bool {
	switch n := v.(type) {
	case Integer, Rational, Float, MString, Boolean:
		return true
	case Name, *Indexed:
		out[canonicalKey(v)] = v
		return true
	case *Func:
		out[canonicalKey(v)] = v
		for _, a := range n.Args {
			if !collectFuncIndets(a, out) {
				return false
			}
		}
		return true
	case *Sum:
		for _, t := range n.Terms {
			if !collectFuncIndets(t, out) {
				return false
			}
		}
		return true
	case *Prod:
		for _, f := range n.Factors {
			if !collectFuncIndets(f, out) {
				return false
			}
		}
		return true
	case *Power:
		return collectFuncIndets(n.Base, out) && collectFuncIndets(n.Exp, out)
	case Seq:
		for _, it := range n.Items {
			if !collectFuncIndets(it, out) {
				return false
			}
		}
		return true
	case List:
		for _, it := range n.Items {
			if !collectFuncIndets(it, out) {
				return false
			}
		}
		return true
	case Set:
		for _, it := range n.Items {
			if !collectFuncIndets(it, out) {
				return false
			}
		}
		return true
	case *Range:
		return collectFuncIndets(n.Lo, out) && collectFuncIndets(n.Hi, out)
	case *Equation:
		return collectFuncIndets(n.Lhs, out) && collectFuncIndets(n.Rhs, out)
	case *Relation:
		return collectFuncIndets(n.Lhs, out) && collectFuncIndets(n.Rhs, out)
	case *Uneval:
		return collectFuncIndets(n.Expr, out)
	}
	return false
}

// nativeDegree implements degree(p), degree(p, x) and degree(p, [x,...]) /
// degree(p, {x,...}). degree(0, …) is -infinity (Maple). ok=false to fall back.
func nativeDegree(args []Value) (Value, bool) {
	if len(args) == 0 || len(args) > 2 {
		return nil, false
	}
	p, ok := toPolyNF(args[0])
	if !ok {
		return nil, false
	}
	zero := len(p.coeff) == 0

	if len(args) == 1 {
		// total degree
		if zero {
			return negInfinity(), true
		}
		return newInt(p.maxDegree(nil)), true
	}

	// degree in a single variable or a set/list of variables
	var vars []string
	switch a := args[1].(type) {
	case Name, *Indexed:
		if zero {
			return negInfinity(), true
		}
		vars = []string{canonicalKey(a)}
		return newInt(p.maxDegree(vars)), true
	case List:
		if !varKeys(a.Items, &vars) {
			return nil, false
		}
		return degreeSetResult(p, vars, zero), true
	case Set:
		if !varKeys(a.Items, &vars) {
			return nil, false
		}
		return degreeSetResult(p, vars, zero), true
	}
	return nil, false
}

func varKeys(items []Value, out *[]string) bool {
	for _, v := range items {
		if _, ok := polyAtom(v); !ok {
			return false
		}
		*out = append(*out, canonicalKey(v))
	}
	return true
}

// degreeSetResult mirrors op_degree's multi-variable form: for the zero
// polynomial it returns 0 (the Sage path's sentinel), else the max over
// monomials of the summed exponents of the listed variables.
func degreeSetResult(p *polyNF, vars []string, zero bool) Value {
	if zero {
		return newInt(0)
	}
	return newInt(p.maxDegree(vars))
}

// maxDegree returns the maximum, over all surviving monomials, of the summed
// exponents of the atoms in `vars` (or of ALL atoms when vars is nil, i.e. total
// degree). The empty-monomial constant contributes 0.
func (p *polyNF) maxDegree(vars []string) int64 {
	var want map[string]bool
	if vars != nil {
		want = map[string]bool{}
		for _, v := range vars {
			want[v] = true
		}
	}
	var max int64
	first := true
	for key := range p.coeff {
		var d int64
		for a, e := range p.mono[key] {
			if want == nil || want[a] {
				d += e
			}
		}
		if first || d > max {
			max = d
			first = false
		}
	}
	return max
}

// isCompoundPoly reports whether v is a structural polynomial expression
// (Sum/Prod/Power) — the kinds whose printed form, not value, currently drives
// comparison. Used by compareValues to decide when to try normal-form compare.
func isCompoundPoly(v Value) bool {
	switch v.(type) {
	case *Sum, *Prod, *Power:
		return true
	}
	return false
}

// comparePolyValues compares two values by expanded polynomial normal form,
// giving a total order in which equal polynomials compare equal regardless of
// term order or structural shape. ok=false if either value is not a plain
// polynomial (then the caller uses the structural/string comparison).
func comparePolyValues(a, b Value) (int, bool) {
	pa, oka := toPolyNF(a)
	if !oka {
		return 0, false
	}
	pb, okb := toPolyNF(b)
	if !okb {
		return 0, false
	}
	return comparePolyNF(pa, pb), true
}

// comparePolyNF is a deterministic total order on normal forms: compare the
// monomials in sorted-key order, breaking ties by coefficient, then by count.
// Equal polynomials have identical {monoKey: coeff} maps, hence compare 0. The
// particular order is arbitrary but stable (it is NOT Maple's print order; the
// printed surface is unaffected — this only governs set/sort/equality).
func comparePolyNF(p, q *polyNF) int {
	pk := sortedKeys(p.coeff)
	qk := sortedKeys(q.coeff)
	for i := 0; i < len(pk) && i < len(qk); i++ {
		if c := strings.Compare(pk[i], qk[i]); c != 0 {
			return c
		}
		if c := p.coeff[pk[i]].Cmp(q.coeff[qk[i]]); c != 0 {
			return c
		}
	}
	return cmpInt(len(pk), len(qk))
}

func sortedKeys(m map[string]*big.Rat) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// expand / coeff via normal form
// ---------------------------------------------------------------------------

// nativeExpand implements expand(p): the fully distributed/collected polynomial.
// Reconstructed from the normal form in a deterministic order. ok=false to fall
// back to Sage for non-polynomial input.
func nativeExpand(v Value) (Value, bool) {
	p, ok := toPolyNF(v)
	if !ok {
		return nil, false
	}
	return fromPolyNF(p), true
}

// nativeCoeff implements coeff(p, x, n) — the coefficient of x^n in p (a
// polynomial in the remaining variables), matching Sage's p.coefficient({x:n}).
// n defaults to 1. ok=false to fall back to Sage.
func nativeCoeff(args []Value) (Value, bool) {
	if len(args) < 2 || len(args) > 3 {
		return nil, false
	}
	xv, ok := polyAtom(args[1])
	if !ok {
		return nil, false
	}
	var n int64 = 1
	if len(args) == 3 {
		e, ok := nonNegIntExp(args[2])
		if !ok {
			return nil, false
		}
		n = e
	}
	p, ok := toPolyNF(args[0])
	if !ok {
		return nil, false
	}
	xk := canonicalKey(xv)
	res := newPolyNF()
	for k, a := range p.atoms {
		res.atoms[k] = a
	}
	for key, c := range p.coeff {
		m := p.mono[key]
		if m[xk] != n {
			continue
		}
		// drop x^n from the monomial; keep the rest
		nm := nfMono{}
		for a, e := range m {
			if a == xk {
				continue
			}
			nm[a] = e
		}
		res.add(nm, c)
	}
	return fromPolyNF(res), true
}

// nativeNumerDenom implements numer(v)/denom(v) for a scalar argument — an
// integer, rational, or a single name/indexed (jet) variable. DT calls
// numer/denom pervasively on constant coefficients (the trace shows them as the
// most frequent vars=[] Sage ops); handling the scalar case natively skips a
// subprocess round-trip.
//
// The convention matches the Sage backend, which computes over Frac(QQ[vars]):
// numer/denom there are POLYNOMIAL-fraction numer/denom, so a constant (even a
// rational like -1/2) is its own numerator with denominator 1 — NOT the rational
// numerator -1 / denominator 2. (This is what DT wants: numer/denom is used to
// clear polynomial denominators of rational functions in the jet variables; a
// scalar has none.) A composite polynomial / rational expression returns
// ok=false → Sage. isNumer selects numer vs denom.
func nativeNumerDenom(args []Value, isNumer bool) (Value, bool) {
	if len(args) != 1 {
		return nil, false
	}
	switch args[0].(type) {
	case Integer, Rational, Name, *Indexed:
		if isNumer {
			return args[0], true
		}
		return newInt(1), true
	}
	return nil, false
}

// nativeContent implements content(p) — the positive rational gcd of p's
// numeric coefficients (gcd of numerators / lcm of denominators). This matches
// the Sage backend's op_content, which computes the numeric content over the
// whole ring and IGNORES the variable argument (so content(6*x*y+9*y, x) = 3,
// not 3*y); native ignores args[1:] likewise. content is the single most
// frequent CAS op on the multi-variable systems. content(0) = 0. A non-
// polynomial argument returns ok=false → Sage.
func nativeContent(p0 Value) (Value, bool) {
	p, ok := toPolyNF(p0)
	if !ok {
		return nil, false
	}
	if len(p.coeff) == 0 {
		return newInt(0), true
	}
	numGCD := new(big.Int) // 0; gcd(0,n)=n bootstraps the fold
	denLCM := big.NewInt(1)
	for _, c := range p.coeff {
		n := new(big.Int).Abs(c.Num())
		d := c.Denom() // big.Rat is always normalized, denom > 0
		numGCD.GCD(nil, nil, numGCD, n)
		g := new(big.Int).GCD(nil, nil, denLCM, d)
		denLCM.Mul(denLCM, new(big.Int).Quo(d, g)) // lcm = denLCM * d / gcd
	}
	return normRat(new(big.Rat).SetFrac(numGCD, denLCM)), true
}

// nativeGCD implements gcd(a, b) for the cases that don't need a full
// multivariate algorithm — the bulk of DT's gcd calls. Matches Sage's QQ[vars]
// convention learned by sampling:
//   - both constants → positive rational gcd: gcd(2,4)=2, gcd(1/2,1/3)=1/6
//   - otherwise → the MONIC gcd (content stripped, leading coeff 1):
//     gcd(2,-u)=1 (a nonzero constant is a unit), gcd(2x-2,4x-4)=x-1,
//     gcd(0, 2x-4)=x-2.
// Univariate (≤1 distinct indeterminate across both args) is the monic Euclidean
// GCD over Q. Genuinely multivariate inputs (≥2 indeterminates) return ok=false
// → Sage. A non-polynomial argument also falls back.
func nativeGCD(a, b Value) (Value, bool) {
	pa, ok := toPolyNF(a)
	if !ok {
		return nil, false
	}
	pb, ok := toPolyNF(b)
	if !ok {
		return nil, false
	}
	// the set of indeterminates actually present across both
	atoms := map[string]Value{}
	for _, p := range []*polyNF{pa, pb} {
		for key, m := range p.coeff {
			_ = m
			for ak, e := range p.mono[key] {
				if e > 0 {
					atoms[ak] = p.atoms[ak]
				}
			}
		}
	}
	switch len(atoms) {
	case 0:
		// Both constants. The Sage backend encodes an integer as {"int":...} →
		// ZZ.gcd (integer gcd: gcd(2,4)=2), but a rational as {"poly":...} →
		// QQ[x].gcd where a nonzero constant is a UNIT → gcd=1. So only the
		// both-integer case is an integer gcd; a rational constant falls back to
		// Sage (which returns 1). (Verify caught native returning 1/6 for
		// gcd(1/2,1/3) where Sage gives 1.)
		ca := constCoeff(pa)
		cb := constCoeff(pb)
		if ca.IsInt() && cb.IsInt() {
			g := new(big.Int).GCD(nil, nil,
				new(big.Int).Abs(ca.Num()), new(big.Int).Abs(cb.Num()))
			return bigInt(g), true
		}
		return nil, false
	case 1:
		var uvar string
		var uval Value
		for k, vv := range atoms {
			uvar, uval = k, vv
		}
		ua := univariateCoeffs(pa, uvar)
		ub := univariateCoeffs(pb, uvar)
		g := univGCD(ua, ub)
		return uniToValue(g, uval), true
	default:
		return nil, false // multivariate → Sage
	}
}

// constCoeff returns the constant coefficient of a polyNF with no indeterminates
// (the empty-monomial coefficient), or 0.
func constCoeff(p *polyNF) *big.Rat {
	if c, ok := p.coeff[monoKey(nfMono{})]; ok {
		return new(big.Rat).Set(c)
	}
	return new(big.Rat)
}

// univariateCoeffs extracts a degree→coefficient map from a polyNF whose only
// indeterminate is uvar (constants land at degree 0).
func univariateCoeffs(p *polyNF, uvar string) map[int64]*big.Rat {
	out := map[int64]*big.Rat{}
	for key, c := range p.coeff {
		var deg int64
		if e, ok := p.mono[key][uvar]; ok {
			deg = e
		}
		out[deg] = new(big.Rat).Set(c)
	}
	return out
}

// univGCD returns the monic gcd of two univariate polynomials over Q (degree→
// coeff maps), via the Euclidean algorithm. gcd(0,0)=0; a nonzero constant
// normalizes to 1.
func univGCD(a, b map[int64]*big.Rat) map[int64]*big.Rat {
	for !uniIsZero(b) {
		a, b = b, uniMod(a, b)
	}
	return uniMonic(a)
}

func uniIsZero(p map[int64]*big.Rat) bool {
	for _, c := range p {
		if c.Sign() != 0 {
			return false
		}
	}
	return true
}

func uniDeg(p map[int64]*big.Rat) (int64, bool) {
	deg := int64(-1)
	found := false
	for d, c := range p {
		if c.Sign() != 0 && (!found || d > deg) {
			deg, found = d, true
		}
	}
	return deg, found
}

// uniMod returns a mod b (remainder of univariate polynomial division over Q).
func uniMod(a, b map[int64]*big.Rat) map[int64]*big.Rat {
	r := map[int64]*big.Rat{}
	for d, c := range a {
		if c.Sign() != 0 {
			r[d] = new(big.Rat).Set(c)
		}
	}
	bd, ok := uniDeg(b)
	if !ok {
		return r // division by zero poly: leave a unchanged
	}
	blc := b[bd]
	for {
		rd, ok := uniDeg(r)
		if !ok || rd < bd {
			return r
		}
		factor := new(big.Rat).Quo(r[rd], blc) // r_lc / b_lc
		shift := rd - bd
		for d, c := range b {
			if c.Sign() == 0 {
				continue
			}
			term := new(big.Rat).Mul(factor, c)
			nd := d + shift
			if cur, ok := r[nd]; ok {
				cur.Sub(cur, term)
			} else {
				r[nd] = new(big.Rat).Neg(term)
			}
		}
		delete(r, rd) // leading term cancels exactly
	}
}

// uniMonic divides a univariate polynomial by its leading coefficient (→ leading
// coeff 1); the zero polynomial stays zero.
func uniMonic(p map[int64]*big.Rat) map[int64]*big.Rat {
	d, ok := uniDeg(p)
	if !ok {
		return map[int64]*big.Rat{0: new(big.Rat)} // zero
	}
	lc := p[d]
	out := map[int64]*big.Rat{}
	for k, c := range p {
		if c.Sign() != 0 {
			out[k] = new(big.Rat).Quo(c, lc)
		}
	}
	return out
}

// uniToValue rebuilds a Value from a univariate degree→coeff map over the
// indeterminate uval, reusing fromPolyNF's descending-degree reconstruction.
func uniToValue(p map[int64]*big.Rat, uval Value) Value {
	nf := newPolyNF()
	key := canonicalKey(uval)
	nf.atoms[key] = uval
	for d, c := range p {
		if c.Sign() == 0 {
			continue
		}
		var m nfMono
		if d == 0 {
			m = nfMono{}
		} else {
			m = nfMono{key: d}
		}
		nf.add(m, c)
	}
	return fromPolyNF(nf)
}


// fromPolyNF reconstructs a Value AST (Sum/Prod/Power/atoms/number) from a
// normal form, in the deterministic comparePolyNF key order. Equality is
// order-independent (compareValues), so the chosen order only affects the
// printed surface.
func fromPolyNF(p *polyNF) Value {
	if len(p.coeff) == 0 {
		return newInt(0)
	}
	keys := sortedKeys(p.coeff)
	// Print in descending total degree (then canonical key), matching Sage's
	// str() so an expand() result reads the same whether produced natively or by
	// Sage — keeping the printed surface (and DT's FactorSorter, which byte-
	// compares convert(,string)) consistent across the two paths.
	sort.SliceStable(keys, func(i, j int) bool {
		di, dj := monoTotalDeg(p.mono[keys[i]]), monoTotalDeg(p.mono[keys[j]])
		if di != dj {
			return di > dj
		}
		return keys[i] < keys[j]
	})
	terms := make([]Value, 0, len(keys))
	for _, k := range keys {
		terms = append(terms, monoTerm(p.coeff[k], p.mono[k], p.atoms))
	}
	if len(terms) == 1 {
		return terms[0]
	}
	return &Sum{Terms: terms}
}

// monoTotalDeg is the sum of exponents in a monomial.
func monoTotalDeg(m nfMono) int64 {
	var d int64
	for _, e := range m {
		d += e
	}
	return d
}

// monoTerm builds coeff * prod(atom^exp) as a Value, omitting a unit coefficient
// and unit exponents so the result reads like ordinary arithmetic output.
func monoTerm(c *big.Rat, m nfMono, atoms map[string]Value) Value {
	akeys := make([]string, 0, len(m))
	for a := range m {
		akeys = append(akeys, a)
	}
	sort.Strings(akeys)
	factors := make([]Value, 0, len(m)+1)
	for _, ak := range akeys {
		e := m[ak]
		if e == 1 {
			factors = append(factors, atoms[ak])
		} else {
			factors = append(factors, &Power{Base: atoms[ak], Exp: newInt(e)})
		}
	}
	coeffVal := normRat(new(big.Rat).Set(c))
	if len(factors) == 0 {
		return coeffVal // pure constant
	}
	cv, isInt := coeffVal.(Integer)
	if isInt && cv.Val.Cmp(big.NewInt(1)) == 0 {
		// coefficient 1: drop it
		if len(factors) == 1 {
			return factors[0]
		}
		return &Prod{Factors: factors}
	}
	return &Prod{Factors: append([]Value{coeffVal}, factors...)}
}

// atomOrConst reports whether v is a scalar constant (integer/rational) or a
// single indeterminate atom (name/jet variable) — values that normal/simplify
// leave unchanged.
func atomOrConst(v Value) (Value, bool) {
	switch v.(type) {
	case Integer, Rational, Name, *Indexed:
		return v, true
	}
	return nil, false
}

// polyAtom reports whether v is an indeterminate atom (a name or a jet/indexed
// variable). Numbers are not atoms; Func/Float etc. are not handled natively.
func polyAtom(v Value) (Value, bool) {
	switch v.(type) {
	case Name, *Indexed:
		return v, true
	}
	return nil, false
}

// signedIntExp returns the exponent as a (possibly negative) int64.
func signedIntExp(v Value) (int64, bool) {
	i, ok := v.(Integer)
	if !ok || !i.Val.IsInt64() {
		return 0, false
	}
	return i.Val.Int64(), true
}

// ratPow computes c^e for a rational c and integer e (negative e -> reciprocal).
// ok=false for 0 raised to a negative power.
func ratPow(c *big.Rat, e int64) (*big.Rat, bool) {
	if e == 0 {
		return big.NewRat(1, 1), true
	}
	neg := e < 0
	if neg {
		if c.Sign() == 0 {
			return nil, false
		}
		e = -e
	}
	res := big.NewRat(1, 1)
	base := new(big.Rat).Set(c)
	for ; e > 0; e-- {
		res.Mul(res, base)
	}
	if neg {
		res.Inv(res)
	}
	return res, true
}

// nonNegIntExp returns the exponent as a non-negative int64, ok=false otherwise.
func nonNegIntExp(v Value) (int64, bool) {
	i, ok := v.(Integer)
	if !ok || i.Val.Sign() < 0 || !i.Val.IsInt64() {
		return 0, false
	}
	return i.Val.Int64(), true
}

// negInfinity builds Maple's -infinity value the way the rest of the interpreter
// represents it: the product (-1)*infinity (matches the Sage neg_infinity decode).
func negInfinity() Value {
	return &Prod{Factors: []Value{Integer{big.NewInt(-1)}, Name{"infinity"}}}
}
