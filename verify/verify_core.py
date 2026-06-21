r"""verify_core.py — independent Sage verifier for differential Thomas
decompositions.  Imported by run_verify.py; run under `sage -python`.

ALL reductions / membership tests use Sage's own polynomial machinery
(Groebner bases, ideal membership, saturation).  We never call the open-maple /
DT reducer to perform a check (that would be circular).  open-maple is used only
upstream to PRODUCE the cell data.

Design
------
Everything lives in a single polynomial ring  QQ[x, y, z, <all jets>, ...]  with
DegRevLex term order.  The independent variables x,y,z are ORDINARY ring
variables here; that they are *units in the coefficient field* (the basis for
content removal) is modelled by SATURATING every ideal by the product of the
ivars in addition to the cell's inequations.  This keeps all of A-E inside one
clean ring and lets ideal membership / saturation answer every question.

Jets name[i,j,k] are flat variables name__i_j_k (see maple_parse).  The DegRevLex
*ranking* used for leaders/initials/separants (check A and passivity ordering) is
the Thomas standard ranking, computed separately from the ring's monomial order.
"""

import re
import itertools

from sage.all import PolynomialRing, QQ, Integer, sage_eval

import maple_parse
import build_input


JETVAR_RE = re.compile(r'\b([A-Za-z]\w*__-?\d+(?:_-?\d+)*)\b')


def collect_vars(strs):
    out = set()
    for s in strs:
        for m in JETVAR_RE.finditer(s):
            out.add(m.group(1))
    return out


def parse_idx(varname):
    base, _, tail = varname.partition("__")
    if not tail:
        return base, None
    return base, tuple(int(t) for t in tail.split("_"))


def rank_key(varname, dvar_order):
    """Thomas standard ranking key. LARGER tuple = HIGHER-ranked (more 'main').
    Primary: total derivative order; then dvar priority (earlier dvar higher);
    then the multi-index itself (lex on raised index)."""
    base, idx = parse_idx(varname)
    if idx is None:
        return (-1, 0, ())
    order = sum(idx)
    try:
        pos = dvar_order.index(base)
    except ValueError:
        pos = len(dvar_order)
    return (order, -pos, idx)


class Problem(object):
    """Holds the ring and the parsed input + cells for one example."""

    def __init__(self, input_system, cells, extra_strs=None):
        self.ivars = list(input_system["ivars"])
        self.dvar_order = list(input_system["dvars"])
        # gather every variable name across input, cells, and extras
        strs = list(input_system["equations"]) + list(input_system.get("inequations", []))
        for c in cells:
            strs += c["equations"] + c["inequations"]
        if extra_strs:
            strs += extra_strs
        self.jetset = collect_vars(strs)
        # add ivars as ring vars
        self.ringvars = sorted(self.jetset) + list(self.ivars)
        self.R = PolynomialRing(QQ, self.ringvars, order="degrevlex")
        self.env = {v: self.R(v) for v in self.ringvars}
        self.input_eqs = [self._p(s) for s in input_system["equations"]]
        self.input_ineqs = [self._p(s) for s in input_system.get("inequations", [])]
        self.cells = []
        for c in cells:
            self.cells.append({
                "equations": [self._p(s) for s in c["equations"]],
                "inequations": [self._p(s) for s in c["inequations"]],
            })

    def _p(self, s):
        return self.R(sage_eval(s, locals=self.env))

    def add_var_strings(self, strs):
        """Ensure all variables in `strs` exist; if any are new, rebuild a bigger
        ring (returns a fresh ring and a coercion). Used by prolongation."""
        newvars = collect_vars(strs) - self.jetset
        if not newvars:
            return self.R, (lambda p: p), self.env
        allvars = sorted(self.jetset | newvars) + list(self.ivars)
        Rb = PolynomialRing(QQ, allvars, order="degrevlex")
        env = {v: Rb(v) for v in allvars}
        coerce = (lambda p: Rb(p))
        return Rb, coerce, env

    # -- ranking primitives (check A) --
    def leader(self, poly):
        occ = [v for v in self.R.gens()
               if str(v) not in self.ivars and poly.degree(v) > 0]
        if not occ:
            return None
        return max(occ, key=lambda v: rank_key(str(v), self.dvar_order))

    def initial(self, poly, ld):
        d = poly.degree(ld)
        return poly.coefficient({ld: d})

    def separant(self, poly, ld):
        return poly.derivative(ld)


# ---------------------------------------------------------------------------
# Saturation / membership (pure Sage)
# ---------------------------------------------------------------------------

def _ivar_gens(prob, R):
    return [R(prob.R(v)) for v in prob.ivars]


