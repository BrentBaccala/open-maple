#!/usr/bin/env python
# certify_340_pif.py <record-index>  [default 340]
#
# Certify ONE pruned reductive_prolong ("sat" role) branch as EMPTY using the
# parameter-in-field reducer (verify_core.cell_field_reducer) instead of a full
# 53-variable Groebner basis. The branch is {EQS = 0, offenders != 0}; if every
# offender pseudo-reduces to 0 modulo the cell ideal over the param-in-field
# ring (x,y,z + constant params collapsed into the coefficient field), the
# required-!=0 offender vanishes on the whole cell -> the branch is empty ->
# the prune was correct.
#
# This is the right-sized tool for record 340, whose full-ring GB blew up
# (>11 h, 35 GB on c200-1). No timeout; the param-in-field ring is small.
import sys, os, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import check_f, verify_core as vc

idx   = int(sys.argv[1]) if len(sys.argv) > 1 else 340
LOG   = os.environ.get("OMRI_LOG", "/tmp/omri-hydrogen-full.log")
IVARS = ("x", "y", "z")

lines = [l for l in open(LOG).read().splitlines() if l.startswith("OMRI_RECORD|")]
rec   = check_f.parse_record(lines[idx - 1])
print("record %d: reason=%s  eqs=%d  offenders=%d"
      % (idx, rec["reason"], len(rec["eqs"]), len(rec["offenders"])), flush=True)

cell  = {"equations": rec["eqs"]}
offs  = [o for o in rec["offenders"] if o.strip() not in ("", "0")]

# Try increasing prolongation orders; order 0 = bare algebraic membership.
for order in (0, 1, 2):
    t0 = time.time()
    try:
        red, parse, nvars = vc.cell_field_reducer(cell, IVARS, prolong_order=order,
                                                  extra_strs=offs)
    except Exception as e:
        print("record %d: order=%d build FAILED: %r" % (idx, order, e), flush=True)
        continue
    print("record %d: order=%d  param-in-field ring jet-vars=%d  (build %.1fs)"
          % (idx, order, nvars, time.time() - t0), flush=True)
    try:
        rems = []
        for o in offs:
            r = red(o)
            rems.append(r)
        allzero = all(r == 0 for r in rems)
    except Exception as e:
        print("record %d: order=%d reduce FAILED: %r" % (idx, order, e), flush=True)
        continue
    dt = time.time() - t0
    if allzero:
        print("record %d: EMPTY -- all %d offender(s) reduce to 0 modulo the cell "
              "(param-in-field, prolong order %d) -> vanish on cell -> branch empty: OK "
              "(%.1fs)" % (idx, len(offs), order, dt), flush=True)
        sys.exit(0)
    else:
        nz = sum(1 for r in rems if r != 0)
        print("record %d: order=%d INCONCLUSIVE -- %d/%d offender(s) nonzero remainder "
              "(%.1fs)" % (idx, order, nz, len(offs), dt), flush=True)

print("record %d: NOT certified empty via param-in-field (orders 0-2) -- needs the "
      "radical/saturation path" % idx, flush=True)
sys.exit(2)
