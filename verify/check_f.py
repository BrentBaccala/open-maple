r"""check_f.py — certify the engine's inconsistency rejections (check F).

Reads an OMRI_RECORD log produced by record_inconsistent.sh and, for each
recorded rejection, INDEPENDENTLY certifies (in Sage) that the rejected branch is
genuinely empty.  A rejection whose certificate FAILS is a wrongly-pruned,
non-empty branch — a real under-count cause.

Record format (one per line):
    OMRI_RECORD|<reason>|<poly>|EQS|<eqs-list>|INEQS|<ineqs-list>
where poly / eqs / ineqs are in Maple jet notation.

Certificates:
  field_element     — the recorded poly must have NO dependent/jet variable and be
                      a nonzero constant (a unit in K) -> the branch is empty. We
                      verify it is a nonzero element of the coefficient field
                      (only ivars / rationals, no jets).
  inequation_zero   — the recorded poly (an inequation that reduced to 0) must
                      pseudo-reduce to 0 modulo the cell's equations -> the
                      inequation is violated everywhere -> branch empty. Verified
                      by vanishes_on(poly, eqs, ivar-sats).
  rank_leader_change— the DivideByInequation step changed p's rank/leader; the
                      engine's proof (comment at the site) is that p=0 then holds
                      without a new prolongation. We give a weaker but sound
                      certificate: the recorded p must vanish on
                      V(eqs)\V(ineqs) (it is implied), OR the combined branch
                      {eqs, ineqs} is empty. We report which.

Universal certificate (the 5 sites closed by gap #2 — discriminant_exhaustion,
dup_inequation, factor_nonsquarefree, leadcoeff_noninvertible, reductive_prolong):
the recorded branch {eqs = 0, ineqs != 0}, augmented with the offending
polynomial(s) as equations (each reason implies the offending poly must vanish on
the surviving branch), has NO solution.  Tested with verify_core.saturated_empty
(1 in ideal(eqs+offenders) saturated by prod(ineqs)).  If the purely-algebraic
test does not certify empty, the eqs are PROLONGED (total derivatives to a bounded
order, exactly as check C does) and the test retried before declaring the record
uncertified.  A record that cannot be certified empty is a wrongly-pruned
NON-empty branch and is reported loudly.

  dup_inequation has NO offending poly (NONE): a duplicate inequation is not, on
  its face, an emptiness reason, so its branch {eqs=0, ineqs!=0} must itself be
  empty.  A dup_inequation that fails to certify empty is a likely real
  over-pruning bug and is flagged prominently.
"""

import sys
import os
import re

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from sage.all import PolynomialRing, QQ, sage_eval
import maple_parse
import verify_core as vc


def split_maple_list(s):
    s = s.strip()
    if s in ("[]", ""):
        return []
    inner = s[1:-1] if s.startswith("[") and s.endswith("]") else s
    return [maple_parse.normalize_poly_string(p)
            for p in maple_parse.split_top_level(inner)]


def parse_offenders(raw):
    """The OMRI poly field may be:
      NONE              -> no offending polynomial (e.g. dup_inequation)
      [p, q, ...]       -> a Maple list of offenders (reductive_prolong)
      <single poly>     -> one offender
    Return a list of normalized offender strings (possibly empty)."""
    raw = raw.strip()
    if raw == "" or raw == "NONE":
        return []
    if raw.startswith("[") and raw.endswith("]"):
        return split_maple_list(raw)
    return [maple_parse.normalize_poly_string(raw)]


def parse_record(line):
    line = line.strip()
    assert line.startswith("OMRI_RECORD|")
    body = line[len("OMRI_RECORD|"):]
    # reason | poly | EQS | eqs | INEQS | ineqs   -- but poly may contain '|'? no,
    # Maple %a never emits '|'. Split on the literal markers.
    m = re.match(r'(?P<reason>[^|]+)\|(?P<poly>.*)\|EQS\|(?P<eqs>.*)\|INEQS\|(?P<ineqs>.*)$', body)
    assert m, "malformed record: %s" % line[:80]
    raw_poly = m.group("poly").strip()
    # legacy single-poly field (kept for the original 4 reasons); offenders list
    # is the new universal-certificate view of the same field.
    poly = maple_parse.normalize_poly_string(raw_poly) if raw_poly not in ("", "NONE") \
        and not (raw_poly.startswith("[") and raw_poly.endswith("]")) else raw_poly
    return {
        "reason": m.group("reason"),
        "poly": poly,
        "offenders": parse_offenders(raw_poly),
        "eqs": split_maple_list(m.group("eqs")),
        "ineqs": split_maple_list(m.group("ineqs")),
        "ivars": None,  # filled by caller
    }


