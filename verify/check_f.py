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


# Reasons closed by completeness gap #2 — certified by the role-aware emptiness
# certificate below.  Each reason fixes the offender's ROLE; the certificate tests
# ONLY that role's placement (no "try the other placement" fallback, which would
# be UNSOUND — see ROLE_OF_REASON).
#
#   EQUATION role  ("eq"):   the reason says the offending polynomial must VANISH
#                            on every surviving point (a discriminant that had to
#                            be 0, a factor forced to 0, a leading coeff forced to
#                            0, an equation that reduced to a nonzero field
#                            constant).  Certify: saturated_empty(eqs+off, ineqs).
#                            A nonzero-constant offender puts 1 in the ideal
#                            directly.
#
#   SATURATION role ("sat"): the reason says the offending polynomial is an
#                            INEQUATION (required != 0) that REDUCED TO 0, i.e. it
#                            vanishes on the whole cell, contradicting != 0.
#                            Certify: saturated_empty(eqs, ineqs+off)  ==  off
#                            vanishes on V(eqs)\V(ineqs).
#
#   NONE role:               no offender (dup_inequation); the branch
#                            {eqs=0, ineqs!=0} must itself be empty.
#
# WHY single-role (not "try both"): the recorded pruning is ONE specific branch
# with ONE offender role.  If a must-vanish EQUATION offender happens to also
# vanish on the cell, the SATURATION test (off != 0) would return "empty"
# trivially, while the ACTUALLY-pruned branch {off = 0 = the whole cell} can be
# NON-empty -> a false PASS that masks the exact over-prune we are hunting.  So
# each reason must use only its own role's placement.
ROLE_OF_REASON = {
    "discriminant_exhaustion": "eq",
    "factor_nonsquarefree":    "eq",
    "leadcoeff_noninvertible": "eq",
    "reductive_prolong_eq":    "eq",
    "reductive_prolong":       "sat",
    "dup_inequation":          "none",
}
UNIVERSAL_REASONS = tuple(ROLE_OF_REASON.keys())

PROLONG_MAX_ORDER = 2  # bounded prolongation order for the fallback
PER_RECORD_TIMEOUT = 120  # seconds; a record exceeding this is reported NEEDS-REVIEW


class _Timeout(Exception):
    pass


def _certify_worker(rec, ivars, dvars, q):
    # runs in a child process so a C-level (Singular) computation can be HARD-killed
    global _DVAR_ORDER
    _DVAR_ORDER = dvars
    try:
        q.put(certify(rec, ivars))
    except Exception as e:
        q.put(("__error__", "%s: %s" % (type(e).__name__, e)))


def certify_with_timeout(rec, ivars, dvars, secs):
    """Run certify() in a child process with a HARD wall kill.  A SIGALRM cannot
    interrupt Singular's C-level Groebner/pseudo-division, so the per-record cap
    must terminate a separate process.  Returns (ok, msg) or raises _Timeout."""
    import multiprocessing as mp
    ctx = mp.get_context("fork")
    q = ctx.Queue()
    p = ctx.Process(target=_certify_worker, args=(rec, ivars, dvars, q))
    p.start()
    p.join(secs)
    if p.is_alive():
        p.terminate()
        p.join()
        raise _Timeout()
    try:
        res = q.get_nowait()
    except Exception:
        raise _Timeout()
    if isinstance(res, tuple) and len(res) == 2 and res[0] == "__error__":
        raise RuntimeError(res[1])
    return res


def _safe_prolong(strs, ivars, order):
    """vc.prolong_strings, but tolerant of jets whose flattened index tuple is
    shorter than len(ivars) (which would otherwise IndexError in raise_index_var).
    Such a string is left un-prolonged rather than crashing the whole record."""
    if not strs:
        return []
    try:
        return vc.prolong_strings(strs, ivars, order)
    except (IndexError, Exception):
        # prolong each independently; drop the ones that can't be differentiated
        out = list(strs)
        for s in strs:
            try:
                for d in vc.prolong_strings([s], ivars, order):
                    if d not in out:
                        out.append(d)
            except Exception:
                pass
        return out


