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
#                 {"ref": <int>}    — an opaque handle to a cached Sage object
#                       (see "CAS expression handles" below)
#       frac  : if true, build over Frac(PolynomialRing) (for normal/numer/denom)
#       want_ref : if true, poly/rational results are cached Sage-side and
#                  returned as {"ref": N} instead of {"poly": str} (optimization)
#
#   response: {"id": <int>, "ok": true,  "result": <result>}
#         or  {"id": <int>, "ok": false, "error": "<message>"}
#
#   <result> shapes:
#       {"poly": "x^2 + 1"}                       — a single expression string
#       {"ref": 42}                               — an opaque handle to a poly /
#             rational result kept Sage-side (see "CAS expression handles" below)
#       {"int": "5"}                              — an exact integer
#       {"bool": true}
#       {"list": [<result>, ...]}                 — ordered list
#       {"factors": {"unit": "<str>",
#                    "factors": [["<facstr>", <mult:int>], ...]}}
#       {"matrix": [[...],[...]]} / {"vector":[...]}
#
# CAS expression handles (refs) — an OPTIONAL optimization on top of the string
# protocol; correctness is identical with refs off.
#   - Arg variant {"ref": N} is usable anywhere {"poly": ...} is. decode_arg
#     resolves it from CACHE and coerces into the op's target ring (fast path
#     R(obj); on failure, the existing string/rebalance path, logged as
#     [ref-coerce-fallback]).
#   - Poly / rational results are kept Sage-side and returned as {"ref": N} when
#     the request carries "want_ref": true (the Go client sets this). The giant
#     result string is then never serialized until the client asks for it.
#   - op "materialize": {"op":"materialize","args":[{"ref":N}]} -> {"poly": str}.
#   - op "clear": drops the whole cache, or only the listed ids
#     ({"args":[{"ref":N},...]}). Returns {"ok": true}. The cache has NO
#     automatic eviction; the Go client clears at decomposition-cell boundaries.
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
# CAS expression handles (refs)
#
# Large expressions (the combined hydrogen system reaches ~200 KB / ~47k terms)
# are expensive to serialize and re-parse on every round-trip. The cache keeps a
# poly/rational result Sage-side and hands the client an opaque integer handle;
# a handle fed back as an op argument is resolved here without ever shipping the
# string. This is purely an optimization — every ref has an equivalent string
# form (materialize), and a ref arg degrades to the string path if it does not
# fit the consuming op's ring.
#
# No automatic eviction: the cache grows until an explicit `clear` or process
# exit, so a handle never goes stale within a live server. The Go client clears
# at decomposition-cell boundaries to bound memory.
# ---------------------------------------------------------------------------

CACHE = {}            # int id -> cached Sage object
_NEXT_REF = [1]       # monotonically increasing id counter (list for closure-free mutation)

# [ref-coerce-fallback] accounting: how often R(obj) failed and we fell back to
# the string path, broken down by op. Printed at clear time and at exit so we
# learn which ring regimes refs do not cleanly fit.
_COERCE_FALLBACKS = {}     # op-name -> count
_COERCE_FALLBACK_TOTAL = [0]

# Current op/member context, set by handle() before args are decoded, so the
# coercion-fallback log line can name the op that triggered it.
_CUR_OP = [""]
_CUR_MEMBER = [""]


def cache_put(obj):
    """Store obj in the cache and return its new integer handle."""
    rid = _NEXT_REF[0]
    _NEXT_REF[0] += 1
    CACHE[rid] = obj
    return rid


def cache_get(rid):
    """Resolve a handle to its cached object. A missing handle is a HARD ERROR
    (a bug or an unexpected server restart, not something to paper over)."""
    try:
        return CACHE[rid]
    except KeyError:
        raise KeyError("unknown expression ref: %r (cache has %d entries)"
                       % (rid, len(CACHE)))


