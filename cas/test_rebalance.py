#!/usr/bin/env python
"""Regression test for the flat-sum AST rebalancing in sage_server.

Run with Sage's Python:

    ~/miniforge3/envs/sage/bin/sage -python cas/test_rebalance.py

Background: evala/simplify on the combined hydrogen system (no end reduction)
produces fully expanded polynomials with tens of thousands of '+'-joined terms.
Parsing such a flat sum via sage_eval -> compile() makes CPython's astfold_expr
recurse once per additive term on the C stack; past ~47k terms the main-thread
stack overflows and the process SIGSEGVs (cysignals mislabels it as a
sig_on/sig_off bug). rebalance() restructures the string into a balanced binary
'+' tree (AST depth O(log N)), so the parse stays well within the C stack while
the algebra is unchanged. This test asserts both correctness and survival.
"""
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sage_server as S  # noqa: E402
from sage.all import PolynomialRing, QQ, FractionField  # noqa: E402
from sage.misc.sage_eval import sage_eval  # noqa: E402

failures = 0


def check(name, cond):
    global failures
    print(("OK  " if cond else "FAIL") + "  " + name)
    if not cond:
        failures += 1


R = PolynomialRing(QQ, ['V2', 'a1', 'x', 'y', 'z'])
F = FractionField(R)
ns = {str(g): g for g in R.gens()}
nsf = {str(g): F(g) for g in R.gens()}

# 1) rebalance must not change the value, across the real expression shapes:
#    plain polynomial, rational (A)*(B)^-1, bare reciprocal, leading sign.
samples = [
    "4*x*y*z^2*V2*a1^2 + 2*x*y^3 - 1*x^2*z^2 - 1*y^4",
    "(2*x*y*z^2 + 2*x*y^3 - 1*x^4 - 1*y^2*z^2)*(-1*x^4 + 2*x^3*y - 1*y^4)^-1",
    "x^-1",
    "-1*x^2 + 2*x*y - 1*y^2",
    "a1^2*(x + y)^-1",
]
for s in samples:
    ring, namespace = (F, nsf) if ('^-1' in s or '/' in s) else (R, ns)
    a = ring(sage_eval(s, locals=namespace))
    b = ring(sage_eval(S.rebalance(s), locals=namespace))
    check("value-preserving: " + s[:48], a == b)

# 2) parse_in_ring must survive a flat sum large enough to overflow the C stack
#    via the naive path (~47k-term ceiling on Sage's Python 3.11).
N = 60000
big = "+".join(f"{(i % 7) + 1}*x^{i % 5}*y^{i % 4}*z^{i % 3}" for i in range(N))
t0 = time.time()
p = S.parse_in_ring(big, R)
check(f"{N}-term flat sum parses without SIGSEGV ({time.time()-t0:.1f}s)", p != 0)

# 3) rational with large numerator and denominator (the combined-system shape).
A = "+".join(f"{(i % 5) + 1}*x^{i % 6}*y^{i % 5}" for i in range(20000))
B = "+".join(f"{(i % 3) + 1}*x^{i % 4}*z^{i % 5}" for i in range(20000))
q = S.parse_in_ring(f"({A})*({B})^-1", F)
check("large rational (A)*(B)^-1 parses", q != 0)

# 4) comma argument-lists must survive: rebalance must NOT collapse f(x,y,z)
#    into f((x,y,z)) (a single tuple arg). Check via parse_symbolic on diff().
from sage.calculus.functional import diff as _diff  # noqa: E402,F401
for s in ["diff(Ps(x, y, z), x)",
          "diff(Ps(x, y, z) + Ps(x, y, z), y)",
          "cos(x)*Ps(x, y, z)"]:
    e = S.parse_symbolic(s, ['x', 'y', 'z'])
    check("symbolic arg-list preserved: " + s[:40], e is not None)

print()
print("PASS" if failures == 0 else f"{failures} FAILURE(S)")
sys.exit(1 if failures else 0)
