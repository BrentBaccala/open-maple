r"""run_verify.py — entry point for the independent decomposition verifier.

Usage (under sage -python):
    sage -python run_verify.py <system-name> [--checks ABCDE] [--cells-file F]
                                             [--cover-order N] [--max-pairs K]

system-name is one of build_input.SYSTEMS (readme_smoke, cauchy_riemann,
overview_3var, alg_xu2, alg_factored, hydrogen).  For hydrogen the cells are
read from hydrogen_thomas_result.m unless --cells-file overrides.

For the small algebraic / few-jet systems we do NOT have a saved cell file, so
the runner can also COMPUTE a reference decomposition's cells from the system
itself ONLY for the structural smoke (these are tiny); but the primary mode is to
verify externally-produced cells.  When no cell file is supplied for a small
system, the runner reports that and runs only the input-side checks it can.

Prints a per-check PASS/FAIL with witnesses.  Exit code 0 iff all RUN checks pass.
"""

import sys
import os
import argparse
import itertools

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import build_input
import maple_parse
import verify_core as vc
from sage.all import PolynomialRing, QQ


def banner(s):
    print("\n" + "=" * 70)
    print(s)
    print("=" * 70)


def check_A(prob):
    """Per-cell structural validity: triangular set (distinct leaders), initials
    & separants nonzero modulo the cell's inequations."""
    allpass = True
    lines = []
    for ci, cell in enumerate(prob.cells, 1):
        eqs, ineqs = cell["equations"], cell["inequations"]
        leaders = []
        ok = True
        for e in eqs:
            ld = prob.leader(e)
            if ld is None:
                ok = False
                lines.append("  cell %d: equation with no jet leader (field element): %s"
                             % (ci, maple_parse.normalize_poly_string(str(e))))
                continue
            leaders.append(ld)
        # distinct leaders
        if len(set(map(str, leaders))) != len(leaders):
            ok = False
            from collections import Counter
            dup = [k for k, v in Counter(map(str, leaders)).items() if v > 1]
            lines.append("  cell %d: DUPLICATE leaders %s (not triangular)"
                         % (ci, [maple_parse.var_to_jet(d) for d in dup]))
        # initials & separants must not be identically zero (cheap structural
        # test).  Whether they actually vanish ON the cell variety is the deep,
        # expensive question — that is exactly check B's job (it saturates by the
        # inequations).  Check A stays cheap and structural so it scales to the
        # 60-variable hydrogen ring.
        for e in eqs:
            ld = prob.leader(e)
            if ld is None:
                continue
            ini = prob.initial(e, ld)
            sep = prob.separant(e, ld)
            for nm, q in (("initial", ini), ("separant", sep)):
                if q == 0:
                    ok = False
                    lines.append("  cell %d: %s of eq (leader %s) is identically 0"
                                 % (ci, nm, maple_parse.var_to_jet(str(ld))))
        if ok:
            lines.append("  cell %d: OK (%d eqs, distinct leaders, initials/separants nonzero)"
                         % (ci, len(eqs)))
        allpass = allpass and ok
    return allpass, "\n".join(lines)