def _log_coerce_fallback(rid, obj, R, err):
    """Emit a structured [ref-coerce-fallback] line to STDERR and tally it."""
    op = _CUR_OP[0]
    member = _CUR_MEMBER[0]
    try:
        frm = repr(obj.parent())
    except Exception:
        frm = type(obj).__name__
    print("[ref-coerce-fallback] op=%s member=%s ref=%s from=%s to=%r err=%s"
          % (op, member, rid, frm, R, err), file=sys.stderr)
    _COERCE_FALLBACK_TOTAL[0] += 1
    _COERCE_FALLBACKS[op] = _COERCE_FALLBACKS.get(op, 0) + 1


def _coerce_fallback_summary():
    """One-line summary of the coercion fallbacks seen so far."""
    if _COERCE_FALLBACK_TOTAL[0] == 0:
        return "[ref-coerce-fallback] total=0"
    by = ", ".join("%s:%d" % (k, v)
                   for k, v in sorted(_COERCE_FALLBACKS.items()))
    return "[ref-coerce-fallback] total=%d by-op=%s" % (_COERCE_FALLBACK_TOTAL[0], by)


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

def _split_top_commas(s):
    """Split s on commas at paren-depth 0 (function argument lists). Returns a
    single-element list when there is no top-level comma."""
    parts = []
    depth = 0
    start = 0
    for i, c in enumerate(s):
        if c == '(':
            depth += 1
        elif c == ')':
            depth -= 1
        elif c == ',' and depth == 0:
            parts.append(s[start:i])
            start = i + 1
    parts.append(s[start:])
    return parts


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


def _split_top_multiplicative(term):
    """Split one additive term into a list of (separator, factor) pairs at
    paren-depth 0.

    The separator is the operator that PRECEDES the factor ('' for the first
    factor, '*' or '/' otherwise). '^' is NOT a separator: a power like x^4 is a
    single factor (its exponent must stay glued to its base). A '*' or '/'
    immediately after '^' is also not a top-level separator — that would be
    inside an exponent grouping — but in practice exponents here are integer
    literals (x^4), so the simple paren-depth-0 scan suffices.

    Keeping '/' as a separator (rather than folding it into the chain) lets the
    balancer preserve division's left-association: only maximal runs of '*'
    factors get reassociated; a '/' always stays a left-deep boundary, so
    a/b/c is never rewritten to a/(b/c)."""
    factors = []
    depth = 0
    start = 0
    sep = ''
    prev = ''  # previous non-space significant char
    n = len(term)
    for i, c in enumerate(term):
        if c == '(':
            depth += 1
        elif c == ')':
            depth -= 1
        elif c in '*/' and depth == 0 and prev != '^':
            factors.append((sep, term[start:i].strip()))
            sep = c
            start = i + 1
        if not c.isspace():
            prev = c
    factors.append((sep, term[start:].strip()))
    return [(s, f) for (s, f) in factors if f != '']


def _balanced_mult(factors):
    """Balance a list of multiplicative factor-strings into a binary '*' tree
    string. Multiplication is associative (exact over QQ), so reassociating a
    flat product f1*f2*...*fk into a balanced tree is value-preserving while
    cutting the AST depth handed to compile() from O(k) to O(log k)."""
    if len(factors) == 1:
        return factors[0]
    mid = len(factors) // 2
    return '(' + _balanced_mult(factors[:mid]) + '*' + _balanced_mult(factors[mid:]) + ')'