# Reasons closed by completeness gap #2 — certified by the universal
# emptiness certificate (saturated_empty + prolongation fallback) below.
UNIVERSAL_REASONS = (
    "discriminant_exhaustion",
    "dup_inequation",
    "factor_nonsquarefree",
    "leadcoeff_noninvertible",
    "reductive_prolong",
)

PROLONG_MAX_ORDER = 2  # bounded prolongation order for the fallback


def _build_ring(strs, ivars):
    jetset = vc.collect_vars(strs)
    ringvars = sorted(jetset) + [v for v in ivars if v not in jetset]
    R = PolynomialRing(QQ, ringvars if ringvars else list(ivars), order="degrevlex")
    env = {v: R(v) for v in R.variable_names()}
    return R, env


def universal_certify(rec, ivars):
    """Universal emptiness certificate for the gap-#2 reasons.

    The recorded branch is {eqs = 0, ineqs != 0}.  Each reason implies the
    offending polynomial(s) must vanish on any surviving point, so we add them as
    equations and ask whether the augmented branch is empty:
        saturated_empty(eqs + offenders, ineqs)   ==  1 in (eqs+off):(prod ineqs)^inf
    If the purely-algebraic test does not certify empty, prolong the eqs (and the
    offenders) to a bounded total order and retry — a missing-prolongation reason
    (reductive_prolong especially) only becomes empty after differentiation.
    """
    offenders = rec["offenders"]
    # ring over all strings we will evaluate (base + bounded prolongation set)
    base_strs = rec["eqs"] + rec["ineqs"] + offenders
    prolonged_eq_strs = vc.prolong_strings(rec["eqs"], ivars, PROLONG_MAX_ORDER)
    prolonged_off_strs = vc.prolong_strings(offenders, ivars, PROLONG_MAX_ORDER) if offenders else []
    all_strs = base_strs + prolonged_eq_strs + prolonged_off_strs
    R, env = _build_ring(all_strs, ivars)

    def ev(s):
        return R(sage_eval(s, locals=env))

    eqs = [ev(s) for s in rec["eqs"]]
    ineqs = [ev(s) for s in rec["ineqs"]]
    offs = [ev(s) for s in offenders]
    ivar_sats = [R(v) for v in ivars]
    sats = ineqs + ivar_sats

    # 1) purely-algebraic saturated emptiness
    if vc.saturated_empty(R, eqs + offs, sats):
        return True, ("branch {eqs=0, offenders=0, ineqs!=0} empty (algebraic): OK"
                      if offs else
                      "branch {eqs=0, ineqs!=0} empty (algebraic): OK")

    # an offender that is itself a nonzero field constant is an immediate unit-contradiction
    for o in offs:
        jetvars_in = [v for v in R.gens() if str(v) not in ivars and o.degree(v) > 0]
        if not jetvars_in and o != 0:
            return True, "an offender is a nonzero field constant (unit=0 contradiction) -> empty: OK"

    # 2) prolongation fallback
    p_eqs = [ev(s) for s in prolonged_eq_strs]
    p_offs = [ev(s) for s in prolonged_off_strs]
    if vc.saturated_empty(R, p_eqs + p_offs, sats):
        return True, ("branch empty after prolongation to order %d: OK" % PROLONG_MAX_ORDER)

    # not certified empty -> wrongly-pruned NON-empty branch
    if rec["reason"] == "dup_inequation":
        return False, ("dup_inequation branch NOT empty even after prolongation — "
                       "LIKELY REAL OVER-PRUNING BUG (duplicate inequation is not an "
                       "emptiness reason; the surviving branch has solutions)")
    return False, ("%s branch NOT certified empty (algebraic or prolonged) — "
                   "POSSIBLE wrongly-pruned NON-empty branch (offenders=%r)"
                   % (rec["reason"], offenders))


