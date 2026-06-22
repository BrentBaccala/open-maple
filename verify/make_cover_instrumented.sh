#!/usr/bin/env bash
# make_cover_instrumented.sh — build a DifferentialThomas source copy carrying
# BOTH instrumentation layers, so a single run feeds check F (cover's
# prunes-are-empty half) AND the cover split/factor verifier:
#
#   1. dt-instr.patch          -> OMRI_RECORD at every Inconsistent:=true site
#                                 (the 412 pruned branches; see make_instrumented.sh)
#   2. inject_cover_hooks.py    -> OMRI_SPLIT census + OMRI_FACTOR product capture
#                                 at the branch-creating split operators.
#
# The canonical ~/DifferentialThomas/src stays untouched (LGPL policy); the
# instrumented copy lands at /tmp/dt-cover-instr.  Idempotent (re-copies fresh).
set -euo pipefail

SRC="$HOME/DifferentialThomas/src"
DST="${DT_COVER_SRC:-/tmp/dt-cover-instr}"
HERE="$(cd "$(dirname "$0")" && pwd)"
PATCH="$HERE/dt-instr.patch"

[ -f "$PATCH" ] || { echo "missing prune patch: $PATCH" >&2; exit 2; }

rm -rf "$DST"
cp -r "$SRC" "$DST"
patch -p1 -d "$DST" < "$PATCH"
python3 "$HERE/inject_cover_hooks.py" "$DST"

echo "cover-instrumented DT source ready at $DST"
echo "  OMRI_RECORD prune hooks : $(grep -rh 'OMRI_RECORD' "$DST" | wc -l)"
echo "  OMRI_SPLIT census hooks : $(grep -rh 'OMRI_SPLIT'  "$DST" | wc -l)"
echo "  OMRI_FACTOR product hook: $(grep -rh 'OMRI_FACTOR' "$DST" | wc -l)"
