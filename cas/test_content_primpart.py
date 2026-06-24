#!/usr/bin/env python
"""Regression test for content/primpart over RATIONAL operands.

Run with Sage's Python:

    ~/miniforge3/envs/sage/bin/sage -python cas/test_content_primpart.py

Background (task sage-cas-content-primpart-rational): commit 361fbae correctly
serializes the ex4_hydrogen reciprocal ((b1^3*x^2*z)^1)^-1 as a genuine fraction
1/(x^2*z) instead of silently collapsing it to a polynomial. DT then hands that
fraction to primpart, which used to crash with "fraction must have unit
denominator". Maple documents content/primpart as extended MULTIPLICATIVELY to
rational normal form:  f(n/d, V) = f(n, V) / f(d, V)  for f in {content, primpart}.

This test covers:
  (a) polynomial operand: behavior unchanged (one-arg rational content, two-arg
      polynomial content / primpart);
  (b) genuine rational operand n/d with a 2-var subset V: assert the
      multiplicative identity content(n/d,V) == content(n,V)/content(d,V) and
      primpart(n/d,V) == primpart(n,V)/primpart(d,V), and that no "unit
      denominator" error is raised;
  (c) the concrete ex4 shape primpart((numerator)*(x^2*z)^-1, V) returns a value.
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sage_server as S  # noqa: E402

from sage.all import PolynomialRing, FractionField, QQ  # noqa: E402

failures = 0


def check(name, cond):
    global failures
    status = "ok  " if cond else "FAIL"
    if not cond:
        failures += 1
    print("[%s] %s" % (status, name))


def req(op, args, vars=None):
    return S.handle({"op": op, "args": args, "vars": vars or [],
                     "member": "", "want_ref": False, "frac": False})


def poly(s):
    return {"poly": s}


def exprlist(names):
    return {"exprlist": list(names)}


def F_of(varnames):
    return FractionField(PolynomialRing(QQ, list(varnames)))


def as_frac(result, varnames):
    """Parse a {"poly":...}/{"int":...} op result back into the fraction field."""
    F = F_of(varnames)
    if "int" in result:
        return F(int(result["int"]))
    assert "poly" in result, result
    from sage.misc.sage_eval import sage_eval
    ns = {str(g): F(g) for g in PolynomialRing(QQ, list(varnames)).gens()}
    return F(sage_eval(result["poly"], locals=ns))


VARS = ["x", "y", "z"]


# ---------------------------------------------------------------------------
# (a) polynomial operand: unchanged behavior
# ---------------------------------------------------------------------------
# one-arg content = rational content (gcd of numeric coeffs)
r = req("content", [poly("6*x + 9*y")], VARS)
check("poly one-arg content(6x+9y) == 3", as_frac(r, VARS) == 3)

r = req("primpart", [poly("6*x + 9*y")], VARS)
check("poly one-arg primpart(6x+9y) == 2x+3y", as_frac(r, VARS) == F_of(VARS)("2*x + 3*y"))

# two-arg content w.r.t. a subset: factor out the polynomial common to V-coeffs
# content(x*y + x*z, [y,z]) = x
r = req("content", [poly("x*y + x*z"), exprlist(["y", "z"])], VARS)
check("poly two-arg content(xy+xz,[y,z]) == x", as_frac(r, VARS) == F_of(VARS)("x"))

r = req("primpart", [poly("x*y + x*z"), exprlist(["y", "z"])], VARS)
check("poly two-arg primpart(xy+xz,[y,z]) == y+z", as_frac(r, VARS) == F_of(VARS)("y + z"))


# ---------------------------------------------------------------------------
# (b) genuine rational operand n/d with a 2-var subset V
#     n = 6*x*y + 9*x*z ,  d = 4*z + 6  ->  must not raise, must satisfy the
#     multiplicative identity.
# ---------------------------------------------------------------------------
N = "6*x*y + 9*x*z"
D = "4*z + 6"
FRAC = "(%s) / (%s)" % (N, D)
V = ["y", "z"]

# one-arg (default: all indeterminates)
c_frac = as_frac(req("content", [poly(FRAC)], VARS), VARS)
c_n = as_frac(req("content", [poly(N)], VARS), VARS)
c_d = as_frac(req("content", [poly(D)], VARS), VARS)
check("rational one-arg content multiplicative: c(n/d)==c(n)/c(d)",
      c_frac == c_n / c_d)

p_frac = as_frac(req("primpart", [poly(FRAC)], VARS), VARS)
p_n = as_frac(req("primpart", [poly(N)], VARS), VARS)
p_d = as_frac(req("primpart", [poly(D)], VARS), VARS)
check("rational one-arg primpart multiplicative: p(n/d)==p(n)/p(d)",
      p_frac == p_n / p_d)

# two-arg w.r.t. subset V=[y,z]
c_frac2 = as_frac(req("content", [poly(FRAC), exprlist(V)], VARS), VARS)
c_n2 = as_frac(req("content", [poly(N), exprlist(V)], VARS), VARS)
c_d2 = as_frac(req("content", [poly(D), exprlist(V)], VARS), VARS)
check("rational two-arg content multiplicative (V=[y,z])",
      c_frac2 == c_n2 / c_d2)

p_frac2 = as_frac(req("primpart", [poly(FRAC), exprlist(V)], VARS), VARS)
p_n2 = as_frac(req("primpart", [poly(N), exprlist(V)], VARS), VARS)
p_d2 = as_frac(req("primpart", [poly(D), exprlist(V)], VARS), VARS)
check("rational two-arg primpart multiplicative (V=[y,z])",
      p_frac2 == p_n2 / p_d2)


# ---------------------------------------------------------------------------
# (c) the concrete ex4 shape: primpart of a numerator times (x^2*z)^-1
#     This is exactly what 361fbae produces and DT then feeds to primpart.
#     The pass criterion is: it returns a value, does NOT raise
#     "fraction must have unit denominator".
# ---------------------------------------------------------------------------
EX4_VARS = ["b1", "x", "z"]
EX4 = "(b1^3 + b1*x + 7) * (x^2*z)^-1"
ok = True
try:
    r = req("primpart", [poly(EX4)], EX4_VARS)
    val = as_frac(r, EX4_VARS)
except Exception as e:  # noqa: BLE001
    ok = False
    print("    ex4-shape raised: %r" % (e,))
check("ex4-shape primpart((num)*(x^2*z)^-1) returns a value (no crash)", ok)

# and the multiplicative identity holds on this shape too
if ok:
    n_ex = "b1^3 + b1*x + 7"
    d_ex = "x^2*z"
    pn = as_frac(req("primpart", [poly(n_ex)], EX4_VARS), EX4_VARS)
    pd = as_frac(req("primpart", [poly(d_ex)], EX4_VARS), EX4_VARS)
    check("ex4-shape primpart multiplicative identity",
          val == pn / pd)


# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
print()
if failures:
    print("FAILED: %d check(s)" % failures)
    sys.exit(1)
print("ALL PASS")