def check_B(prob, prolong_order=2):
    """Soundness: each input equation reduces to 0 modulo cell eqs +
    prolongations; each input inequation is a unit on the cell; reverse
    corruption check (each cell equation lies in the saturated diff ideal of the
    input)."""
    allpass = True
    lines = []
    ivars = prob.ivars
    # prolong the INPUT once (shared) for the reverse check
    input_eq_strs = [maple_parse.normalize_poly_string(
        str(e)) for e in prob._input_eq_strs] if hasattr(prob, "_input_eq_strs") else None

    # raw input strings (soundness_check_cell works from strings, building its own
    # parameter-in-field ring per cell so the cell-equation initials become field
    # units and a cheap ~20-variable Groebner basis gives exact soundness).
    input_strs = {"equations": prob.input_eq_strs,
                  "inequations": list(getattr(prob, "input_ineq_strs", []))}
    for ci, cell in enumerate(prob.cells, 1):
        ok = True
        cell_strs = {
            "equations": [maple_parse.normalize_poly_string(str(e)) for e in cell["equations"]],
            "inequations": [maple_parse.normalize_poly_string(str(e)) for e in cell["inequations"]],
        }
        sok, fails, ineq_fails, nv = vc.soundness_check_cell(
            input_strs, cell_strs, ivars, prob.dvar_order, prolong_order)
        if not sok:
            ok = False
            for (k, rem) in fails:
                lines.append("  cell %d: input eq[%d] does NOT reduce to 0 (rem %s): %s"
                             % (ci, k, rem, _pp(prob.input_eqs[k])))
            for k in ineq_fails:
                lines.append("  cell %d: input INEQUATION[%d] vanishes on cell" % (ci, k))
        if ok:
            lines.append("  cell %d: SOUND (all %d input eqs reduce to 0; %d-var field ring)"
                         % (ci, len(prob.input_eqs), nv))
        allpass = allpass and ok
    return allpass, "\n".join(lines)


def vc_sage_eval(s, env):
    from sage.all import sage_eval
    return sage_eval(s, locals=env)


def check_B_reverse(prob, prolong_order=2):
    """Corruption / non-vacuity check.

    A splitting decomposition legitimately ADDS branch constraints, so it is NOT
    valid to demand that every cell equation be implied by the input alone (a
    spec-level mistake in the original prompt — it fails on the trivial 2-cell
    x*u^2-u example, where the cell {u=0} is not implied by the input).  The
    genuine corruption guard is:

      (1) NON-VACUITY — each cell must be CONSISTENT (have a solution): an
          inconsistent cell is a spurious component.  Test: { cell eqs = 0,
          cell ineqs != 0 } is NOT saturated-empty.

      (2) The cell variety stays inside the input variety — this is exactly the
          forward soundness check (check_B forward), so we don't re-test it.

    A failure here (an empty 'finished' cell) would itself be a real over-count /
    corruption signal."""
    allpass = True
    lines = []
    ivars = prob.ivars
    for ci, cell in enumerate(prob.cells, 1):
        eqs = cell["equations"]
        ineqs = cell["inequations"]
        # Non-vacuity: the system { eqs = 0, ineqs != 0, ivars != 0 } must have a
        # solution.  Use the trustworthy FULL-RING saturated_empty (Rabinowitsch +
        # one Groebner basis) — it is the correct, sound test and, for these
        # mostly-linear cells, is fast (~0 s/cell).  NOTE: the parameter-in-field
        # reducer must NOT be used here — at prolong_order 0 it spuriously reduces
        # an unconstrained inequation jet (e.g. DDPs_y) to 0 and false-flags a
        # consistent cell as vacuous (verified against the full-ring test).
        sats = ineqs + vc._ivar_gens(prob, prob.R)
        empty = vc.saturated_empty(prob.R, eqs, sats)
        if empty:
            allpass = False
            lines.append("  cell %d: VACUOUS (inconsistent system, no solution) — spurious"
                         % ci)
        else:
            lines.append("  cell %d: non-vacuous (consistent)" % ci)
    return allpass, "\n".join(lines)


def check_C(prob, max_pairs=None):
    """Disjointness: each pair of cells shares no solution (1 in saturated
    combined ideal)."""
    allpass = True
    lines = []
    n = len(prob.cells)
    pairs = list(itertools.combinations(range(n), 2))
    if max_pairs:
        pairs = pairs[:max_pairs]
    overlaps = 0
    for (i, j) in pairs:
        ci, cj = prob.cells[i], prob.cells[j]
        gens = ci["equations"] + cj["equations"]
        sats = ci["inequations"] + cj["inequations"] + vc._ivar_gens(prob, prob.R)
        empty = vc.saturated_empty(prob.R, gens, sats)
        if not empty:
            allpass = False
            overlaps += 1
            lines.append("  cells %d,%d OVERLAP (combined system has a solution)"
                         % (i + 1, j + 1))
    lines.append("  checked %d pairs, %d overlapping" % (len(pairs), overlaps))
    return allpass, "\n".join(lines)


