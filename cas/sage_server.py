#!/usr/bin/env sage-python
# -*- coding: utf-8 -*-
#
# sage_server.py — long-lived JSON-lines computer-algebra server for the
# open_maple → DifferentialThomas Phase-3 Sage backend.
#
# Protocol (one JSON object per line, request on stdin, response on stdout):
#
#   request : {"op": "<name>", "vars": ["x","y",...], "args": [<arg>,...],
#              "frac": <bool>, "id": <int>}
#       op    : the delegated computer-algebra op (factor, gcd, normal, ...)
#       vars  : the list of *sanitized* indeterminate names (already round-trip
#               safe identifiers; the Go side sanitizes u[1,0] -> u_1_0).
#       args  : op operands. Each operand is one of:
#                 {"poly": "<sage/maple-parseable string>"}  — a polynomial /
#                       rational / symbolic expression in the sanitized vars
#                 {"int": "123"}    — an exact integer (decimal string)
#                 {"name": "x"}     — a bare variable / symbol name
#                 {"matrix": [[...],[...]]}  — rows of poly strings
#                 {"vector": [...]}          — entries (poly strings)
#                 {"raw": <json>}   — a literal JSON scalar (int/str/bool)
#       frac  : if true, build over Frac(PolynomialRing) (for normal/numer/denom)
#
#   response: {"id": <int>, "ok": true,  "result": <result>}
#         or  {"id": <int>, "ok": false, "error": "<message>"}
#
#   <result> shapes:
#       {"poly": "x^2 + 1"}                       — a single expression string
#       {"int": "5"}                              — an exact integer
#       {"bool": true}
#       {"list": [<result>, ...]}                 — ordered list
#       {"factors": {"unit": "<str>",
#                    "factors": [["<facstr>", <mult:int>], ...]}}
#       {"matrix": [[...],[...]]} / {"vector":[...]}
#
# All polynomial output strings are Sage's str() form, which is Maple-parseable
# (x^2 + 2*x + 1, 1/2, ^/*/+ ); the Go side re-parses them with the existing
# Phase-1 tokenizer+parser.
#
# Domain: characteristic 0 over QQ / QQ(params).  The FactorStrong tower-RootOf
# path is NOT implemented here (returns a structured error).
#
# Licensing: this server never invokes Maple; it only uses Sage.  Fixtures are
# computed independently.

from __future__ import print_function
import sys
import json
import traceback

from sage.all import (
    QQ, ZZ, PolynomialRing, FractionField, SR, var, matrix, vector,
    gcd as sage_gcd, lcm as sage_lcm, binomial as sage_binomial,
    Integer as SageInteger,
)


# ---------------------------------------------------------------------------
# Ring / parsing helpers
# ---------------------------------------------------------------------------

def make_ring(varnames, frac=False):
    """Build a polynomial ring (or its fraction field) over QQ in varnames.

    If varnames is empty, fall back to a single dummy generator so a constant
    can still be parsed; callers that need pure-constant arithmetic should
    handle it before calling.
    """
    if not varnames:
        # A ring with no generators: use QQ directly for constants, but most
        # ops want a ring with .gens(); give a 1-var ring on a fresh name.
        R = PolynomialRing(QQ, ['t_dummy'])
    else:
        R = PolynomialRing(QQ, list(varnames))
    if frac:
        return FractionField(R)
    return R


def ring_namespace(R):
    """Map generator name -> generator object for sage_eval-style parsing."""
    ns = {}
    base = R.base_ring() if hasattr(R, 'base_ring') else R
    # FractionField: get the underlying polynomial ring's gens
    PR = R.ring() if hasattr(R, 'ring') else R
    try:
        for g in PR.gens():
            ns[str(g)] = R(g)
    except Exception:
        pass
    return ns


# ---------------------------------------------------------------------------
# Flat-sum AST rebalancing
#
# Some DT ops (notably evala/simplify on the combined hydrogen system, which
# runs with no end reduction) hand us fully expanded expressions whose
# numerators are flat sums of tens of thousands of monomials. sage_eval parses
# via Python's compile(), and a flat sum  t1 + t2 + ... + tN  compiles to an
# N-deep left-associated AST. CPython's constant-folder astfold_expr
# (Python/ast_opt.c) then recurses once per additive term *on the C stack*. On
# Sage's Python 3.11 there is no C-recursion guard (setrecursionlimit does NOT
# gate this path), so beyond ~47k terms the 8 MB main-thread stack overflows and
# the process SIGSEGVs — surfacing as cysignals' misleading "compiled module ...
# not wrapped with sig_on/sig_off" banner.
#
# Fix: rewrite the string so long flat sums become *balanced* binary trees
# (AST depth O(log N) ~ 16 for 60k terms instead of O(N)), recursing into
# parenthesised subexpressions. This is pure string restructuring — sage_eval
# still does all the algebra, and the reassociation is exact over QQ.
# ---------------------------------------------------------------------------

