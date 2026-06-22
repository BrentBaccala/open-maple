#!/usr/bin/env python
# check_cover.py <combined-omri-log>
#
# The COVER half of the completeness proof — the companion to check_f.py.
#
# Thomas decomposition is a recursive case-splitting tree.  Its main loop
# (DifferentialThomas/src `main`, DoNextStep) gives every live system exactly one
# fate per step: SPLIT (spawn sibling systems, the mutated system continuing as
# the complementary branch), PRUNE (Inconsistent:=true), FINISH (a simple system),
# or plain insert (continue).  New systems are created ONLY at the seven DeepCopy
# split sites (grep-verified): SplitByInitial, SplitBySquarefree(Old),
# DivideByInequation(Old), InequationLCM, the reduction PRSGCD split, and the two
# Factorize splits (denominator + equation).
#
# Cover (V(input) = U V(surviving cell_i)) follows from the loop invariant
#   U V(live systems) = V(input),
# maintained from start (one live system = input) to finish (live = the cells) IF:
#   (a) every split is EXHAUSTIVE  — U V(children) = V(parent), and
#   (b) every prune removes an EMPTY system.
# (b) is check_f.py (412/412 PASS).  (a) is what THIS tool certifies:
#
#   * SplitByInitial / SplitBySquarefree / DivideByInequation / InequationLCM /
#     reduction-PRSGCD are binary  s=0  XOR  s!=0  dichotomies — exhaustive as a
#     LOGICAL TAUTOLOGY for any polynomial s (no computation).  The OMRI_SPLIT
#     census just confirms the runtime used only these known operators.
#   * Factorize denominator split is also a binary  denom=0 XOR denom!=0.
#   * Factorize EQUATION split is the ONE non-tautological case:
#         q=0  <=>  (fak1=0)  XOR  (fak2=0 and fak1!=0)
#     which is exhaustive IFF  fak1 * fak2 = q  (as associates).  Each such split
#     emits OMRI_FACTOR|q|fak1|fak2; this tool verifies the product identity.
#
# A failed product identity = a factor split that could DROP solutions = a real
# cover hole.  All identities holding + the census being clean + check_f PASS
# ==> the decomposition covers the whole parameter space.

import sys, os, re
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import maple_parse
from sage.all import PolynomialRing, QQ
from sage.misc.sage_eval import sage_eval

KNOWN_OPS = {"SplitByInitial", "SplitBySquarefreeOld", "DivideByInequationOld",
             "InequationLCM", "Factorize", "ReductionPRSGCD"}

LOG = sys.argv[1] if len(sys.argv) > 1 else "/tmp/omri-cover.log"
raw = open(LOG, errors="replace").read().splitlines()

splits  = [l for l in raw if l.startswith("OMRI_SPLIT|")]
factors = [l for l in raw if l.startswith("OMRI_FACTOR|")]
prunes  = [l for l in raw if l.startswith("OMRI_RECORD|")]

# ---- 1. split census: confirm only known branch-creating operators fired -------
census = {}
for l in splits:
    op = l.split("|", 1)[1].strip()
    census[op] = census.get(op, 0) + 1
unknown = sorted(set(census) - KNOWN_OPS)
print("split-operator census (%d split-operator invocations):" % len(splits))
for op in sorted(census):
    tag = "  TAUTOLOGICAL-BINARY" if op != "Factorize" else "  (factor: product-checked below)"
    print("  %-22s %5d%s" % (op, census[op], tag))
if unknown:
    print("  *** UNKNOWN split operator(s): %s — static enumeration incomplete!" % unknown)

# ---- 2. factor product identity:  fak1 * fak2 == q  (scalar associates) ---------
def associates(p1, p2):
    """True iff p1 and p2 are nonzero scalar multiples (same variety)."""
    if p1 == 0 or p2 == 0:
        return p1 == 0 and p2 == 0
    d1, d2 = p1.dict(), p2.dict()
    if set(d1) != set(d2):
        return False
    ratios = {c1 / d2[m] for m, c1 in d1.items()}
    return len(ratios) == 1

def san(s):
    return maple_parse.normalize_poly_string(s)

n_ok = n_bad = n_rootof = 0
bad = []
for l in factors:
    _, q, f1, f2 = l.split("|", 3)
    if "RootOf" in q or "RootOf" in f1 or "RootOf" in f2:
        n_rootof += 1
        continue
    q, f1, f2 = san(q), san(f1), san(f2)
    names = sorted(set(re.findall(r"[A-Za-z]\w*", " ".join([q, f1, f2]))))
    R = PolynomialRing(QQ, names, order="degrevlex") if names else QQ
    env = {n: R(n) for n in names}
    Q  = R(sage_eval(q,  locals=env))
    F1 = R(sage_eval(f1, locals=env))
    F2 = R(sage_eval(f2, locals=env))
    if associates(F1 * F2, Q):
        n_ok += 1
    else:
        n_bad += 1
        bad.append((q, f1, f2))

print("\nfactor product identities (fak1*fak2 == q):")
print("  %d OK, %d FAILED, %d skipped (RootOf — needs algebraic-extension check)"
      % (n_ok, n_bad, n_rootof))
for q, f1, f2 in bad[:10]:
    print("  *** FAIL: (%s)*(%s) != %s" % (f1, f2, q))

# ---- 3. combined verdict --------------------------------------------------------
ok = (not unknown) and n_bad == 0 and n_rootof == 0
if ok:
    verdict = ("PASS — every split is exhaustive (binary tautologies + %d verified "
               "factor products); only known operators fired" % n_ok)
else:
    verdict = "INCOMPLETE — see failures above"
print("\nCOVER (split half): %s" % verdict)
print("  prune records in this run: %d (cover's other half — certify with check_f.py)"
      % len(prunes))
if ok:
    print("  => combined with check_f PASS, the surviving cells cover V(input).")
sys.exit(0 if ok else 1)