def _rebalance_term(term):
    """Rebalance one additive term: balance its top-level multiplicative chain
    and recurse into any parenthesised sub-expressions.

    A term has no top-level additive operators of its own, but it may be a long
    flat product (a single monomial with thousands of factors — the combined-
    hydrogen pseudo-remainders) whose left-deep '*' AST overflows astfold the
    same way a flat sum does. Split the term at top-level '*'/'/'; rebalance the
    contents of each factor (so nested parens get balanced); then re-join,
    reassociating only maximal runs of '*' factors into balanced binary trees
    (runs are split at every '/' so division stays left-associative — see
    _split_top_multiplicative)."""
    pieces = _split_top_multiplicative(term)
    if len(pieces) == 1:
        # No top-level multiplication: just balance any parenthesised parts.
        return _rebalance_parens(pieces[0][1])
    # Reassociate ONLY maximal runs of '*'-connected factors. A '/' has the same
    # precedence as '*' and is left-associative, so a/b*c == (a/b)*c but
    # a/(b*c) != a/b*c: we must never let a balanced group span a '/' boundary.
    # Strategy: build the result left-to-right; whenever a '/' separator appears,
    # close off the current '*' run as a balanced group, then continue left-deep
    # from there. Each '*' run between '/' boundaries (and the leading one) is
    # balanced independently; the '/'s remain left-deep.
    out = []          # finished left-deep fragments, joined verbatim
    run = []          # pending '*'-connected factors to balance together

    def flush():
        # Append the pending '*' run as a balanced group, multiplied onto any
        # existing left-deep output. No-op when the run is empty (e.g. two '/'
        # in a row: a/b/c).
        if not run:
            return
        bal = _balanced_mult(run)
        if out:
            out.append('*')
        out.append(bal)
        run.clear()

    for sep, fac in pieces:
        fac = _rebalance_parens(fac)
        if sep == '/':
            # close the current '*' run, then emit the '/' boundary left-deep
            flush()
            out.append('/')
            out.append('(' + fac + ')')
        else:  # '' (leading) or '*'
            run.append(fac)
    flush()
    return ''.join(out)


def _rebalance_parens(term):
    """Recursively rebalance the contents of every top-level (...) group inside
    a single factor (which has no top-level additive or multiplicative
    operators of its own)."""
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
    # A top-level comma is an argument-list separator (e.g. diff(Ps(x,y,z), x)),
    # NOT an additive operator: rebalance each argument independently and rejoin
    # with commas, WITHOUT wrapping the list in parens (which would collapse the
    # arguments into a single Python tuple).
    args = _split_top_commas(s)
    if len(args) > 1:
        return ','.join(rebalance(a) for a in args)
    terms = [_rebalance_term(t) for t in _split_top_additive(s)]
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

def coerce_into_ring(rid, obj, R):
    """Coerce a cached object into the op's target ring R.

    Fast path: R(obj). On any coercion failure, fall back to the exact string /
    rebalance path used for {"poly": ...} args (parse_in_ring(str(obj), R)) and
    log a [ref-coerce-fallback] line. This keeps a ref correct across every ring
    regime: if the cached object does not fit the consuming op's ring, we degrade
    to exactly today's string behavior, never to a wrong answer."""
    try:
        return R(obj)
    except Exception as err:
        _log_coerce_fallback(rid, obj, R, err)
        return parse_in_ring(str(obj), R)


def arg_poly_str(a):
    """Return the expression-string form of a poly/name/int/ref arg. Materializes
    a cached ref to its str(). Used by the SR / symbolic fallback paths (expand,
    simplify, diff, indets) that parse the operand string directly rather than
    through a ring."""
    if "poly" in a:
        return a["poly"]
    if "ref" in a:
        return str(cache_get(a["ref"]))
    if "name" in a:
        return a["name"]
    if "int" in a:
        return a["int"]
    raise ValueError("cannot stringify arg: %r" % (a,))


def decode_into_ring(a, R):
    """decode_arg, but a BARE SCALAR result (a Python/Sage int from an {"int"}
    arg, or a ref/poly that reduced to a plain integer/rational in ZZ/QQ) is
    lifted into the ring R so the consuming op's polynomial methods
    (.degree(x)/.coefficient/.factor/...) work — a raw Sage Integer's .degree()
    takes no positional argument (the ex4 primpart->1->degree crash).

    A genuine ring element OR a fraction-field element is returned UNCHANGED: we
    must NOT force a rational function into a polynomial ring (that raises
    "fraction must have unit denominator") — ops that legitimately handle
    fractions keep their own ring. The lift only applies to scalars that have no
    polynomial structure of their own."""
    v = decode_arg(a, R)
    try:
        p = v.parent()
    except Exception:
        # not a Sage element at all (e.g. a python int from {"raw"}) -> lift
        return R(v)
    # Lift only plain ZZ/QQ scalars; leave polynomial rings and fraction fields
    # (which already have the right methods / must not be force-coerced) alone.
    if p in (ZZ, QQ):
        return R(v)
    return v