def _split_top_additive(s):
    """Split s into signed additive terms at paren-depth 0.

    A '+'/'-' is an additive operator only when it is not unary, i.e. the
    previous significant char is not one of  ( ^ * / + -  (this keeps exponent
    signs like x^-1 and leading unary signs attached to their term)."""
    terms = []
    depth = 0
    start = 0
    prev = ''  # previous non-space significant char
    for i, c in enumerate(s):
        if c == '(':
            depth += 1
        elif c == ')':
            depth -= 1
        elif c in '+-' and depth == 0 and prev not in ('', '(', '^', '*', '/', '+', '-'):
            terms.append(s[start:i])
            start = i
        if not c.isspace():
            prev = c
    terms.append(s[start:])
    return [t.strip() for t in terms if t.strip()]


def _rebalance_parens(term):
    """Recursively rebalance the contents of every top-level (...) group inside
    a single term (a term has no top-level additive operators of its own)."""
    out = []
    i = 0
    n = len(term)
    while i < n:
        c = term[i]
        if c == '(':
            depth = 1
            j = i + 1
            while j < n and depth:
                if term[j] == '(':
                    depth += 1
                elif term[j] == ')':
                    depth -= 1
                j += 1
            inner = term[i + 1:j - 1]
            out.append('(' + rebalance(inner) + ')')
            i = j
        else:
            out.append(c)
            i += 1
    return ''.join(out)


def _balanced_join(terms):
    """Join signed term-strings into a balanced binary '+' tree string."""
    if len(terms) == 1:
        return '(' + terms[0] + ')'
    mid = len(terms) // 2
    return '(' + _balanced_join(terms[:mid]) + '+' + _balanced_join(terms[mid:]) + ')'


def rebalance(s):
    """Rewrite an expression string so long flat sums become balanced binary
    trees, recursing into parenthesised subexpressions. Keeps the AST depth
    handed to compile() at O(log N), avoiding the astfold_expr C-stack overflow
    on huge expanded polynomials."""
    terms = [_rebalance_parens(t) for t in _split_top_additive(s)]
    return _balanced_join(terms)


def parse_in_ring(s, R):
    """Parse a Maple/Sage-form expression string into an element of ring R."""
    ns = ring_namespace(R)
    # sage_eval parses arithmetic expressions using the provided locals. Big
    # flat sums are rebalanced first so compile() never recurses N-deep (see the
    # _split_top_additive / rebalance block above).
    from sage.misc.sage_eval import sage_eval
    return R(sage_eval(rebalance(s), locals=ns))


def parse_symbolic(s, varnames):
    """Parse an expression into Sage's symbolic ring SR (for diff over
    transcendental functions like cos(phi[0]))."""
    import re
    import sage.all as _sa
    from sage.all import function
    from sage.misc.sage_eval import sage_eval
    ns = {}
    for nm in varnames:
        ns[nm] = var(nm)
    # An applied-function head that is neither a declared variable nor a known
    # Sage callable (cos/sin/diff/...) is an unknown differential dependent
    # variable, e.g. u in u(x, y) or diff(u(x, y), x). Declare it as a symbolic
    # function so Sage keeps diff(u(x, y), x) unevaluated instead of raising
    # NameError ("name 'u' is not defined"). This is what makes the leader of a
    # single first-order equation (u[1,0] -> diff(u(x, y), x)) printable.
    for m in re.finditer(r'([A-Za-z_][A-Za-z0-9_]*)\s*\(', s):
        nm = m.group(1)
        if nm in ns or hasattr(_sa, nm):
            continue
        ns[nm] = function(nm)
    return SR(sage_eval(rebalance(s), locals=ns))


# ---------------------------------------------------------------------------
# Argument decoding
# ---------------------------------------------------------------------------

def decode_arg(a, R):
    """Decode one request arg into a ring element (or python scalar)."""
    if "poly" in a:
        return parse_in_ring(a["poly"], R)
    if "int" in a:
        return ZZ(int(a["int"]))
    if "name" in a:
        ns = ring_namespace(R)
        if a["name"] in ns:
            return ns[a["name"]]
        # unknown name -> treat as a fresh generator string parse
        return parse_in_ring(a["name"], R)
    if "raw" in a:
        return a["raw"]
    raise ValueError("cannot decode arg: %r" % (a,))


def decode_matrix(a, R):
    rows = a["matrix"]
    return matrix(R, [[parse_in_ring(c, R) if isinstance(c, str) else R(c)
                       for c in row] for row in rows])


def decode_vector(a, R):
    ents = a["vector"]
    return vector(R, [parse_in_ring(c, R) if isinstance(c, str) else R(c)
                      for c in ents])


