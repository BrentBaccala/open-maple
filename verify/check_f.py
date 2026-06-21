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


def parse_record(line):
    line = line.strip()
    assert line.startswith("OMRI_RECORD|")
    body = line[len("OMRI_RECORD|"):]
    # reason | poly | EQS | eqs | INEQS | ineqs   -- but poly may contain '|'? no,
    # Maple %a never emits '|'. Split on the literal markers.
    m = re.match(r'(?P<reason>[^|]+)\|(?P<poly>.*)\|EQS\|(?P<eqs>.*)\|INEQS\|(?P<ineqs>.*)$', body)
    assert m, "malformed record: %s" % line[:80]
    return {
        "reason": m.group("reason"),
        "poly": maple_parse.normalize_poly_string(m.group("poly")),
        "eqs": split_maple_list(m.group("eqs")),
        "ineqs": split_maple_list(m.group("ineqs")),
        "ivars": None,  # filled by caller
    }


def certify(rec, ivars):
    """Return (ok:bool, msg:str) certifying the rejection is genuinely empty."""
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