def decode_allow_frac(a, varnames):
    """Decode an operand so a genuine FRACTION survives, returning a pair
    (value, poly_ring) where poly_ring is the polynomial ring (frac=False) the
    polynomial case lives in.

    Semantics (matches what content/primpart want):
      - plain ZZ/QQ scalar  -> lifted into the polynomial ring (so it gains
        .degree(x)/.coefficient/... — do not regress the ex4 primpart->1->degree
        fix from decode_into_ring);
      - polynomial (denominator 1 over the fraction field) -> polynomial-ring
        element;
      - genuine fraction n/d  -> fraction-field element (NOT force-coerced into
        the polynomial ring, which would raise "fraction must have unit
        denominator").

    Parsing happens over the FRACTION FIELD so a fractional {"poly"} string (or
    a fraction-valued ref) does not blow up in decode_arg before we can inspect
    it. We then demote back to the polynomial ring when the value is integral."""
    R = make_ring(varnames)            # polynomial ring (frac=False)
    F = make_ring(varnames, frac=True)  # its fraction field
    v = decode_arg(a, F)
    # decode_arg may hand back a bare python/Sage scalar (an {"int"} or {"raw"}
    # arg) — lift it into the polynomial ring directly.
    try:
        p = v.parent()
    except Exception:
        return R(v), R
    if p in (ZZ, QQ):
        return R(v), R
    # A fraction-field element with unit denominator is really a polynomial:
    # demote it into R so the existing polynomial path runs unchanged.
    try:
        if v.denominator() == 1:
            return R(v.numerator()), R
    except (AttributeError, TypeError):
        pass
    # genuine fraction (or some other ring element) -> leave it alone
    return v, R


def decode_arg(a, R):
    """Decode one request arg into a ring element (or python scalar)."""
    if "poly" in a:
        return parse_in_ring(a["poly"], R)
    if "ref" in a:
        rid = a["ref"]
        return coerce_into_ring(rid, cache_get(rid), R)
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

# Set by handle() from the request's "want_ref" field. When true, enc_poly
# caches the object Sage-side and returns a {"ref": N} handle instead of the
# (potentially huge) string. Reset to False for every request so refs are
# strictly opt-in per call.
_WANT_REF = [False]


def _is_symbolic_ring_elt(p):
    """True if p lives in Sage's symbolic ring SR. SR results come from the
    diff/expand/simplify fallback paths and carry unevaluated function
    applications (diff(a(x), x), cos(phi)) whose str() is NOT a clean polynomial
    in sanitized variables — caching them as refs would let an unsanitized
    function head (a[0], a(x)) flow back to a later op and break parsing. So SR
    results are always returned as strings, never refs."""
    try:
        return p.parent() is SR
    except Exception:
        return False


def enc_poly(p):
    """Encode a single poly/rational result. Returns a {"ref": N} handle when the
    request asked for one (want_ref) AND the result is a genuine polynomial /
    rational ring element; SR (symbolic) results are always returned as strings
    (see _is_symbolic_ring_elt)."""
    if _WANT_REF[0] and not _is_symbolic_ring_elt(p):
        return {"ref": cache_put(p)}
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
    p = decode_into_ring(req["args"][0], R)  # coerce: see op_degree
    if p == 0:
        return {"factors": {"unit": "0", "factors": []}}
    F = p.factor()
    unit = F.unit()
    facs = [[str(fac), int(mult)] for (fac, mult) in F]
    return {"factors": {"unit": str(unit), "factors": facs}}


