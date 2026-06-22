#!/usr/bin/env bash
# make_instrumented.sh — build the instrumented DifferentialThomas source copy
# used by check F.  Copies ~/DifferentialThomas/src to /tmp/dt-instr (untouched-
# LGPL policy: we NEVER edit ~/DifferentialThomas/src in place) and applies the
# OMRI_RECORD printf hooks at the Inconsistent:=true sites.
#
# Hooked sites (printf an OMRI_RECORD line just before Inconsistent:=true).
# ALL 11 Inconsistent:=true sites in the DT source are now hooked:
#   main               : 3 sites — equation->nonzero field element (464);
#                        inequation->0 (489); DivideByInequation rank/leader change (510).
#   reduction          : 2 sites — field element + InconsistentPolynom (572);
#                        tail-reduction rank/leader change (623).
#   strategy           : 1 site  — RemoveLeadingFieldElements leading element (29).
#   algebraic          : 2 sites — SplitBySquarefree discriminant exhaustion (94,
#                        reason discriminant_exhaustion); InequationLCM two equal
#                        inequations (200, reason dup_inequation).
#   factor             : 2 sites — non-squarefree factoring RootOf->non-RootOf (59,
#                        reason factor_nonsquarefree); "leading coefficient should be
#                        invertible" catch (124, reason leadcoeff_noninvertible).
#   differentialsystems: 1 site  — ReduceQListInSystem reductive-prolongation check,
#                        an inequation reduced to 0 OR an equation reduced to a
#                        nonzero field element (465, reason reductive_prolong).
#
# Idempotent: re-copies fresh each time, then patches.
set -euo pipefail

SRC="$HOME/DifferentialThomas/src"
DST="${DT_INSTR_SRC:-/tmp/dt-instr}"
PATCH="$(cd "$(dirname "$0")" && pwd)/dt-instr.patch"

[ -f "$PATCH" ] || { echo "missing patch: $PATCH" >&2; exit 2; }

rm -rf "$DST"
cp -r "$SRC" "$DST"
patch -p1 -d "$DST" < "$PATCH"

echo "instrumented DT source ready at $DST"
echo "  OMRI_RECORD hooks: $(grep -rh 'OMRI_RECORD' "$DST"/main "$DST"/reduction "$DST"/strategy "$DST"/algebraic "$DST"/factor "$DST"/differentialsystems | wc -l) (expected 11)"