def saturated_empty(R, gens, sats):
    """True iff the system { g = 0 for g in gens, s != 0 for s in sats } has NO
    solution over the algebraic closure, i.e. 1 in (gens):(prod sats)^inf.
    Rabinowitsch: add fresh t, generator t*prod(sats) - 1, test 1 in ideal."""
    base = R.base_ring()
    names = list(R.variable_names()) + ["RABt"]
    RR = PolynomialRing(base, names, order="degrevlex")
    inj = {v: RR(v) for v in R.variable_names()}
    def up(p):
        return RR(p.subs({R(k): inj[k] for k in R.variable_names()})) if p.parent() is R else RR(p)
    G = [RR(p) for p in gens]
    if sats:
        prod = RR(1)
        for s in sats:
            prod *= RR(s)
        G.append(RR("RABt") * prod - 1)
    if not G:
        return False
    return RR(1) in RR.ideal(G)


def ritt_reduce(R, poly, triangular, dvar_order, ivars):
    """Classical Ritt / triangular-set pseudo-reduction of `poly` modulo the
    triangular set `triangular` (a list of ring elements forming a triangular
    set: distinct leaders).  Returns the pseudo-remainder.

    For each member t with leader L_t and initial I_t, while poly's degree in L_t
    is >= t's degree in L_t, replace poly by  prem(poly, t, L_t) = I_t^? * poly
    reduced by t (Sage's polynomial pseudo-quotient).  A poly reducing to 0 means
    it lies in the saturated ideal of the triangular set (saturated by the
    initials), which — given check A guarantees the initials are nonzero on the
    cell — is exactly 'pseudo-reduces to 0 modulo the cell'.

    This is far cheaper than a Groebner basis over a 60+-variable ring and is the
    mathematically faithful soundness test (the same reduction DT performs, done
    independently in Sage's own polynomial arithmetic)."""
    ivar_set = set(ivars)

    def leader(p):
        occ = [v for v in R.gens() if str(v) not in ivar_set and p.degree(v) > 0]
        if not occ:
            return None
        return max(occ, key=lambda v: rank_key(str(v), dvar_order))

    # order triangular set by descending leader rank so we eliminate top-down
    tris = []
    for t in triangular:
        L = leader(t)
        if L is not None:
            tris.append((L, t))
    tris.sort(key=lambda LT: rank_key(str(LT[0]), dvar_order), reverse=True)

    r = poly
    changed = True
    guard = 0
    while changed and r != 0:
        changed = False
        guard += 1
        if guard > 1000:
            break
        L = leader(r)
        if L is None:
            break
        for (Lt, t) in tris:
            if str(Lt) == str(L) and r.degree(L) >= t.degree(L):
                # pseudo-remainder of r by t w.r.t. L
                # Sage univariate-in-L pseudo division: view in R[L]
                r = _prem_wrt(R, r, t, L)
                changed = True
                break
    return r


def _prem_wrt(R, f, g, L):
    """Pseudo-remainder of f by g with respect to variable L (both in R)."""
    # Represent as univariate polynomials in L over the fraction field of the
    # other variables, take remainder, then clear denominators back into R.
    from sage.all import PolynomialRing as _PR
    others = [v for v in R.gens() if v != L]
    if not others:
        Runi = _PR(R.base_ring(), [str(L)])
    else:
        K = R.remove_var(L).fraction_field()
        Runi = _PR(K, [str(L)])
    fL = Runi(f.polynomial(L)) if hasattr(f, "polynomial") else Runi(f)
    gL = Runi(g.polynomial(L)) if hasattr(g, "polynomial") else Runi(g)
    rem = fL % gL
    # map back to R: rem has coeffs in K (rational fns in others); clear denoms
    num = rem.numerator() if hasattr(rem, "numerator") else rem
    # build R element from rem by substituting
    res = R(0)
    Lv = L
    for i, c in enumerate(rem.list()):
        if c == 0:
            continue
        # c is in K = Frac(R/(L)); take numerator (pseudo-remainder clears denoms
        # naturally up to a power of g's initial, which is a unit on the cell)
        cn = c.numerator() if hasattr(c, "numerator") else c
        res += R(cn) * Lv ** i
    return res


def _max_order_strs(strs):
    """Max total derivative order of any jet appearing in the strings."""
    pat = re.compile(r'\b[A-Za-z]\w*__(-?\d+(?:_-?\d+)*)\b')
    mo = 0
    for s in strs:
        for m in pat.finditer(s):
            mo = max(mo, sum(int(t) for t in m.group(1).split("_")))
    return mo