def op_gcd(req):
    R = make_ring(req["vars"])
    a = decode_into_ring(req["args"][0], R)  # coerce: see op_degree
    b = decode_into_ring(req["args"][1], R)
    return enc_poly(a.gcd(b))


def op_lcm(req):
    """lcm is variadic: the least common multiple of an arbitrary number of
    polynomials. lcm(x, y, z) = x*y*z."""
    R = make_ring(req["vars"])
    args = req["args"]
    if not args:
        return enc_poly(R(1))
    acc = decode_into_ring(args[0], R)  # coerce: see op_degree
    for a in args[1:]:
        acc = acc.lcm(decode_into_ring(a, R))
    return enc_poly(acc)


# Relational operators as they appear on the wire (space-padded; sanitizeExpr
# emits "lhs = rhs", "p <> 0", etc.). Longer / two-char ops first so a prefix
# scan never mis-splits "<=" as "<".
_REL_OPS = (" <> ", " <= ", " >= ", " = ", " < ", " > ")


def _split_relation(s):
    """Split 'lhs OP rhs' on the single top-level (paren-depth 0) relational
    operator. Returns (op, lhs, rhs) with op/lhs/rhs stripped, or None when there
    is no top-level relation. Maple forbids relations nested in arithmetic, so at
    depth 0 there is at most one."""
    depth = 0
    for i, c in enumerate(s):
        if c in "([{":
            depth += 1
        elif c in ")]}":
            depth -= 1
        elif depth == 0:
            for op in _REL_OPS:
                if s[i:i + len(op)] == op:
                    return op.strip(), s[:i].strip(), s[i + len(op):].strip()
    return None


def _map_structure(req, op_fn):
    """Maple's normal/expand/numer/denom (and friends) apply RECURSIVELY through
    lists, sets, and equations/relations (normal help page: "applies recursively
    to lists, sets, ranges, equations, relations"). open-maple wires a list/set
    as {"exprlist": [...]} and an equation/relation as {"poly": "lhs OP rhs"}.
    If the first arg is such a structure, apply op_fn elementwise and return the
    same shape; otherwise return None so the caller runs its scalar path.

    op_fn is the op handler itself, re-invoked on a single scalar {"poly": ...}
    arg — a scalar never re-enters this branch, so the recursion terminates."""
    a = req["args"][0]
    if not isinstance(a, dict):
        return None
    vars = req.get("vars", [])
    if "exprlist" in a:
        # list/set: map over elements (the list-vs-set distinction is not carried
        # on the wire, so the result comes back as a list). An element may be a
        # live {"ref":N} handle (preserved by encodeExprlist for swollen
        # intermediates) — pass it straight through so the op runs on the cached
        # object rather than a re-parsed multi-MB string.
        out = []
        for el in a["exprlist"]:
            arg = el if isinstance(el, dict) else {"poly": el}
            out.append(op_fn({"vars": vars, "args": [arg]}))
        return enc_list(out)
    if "poly" in a:
        split = _split_relation(a["poly"])
        if split is not None:
            opstr, lhs, rhs = split
            ls = _enc_str(op_fn({"vars": vars, "args": [{"poly": lhs}]}))
            rs = _enc_str(op_fn({"vars": vars, "args": [{"poly": rhs}]}))
            if ls is not None and rs is not None:
                return {"poly": "%s %s %s" % (ls, opstr, rs)}
    return None


def _enc_str(enc):
    """Expression-string form of a scalar enc result ({"poly":...}, a {"ref":...}
    handle materialized via the cache, or {"int":...}); None for anything else
    (e.g. a {"list":...}), so the caller can decline to recombine."""
    if "poly" in enc:
        return enc["poly"]
    if "ref" in enc:
        return str(cache_get(enc["ref"]))
    if "int" in enc:
        return enc["int"]
    return None