def _build_ring(strs, ivars):
    """Build the polynomial ring over ONLY the variables that actually appear in
    `strs` (jet vars + any ivar that appears).  This per-record restriction is
    sound for emptiness — 1 in (gens):(sats)^inf over QQ[appearing-vars] iff over
    QQ[all-vars], since free variables don't affect satisfiability — and keeps the
    Groebner small instead of forcing the full ~60-variable ring on every record."""
    jetset = vc.collect_vars(strs)
    # only include ivars that genuinely occur (as bare tokens) in the strings
    present_ivars = [v for v in ivars
                     if any(re.search(r'(?<![A-Za-z0-9_])%s(?![A-Za-z0-9_\[])' % re.escape(v), s)
                            for s in strs)]
    ringvars = sorted(jetset) + [v for v in present_ivars if v not in jetset]
    if not ringvars:
        ringvars = [ivars[0]] if ivars else ["__dummy"]
    R = PolynomialRing(QQ, ringvars, order="degrevlex")
    env = {v: R(v) for v in R.variable_names()}
    return R, env, present_ivars


def universal_certify(rec, ivars):
    """Role-aware emptiness certificate for the gap-#2 reasons.

    The recorded branch is {eqs = 0, ineqs != 0}.  The offending polynomial's role
    is fixed by the reason (ROLE_OF_REASON); we test ONLY that placement, both on
    the recorded (algebraic) system and after a bounded prolongation, and report
    WHICH placement certified.  No cross-role fallback (that would be unsound).
    """
    role = ROLE_OF_REASON[rec["reason"]]
    offenders = rec["offenders"]

    # ---- cheap structural shortcuts (no Groebner) -------------------------------
    eqs_set = set(s.strip() for s in rec["eqs"])
    ineqs_set = set(s.strip() for s in rec["ineqs"])
    for o in offenders:
        os_ = o.strip()
        if os_ in ("", "0"):
            continue
        # nonzero numeric constant offender
        try:
            if role == "eq" and float(os_) != 0.0:
                return True, "[placement=eq,shortcut] offender is a nonzero constant (1 in ideal) -> empty: OK"
        except ValueError:
            pass
        if role == "eq":
            # offender must vanish but it is itself a recorded inequation -> v=0 & v!=0
            if os_ in ineqs_set:
                return True, "[placement=eq,shortcut] offender required =0 is also an inequation (!=0) -> empty: OK"
        if role == "sat":
            # offender (an inequation, required !=0) is literally a recorded equation (=0)
            if os_ in eqs_set:
                return True, "[placement=sat,shortcut] offender required !=0 is also a recorded equation (=0) -> empty: OK"

    # SAT-role FAST certificate, tried BEFORE the costly full-ring build + bounded
    # prolongation below: param-in-field membership. Collapse the ivars + constant
    # parameters into the coefficient field K, leaving only the genuine jet
    # unknowns as ring variables, so the cell Groebner basis is tiny where the
    # full 53-var ring GB blows up (record 340: 53 ring vars / >11 h / 35 GB  ->
    # 12 jet vars / 0.2 s). Sound for this placement: over K the cell-equation
    # initials are units, so membership in the cell ideal equals membership in the
    # saturated ideal (eqs):initials^inf — i.e. the required-!=0 offender vanishes
    # on the cell. Returns immediately on success, skipping the heavy machinery.
    if role == "sat":
        try:
            off_strs = [s for s in offenders if s.strip() not in ("", "0")]
            if off_strs:
                redf, _pf, _nv = vc.cell_field_reducer(
                    {"equations": rec["eqs"]}, ivars, prolong_order=0, extra_strs=off_strs)
                if all(redf(s) == 0 for s in off_strs):
                    return True, ("[placement=sat] offender(s) reduce to 0 modulo the cell "
                                  "over the parameter-in-field ring -> vanish on cell -> empty: OK")
        except Exception:
            pass

    base_strs = rec["eqs"] + rec["ineqs"] + offenders
    prolonged_eq_strs = _safe_prolong(rec["eqs"], ivars, PROLONG_MAX_ORDER)
    prolonged_off_strs = _safe_prolong(offenders, ivars, PROLONG_MAX_ORDER) if offenders else []
    R, env, present_ivars = _build_ring(base_strs + prolonged_eq_strs + prolonged_off_strs, ivars)

    def ev(s):
        return R(sage_eval(s, locals=env))

    eqs = [ev(s) for s in rec["eqs"]]
    ineqs = [ev(s) for s in rec["ineqs"]]
    offs = [o for o in (ev(s) for s in offenders) if o != 0]  # literal-0 offenders are inert
    ivar_sats = [R(v) for v in present_ivars]  # only saturate by ivars that appear
    sats = ineqs + ivar_sats

    if role == "none":  # dup_inequation: the branch itself must be empty
        if vc.saturated_empty(R, eqs, sats):
            return True, "[placement=none] branch {eqs=0, ineqs!=0} empty (algebraic): OK"
        p_eqs = [ev(s) for s in prolonged_eq_strs]
        if vc.saturated_empty(R, p_eqs, sats):
            return True, ("[placement=none] branch empty after prolongation order %d: OK"
                          % PROLONG_MAX_ORDER)
        return False, ("[placement=none] dup_inequation branch NOT empty even after "
                       "prolongation — LIKELY REAL OVER-PRUNING BUG (duplicate "
                       "inequation is not an emptiness reason; branch has solutions)")

    if not offs:
        # role expects an offender but every recorded offender is literal 0:
        # the role's contradiction can't be exhibited -> fall back to bare branch.
        if vc.saturated_empty(R, eqs, sats):
            return True, "[placement=%s,no-offender] bare branch empty (algebraic): OK" % role
        return False, ("[placement=%s] no usable (nonzero) offender recorded and bare "
                       "branch NOT empty — cannot certify (instrumentation gap?)" % role)

    if role == "eq":
        # offenders must VANISH on the surviving branch
        for o in offs:  # a nonzero field constant (no jet vars) -> 1 in ideal directly
            jetvars_in = [v for v in o.variables() if str(v) not in present_ivars]
            if not jetvars_in:  # o involves only ivars/constants and o != 0
                return True, "[placement=eq] offender is a nonzero field element (1 in ideal) -> empty: OK"
        if vc.saturated_empty(R, eqs + offs, sats):
            return True, "[placement=eq] {eqs=0, offenders=0, ineqs!=0} empty (algebraic): OK"
        p_eqs = [ev(s) for s in prolonged_eq_strs]
        p_offs = [o for o in (ev(s) for s in prolonged_off_strs) if o != 0]
        if vc.saturated_empty(R, p_eqs + p_offs, sats):
            return True, ("[placement=eq] empty after prolongation order %d: OK"
                          % PROLONG_MAX_ORDER)
        return False, ("[placement=eq] %s branch NOT certified empty (offenders as "
                       "equations, algebraic or prolonged) — POSSIBLE wrongly-pruned "
                       "NON-empty branch (offenders=%r)" % (rec["reason"], offenders))

    # role == "sat": offenders are inequations (required !=0) that must vanish on cell.
    # FASTEST sufficient path — TRIANGULAR PSEUDO-REDUCTION (no Groebner): the
    # engine reduced the offending inequation to 0 via ReduceWRTJanetTrees, i.e.
    # triangular pseudo-reduction modulo the cell's equations (a triangular set).
    # We replicate that independently with vc.ritt_reduce: if the pseudo-remainder
    # of the offender modulo `eqs` is 0, the offender lies in (eqs):(initials)^inf,
    # which vanishes on V(eqs)\V(initials); the cell's inequations keep the initials
    # nonzero, so the required-!=0 offender vanishes everywhere on the cell -> empty.
    # This is the SAME reduction DT performed and avoids any 60-var Groebner basis
    # (those are intractable on the dense parameter cells).
    DVAR_ORDER = globals().get("_DVAR_ORDER") or list(ivars)
    try:
        if all(vc.ritt_reduce(R, o, eqs, DVAR_ORDER, present_ivars) == 0 for o in offs):
            return True, ("[placement=sat] offender(s) pseudo-reduce to 0 modulo the cell "
                          "(triangular Ritt reduction) -> vanish on cell -> empty: OK")
    except Exception:
        pass
    # next: each offender lies in ideal(eqs) ALONE (GB of cell equations, no sat var).
    try:
        Ieq = R.ideal(eqs) if eqs else R.ideal([R(0)])
        GBe = Ieq.groebner_basis()
        if all(R(o).reduce(GBe) == 0 for o in offs):
            return True, "[placement=sat] offender(s) in ideal(eqs) -> vanish on cell -> empty: OK"
    except Exception:
        pass
    # next sufficient path: each offender lies in the SATURATED ideal (eqs):ineqs^inf
    # (handles the case where vanishing needs the inequations saturated out).
    try:
        RR, GB, up = vc.saturated_groebner(R, eqs, sats)
        if all(vc.in_ideal_via_gb(RR, GB, up(o)) for o in offs):
            return True, ("[placement=sat] offender(s) in saturated ideal (eqs):ineqs^inf "
                          "-> vanish on cell -> empty: OK")
    except Exception:
        pass
    if vc.saturated_empty(R, eqs, sats + offs):
        return True, "[placement=sat] offender(s) vanish on the cell (radical, required !=0) -> empty: OK"
    p_eqs = [ev(s) for s in prolonged_eq_strs]
    try:
        RRp, GBp, upp = vc.saturated_groebner(R, p_eqs, sats)
        if all(vc.in_ideal_via_gb(RRp, GBp, upp(o)) for o in offs):
            return True, ("[placement=sat] offender(s) in saturated ideal of the "
                          "prolonged cell (order %d) -> empty: OK" % PROLONG_MAX_ORDER)
    except Exception:
        pass
    if vc.saturated_empty(R, p_eqs, sats + offs):
        return True, ("[placement=sat] offender(s) vanish on the prolonged cell "
                      "(radical, order %d) -> empty: OK" % PROLONG_MAX_ORDER)
    return False, ("[placement=sat] %s branch NOT certified empty (offending "
                   "inequation does not vanish on the cell, algebraic or prolonged) "
                   "— POSSIBLE wrongly-pruned NON-empty branch (offenders=%r)"
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
    ap.add_argument("--dvars",
                    default="DDPs,DPs,Ps,Vf,rho,V1,V2,V3,V4,a0,a1,b0,b1,c0,c1",
                    help="dependent-variable ranking order (highest first) for the "
                         "triangular Ritt pseudo-reduction certificate; default is the "
                         "hydrogen DVar list")
    args = ap.parse_args()
    ivars = [v.strip() for v in args.ivars.split(",") if v.strip()]
    global _DVAR_ORDER
    _DVAR_ORDER = [v.strip() for v in args.dvars.split(",") if v.strip()]
    lines = [l for l in open(args.logfile) if l.startswith("OMRI_RECORD|")]
    print("read %d inconsistency record(s); ivars=%s; dvars=%s"
          % (len(lines), ivars, _DVAR_ORDER), flush=True)
    allok = True
    n_review = 0
    failed = []
    for i, line in enumerate(lines, 1):
        rec = parse_record(line)
        try:
            ok, msg = certify_with_timeout(rec, ivars, _DVAR_ORDER, PER_RECORD_TIMEOUT)
            status = "CERTIFIED-EMPTY" if ok else "FAILED"
        except _Timeout:
            ok, status = None, "NEEDS-REVIEW(timeout %ds)" % PER_RECORD_TIMEOUT
            msg = ("certification exceeded the per-record timeout — not certified "
                   "empty and not refuted; offenders=%r" % rec["offenders"])
            n_review += 1
        except Exception as e:
            ok, status = None, "NEEDS-REVIEW(error)"
            msg = "certification raised %s: %s" % (type(e).__name__, e)
            n_review += 1
        print("  record %d [%s]: %s -- %s" % (i, rec["reason"], status, msg), flush=True)
        if ok is False:
            failed.append((i, rec["reason"]))
        if ok is not True:
            allok = False
    print("CHECK F: %s (%d records; %d FAILED, %d NEEDS-REVIEW)"
          % ("PASS (all rejections genuinely empty)" if allok and n_review == 0
             else ("FAIL — a rejection was not certified empty" if failed
                   else "INCONCLUSIVE — some records need review"),
             len(lines), len(failed), n_review), flush=True)
    if failed:
        print("  FAILED records: %s" % failed, flush=True)
    return 0 if (allok and n_review == 0) else 1


if __name__ == "__main__":
    sys.exit(main())