# ---------------------------------------------------------------------------
# Result encoding
# ---------------------------------------------------------------------------

def enc_poly(p):
    return {"poly": str(p)}


def enc_int(n):
    return {"int": str(n)}


def enc_list(items):
    return {"list": items}


# ---------------------------------------------------------------------------
# Op implementations
# ---------------------------------------------------------------------------

def op_factor(req):
    """factor / factors -> {factors: {unit, factors:[[fac,mult],...]}}.

    Matches Maple's factors(p) = [unit, [[f1,m1],...]] shape (the Go side
    builds the Maple List from this structured form).
    """
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if p == 0:
        return {"factors": {"unit": "0", "factors": []}}
    F = p.factor()
    unit = F.unit()
    facs = [[str(fac), int(mult)] for (fac, mult) in F]
    return {"factors": {"unit": str(unit), "factors": facs}}


def op_gcd(req):
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    return enc_poly(a.gcd(b))


def op_lcm(req):
    """lcm is variadic: the least common multiple of an arbitrary number of
    polynomials. lcm(x, y, z) = x*y*z."""
    R = make_ring(req["vars"])
    args = req["args"]
    if not args:
        return enc_poly(R(1))
    acc = decode_arg(args[0], R)
    for a in args[1:]:
        acc = acc.lcm(decode_arg(a, R))
    return enc_poly(acc)


def op_expand(req):
    # expand over SR to handle symbolic too, then return string.
    try:
        R = make_ring(req["vars"])
        p = decode_arg(req["args"][0], R)
        return enc_poly(p)  # ring elements are already expanded
    except Exception:
        e = parse_symbolic(req["args"][0]["poly"], req["vars"])
        return enc_poly(e.expand())


def op_normal(req):
    """normal(f) -> simplified rational function string."""
    R = make_ring(req["vars"], frac=True)
    f = decode_arg(req["args"][0], R)
    # FractionField elements are automatically in lowest terms.
    return enc_poly(f)


def op_numer(req):
    R = make_ring(req["vars"], frac=True)
    f = decode_arg(req["args"][0], R)
    return enc_poly(f.numerator())


def op_denom(req):
    R = make_ring(req["vars"], frac=True)
    f = decode_arg(req["args"][0], R)
    return enc_poly(f.denominator())


def op_degree(req):
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    # Maple: degree(0, ...) == -infinity (and Sage's univariate p.degree() on the
    # zero polynomial raises a bare NotImplementedError). DT computes
    # `p['Rank'] := degree(StandardForm(p), Leader(p))` and tests `degree(...) >= 0`,
    # both of which want the -infinity convention for the zero polynomial.
    if p == 0:
        return {"neg_infinity": True}
    if len(req["args"]) >= 2 and R.ngens() > 1:
        a = req["args"][1]
        # Maple degree(p, [x,y,...]) / degree(p, {x,...}): the maximum total
        # degree among the listed variables over all monomials of p.
        if isinstance(a, dict) and "exprlist" in a:
            # NOTE: Maple distinguishes a list [x,y] (order-sensitive VECTOR
            # degree: degree(p,x1) + degree(lcoeff(p,x1),[x2..])) from a set
            # {x,y} (total degree). We compute the total/max form for both. This
            # is the correct answer for the set form; the list (vector) form is
            # NOT exercised by DifferentialThomas (verified: no degree(p,[...])
            # call sites in ~/DifferentialThomas/src), so the vector form is
            # deliberately deferred rather than implemented.
            gens = [parse_in_ring(s, R) for s in a["exprlist"]]
            idxs = []
            for g in gens:
                try:
                    idxs.append(R.gens().index(g))
                except Exception:
                    pass
            if p == 0:
                # Maple: degree of 0 is -infinity, but DT only compares via this
                # so 0 is a safe sentinel here (no negative path is exercised).
                return enc_int(0)
            if not idxs:
                return enc_int(0)
            return enc_int(max(sum(e[i] for i in idxs) for e in p.exponents()))
        x = decode_arg(a, R)
        return enc_int(p.degree(x))
    return enc_int(p.degree())


def op_ldegree(req):
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    # Maple: the identically-zero polynomial has ldegree +infinity (degree
    # -infinity). DT compares ldegree only via >= so a sentinel would do, but
    # match Maple exactly.
    if p == 0:
        return {"pos_infinity": True}
    if len(req["args"]) >= 2:
        a = req["args"][1]
        # list/set of variables: total (set) low degree — the minimal sum of the
        # listed variables' exponents over all monomials. (The order-sensitive
        # vector form for a list is not used by DT; the total form is correct for
        # the set form and a safe non-crashing answer for the list form.)
        if isinstance(a, dict) and "exprlist" in a:
            gens = {str(g): i for i, g in enumerate(R.gens())}
            idxs = [gens[s] for s in a["exprlist"] if s in gens]
            if not idxs:
                return enc_int(0)
            return enc_int(min(sum(e[i] for i in idxs) for e in p.exponents()))
        x = decode_arg(a, R)
        try:
            xi = R.gens().index(x)
        except Exception:
            xi = None
        if xi is not None:
            degs = [e[xi] for e in p.exponents()]
            return enc_int(min(degs))
    degs = [sum(e) if hasattr(e, '__iter__') else e for e in p.exponents()]
    return enc_int(min(degs))