def op_expand(req):
    mapped = _map_structure(req, op_expand)
    if mapped is not None:
        return mapped
    # expand over SR to handle symbolic too, then return string.
    try:
        R = make_ring(req["vars"])
        p = decode_arg(req["args"][0], R)
        return enc_poly(p)  # ring elements are already expanded
    except Exception:
        e = parse_symbolic(arg_poly_str(req["args"][0]), req["vars"])
        return enc_poly(e.expand())


def op_normal(req):
    """normal(f) -> simplified rational function string."""
    mapped = _map_structure(req, op_normal)
    if mapped is not None:
        return mapped
    R = make_ring(req["vars"], frac=True)
    f = decode_arg(req["args"][0], R)
    # FractionField elements are automatically in lowest terms.
    return enc_poly(f)


def op_numer(req):
    mapped = _map_structure(req, op_numer)
    if mapped is not None:
        return mapped
    R = make_ring(req["vars"], frac=True)
    f = decode_arg(req["args"][0], R)
    return enc_poly(f.numerator())


def op_denom(req):
    mapped = _map_structure(req, op_denom)
    if mapped is not None:
        return mapped
    R = make_ring(req["vars"], frac=True)
    f = decode_arg(req["args"][0], R)
    return enc_poly(f.denominator())


def op_degree(req):
    R = make_ring(req["vars"])
    # Coerce into R so a bare scalar (an {"int"} arg, or a ref/poly that reduced
    # to a constant — e.g. primpart returning 1) is a ring element with a working
    # .degree(x); a raw Sage Integer's .degree() takes no positional argument.
    p = decode_into_ring(req["args"][0], R)
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
    # Coerce into R (see op_degree): a bare scalar must be a ring element so its
    # low-degree methods work.
    p = decode_into_ring(req["args"][0], R)
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
    # Coerce into R so a constant operand (a bare {"int"} arg, or a ref/poly that
    # reduced to a scalar) is a ring element with the polynomial methods below;
    # a raw Sage Integer has no .parent().ngens()/.coefficient(). (See op_degree.)
    p = decode_into_ring(req["args"][0], R)
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
    p = decode_into_ring(req["args"][0], R)  # coerce: see op_degree
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
    p = decode_into_ring(req["args"][0], R)  # coerce: see op_degree
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
    p = decode_into_ring(req["args"][0], R)  # coerce: see op_degree
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


def _obj_variables(obj):
    """Indeterminates of a cached ring element. A polynomial has .variables(); a
    FractionFieldElement does NOT (it raises AttributeError) — union the
    numerator's and denominator's variables. This reads straight off the cached
    object, with no string round-trip or re-parse."""
    try:
        return set(obj.variables())
    except AttributeError:
        try:
            return (set(obj.numerator().variables())
                    | set(obj.denominator().variables()))
        except Exception:
            return set()
    except Exception:
        return set()


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
        for el in a["exprlist"]:
            if isinstance(el, dict) and "ref" in el:
                # live ref element: read variables off the cached object, never
                # re-parse a stringified (possibly multi-MB) swollen polynomial.
                vs |= _obj_variables(cache_get(el["ref"]))
            else:
                vs |= vars_of(el)
    elif "ref" in a:
        # A cached object already lives in a ring: read its variables directly.
        vs = _obj_variables(cache_get(a["ref"]))
    else:
        s = a.get("poly", a.get("name", a.get("int", "")))
        vs = vars_of(str(s))
    return enc_list([{"name": str(v)} for v in sorted(vs, key=str)])


def op_divide(req):
    """divide(a,b) -> exact division check; returns {bool, quotient}."""
    R = make_ring(req["vars"])
    a = decode_into_ring(req["args"][0], R)  # coerce: see op_degree
    b = decode_into_ring(req["args"][1], R)
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


