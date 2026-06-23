#!/usr/bin/env python
"""Regression test for the CAS expression-handle (ref) cache in sage_server.

Run with Sage's Python:

    ~/miniforge3/envs/sage/bin/sage -python cas/test_refs.py

Covers:
  - ref round-trip: ingest a string -> get a ref -> materialize -> equals original
  - a ref consumed by a DIFFERENT ring regime than it was created in:
      * poly ring -> fraction field (clean fast-path coercion)
      * a case that forces the SR / subset-var fallback so the
        [ref-coerce-fallback] path is exercised and logged
  - clear: whole-cache and by-id
  - unknown-ref hard error (materialize and consume)

The server functions are driven directly (no subprocess) via handle()/op_* —
the same entry points main() uses per request.
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sage_server as S  # noqa: E402

failures = 0


def check(name, cond):
    global failures
    status = "ok  " if cond else "FAIL"
    if not cond:
        failures += 1
    print("[%s] %s" % (status, name))


def req(op, args, vars=None, member="", want_ref=False, frac=False):
    return S.handle({"op": op, "args": args, "vars": vars or [],
                     "member": member, "want_ref": want_ref, "frac": frac})


def reset_cache():
    S.CACHE.clear()
    S._NEXT_REF[0] = 1
    S._COERCE_FALLBACKS.clear()
    S._COERCE_FALLBACK_TOTAL[0] = 0


# ---------------------------------------------------------------------------
# 1. Ref round-trip: ingest string -> ref -> materialize -> equals original
# ---------------------------------------------------------------------------
reset_cache()
vs = ["x", "y"]
# expand returns the input as-is for a polynomial; ask for a ref.
r1 = req("expand", [{"poly": "x^2 + 2*x*y + y^2"}], vars=vs, want_ref=True)
check("expand(want_ref) returns a ref", "ref" in r1)
rid = r1["ref"]
mat = req("materialize", [{"ref": rid}], vars=vs)
check("materialize returns a poly string", "poly" in mat)
# Compare as ring elements (term order may differ from the input string).
from sage.all import PolynomialRing, QQ  # noqa: E402
R = PolynomialRing(QQ, vs)
check("materialized ref equals original",
      R(mat["poly"]) == R("x^2 + 2*x*y + y^2"))

# A ref fed back as an op arg gives the same result as the string form.
r_gcd_ref = req("gcd", [{"ref": rid}, {"poly": "x + y"}], vars=vs)
r_gcd_str = req("gcd", [{"poly": "x^2 + 2*x*y + y^2"}, {"poly": "x + y"}], vars=vs)
check("gcd(ref, poly) == gcd(poly, poly)",
      R(r_gcd_ref["poly"]) == R(r_gcd_str["poly"]))


# ---------------------------------------------------------------------------
# 2a. Ref created in poly ring, consumed by the fraction field (poly -> frac).
#     This is a clean fast-path coercion (no fallback expected).
# ---------------------------------------------------------------------------
reset_cache()
vs = ["x", "y"]
rp = req("expand", [{"poly": "x^2 - y^2"}], vars=vs, want_ref=True)
rid = rp["ref"]
fallbacks_before = S._COERCE_FALLBACK_TOTAL[0]
# numer over the fraction field consumes the poly ref; numerator of x^2-y^2 is itself.
rn = req("numer", [{"ref": rid}], vars=vs, frac=True)
check("numer(poly-ref) over frac field works", "poly" in rn or "ref" in rn)
numer_str = rn["poly"] if "poly" in rn else req("materialize", [{"ref": rn["ref"]}])["poly"]
check("numer(x^2-y^2) == x^2-y^2 via poly->frac ref",
      R(numer_str) == R("x^2 - y^2"))
check("poly->frac coercion took the fast path (no fallback)",
      S._COERCE_FALLBACK_TOTAL[0] == fallbacks_before)


# ---------------------------------------------------------------------------
# 2b. Exercise the [ref-coerce-fallback] PATH directly.
#
#     Sage's by-name coercion turns out to be very robust: R(obj) succeeds across
#     essentially every realistic ring regime (poly<->frac, subset/superset var
#     sets, Frac-coefficient rings, SR<->poly). That is good news — refs work
#     cleanly almost everywhere — but it means the decode-time fallback is hard
#     to trigger with a normal op. So we drive coerce_into_ring() directly with a
#     cached object whose PARENT genuinely refuses direct coercion into the target
#     ring while its STRING form reparses fine there. We use a stub whose __call__
#     -as-target raises but whose str() is a clean polynomial. This proves the
#     fallback both (a) fires and logs, and (b) yields the correct value.
# ---------------------------------------------------------------------------
reset_cache()
vs = ["x", "y"]

# A target "ring" whose direct __call__ raises (simulating a ring regime that
# refuses the cached object's parent) but which still parses a string via the
# real ring R. This drives coerce_into_ring()'s except branch deterministically
# and confirms it (a) recovers the correct value through parse_in_ring and
# (b) emits + tallies the [ref-coerce-fallback] line under the current op.
_cached_obj = R("x^2*y + x")

class _PickyRing:
    def __init__(self, real):
        self._real = real
        self.tried_direct = False
    def __call__(self, obj):
        # Refuse ONLY the original cached object (the "parent doesn't fit this
        # ring" case). Everything the string-fallback path produces (the
        # sage_eval result inside parse_in_ring) coerces normally through the
        # real ring, so the fallback recovers the correct value.
        if obj is _cached_obj:
            self.tried_direct = True
            raise TypeError("picky ring refuses direct coercion")
        return self._real(obj)
    # parse_in_ring uses ring_namespace(R) -> R.ring()/R.base_ring()/R.gens();
    # delegate those to the real ring so the string path works.
    def ring(self): return self._real
    def base_ring(self): return self._real.base_ring()
    def gens(self): return self._real.gens()
    def __repr__(self): return "PickyRing(%r)" % self._real

rid = S.cache_put(_cached_obj)
picky = _PickyRing(R)
S._CUR_OP[0] = "gcd"   # simulate the op context handle() would set
fallbacks_before = S._COERCE_FALLBACK_TOTAL[0]
coerced = S.coerce_into_ring(rid, S.cache_get(rid), picky)
check("ref recovered via string fallback when direct coercion refused",
      R(coerced) == R("x^2*y + x"))
check("direct fast-path coercion was attempted first", picky.tried_direct)
check("[ref-coerce-fallback] fired", S._COERCE_FALLBACK_TOTAL[0] > fallbacks_before)
check("fallback attributed to current op (gcd)",
      S._COERCE_FALLBACKS.get("gcd", 0) >= 1)


# ---------------------------------------------------------------------------
# 3. clear: whole-cache and by-id
# ---------------------------------------------------------------------------
reset_cache()
vs = ["x"]
r1 = req("expand", [{"poly": "x + 1"}], vars=vs, want_ref=True)
r2 = req("expand", [{"poly": "x + 2"}], vars=vs, want_ref=True)
id1, id2 = r1["ref"], r2["ref"]
check("two refs cached", id1 in S.CACHE and id2 in S.CACHE)
# clear by id: drop only id1
req("clear", [{"ref": id1}])
check("clear by-id dropped id1", id1 not in S.CACHE)
check("clear by-id kept id2", id2 in S.CACHE)
# clear whole cache
req("clear", [])
check("clear (whole) emptied the cache", len(S.CACHE) == 0)


# ---------------------------------------------------------------------------
# 4. unknown-ref is a HARD ERROR (materialize and consume)
# ---------------------------------------------------------------------------
reset_cache()
hit = False
try:
    req("materialize", [{"ref": 9999}], vars=["x"])
except KeyError:
    hit = True
check("materialize of unknown ref raises", hit)

hit = False
try:
    req("gcd", [{"ref": 9999}, {"poly": "x"}], vars=["x"])
except KeyError:
    hit = True
check("consuming an unknown ref raises", hit)


print()
print(S._coerce_fallback_summary())
print()
if failures:
    print("FAILED: %d check(s)" % failures)
    sys.exit(1)
print("all ref tests passed")