def op_coeff(req):
    """coeff(p, x, n) -> coefficient of x^n."""
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    x = decode_arg(req["args"][1], R)
    a2 = req["args"][2] if len(req["args"]) >= 3 else {"int": "1"}
    n = int(a2.get("int", a2.get("poly", "1")))
    # univariate polys take an integer degree; multivariate take {var: deg}.
    R = p.parent()
    if R.ngens() <= 1:
        # honor x: if x is the sole generator, index by degree; if x is some
        # other (absent) variable, coeff(p, x, 0) = p and coeff(p, x, n>0) = 0.
        try:
            xi = R.gens().index(x)
        except (ValueError, Exception):
            xi = None
        if xi is not None:
            c = p[n]  # univariate index by degree of the generator
        else:
            c = p if n == 0 else R(0)
    else:
        c = p.coefficient({x: n})
    return enc_poly(c)


def _lcoeff_wrt_var(p, x, R):
    """lcoeff(p, x) = coeff(p, x, degree(p, x)): the coefficient of the highest
    power of x present, a polynomial in the remaining variables."""
    if R.ngens() > 1:
        d = p.degree(x)
        return p.coefficient({x: d})
    # univariate ring: honor x (== the sole generator) -> leading coefficient.
    return p.leading_coefficient()


def op_lcoeff(req):
    """lcoeff(p [,x]): leading coefficient. With a single variable x, the
    coefficient of the highest power of x. With a list/set [x1,...,xn], Maple's
    nested-lexicographic leading coefficient:
    lcoeff(p, [x1,...,xn]) = lcoeff(...lcoeff(p, x1)..., xn)."""
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if p == 0:
        return enc_int(0)
    if len(req["args"]) >= 2:
        a = req["args"][1]
        if isinstance(a, dict) and "exprlist" in a:
            gens = {str(g): g for g in R.gens()}
            c = p
            for nm in a["exprlist"]:
                g = gens.get(nm)
                if g is None:
                    continue
                c = _lcoeff_wrt_var(c, g, c.parent())
            return enc_poly(c)
        x = decode_arg(a, R)
        return enc_poly(_lcoeff_wrt_var(p, x, R))
    # no var given: leading coefficient w.r.t. all indeterminates
    return enc_poly(p.leading_coefficient())


def op_tcoeff(req):
    """tcoeff(p, x): the TRAILING coefficient = coeff(p, x, ldegree(p, x)), i.e.
    the coefficient of the LOWEST power of x present (NOT the constant term).
    tcoeff(x^2+x, x) = 1, tcoeff(3x^3+5x^2, x) = 5. Without x, the trailing
    coefficient w.r.t. all indeterminates."""
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if p == 0:
        return enc_int(0)
    if len(req["args"]) >= 2:
        x = decode_arg(req["args"][1], R)
        if R.ngens() > 1:
            try:
                xi = R.gens().index(x)
            except Exception:
                xi = None
            if xi is not None:
                d = min(e[xi] for e in p.exponents())
                return enc_poly(p.coefficient({x: d}))
        else:
            # univariate ring: coeff of lowest power of x
            d = p.valuation()
            return enc_poly(p[d])
    # no variable: trailing coeff over the natural monomial order (lowest term).
    return enc_poly(p.constant_coefficient())


def op_coeffs(req):
    """coeffs(p, x) -> the coefficients of p w.r.t. the variable(s) x, each a
    polynomial in the remaining variables. Without x, the numeric coefficients.

    The optional 3rd by-name arg (term sequence) is not implemented — DT uses
    only the coefficient set: {coeffs(collect(expand(p-q),s),s)}.

    coeffs(-6x+3y+23x^2-4xyz+7z^2, x) -> {23, -4yz-6, 7z^2+3y}."""
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if len(req["args"]) >= 2:
        Vnames = _vnames_arg(req["args"][1])
        gens = {str(g): i for i, g in enumerate(R.gens())}
        V_idx = [gens[v] for v in Vnames if v in gens]
        if V_idx:
            V_set = set(V_idx)
            ngens = R.ngens()
            # Group p's terms by their V-exponent tuple; each group's value is
            # the coefficient polynomial in the remaining variables.
            groups = {}
            for mon, c in p.dict().items():
                exps = [int(mon)] if isinstance(mon, int) else list(mon)
                rest_exp = [0 if i in V_set else exps[i] for i in range(ngens)]
                term = c * R.monomial(*rest_exp)
                vkey = tuple(exps[i] for i in V_idx)
                groups[vkey] = groups.get(vkey, R(0)) + term
            return enc_list([enc_poly(v) for v in groups.values()])
    coeffs = [enc_poly(c) for c in p.coefficients()]
    return enc_list(coeffs)


