"""Tests for the structural _back_to_R fix (task 428).

The combined-hydrogen prem result coercion used to fall through R(e) (which
raises RecursionError during compilation on a large univariate-over-Frac
element) to R(SR(str(e))), a multi-MB-string round trip that took >400 s.
_back_to_R now rebuilds the multivariate element structurally from the
univariate coefficient list. These tests check correctness (bit-equal to the
old SR path on small operands; correct shape on synthetic positive-degree and
rational-coefficient cases) and a performance guard on the real failing
operand.
"""
from __future__ import print_function
import os
import sys
import json
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sage_server as S
from sage.all import SR


def _old_back(R, e):
    """The pre-fix coercion: direct then SR/string fallback."""
    try:
        return R(e)
    except Exception:
        return R(SR(str(e)))


def _prem_remainder(req):
    R = S.make_ring(req["vars"])
    a = S.decode_arg(req["args"][0], R)
    b = S.decode_arg(req["args"][1], R)
    x = S.decode_arg(req["args"][2], R)
    Ru = S._univariate_in_x(R, x)
    _, r = Ru(a).pseudo_quo_rem(Ru(b))
    rr = r.numerator() if hasattr(r, "numerator") else r
    return R, rr, x


def check(name, cond):
    print(("[ok  ] " if cond else "[FAIL] ") + name)
    assert cond, name


def test_constant_result_equals_old():
    """A prem whose remainder is constant in x: structural == SR path."""
    req = {"op": "prem", "vars": ["x", "y"],
           "args": [{"poly": "x^2*y^2 + y"},
                    {"poly": "x*y + 1"},
                    {"name": "x"}]}
    R, rr, x = _prem_remainder(req)
    new = S._back_to_R(R, rr, x)
    old = _old_back(R, rr)
    check("constant-in-x prem result equals SR path", new == old)


def test_positive_degree_result():
    """deg(b,x)=2 leaves a degree-1-in-x remainder; verify exact value."""
    req = {"op": "prem", "vars": ["x", "y"],
           "args": [{"poly": "x^4*y + x^3 + x^2*y^2 + x + y"},
                    {"poly": "x^2*y + x + 1"},
                    {"name": "x"}]}
    R, rr, x = _prem_remainder(req)
    new = S._back_to_R(R, rr, x)
    expected = R("-x*y^4 + x*y^3 + x*y^2 + y^2")
    check("positive-degree-in-x prem result is exact", new == expected)
    check("positive-degree-in-x result lives in R", new.parent() is R)


def test_rational_coefficient_via_rem():
    """op_rem can yield a genuinely-rational coefficient (denominator in the
    other vars). _back_to_R must produce the right Frac(R) element, not crash."""
    req = {"op": "rem", "vars": ["x", "y"],
           "args": [{"poly": "x^4*y + x^3 + x^2*y^2 + x + y"},
                    {"poly": "x^2*y + x + 1"},
                    {"name": "x"}]}
    out = S.op_rem(req)
    check("op_rem returns a poly string", "poly" in out)
    check("op_rem result has the expected /y denominator",
          out["poly"] == "(-x*y^2 + x*y + x + 1)/y")


def test_op_prem_end_to_end():
    """op_prem (which calls _back_to_R(R, ., x)) returns the structural value."""
    req = {"op": "prem", "vars": ["x", "y"],
           "args": [{"poly": "x^4*y + x^3 + x^2*y^2 + x + y"},
                    {"poly": "x^2*y + x + 1"},
                    {"name": "x"}]}
    out = S.op_prem(req)
    check("op_prem end-to-end value",
          out.get("poly") == "-x*y^4 + x*y^3 + x*y^2 + y^2")


def test_large_operand_performance():
    """Performance guard on the real combined-hydrogen failing prem operand.

    The fixture is the captured op=prem request that hit the 120 s timeout in
    the combined run. Pre-fix, _back_to_R on its remainder took >400 s; the
    structural path must finish in a few seconds. Skipped if the fixture is
    absent."""
    fixture = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                           "fixtures", "prem_combined_hydrogen.json")
    if not os.path.exists(fixture):
        print("[skip] large-operand performance (fixture %s absent)" % fixture)
        return
    with open(fixture) as f:
        req = json.load(f)
    R, rr, x = _prem_remainder(req)
    t0 = time.time()
    res = S._back_to_R(R, rr, x)
    dt = time.time() - t0
    print("[info] _back_to_R on combined-hydrogen prem: %.3f s (result %d chars)"
          % (dt, len(str(res))))
    check("large prem result coercion under 30 s (was >400 s)", dt < 30.0)


if __name__ == "__main__":
    test_constant_result_equals_old()
    test_positive_degree_result()
    test_rational_coefficient_via_rem()
    test_op_prem_end_to_end()
    test_large_operand_performance()
    print("\nALL PASS")
