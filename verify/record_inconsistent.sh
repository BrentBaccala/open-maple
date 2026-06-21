#!/usr/bin/env bash
# record_inconsistent.sh — run a Maple .mpl through open-maple with the
# INSTRUMENTED DifferentialThomas source so every point where the engine sets
# Inconsistent:=true appends an OMRI_RECORD line; capture those records to the
# file named by OPENMAPLE_RECORD_INCONSISTENT (check F instrumentation).
#
# The instrumented copy lives at /tmp/dt-instr (created by make_instrumented.sh);
# the canonical ~/DifferentialThomas/src stays untouched, so NORMAL runs (which
# don't set DT_SRC to the instrumented copy) are unaffected.
#
# Usage:
#   OPENMAPLE_RECORD_INCONSISTENT=/path/to/log.txt \
#     verify/record_inconsistent.sh path/to/system.mpl
#
set -euo pipefail

MPL="${1:?usage: record_inconsistent.sh FILE.mpl}"
LOG="${OPENMAPLE_RECORD_INCONSISTENT:?set OPENMAPLE_RECORD_INCONSISTENT to the output path}"
DTINSTR="${DT_INSTR_SRC:-/tmp/dt-instr}"

if [ ! -d "$DTINSTR" ]; then
  echo "instrumented DT source not found at $DTINSTR; run verify/make_instrumented.sh" >&2
  exit 2
fi

export PATH="$HOME/.local/go-toolchain/go/bin:$PATH"
export GOPATH="$HOME/.local/gopath"
export GOFLAGS=-mod=mod
export OPENMAPLE_CAS=sage
export DT_SRC="$DTINSTR"

cd "$HOME/open-maple/src"
# Run; tee full output, then extract the tagged records into the log.
RAW="$(mktemp)"
go run . "$MPL" 2>&1 | tee "$RAW"
grep '^OMRI_RECORD|' "$RAW" > "$LOG" || true
echo "recorded $(wc -l < "$LOG") inconsistency record(s) to $LOG" >&2
rm -f "$RAW"