def cell_field_reducer(cell, ivars, prolong_order=1, extra_strs=None):
    """Build a parameter-in-field reducer for one cell (no input needed).

    Returns (red, parse, nvars) where:
      red(poly_str)   -> the normal form of poly_str modulo the (prolonged) cell
                         ideal over the parameter-in-field ring (0 iff in ideal);
      parse(poly_str) -> the ring element;
      nvars           -> number of jet ring variables.
    Used by both the soundness check and the passivity (Delta-poly) check so they
    share the same independent, cheap reduction engine."""
    import re
    from sage.all import PolynomialRing as _PR, QQ as _QQ, FractionField as _FF, sage_eval as _se
    cell_eqs = [maple_parse.normalize_poly_string(str(e)) for e in cell["equations"]]
    extra = list(extra_strs or [])
    _jp0 = re.compile(r'\b[A-Za-z]\w*__(-?\d+(?:_-?\d+)*)\b')
    def _mo(strs):
        mo = 0
        for s in strs:
            for m in _jp0.finditer(s):
                mo = max(mo, sum(int(t) for t in m.group(1).split("_")))
        return mo
    if _mo(cell_eqs + extra) == 0:
        prolong_order = 0
    prol = prolong_strings(cell_eqs, ivars, prolong_order)
    allstrs = prol + extra
    jp = re.compile(r'\b([A-Za-z]\w*)__(-?\d+(?:_-?\d+)*)\b')
    bases = {}
    for s in allstrs:
        for m in jp.finditer(s):
            bases.setdefault(m.group(1), set()).add(
                tuple(int(t) for t in m.group(2).split("_")))
    param_bases = set()
    for base, idxs in bases.items():
        higher = [i for i in idxs if sum(i) > 0]
        if not higher:
            continue
        is_param = True
        for i in higher:
            var = base + "__" + "_".join(str(t) for t in i)
            for s in cell_eqs:
                if re.search(r'\b' + re.escape(var) + r'\b', s):
                    if len(set(m.group(0) for m in jp.finditer(s))) > 1:
                        is_param = False
                        break
            if not is_param:
                break
        if is_param:
            param_bases.add(base)
    field_params = [b + "__" + "_".join(["0"] * len(ivars)) for b in param_bases]
    field_gens = list(ivars) + field_params
    pderiv = set()
    for b in param_bases:
        for i in bases[b]:
            if sum(i) > 0:
                pderiv.add(b + "__" + "_".join(str(t) for t in i))
    all_vars = set()
    for s in allstrs:
        for m in jp.finditer(s):
            all_vars.add(m.group(0))
    ring_vars = sorted(v for v in all_vars if v not in field_params and v not in pderiv)
    K = _FF(_PR(_QQ, field_gens)) if field_gens else _QQ
    Rj = _PR(K, ring_vars) if ring_vars else K
    env = {}
    if ring_vars:
        for v in ring_vars:
            env[v] = Rj(v)
        bp = K.base() if field_gens else None
        for g in field_gens:
            env[g] = Rj(K(bp(g)))
        for v in pderiv:
            env[v] = Rj(0)
    else:
        bp = K.base() if field_gens else None
        for g in field_gens:
            env[g] = (K(bp(g)) if field_gens else _QQ(0))
        for v in pderiv:
            env[v] = K(0)

    def parse(s):
        return Rj(_se(maple_parse.normalize_poly_string(s) if "[" in s else s, locals=env)) \
            if False else Rj(_se(s, locals=env))

    cellI = [Rj(_se(s, locals=env)) for s in prol]
    ng = Rj.ngens() if hasattr(Rj, "ngens") else 0
    I = Rj.ideal(cellI) if hasattr(Rj, "ideal") else None
    if I is not None and hasattr(I, "groebner_basis"):
        GB = I.groebner_basis()
        def red(s):
            return Rj(_se(s, locals=env)).reduce(GB)
    elif ng == 1:
        # univariate poly ring over a field: principal ideal; reduce mod gcd.
        from sage.all import gcd as _gcd
        g = Rj(0)
        for p in cellI:
            g = _gcd(g, p)
        def red(s):
            p = Rj(_se(s, locals=env))
            return p % g if g != 0 else p
    else:
        def red(s):
            return Rj(_se(s, locals=env))
    return red, parse, ng