def _back_to_R(R, e, x=None):
    """Coerce an element of Frac(other)[x] back into the multivariate ring R.

    The results of rem/quo/prem/pquo are polynomials in x with coefficients
    polynomial (or rational) in the other vars, hence members of R (or Frac(R)
    when a coefficient is genuinely rational). When the main variable x is known,
    we rebuild R-element STRUCTURALLY from the univariate coefficient list:
    sum_i (coeff_i coerced into R) * x^i. Each coeff_i is a FractionFieldElement
    over QQ[other]; its numerator/denominator are polynomials whose generators
    share R's names, so they coerce into R directly via libSingular.

    This structural path avoids the catastrophic str()->SR->coerce fallback:
    R(e) on a large univariate-over-Frac element raises RecursionError during
    compilation (the same deep-AST issue rebalance fixes for parse), which sent
    the prem result through R(SR(str(e))) -- a 2.9 MB-string round trip that took
    >400 s on the combined-hydrogen pseudo-remainder. The structural rebuild does
    the same work in ~1 s and stays entirely in libSingular.

    Without x (no main var), fall back to the direct/SR coercion as before."""
    if x is not None:
        try:
            return _back_to_R_struct(R, e, x)
        except Exception:
            pass  # structural path failed; fall through to the generic coercions
    try:
        return R(e)
    except Exception:
        pass
    # e may be a univariate poly over Frac(other); rebuild term by term.
    return R(SR(str(e)))


def _back_to_R_struct(R, e, x):
    """Structural coercion of a univariate-in-x polynomial over Frac(QQ[other])
    into R (or Frac(R) if a coefficient is genuinely rational). See _back_to_R."""
    Rx = R(x)
    res = R.zero()
    acc_x = R.one()                # x^i, built incrementally
    FR = None                      # Frac(R), lazily, only if a rational coeff appears
    for c in e.list():             # [c0, c1, ...] low-to-high; constants give []
        if c != 0:
            num = c.numerator()
            den = c.denominator()
            Rnum = R(num)
            Rden = R(den)
            if Rden == 1:
                cR = Rnum
            else:
                if FR is None:
                    FR = FractionField(R)
                cR = FR(Rnum) / FR(Rden)
                # if the rational coeff actually divides out to a polynomial,
                # keep it in R so the result is a clean polynomial.
                if cR.denominator() == 1:
                    cR = R(cR.numerator())
            res = res + cR * acc_x
        acc_x = acc_x * Rx
    return res


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
        return enc_poly(_back_to_R(R, r, x))
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
        return enc_poly(_back_to_R(R, q, x))
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
        return enc_poly(_back_to_R(R, r.numerator(), x) if hasattr(r, 'numerator')
                        else _back_to_R(R, r, x))
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
        return enc_poly(_back_to_R(R, q.numerator(), x) if hasattr(q, 'numerator')
                        else _back_to_R(R, q, x))
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


def _poly_content(p, req):
    """content(p, V) for a POLYNOMIAL p, dispatching on the one-/two-arg form
    exactly as op_content does for the polynomial case."""
    if len(req["args"]) < 2:
        return _content(p)              # one-arg: rational content
    return _content_wrt(p, _vnames_arg(req["args"][1]))


def _poly_primpart(p, req):
    """primpart(p, V) = p / content(p, V) for a POLYNOMIAL p."""
    if p == 0:
        return p
    c = _poly_content(p, req)
    if c == 0:
        return p
    return p / c