def op_collect(req):
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    return enc_poly(p)  # ring elements already collected


def op_indets(req):
    R = make_ring(req["vars"])
    a = req["args"][0]
    vs = set()
    def vars_of(s):
        try:
            return set(R(parse_in_ring(s, R)).variables())
        except AttributeError:
            return set()  # constant
        except Exception:
            return set()
    if "exprlist" in a:
        for s in a["exprlist"]:
            vs |= vars_of(s)
    else:
        s = a.get("poly", a.get("name", a.get("int", "")))
        vs = vars_of(str(s))
    return enc_list([{"name": str(v)} for v in sorted(vs, key=str)])


def op_divide(req):
    """divide(a,b) -> exact division check; returns {bool, quotient}."""
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    q, r = a.quo_rem(b)
    exact = (r == 0)
    return {"divide": {"exact": bool(exact), "quotient": str(q)}}


def _univariate_in_x(R, x):
    """Build the univariate-in-x ring over the fraction field of the other
    variables: Frac(QQ[other vars])[x]. This is the ring in which rem/quo/prem/
    pquo must divide so that division is "treated as polynomials in x" with the
    other variables living in the coefficients (Maple's a = b*q + r with
    degree(r, x) < degree(b, x))."""
    return PolynomialRing(FractionField(_coeff_ring_excluding(R, x)), str(x))


def _back_to_R(R, e):
    """Coerce an element of Frac(other)[x] (or its coefficients-over-Frac form)
    back into the multivariate ring R, clearing any denominators that are pure
    rationals. The results of rem/quo/prem/pquo on polynomials in x with
    coefficients polynomial in the other vars are themselves polynomials in R."""
    try:
        return R(e)
    except Exception:
        pass
    # e may be a univariate poly over Frac(other); rebuild term by term.
    return R(SR(str(e)))


def op_rem(req):
    """rem(a, b, x): remainder of a divided by b as polynomials in x.

    a = b*q + r with degree(r, x) < degree(b, x); the coefficients live in the
    other variables. The optional 4th arg 'q' (to receive the quotient) is not
    supported here — DT uses only the 3-arg form."""
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    if len(req["args"]) >= 3:
        x = decode_arg(req["args"][2], R)
        Ru = _univariate_in_x(R, x)
        au, bu = Ru(a), Ru(b)
        _, r = au.quo_rem(bu)
        return enc_poly(_back_to_R(R, r))
    _, r = a.quo_rem(b)
    return enc_poly(r)


def op_quo(req):
    """quo(a, b, x): quotient of a divided by b as polynomials in x. See op_rem
    for the convention. The optional 4th arg 'r' is not supported (DT uses the
    3-arg form)."""
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    if len(req["args"]) >= 3:
        x = decode_arg(req["args"][2], R)
        Ru = _univariate_in_x(R, x)
        au, bu = Ru(a), Ru(b)
        q, _ = au.quo_rem(bu)
        return enc_poly(_back_to_R(R, q))
    q, _ = a.quo_rem(b)
    return enc_poly(q)


def op_prem(req):
    """pseudo-remainder prem(a, b, x): m*a = b*q + r with
    m = lcoeff(b, x)^(deg(a,x) - deg(b,x) + 1), degree(r, x) < degree(b, x)."""
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    if len(req["args"]) >= 3:
        x = decode_arg(req["args"][2], R)
        Ru = _univariate_in_x(R, x)
        au = Ru(a)
        bu = Ru(b)
        q, r = au.pseudo_quo_rem(bu)
        return enc_poly(_back_to_R(R, r.numerator()) if hasattr(r, 'numerator')
                        else _back_to_R(R, r))
    # 2-arg fallback: no main variable supplied. Sage's multivariate ring has no
    # .pseudo_quo_rem; require the explicit variable rather than guess a main
    # variable (DT always calls the 3-arg form).
    raise ValueError("prem(a, b) requires an explicit main variable x: "
                     "use prem(a, b, x)")


def op_pquo(req):
    """pseudo-quotient pquo(a, b, x): the q in m*a = b*q + r (same convention as
    prem). Like prem, must divide in the univariate-in-x ring."""
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    if len(req["args"]) >= 3:
        x = decode_arg(req["args"][2], R)
        Ru = _univariate_in_x(R, x)
        au = Ru(a)
        bu = Ru(b)
        q, r = au.pseudo_quo_rem(bu)
        return enc_poly(_back_to_R(R, q.numerator()) if hasattr(q, 'numerator')
                        else _back_to_R(R, q))
    raise ValueError("pquo(a, b) requires an explicit main variable x: "
                     "use pquo(a, b, x)")