def soundness_check_cell(input_system, cell, ivars, dvar_order, prolong_order=1):
    """Independent SOUNDNESS test for one cell, using the parameter-in-field ring.

    Steps:
      1. Prolong the cell equations to `prolong_order`.
      2. Detect constant-parameter dvars (only constancy constraints) and move
         their order-0 jets + the ivars into the coefficient field; substitute
         parameter-derivative jets to 0; the remaining (principal) jets are the
         ring variables — only ~15-25 of them, so the GB is cheap.  Because the
         cell-equation initials are polynomials in the parameters, they become
         FIELD UNITS, so plain ideal membership over this ring == the
         saturated-by-initials membership == 'pseudo-reduces to 0 mod the cell'.
      3. Compute the cell ideal's Groebner basis once; reduce every input equation.

    Returns (ok, fail_list) where fail_list holds (input_index, remainder_str) for
    each input equation that does NOT reduce to 0."""
    import re
    from sage.all import PolynomialRing as _PR, QQ as _QQ, FractionField as _FF, sage_eval as _se
    cell_eqs = [maple_parse.normalize_poly_string(str(e)) for e in cell["equations"]]
    cell_ineqs = [maple_parse.normalize_poly_string(str(e)) for e in cell["inequations"]]
    in_eqs = list(input_system["equations"])
    in_ineqs = list(input_system.get("inequations", []))
    # If the INPUT is purely algebraic (no jet of order>0 anywhere in it), then
    # there is no differential structure and prolongation is meaningless (it would
    # invent phantom derivative jets).  Detect and disable prolongation.
    _jp0 = re.compile(r'\b[A-Za-z]\w*__(-?\d+(?:_-?\d+)*)\b')
    def _max_order(strs):
        mo = 0
        for s in strs:
            for m in _jp0.finditer(s):
                mo = max(mo, sum(int(t) for t in m.group(1).split("_")))
        return mo
    if _max_order(in_eqs + cell_eqs) == 0:
        prolong_order = 0
    prol = prolong_strings(cell_eqs, ivars, prolong_order)
    allstrs = prol + in_eqs + in_ineqs

    jp = re.compile(r'\b([A-Za-z]\w*)__(-?\d+(?:_-?\d+)*)\b')
    bases = {}
    for s in allstrs:
        for m in jp.finditer(s):
            base = m.group(1)
            idx = tuple(int(t) for t in m.group(2).split("_"))
            bases.setdefault(base, set()).add(idx)
    # constant-parameter detection: a base is a parameter iff every order>0 jet of
    # it appears ONLY alone (constancy eq form) — never together with another jet.
    param_bases = set()
    for base, idxs in bases.items():
        higher = [i for i in idxs if sum(i) > 0]
        if not higher:
            continue
        is_param = True
        for i in higher:
            var = base + "__" + "_".join(str(t) for t in i)
            for s in cell_eqs + in_eqs:
                if re.search(r'\b' + re.escape(var) + r'\b', s):
                    if len(set(m.group(0) for m in jp.finditer(s))) > 1:
                        is_param = False
                        break
            if not is_param:
                break
        if is_param:
            param_bases.add(base)
    field_params = [b + "__" + "_".join(["0"] * len(ivars)) for b in param_bases]
    field_gens = list(ivars) + field_params
    pderiv = set()
    for b in param_bases:
        for i in bases[b]:
            if sum(i) > 0:
                pderiv.add(b + "__" + "_".join(str(t) for t in i))
    all_vars = set()
    for s in allstrs:
        for m in jp.finditer(s):
            all_vars.add(m.group(0))
    ring_vars = sorted(v for v in all_vars if v not in field_params and v not in pderiv)
    K = _FF(_PR(_QQ, field_gens)) if field_gens else _QQ
    Rj = _PR(K, ring_vars) if ring_vars else K
    env = {}
    if ring_vars:
        for v in ring_vars:
            env[v] = Rj(v)
        bp = K.base() if field_gens else None
        for g in field_gens:
            env[g] = Rj(K(bp(g)))
        for v in pderiv:
            env[v] = Rj(0)
    else:
        bp = K.base() if field_gens else None
        for g in field_gens:
            env[g] = K(bp(g))
        for v in pderiv:
            env[v] = K(0)
    cellI = [Rj(_se(s, locals=env)) for s in prol]
    ng = Rj.ngens() if hasattr(Rj, "ngens") else 0
    if ng == 1:
        # univariate polynomial ring over a field: ideal principal; reduce mod gcd.
        # (its elements lack .reduce(GB); use the Euclidean remainder.)
        from sage.all import gcd as _gcd
        g = Rj(0)
        for p in cellI:
            g = _gcd(g, p)
        def red(s):
            p = Rj(_se(s, locals=env))
            return p % g if g != 0 else p
    elif ng > 1:
        GB = Rj.ideal(cellI).groebner_basis()
        def red(s):
            return Rj(_se(s, locals=env)).reduce(GB)
    else:
        # everything collapsed to the field: membership = literal zero
        def red(s):
            return Rj(_se(s, locals=env))
    fails = []
    for k, ie in enumerate(in_eqs):
        r = red(ie)
        if r != 0:
            fails.append((k, str(r)[:80]))
    ineq_fails = []
    for k, ii in enumerate(in_ineqs):
        if red(ii) == 0:
            ineq_fails.append(k)
    return (len(fails) == 0 and len(ineq_fails) == 0), fails, ineq_fails, len(ring_vars)


