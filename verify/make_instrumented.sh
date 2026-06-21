#!/usr/bin/env bash
# make_instrumented.sh — build the instrumented DifferentialThomas source copy
# used by check F.  Copies ~/DifferentialThomas/src to /tmp/dt-instr (untouched-
# LGPL policy: we NEVER edit ~/DifferentialThomas/src in place) and applies the
# OMRI_RECORD printf hooks at the Inconsistent:=true sites.
#
# Hooked sites (printf an OMRI_RECORD line just before Inconsistent:=true):
#   main      : 3 sites — equation->nonzero field element (464);
#               inequation->0 (489); DivideByInequation rank/leader change (510).
#   reduction : 2 sites — field element + InconsistentPolynom (572);
#               tail-reduction rank/leader change (623).
#   strategy  : 1 site  — RemoveLeadingFieldElements leading element (29).
# (The prompt named the three main-file sites; empirically the small-system
#  rejections fire in reduction/strategy, so those are hooked too.  The remaining
#  Inconsistent sites in algebraic/factor/differentialsystems are specialized —
#  discriminant exhaustion, a leading-coeff catch block, a list-select dedup —
#  and are documented as not individually recorded.)
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
echo "  OMRI_RECORD hooks: $(grep -rc 'OMRI_RECORD' "$DST"/main "$DST"/reduction "$DST"/strategy | awk -F: '{s+=$2} END{print s}')"
