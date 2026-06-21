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


def parse_in_ring(s, R):
    """Parse a Maple/Sage-form expression string into an element of ring R."""
    ns = ring_namespace(R)
    # sage_eval parses arithmetic expressions using the provided locals.
    from sage.misc.sage_eval import sage_eval
    return R(sage_eval(s, locals=ns))


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
    return SR(sage_eval(s, locals=ns))


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
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    return enc_poly(a.lcm(b))


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
    # low degree: minimal total/var degree of a monomial
    if p == 0:
        return enc_int(0)
    if len(req["args"]) >= 2:
        x = decode_arg(req["args"][1], R)
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
        c = p[n]  # univariate index by degree
    else:
        c = p.coefficient({x: n})
    return enc_poly(c)


def op_lcoeff(req):
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if p == 0:
        return enc_int(0)
    if len(req["args"]) >= 2 and R.ngens() > 1:
        x = decode_arg(req["args"][1], R)
        d = p.degree(x)
        c = p.coefficient({x: d})
        return enc_poly(c)
    # univariate (or no var given): leading coefficient
    return enc_poly(p.leading_coefficient())


def op_tcoeff(req):
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if p == 0:
        return enc_int(0)
    # trailing coefficient = constant term in the univariate sense; use the
    # lowest-degree coefficient.
    return enc_poly(p.constant_coefficient())


def op_coeffs(req):
    """coeffs(p [,vars]) -> list of coefficients."""
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
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


def op_rem(req):
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    _, r = a.quo_rem(b)
    return enc_poly(r)


def op_quo(req):
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    q, _ = a.quo_rem(b)
    return enc_poly(q)


def _univariate(p, x, R):
    """Return p as a univariate polynomial in x (over the multivariate
    coefficient ring) for prem/pquo."""
    return p.polynomial(x)


def op_prem(req):
    """pseudo-remainder prem(a, b, x)."""
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    if len(req["args"]) >= 3:
        x = decode_arg(req["args"][2], R)
        Ru = PolynomialRing(FractionField(_coeff_ring_excluding(R, x)), str(x))
        au = Ru(a)
        bu = Ru(b)
        q, r = au.pseudo_quo_rem(bu)
        return enc_poly(R(r.numerator()) if hasattr(r, 'numerator') else R(r))
    q, r = a.pseudo_quo_rem(b)
    return enc_poly(r)


def op_pquo(req):
    R = make_ring(req["vars"])
    a = decode_arg(req["args"][0], R)
    b = decode_arg(req["args"][1], R)
    q, r = a.pseudo_quo_rem(b)
    return enc_poly(q)


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


def op_primpart(req):
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if p == 0:
        return enc_int(0)
    return enc_poly(p / _content(p))


def op_content(req):
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    if p == 0:
        return enc_int(0)
    return enc_poly(_content(p))


def op_sqrfree(req):
    R = make_ring(req["vars"])
    p = decode_arg(req["args"][0], R)
    F = p.squarefree_decomposition()
    facs = [[str(fac), int(mult)] for (fac, mult) in F]
    return {"factors": {"unit": str(F.unit()), "factors": facs}}


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
    """diff(f, x) — polynomial AND symbolic (cos(phi[0]) etc.)."""
    fstr = req["args"][0].get("poly", req["args"][0].get("name", ""))
    xarg = req["args"][1]
    xstr = xarg.get("poly", xarg.get("name", ""))

    if _looks_symbolic(fstr, req["vars"]):
        f = parse_symbolic(fstr, req["vars"])
        xs = parse_symbolic(xstr, req["vars"])
        return enc_poly(f.derivative(xs))

    R = make_ring(req["vars"])
    f = decode_arg(req["args"][0], R)
    x = decode_arg(xarg, R)
    # Coerce f into the ring so constants (Sage Integer/Rational, which lack a
    # `.derivative` method) differentiate correctly to 0. DT's
    # PartialDerivativeInternal calls diff on constant terms once the structural
    # type(p,`+`)/`*`/`^` checks branch correctly (e.g. diff(-1, y) for the unit
    # factor of a -u[1,0] term).
    f = R(f)
    return enc_poly(f.derivative(x))


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
    main()