def saturated_groebner(R, gens, sats):
    """Return (RR, GB, up) where RR is the Rabinowitsch ring (one fresh var per
    saturation generator collapsed into a single t), GB is the reduced Groebner
    basis of the saturation ideal (gens):(prod sats)^inf computed via the t-trick,
    and up coerces an R-element into RR.  Computing the GB ONCE lets many
    membership tests (input-eq reductions) share it — far cheaper than a fresh
    radical-membership solve per equation."""
    base = R.base_ring()
    names = list(R.variable_names()) + ["SATt"]
    RR = PolynomialRing(base, names, order="degrevlex")
    up = (lambda p: RR(p))
    G = [RR(p) for p in gens]
    if sats:
        prod = RR(1)
        for s in sats:
            prod *= RR(s)
        G.append(RR("SATt") * prod - 1)
    I = RR.ideal(G) if G else RR.ideal([RR(0)])
    GB = I.groebner_basis()
    return RR, GB, up


def in_ideal_via_gb(RR, GB, poly):
    """True iff poly (already in RR) reduces to 0 modulo the Groebner basis GB,
    i.e. poly is in the ideal.  This is IDEAL membership (not radical); it is the
    correct test for 'pseudo-reduces to 0' / lies in the saturated ideal."""
    return RR(poly).reduce(GB) == 0


def vanishes_on(R, poly, gens, sats):
    """True iff poly = 0 on V(gens) \ V(sats), i.e. poly in radical of the
    saturated ideal (gens):(prod sats)^inf.

    Equivalent test (combines saturation + radical membership in one Rabinowitsch
    system): poly vanishes wherever gens vanish and sats are nonzero
      <=>  the system { gens=0, sats != 0, poly != 0 } is INCONSISTENT
      <=>  saturated_empty(gens, sats + [poly]).
    This is exactly a Nullstellensatz certificate and needs no separate radical
    computation."""
    return saturated_empty(R, gens, list(sats) + [poly])


# ---------------------------------------------------------------------------
# Prolongation
# ---------------------------------------------------------------------------

def raise_index_var(varname, axis):
    base, idx = parse_idx(varname)
    idx = list(idx)
    idx[axis] += 1
    return maple_parse.jet_to_var(base, tuple(idx))


def total_derivative_str(poly_str, ivars, axis):
    """Symbolic total derivative of a normalized polynomial STRING w.r.t.
    ivar[axis], returned as a normalized string.  Uses sympy on flat jet symbols
    (pure CAS, engine-independent)."""
    import sympy
    # build sympy expr; flat jet symbols + ivar symbols
    names = set(JETVAR_RE.findall(poly_str)) | set(ivars)
    syms = {n: sympy.Symbol(n) for n in names}
    # also bare ivar tokens
    for iv in ivars:
        syms.setdefault(iv, sympy.Symbol(iv))
    expr = sympy.sympify(poly_str.replace("**", "^").replace("^", "**"), locals=syms)
    expr = sympy.expand(expr)
    res = sympy.Integer(0)
    for s in expr.free_symbols:
        part = sympy.diff(expr, s)
        if part == 0:
            continue
        nm = s.name
        if nm in ivars:
            if nm == ivars[axis]:
                res += part
            continue
        base, idx = parse_idx(nm)
        if idx is None:
            continue
        rv = raise_index_var(nm, axis)
        res += part * sympy.Symbol(rv)
    res = sympy.expand(res)
    return maple_parse.normalize_poly_string(str(res).replace("**", "^"))


def prolong_strings(eq_strs, ivars, order):
    """Return eq_strs plus all total derivatives up to total `order`.
    BFS over derivative multi-indices."""
    out = list(eq_strs)
    frontier = list(eq_strs)
    for _ in range(order):
        nxt = []
        for s in frontier:
            for axis in range(len(ivars)):
                d = total_derivative_str(s, ivars, axis)
                if d not in out:
                    out.append(d)
                    nxt.append(d)
        frontier = nxt
        if not frontier:
            break
    return out