def _coeff_ring_excluding(R, x):
    others = [g for g in R.gens() if g != x]
    if not others:
        return QQ
    return PolynomialRing(QQ, [str(g) for g in others])


def _content(p):
    """Integer (rational) content of a polynomial over QQ, robust to the
    univariate-over-QQ case where .content() returns an ideal / is absent."""
    if p == 0:
        return QQ(0)
    try:
        c = p.content()
        # univariate QQ polys: .content() can be an ideal; coerce
        c = QQ(c)
        return c
    except Exception:
        pass
    # gcd of numerators / lcm of denominators of the rational coefficients
    coeffs = list(p.coefficients())
    from sage.arith.functions import lcm as arith_lcm
    nums = [QQ(c).numerator() for c in coeffs]
    dens = [QQ(c).denominator() for c in coeffs]
    g = ZZ(0)
    for n in nums:
        g = g.gcd(n)
    d = ZZ(1)
    for de in dens:
        d = arith_lcm(d, de)
    return QQ(g) / QQ(d)


def _vnames_arg(a):
    """Extract the variable-name list from a content/primpart second argument
    ({"name": x} or {"exprlist": [...]} or {"poly": "x"})."""
    if "exprlist" in a:
        return list(a["exprlist"])
    if "name" in a:
        return [a["name"]]
    if "poly" in a:
        return [a["poly"]]
    return []


def op_primpart(req):
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if p == 0:
        return enc_int(0)
    # primpart(p, V) = p / content(p, V). Like content, the two-arg form must
    # divide out the *polynomial* content w.r.t. V, not just the rational content.
    if len(req["args"]) < 2:
        c = _content(p)
    else:
        c = _content_wrt(p, _vnames_arg(req["args"][1]))
    if c == 0:
        return enc_poly(p)
    return enc_poly(p / c)


def _content_wrt(p, Vnames):
    """Maple content(p, V): p viewed as a polynomial in the variables V, return
    the gcd of its coefficients (which are polynomials in the remaining vars).

    Maple's one-arg content(p) is the rational content (gcd of numeric coeffs);
    the two-arg form factors out a *polynomial* common to the V-coefficients,
    e.g. content(x*Vf + x*rho, [Vf, rho]) = x. DT divides every polynomial by
    this in SimplifyPolynom, so getting it wrong (returning the rational content
    and leaving the x factor in) corrupts the reduction.

    Returned up to a rational unit times the rational content — exactly what DT
    needs (it divides p by the content, then re-normalizes via StandardFormSimplify).
    """
    R = p.parent()
    rc = _content(p)            # rational content (gcd nums / lcm dens)
    if rc == 0:
        return R(0)
    b = p / rc                  # integer-primitive
    gens = {str(g): i for i, g in enumerate(R.gens())}
    V_idx = [gens[v] for v in Vnames if v in gens]
    if not V_idx:
        return rc
    V_set = set(V_idx)
    ngens = R.ngens()
    # group b's terms by their V-exponents; each group's value is the
    # coefficient polynomial in the remaining variables. A univariate ring's
    # .dict() has bare int exponent keys, not tuples — normalize both.
    groups = {}
    for mon, c in b.dict().items():
        exps = [int(mon)] if isinstance(mon, int) else list(mon)
        rest_exp = [0 if i in V_set else exps[i] for i in range(ngens)]
        term = c * R.monomial(*rest_exp)
        vkey = tuple(exps[i] for i in V_idx)
        groups[vkey] = groups.get(vkey, R(0)) + term
    g = R(0)
    for poly in groups.values():
        g = poly if g == 0 else g.gcd(poly)
    gc = _content(g)            # strip g's own rational content -> primitive
    if gc != 0:
        g = g / gc
    return rc * g


def op_content(req):
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if p == 0:
        return enc_int(0)
    if len(req["args"]) < 2:
        return enc_poly(_content(p))   # one-arg: rational content
    return enc_poly(_content_wrt(p, _vnames_arg(req["args"][1])))


def _squarefree_via_factor(p):
    """Square-free decomposition built from factor(): collect the irreducible
    factors by multiplicity, multiplying together all irreducibles sharing a
    multiplicity into one square-free factor. Works for both univariate and
    multivariate rings (Sage's multivariate libsingular polynomials have factor()
    but NOT squarefree_decomposition()). Returns (unit_str, [[fac_str, mult],...]).
    """
    F = p.factor()
    R = p.parent()
    by_mult = {}
    for fac, mult in F:
        by_mult[mult] = by_mult.get(mult, R(1)) * fac
    unit = F.unit()
    facs = [[str(by_mult[m]), int(m)] for m in sorted(by_mult)]
    return str(unit), facs