def certify(rec, ivars):
    """Return (ok:bool, msg:str) certifying the rejection is genuinely empty."""
    if rec["reason"] in UNIVERSAL_REASONS:
        return universal_certify(rec, ivars)

    strs = [rec["poly"]] + rec["eqs"] + rec["ineqs"]
    jetset = vc.collect_vars(strs)
    ringvars = sorted(jetset) + list(ivars)
    R = PolynomialRing(QQ, ringvars, order="degrevlex")
    env = {v: R(v) for v in ringvars}
    poly = R(sage_eval(rec["poly"], locals=env))
    eqs = [R(sage_eval(s, locals=env)) for s in rec["eqs"]]
    ineqs = [R(sage_eval(s, locals=env)) for s in rec["ineqs"]]
    ivar_sats = [R(v) for v in ivars]

    if rec["reason"] == "field_element":
        # must be a nonzero constant w.r.t. all jet variables (only ivars/rationals)
        jetvars_in = [v for v in R.gens() if str(v) not in ivars and poly.degree(v) > 0]
        if jetvars_in:
            return False, ("NOT a field element: still depends on jets %s"
                           % [maple_parse.var_to_jet(str(v)) for v in jetvars_in])
        if poly == 0:
            return False, "field element is 0 (not a unit) — does NOT certify emptiness"
        return True, "nonzero field element (unit in K) -> branch empty: OK"

    if rec["reason"] == "leading_field_element":
        # the strategy-site rejection: a LEADING Q element that is a field element
        # and is either (equation != 0) or (inequation = 0). Try both certificates.
        jetvars_in = [v for v in R.gens() if str(v) not in ivars and poly.degree(v) > 0]
        if not jetvars_in and poly != 0:
            return True, "nonzero leading field element (unit) -> branch empty: OK"
        if vc.vanishes_on(R, poly, eqs, ineqs + ivar_sats):
            return True, "leading element reduces to 0 mod cell -> empty: OK"
        if poly == 0:
            return True, "leading element is 0 -> empty: OK"
        return False, "leading field element NOT certified empty — branch may be NON-empty"

    if rec["reason"] == "inequation_zero":
        # the inequation reduced to 0 ; certify it vanishes on the cell eqs
        if vc.vanishes_on(R, poly, eqs, ineqs + ivar_sats):
            return True, "inequation reduces to 0 mod cell -> violated everywhere -> empty: OK"
        # if poly itself is literally 0 it trivially vanishes
        if poly == 0:
            return True, "inequation is literally 0 -> empty: OK"
        return False, "inequation does NOT reduce to 0 on the cell — branch may be NON-empty"

    if rec["reason"] == "rank_leader_change":
        # certify the combined branch {eqs=0, ineqs!=0, poly=0} is empty, OR poly
        # is implied (vanishes on the cell).
        if vc.vanishes_on(R, poly, eqs, ineqs + ivar_sats):
            return True, "recorded p is implied by the cell (vanishes on it): OK"
        if vc.saturated_empty(R, eqs + [poly], ineqs + ivar_sats):
            return True, "branch {eqs, p=0, ineqs!=0} is empty: OK"
        return False, ("rank/leader-change branch NOT certified empty and p NOT "
                       "implied — POSSIBLE wrongly-pruned non-empty branch")

    return False, "unknown reason %r" % rec["reason"]


def main():
    import argparse
    ap = argparse.ArgumentParser()
    ap.add_argument("logfile")
    ap.add_argument("--ivars", default="x,y,z")
    args = ap.parse_args()
    ivars = [v.strip() for v in args.ivars.split(",") if v.strip()]
    lines = [l for l in open(args.logfile) if l.startswith("OMRI_RECORD|")]
    print("read %d inconsistency record(s); ivars=%s" % (len(lines), ivars))
    allok = True
    for i, line in enumerate(lines, 1):
        rec = parse_record(line)
        ok, msg = certify(rec, ivars)
        print("  record %d [%s]: %s -- %s"
              % (i, rec["reason"], "CERTIFIED-EMPTY" if ok else "FAILED", msg))
        allok = allok and ok
    print("CHECK F: %s" % ("PASS (all rejections genuinely empty)" if allok
                           else "FAIL (a rejection was not certified empty)"))
    return 0 if allok else 1


if __name__ == "__main__":
    sys.exit(main())
