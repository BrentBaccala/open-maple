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
	case "coeff":
		v, ok = nativeCoeff(args)
	}
	if !ok {
		return nil, false
	}
	if it.verifyNative {
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
	p, ok := toPolyNF(v)
	if !ok {
		return nil, false
	}
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

// polyAtom reports whether v is an indeterminate atom (a name or a jet/indexed
// variable). Numbers are not atoms; Func/Float etc. are not handled natively.
func polyAtom(v Value) (Value, bool) {
	switch v.(type) {
	case Name, *Indexed:
		return v, true
	}
	return nil, false
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