def op_sqrfree(req):
    """sqrfree(p [,x]): square-free factorization. With a main variable (or
    list/set of variables) x, p is treated as a polynomial in x with the other
    variables as coefficients, so e.g. sqrfree(f,x) and sqrfree(f,y) differ.
    Keeps the {unit, factors:[[f,m],...]} return shape.

    The decomposition is computed via factor() (grouping irreducibles by
    multiplicity), since Sage's multivariate polynomials lack
    squarefree_decomposition(). Unit / content placement may differ from Maple's
    exact textual output, but the product reconstructs p and the factor
    multiplicities (the square-free structure) match."""
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if len(req["args"]) >= 2:
        Vnames = _vnames_arg(req["args"][1])
        gens = {str(g): g for g in R.gens()}
        Vgens = [gens[v] for v in Vnames if v in gens]
        others = [g for g in R.gens() if g not in set(Vgens)]
        if Vgens and others:
            # Square-free factorization treating only the main variable(s) as
            # indeterminates: move to Frac(QQ[others])[Vgens] where the other
            # variables live in the coefficient field. (When there are no other
            # variables, every variable is an indeterminate -> the full-ring
            # path below, which is what Maple's sqrfree(f, x, y) does too.)
            base = FractionField(PolynomialRing(QQ, [str(g) for g in others]))
            Rv = PolynomialRing(base, [str(g) for g in Vgens])
            pv = Rv(p)
            if Rv.ngens() <= 1:
                F = pv.squarefree_decomposition()
                facs = [[str(R(SR(str(fac)))), int(mult)] for (fac, mult) in F]
                unit = F.unit()
                ustr = str(unit) if unit in (1, -1) else str(R(SR(str(unit))))
                return {"factors": {"unit": ustr, "factors": facs}}
            unit, facs = _squarefree_via_factor(pv)
            facs = [[str(R(SR(f))), m] for (f, m) in facs]
            return {"factors": {"unit": unit, "factors": facs}}
    unit, facs = _squarefree_via_factor(p)
    return {"factors": {"unit": unit, "factors": facs}}


def op_resultant(req):
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    x = decode_arg(req["args"][2], R) if len(req["args"]) >= 3 else None
    if x is not None:
        return enc_poly(a.resultant(b, x))
    return enc_poly(a.resultant(b))


def _looks_symbolic(s, varnames):
    """Heuristic: does the expression contain a function call over a variable
    (e.g. cos(phi)) that requires the symbolic ring rather than a polynomial
    ring?  A bare variable followed by '(' is the signal."""
    import re
    # any alphabetic identifier immediately followed by '(' that is NOT one of
    # the declared polynomial variables used as a multiplication is symbolic.
    for m in re.finditer(r'([A-Za-z_][A-Za-z0-9_]*)\s*\(', s):
        name = m.group(1)
        # a declared variable can't be a function head in poly form
        return True
    return False


def op_diff(req):
    """diff(f, x1, x2, ...) — differentiate by EACH variable in turn.

    Maple's diff(f, x, x) is the second derivative, and DT's JetList2Diff emits
    exactly this form for a higher-order jet: u[2,0] -> diff(u(x,t), x, x). Only
    differentiating by the first variable (the original bug) silently dropped the
    order, rendering u[2,0] as diff(u(x,t), x). Handles polynomial and symbolic
    (cos(phi[0]), unknown function u(x,t), ...) operands.
    """
    fstr = req["args"][0].get("poly", req["args"][0].get("name", ""))
    xargs = req["args"][1:]

    if _looks_symbolic(fstr, req["vars"]):
        f = parse_symbolic(fstr, req["vars"])
        for xarg in xargs:
            xstr = xarg.get("poly", xarg.get("name", ""))
            f = f.derivative(parse_symbolic(xstr, req["vars"]))
        return enc_poly(f)

    R = make_ring(req["vars"])
    # Coerce f into the ring so constants (Sage Integer/Rational, which lack a
    # `.derivative` method) differentiate correctly to 0. DT's
    # PartialDerivativeInternal calls diff on constant terms once the structural
    # type(p,`+`)/`*`/`^` checks branch correctly (e.g. diff(-1, y) for the unit
    # factor of a -u[1,0] term).
    f = R(decode_arg(req["args"][0], R))
    for xarg in xargs:
        f = f.derivative(decode_arg(xarg, R))
    return enc_poly(f)


def op_simplify(req):
    try:
        R = make_ring(req["vars"], frac=True)
        f = decode_arg(req["args"][0], R)
        return enc_poly(f)
    except Exception:
        e = parse_symbolic(req["args"][0]["poly"], req["vars"])
        return enc_poly(e.simplify_full())