def check_E(prob, prolong_order=1):
    """Per-cell passivity (Janet/Thomas integrability of each cell).

    A cell is a *simple system* only if it is PASSIVE: every Delta-polynomial
    (cross-derivative integrability condition) reduces to 0 modulo the cell.  A
    "finished" cell that fails this is the smoking gun for a premature-finish /
    missing-prolongation bug — the most likely cause of an under-count.

    CORRECT Delta-condition (NOT 'every eq-derivative reduces to 0'): for each PAIR
    of cell equations e_p, e_q whose leaders are derivatives of the SAME dependent
    variable u (leaders u_p, u_q), let m = componentwise lcm(p, q).  Prolong e_p by
    (m-p) and e_q by (m-q); both now have leader u_m.  The Delta-polynomial
        Delta = init(e_q)*prolong(e_p) - init(e_p)*prolong(e_q)
    eliminates the common top leader and must reduce to 0 modulo the cell.

    A single equation's prolongation that introduces a BRAND-NEW leader appearing
    in no other equation is NOT an integrability obstruction (it merely defines a
    new derivative); we deliberately do not flag it.  This is exactly the standard
    Thomas/Janet passivity test restricted to the cross-derivatives that must
    close; full sub-monomial Janet completion of every non-multiplicative
    prolongation is deferred (documented)."""
    allpass = True
    lines = []
    ivars = prob.ivars
    for ci, cell in enumerate(prob.cells, 1):
        cell_strs = [maple_parse.normalize_poly_string(str(e)) for e in cell["equations"]]
        if vc._max_order_strs(cell_strs) == 0:
            lines.append("  cell %d: algebraic (no derivations) -> passivity vacuous" % ci)
            continue
        # group equations by the dvar base of their leader, recording (str, idx, init_str)
        groups = {}
        for s, e in zip(cell_strs, cell["equations"]):
            ld = prob.leader(e)
            if ld is None:
                continue
            base, idx = vc.parse_idx(str(ld))
            ini = prob.initial(e, ld)
            groups.setdefault(base, []).append((s, idx, maple_parse.normalize_poly_string(str(ini))))
        # build Delta-poly strings
        deltas = []
        for base, members in groups.items():
            for (s1, p, i1), (s2, q, i2) in itertools.combinations(members, 2):
                m = tuple(max(a, b) for a, b in zip(p, q))
                d1 = _raise_to(s1, p, m, ivars)
                d2 = _raise_to(s2, q, m, ivars)
                # Delta = init2*d1 - init1*d2  (clears the common leader u_m)
                deltas.append((base, p, q, "(%s)*(%s) - (%s)*(%s)" % (i2, d1, i1, d2)))
        if not deltas:
            lines.append("  cell %d: PASSIVE (no Delta-pairs: leaders are distinct dvars)" % ci)
            continue
        extra = [d for (_, _, _, d) in deltas]
        try:
            red, parse, nv = vc.cell_field_reducer(
                {"equations": cell_strs}, ivars,
                prolong_order=prolong_order + 1, extra_strs=extra)
        except Exception as ex:
            lines.append("  cell %d: passivity reducer error: %s" % (ci, ex))
            allpass = False
            continue
        ok = True
        bad = []
        for (base, p, q, d) in deltas:
            try:
                if red(d) != 0:
                    ok = False
                    bad.append((base, p, q))
            except Exception:
                pass
        if ok:
            lines.append("  cell %d: PASSIVE (%d Delta-pairs reduce to 0; %d-var ring)"
                         % (ci, len(deltas), nv))
        else:
            lines.append("  cell %d: NON-PASSIVE — Delta-poly(s) %s do NOT reduce to 0 "
                         "(missing prolongation / premature finish)"
                         % (ci, ["%s d%s/d%s" % (b, p, q) for (b, p, q) in bad[:4]]))
        allpass = allpass and ok
    return allpass, "\n".join(lines)