def op_primpart(req):
    # Decode so a genuine fraction survives (the ex4 reciprocal 1/X). A plain
    # scalar is lifted into R; a polynomial stays in R; a fraction stays in F.
    v, R = decode_allow_frac(req["args"][0], req["vars"])
    is_frac = (v.parent() is not R)
    if not is_frac:
        # ---- polynomial path: unchanged behavior ----
        if v == 0:
            return enc_int(0)
        return enc_poly(_poly_primpart(v, req))
    # ---- rational path: Maple's multiplicative extension ----
    #   primpart(n/d, V) = primpart(n, V) / primpart(d, V)
    # Sign rule: content carries the sign (positive), primpart keeps it; the
    # polynomial helpers already follow that convention, so applying them to
    # numerator and denominator separately stays consistent.
    F = v.parent()
    n = v.numerator()
    d = v.denominator()
    if n == 0:
        return enc_int(0)
    pn = _poly_primpart(n, req)
    pd = _poly_primpart(d, req)
    return enc_poly(F(pn) / F(pd))


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
    # Decode so a genuine fraction survives (see decode_allow_frac).
    v, R = decode_allow_frac(req["args"][0], req["vars"])
    is_frac = (v.parent() is not R)
    if not is_frac:
        # ---- polynomial path: unchanged behavior ----
        if v == 0:
            return enc_int(0)
        return enc_poly(_poly_content(v, req))
    # ---- rational path: Maple's multiplicative extension ----
    #   content(n/d, V) = content(n, V) / content(d, V)
    F = v.parent()
    n = v.numerator()
    d = v.denominator()
    if n == 0:
        return enc_int(0)
    cn = _poly_content(n, req)
    cd = _poly_content(d, req)
    if cd == 0:
        return enc_poly(F(cn))
    return enc_poly(F(cn) / F(cd))


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
    p = decode_into_ring(req["args"][0], R)  # coerce: see op_degree
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
    a = decode_into_ring(req["args"][0], R)  # coerce: see op_degree
    b = decode_into_ring(req["args"][1], R)
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
    a0 = req["args"][0]
    fstr = a0.get("poly", a0.get("name", ""))
    if not fstr and "ref" in a0:
        # A cached object is always a polynomial/rational ring element (enc_poly
        # never caches a symbolic SR expression), so its str() is poly-form and
        # _looks_symbolic will (correctly) route it to the polynomial path below.
        fstr = str(cache_get(a0["ref"]))
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
    f = decode_into_ring(req["args"][0], R)
    for xarg in xargs:
        f = f.derivative(decode_arg(xarg, R))
    return enc_poly(f)


def op_simplify(req):
    try:
        R = make_ring(req["vars"], frac=True)
        f = decode_arg(req["args"][0], R)
        return enc_poly(f)
    except Exception:
        e = parse_symbolic(arg_poly_str(req["args"][0]), req["vars"])
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

def op_materialize(req):
    """materialize({"ref":N}) -> {"poly": str}. Forces a cached object to its
    string form. A want_ref on this op is ignored — materialize always returns a
    string (that is its entire purpose)."""
    a = req["args"][0]
    if "ref" not in a:
        raise ValueError("materialize requires a {\"ref\":N} arg, got %r" % (a,))
    rid = a["ref"]
    return {"poly": str(cache_get(rid))}


def op_clear(req):
    """clear() drops the ENTIRE cache; clear({"ref":N},...) drops only those ids.
    Returns {"ok": true}. Also logs the running coercion-fallback summary so a run
    log shows the ref-coerce health at each cell boundary."""
    args = req.get("args") or []
    if not args:
        CACHE.clear()
    else:
        for a in args:
            if "ref" in a:
                CACHE.pop(a["ref"], None)
    print(_coerce_fallback_summary(), file=sys.stderr)
    return {"ok": True}


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
    # CAS expression handles
    "materialize": op_materialize,
    "clear": op_clear,
}


def handle(req):
    op = req["op"]
    fn = OPS.get(op)
    if fn is None:
        raise ValueError("unknown op: %s" % op)
    # Op context for the [ref-coerce-fallback] log lines emitted during arg
    # decoding, and the per-request want_ref flag consumed by enc_poly.
    # materialize/clear never return a ref themselves.
    _CUR_OP[0] = op
    _CUR_MEMBER[0] = req.get("member", "") or ""
    _WANT_REF[0] = bool(req.get("want_ref")) and op not in ("materialize", "clear")
    try:
        return fn(req)
    finally:
        _WANT_REF[0] = False


def main():
    out = sys.stdout
    try:
        _main_loop(out)
    finally:
        # Final coercion-fallback tally so the run log shows which ring regimes
        # refs did not cleanly fit, even when the client never sent a clear.
        print(_coerce_fallback_summary(), file=sys.stderr)


def _main_loop(out):
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