def op_binomial(req):
    def asint(a):
        if "int" in a:
            return ZZ(int(a["int"]))
        return ZZ(int(a.get("poly", a.get("name"))))
    n = asint(req["args"][0])
    k = asint(req["args"][1])
    return enc_int(sage_binomial(n, k))


# --- linear algebra --------------------------------------------------------

def op_matrix(req):
    R = make_ring(req["vars"])
    M = decode_matrix(req["args"][0], R)
    return {"matrix": [[str(M[i, j]) for j in range(M.ncols())]
                       for i in range(M.nrows())]}


def op_la(req):
    """LinearAlgebra:-<member> dispatch."""
    member = req["member"]
    R = make_ring(req["vars"])
    a = req["args"]
    if member == "ColumnDimension":
        M = decode_matrix(a[0], R)
        return enc_int(M.ncols())
    if member == "RowDimension":
        M = decode_matrix(a[0], R)
        return enc_int(M.nrows())
    if member == "Rank":
        M = decode_matrix(a[0], R)
        return enc_int(M.rank())
    if member == "Transpose":
        M = decode_matrix(a[0], R)
        Mt = M.transpose()
        return {"matrix": [[str(Mt[i, j]) for j in range(Mt.ncols())]
                           for i in range(Mt.nrows())]}
    if member == "LinearSolve":
        M = decode_matrix(a[0], R)
        b = decode_vector(a[1], R)
        x = M.solve_right(b)
        return {"vector": [str(e) for e in x]}
    if member == "DiagonalMatrix":
        ents = [parse_in_ring(c, R) if isinstance(c, str) else R(c)
                for c in a[0]["vector"]]
        from sage.all import diagonal_matrix
        D = diagonal_matrix(R, ents)
        return {"matrix": [[str(D[i, j]) for j in range(D.ncols())]
                           for i in range(D.nrows())]}
    raise ValueError("LinearAlgebra member not implemented: %s" % member)


# --- deferred --------------------------------------------------------------

def op_deferred(req):
    raise ValueError(
        "not yet implemented (Phase 3 deferred): %s" % req["op"])


OPS = {
    "factor": op_factor,
    "factors": op_factor,
    "gcd": op_gcd,
    "lcm": op_lcm,
    "expand": op_expand,
    "normal": op_normal,
    "numer": op_numer,
    "denom": op_denom,
    "degree": op_degree,
    "ldegree": op_ldegree,
    "coeff": op_coeff,
    "coeffs": op_coeffs,
    "lcoeff": op_lcoeff,
    "tcoeff": op_tcoeff,
    "collect": op_collect,
    "indets": op_indets,
    "divide": op_divide,
    "rem": op_rem,
    "quo": op_quo,
    "prem": op_prem,
    "pquo": op_pquo,
    "primpart": op_primpart,
    "content": op_content,
    "sqrfree": op_sqrfree,
    "resultant": op_resultant,
    "diff": op_diff,
    "Diff": op_diff,
    "simplify": op_simplify,
    "binomial": op_binomial,
    "Matrix": op_matrix,
    "LinearAlgebra": op_la,
    # explicitly deferred tower-RootOf path
    "AFactors": op_deferred,
    "evala": op_simplify,
}


def handle(req):
    op = req["op"]
    fn = OPS.get(op)
    if fn is None:
        raise ValueError("unknown op: %s" % op)
    return fn(req)


def main():
    out = sys.stdout
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except Exception as e:
            out.write(json.dumps({"ok": False, "error": "bad json: %s" % e}) + "\n")
            out.flush()
            continue
        rid = req.get("id")
        try:
            result = handle(req)
            out.write(json.dumps({"id": rid, "ok": True, "result": result}) + "\n")
        except Exception as e:
            msg = str(e)
            if req.get("debug"):
                msg = msg + "\n" + traceback.format_exc()
            out.write(json.dumps({"id": rid, "ok": False, "error": msg}) + "\n")
        out.flush()


if __name__ == "__main__":
    # Some DT ops hand us very large *flat* expressions — a fully expanded
    # polynomial with thousands of '+'-joined terms (e.g. the hydrogen ansatz's
    # constancy/Vf substitutions, or the combined system's no-end-reduction
    # evala). Sage's string parsing routes through sage_eval -> compile(), whose
    # constant-folder astfold_expr recurses once per additive AST node *on the C
    # stack*. On Python 3.11 setrecursionlimit does NOT gate that path, so a flat
    # sum past ~47k terms overflows the 8 MB main-thread stack and SIGSEGVs
    # (cysignals then mislabels it as a sig_on/sig_off bug). The real fix is the
    # rebalance() pass in parse_in_ring/parse_symbolic, which keeps the compiled
    # AST O(log N) deep. The raised recursion limit below only covers ordinary
    # deep Python recursion elsewhere in Sage; it is not what makes the big
    # polynomial parses safe.
    sys.setrecursionlimit(100000)
    main()