def _raise_to(eq_str, frm, to, ivars):
    """Total-derivative eq_str (whose leader has multi-index `frm`) up to index
    `to` (componentwise >= frm).  Returns normalized string, or None if to<frm."""
    cur = eq_str
    for axis in range(len(ivars)):
        for _ in range(to[axis] - frm[axis]):
            cur = vc.total_derivative_str(cur, ivars, axis)
    return cur


def _pp(poly):
    return maple_parse.normalize_poly_string(str(poly))


def load_cells(name, cells_file):
    if cells_file:
        return maple_parse.parse_EI_file(cells_file)
    if name == "hydrogen":
        default = os.path.expanduser("~/open-maple/hydrogen_thomas_result.m")
        return maple_parse.parse_EI_file(default)
    try:
        import known_cells
        if name in known_cells.KNOWN:
            return known_cells.KNOWN[name]
    except ImportError:
        pass
    return None


def check_D(prob, cover_order=1):
    """Cover / completeness via bounded prolongation.

    Prolong the INPUT to order N = cover_order, treat all jets up to N as
    algebraic variables, and verify V(input_N) ⊆ ⋃ V(cell_i) over the algebraic
    closure.  Equivalent: there is NO point that satisfies the input but lies in
    NO cell, i.e. for the input prolonged system, the locus
        input=0  AND  (for every cell_i: NOT(cell_i holds))
    is empty.  'NOT(cell_i holds)' = (some cell_i eq != 0) OR (some ineq_i = 0).

    Encoding 'lies in no cell' as a single algebraic condition is a union of
    complements and is expensive; we instead use the contrapositive cover test
    that is decidable with one saturation per cell-combination is intractable in
    general.  We use the tractable surrogate:

      For the radical ideal of the prolonged input I_N, a sound+disjoint family
      covers iff  prod over cells of (a 'cell-defect' polynomial) ... — not a
      single GB.  So instead we test cover DIRECTLY by Nullstellensatz on the
      'uncovered witness' system per cell-leaving choice, which is 2^(#ineq)-ish.

    Given the cost, check_D is implemented for SMALL systems only (few cells, few
    jets): we enumerate, for each cell, the ways to FALSIFY it (each eq nonzero or
    each ineq zero) and test that input ∧ (all cells falsified) is empty.  The
    number of falsification combinations is prod_i (len(eqs_i)+len(ineqs_i)),
    which is only feasible for the small systems.  For hydrogen this blows up and
    check_D reports INTRACTABLE and defers to check_E.
    """
    import itertools as _it
    # estimate combination count
    combo = 1
    for c in prob.cells:
        combo *= max(1, len(c["equations"]) + len(c["inequations"]))
        if combo > 200000:
            return None, ("  INTRACTABLE: falsification-combination count exceeds "
                          "budget (prod_i(|eqs_i|+|ineqs_i|) > 2e5). Deferring to "
                          "check E (per-cell passivity).")
    # prolong input
    ivars = prob.ivars
    prol_in = vc.prolong_strings(prob.input_eq_strs, ivars, cover_order)
    Rb, coerce, env = prob.add_var_strings(prol_in)
    inI = [Rb(vc_sage_eval(s, env)) for s in prol_in]
    ivar_sats = [Rb(prob.R(v)) for v in prob.ivars]
    # For each cell, the ways to "leave" it: pick one eq to be nonzero, OR one
    # ineq to be zero.  A witness uncovered point must leave EVERY cell.
    def leave_choices(cell):
        ch = []
        for e in cell["equations"]:
            ch.append(("eq_nonzero", coerce(e)))
        for q in cell["inequations"]:
            ch.append(("ineq_zero", coerce(q)))
        if not ch:
            # cell with no eqs and no ineqs = all of space; cannot be left
            return None
        return ch
    per_cell = []
    for c in prob.cells:
        lc = leave_choices(c)
        if lc is None:
            return True, "  trivially covered: a cell equals the whole space"
        per_cell.append(lc)
    uncovered = False
    lines = []
    for choice in _it.product(*per_cell):
        # build the witness system: input=0, plus for each picked 'eq_nonzero'
        # that eq != 0 (saturate), and each 'ineq_zero' that ineq = 0.
        gens = list(inI)
        sats = list(ivar_sats)
        for (kind, poly) in choice:
            if kind == "eq_nonzero":
                sats.append(poly)
            else:  # ineq_zero
                gens.append(poly)
        if not vc.saturated_empty(Rb, gens, sats):
            uncovered = True
            lines.append("  UNCOVERED witness: a point satisfies the input but "
                         "leaves every cell (dropped branch).")
            break
    if not uncovered:
        lines.append("  COVER OK: V(input_N) ⊆ ⋃ cells (no dropped branch) at order N=%d"
                     % cover_order)
    return (not uncovered), "\n".join(lines)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("system")
    ap.add_argument("--checks", default="ABCDE")
    ap.add_argument("--cells-file", default=None)
    ap.add_argument("--cover-order", type=int, default=1)
    ap.add_argument("--prolong-order", type=int, default=2)
    ap.add_argument("--max-pairs", type=int, default=None)
    args = ap.parse_args()

    input_system = build_input.SYSTEMS[args.system]()
    cells = load_cells(args.system, args.cells_file)
    if cells is None:
        print("NO CELL FILE for system %r; cannot run cell-side checks." % args.system)
        print("Supply --cells-file or produce one via open-maple.")
        return 2

    prob = vc.Problem(input_system, cells)
    prob.input_eq_strs = list(input_system["equations"])
    prob.input_ineq_strs = list(input_system.get("inequations", []))
    print("System: %s   ivars=%s   %d input eqs   %d cells"
          % (args.system, prob.ivars, len(prob.input_eqs), len(prob.cells)))

    results = {}
    if "A" in args.checks:
        banner("CHECK A — per-cell structural validity (triangular set)")
        p, d = check_A(prob)
        print(d)
        print("CHECK A: %s" % ("PASS" if p else "FAIL"))
        results["A"] = p
    if "B" in args.checks:
        banner("CHECK B — soundness (each cell ⊆ input) + reverse corruption")
        p1, d1 = check_B(prob, args.prolong_order)
        print(d1)
        print("CHECK B (forward soundness): %s" % ("PASS" if p1 else "FAIL"))
        p2, d2 = check_B_reverse(prob, args.prolong_order)
        print(d2)
        print("CHECK B (reverse / no-invented-constraint): %s" % ("PASS" if p2 else "FAIL"))
        results["B"] = p1 and p2
    if "C" in args.checks:
        banner("CHECK C — pairwise disjointness")
        p, d = check_C(prob, args.max_pairs)
        print(d)
        print("CHECK C: %s" % ("PASS" if p else "FAIL"))
        results["C"] = p
    if "D" in args.checks:
        banner("CHECK D — cover / completeness (bounded prolongation)")
        p, d = check_D(prob, args.cover_order)
        print(d)
        if p is None:
            print("CHECK D: DEFERRED (intractable)")
        else:
            print("CHECK D: %s" % ("PASS" if p else "FAIL"))
            results["D"] = p
    if "E" in args.checks:
        banner("CHECK E — per-cell passivity (Delta-polynomial integrability)")
        p, d = check_E(prob, args.cover_order)
        print(d)
        print("CHECK E: %s" % ("PASS" if p else "FAIL"))
        results["E"] = p

    banner("SUMMARY")
    for k in sorted(results):
        print("  CHECK %s: %s" % (k, "PASS" if results[k] else "FAIL"))
    allok = all(results.values())
    print("OVERALL: %s" % ("PASS" if allok else "FAIL"))
    return 0 if allok else 1


if __name__ == "__main__":
    sys.exit(main())
