#!/usr/bin/env bash
#
# run_examples.sh — canonical regression suite for the open-maple
# DifferentialThomas example programs.
#
# Runs each ~/thomas-experiments example through `openmaple` (go run .) on the
# Sage backend, asserts the expected number of simple systems, and prints a
# per-example PASS/FAIL plus a final summary. Exits nonzero on any failure.
#
# Each example streams to the live log AS IT RUNS (tee), which matters for ex4:
# it is one ~30-minute command and would trip the task-runner's 600 s stream
# watchdog if output did not keep flowing. openmaple prints progress (sage call
# numbers, rounds, decomposition info) so the stream stays alive.
#
# Usage:
#   tests/run_examples.sh                 # ex1..ex4 (full, ~30+ min for ex4)
#   tests/run_examples.sh --quick         # ex1..ex3 only (skip the long ex4)
#   tests/run_examples.sh --log FILE      # stream to FILE (default: a temp log)
#
# Expected decompositions (per STATUS.md):
#   ex1_singular_ode -> 2,  ex2_params -> 3 labeled parts,
#   ex3_ode1d -> 13
# ex4_hydrogen: pass criterion is COMPLETION WITHOUT ERROR (HYDROGEN_THOMAS_DONE
#   present AND exit 0) — NOT a specific count. Commit 361fbae fixed a
#   reciprocal-serialization bug (1/X was silently collapsing to X), so the old
#   "29" was computed from a wrong intermediate and we have no Maple ground-truth
#   to compare against. The cell count is printed as INFO only.
# ex1b_discover is an accessor/typing probe (no count) — run as a smoke check.

set -uo pipefail

HOME_DIR="${HOME}"
SRC_DIR="${HOME_DIR}/open-maple/src"
EXP_DIR="${HOME_DIR}/thomas-experiments"

export PATH="${HOME_DIR}/.local/go-toolchain/go/bin:${PATH}"
export GOPATH="${HOME_DIR}/.local/gopath"
export GOFLAGS=-mod=mod
export OPENMAPLE_CAS=sage

QUICK=0
LOG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --quick) QUICK=1; shift ;;
    --log)   LOG="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

# Allow the task runner's live log to be used so the stream stays alive.
if [ -z "${LOG}" ]; then
  LOG="${TASK_LIVE_LOG:-/tmp/open-maple-examples-$(date +%Y%m%d-%H%M%S).log}"
fi
echo "streaming to: ${LOG}"

cd "${SRC_DIR}" || { echo "cannot cd ${SRC_DIR}" >&2; exit 2; }

PASS=0
FAIL=0
declare -a RESULTS

# run_example NAME FILE PATTERN EXPECTED
#   PATTERN extracts the count via the awk program in count_of(); EXPECTED is the
#   integer to assert. A timeout guards each run.
run_example() {
  local name="$1" file="$2" kind="$3" expected="$4" tmo="$5"
  local path="${EXP_DIR}/${file}"
  local out
  out="$(mktemp)"
  echo ""                                                  | tee -a "${LOG}"
  echo "=================================================" | tee -a "${LOG}"
  echo "RUN ${name}  (${file}, expect ${expected})"        | tee -a "${LOG}"
  echo "=================================================" | tee -a "${LOG}"

  # Stream to BOTH the live log and a per-example capture (for count extraction).
  timeout "${tmo}" go run . "${path}" 2>&1 | tee -a "${LOG}" | tee "${out}"
  local rc=${PIPESTATUS[0]}

  local got
  got="$(count_of "${kind}" "${out}")"

  if [ "${rc}" -ne 0 ]; then
    echo "[FAIL] ${name}: openmaple exited ${rc}" | tee -a "${LOG}"
    RESULTS+=("FAIL ${name} (exit ${rc}, got=${got})")
    FAIL=$((FAIL+1))
  elif [ "${got}" != "${expected}" ]; then
    echo "[FAIL] ${name}: got ${got} simple systems, expected ${expected}" | tee -a "${LOG}"
    RESULTS+=("FAIL ${name} (got ${got}, want ${expected})")
    FAIL=$((FAIL+1))
  else
    echo "[PASS] ${name}: ${got} simple systems" | tee -a "${LOG}"
    RESULTS+=("PASS ${name} (${got})")
    PASS=$((PASS+1))
  fi
  rm -f "${out}"
}

# run_ex4_completion FILE TIMEOUT — ex4_hydrogen pass = completes without error.
#   PASS iff exit 0 AND HYDROGEN_THOMAS_DONE present. The cell count is printed
#   as INFO only (see header: no Maple ground-truth, count may legitimately
#   differ from the old buggy 29).
run_ex4_completion() {
  local file="$1" tmo="$2"
  local name="ex4_hydrogen"
  local path="${EXP_DIR}/${file}"
  local out
  out="$(mktemp)"
  echo ""                                                  | tee -a "${LOG}"
  echo "=================================================" | tee -a "${LOG}"
  echo "RUN ${name}  (${file}, expect: completes w/o error)" | tee -a "${LOG}"
  echo "=================================================" | tee -a "${LOG}"

  timeout "${tmo}" go run . "${path}" 2>&1 | tee -a "${LOG}" | tee "${out}"
  local rc=${PIPESTATUS[0]}

  local got
  got="$(count_of ex4 "${out}")"
  local done_marker=0
  grep -q 'HYDROGEN_THOMAS_DONE' "${out}" && done_marker=1

  if [ "${rc}" -ne 0 ]; then
    echo "[FAIL] ${name}: openmaple exited ${rc}" | tee -a "${LOG}"
    RESULTS+=("FAIL ${name} (exit ${rc})")
    FAIL=$((FAIL+1))
  elif [ "${done_marker}" -ne 1 ]; then
    echo "[FAIL] ${name}: no HYDROGEN_THOMAS_DONE marker (did not complete)" | tee -a "${LOG}"
    RESULTS+=("FAIL ${name} (no DONE marker)")
    FAIL=$((FAIL+1))
  else
    echo "[PASS] ${name}: completed without error (INFO: ${got} simple systems)" | tee -a "${LOG}"
    RESULTS+=("PASS ${name} (completed; INFO count=${got})")
    PASS=$((PASS+1))
  fi
  rm -f "${out}"
}

# count_of KIND FILE — extract the simple-system count from captured output.
count_of() {
  local kind="$1" f="$2"
  case "${kind}" in
    ex1)  # "number of simple systems: N"
      awk -F': *' '/number of simple systems:/ {n=$2} END {print n+0}' "${f}" ;;
    ex2)  # count the "=== label : N simple system(s) ===" blocks
      grep -cE '=== .* : [0-9]+ simple system\(s\) ===' "${f}" ;;
    ex3)  # "1D ODE part: N simple systems in ..."
      awk '/1D ODE part:/ {print $4; exit}' "${f}" ;;
    ex4)  # "HYDROGEN_THOMAS_DONE  N simple systems in ..."
      awk '/HYDROGEN_THOMAS_DONE/ {print $2; exit}' "${f}" ;;
    *) echo 0 ;;
  esac
}

# ---- ex1b smoke (no count assertion, just that it runs clean) ---------------
echo ""                                            | tee -a "${LOG}"
echo "SMOKE ex1b_discover (accessor/typing probe)" | tee -a "${LOG}"
if timeout 200 go run . "${EXP_DIR}/ex1b_discover.mpl" 2>&1 | tee -a "${LOG}" >/dev/null; then
  echo "[PASS] ex1b_discover smoke (ran clean)" | tee -a "${LOG}"
  RESULTS+=("PASS ex1b_discover (smoke)")
  PASS=$((PASS+1))
else
  echo "[FAIL] ex1b_discover smoke" | tee -a "${LOG}"
  RESULTS+=("FAIL ex1b_discover (smoke)")
  FAIL=$((FAIL+1))
fi

# ---- counted examples -------------------------------------------------------
run_example "ex1_singular_ode" "ex1_singular_ode.mpl" ex1 2  300
run_example "ex2_params"       "ex2_params.mpl"        ex2 3  300
run_example "ex3_ode1d"        "ex3_ode1d.mpl"         ex3 13 600

if [ "${QUICK}" -eq 0 ]; then
  # ex4 is the ~30-minute hydrogen ansatz. Generous timeout (45 min).
  # Pass = completes without error (HYDROGEN_THOMAS_DONE + exit 0); count is INFO.
  run_ex4_completion "ex4_hydrogen.mpl" 2700
else
  echo "" | tee -a "${LOG}"
  echo "(--quick: skipping ex4_hydrogen)" | tee -a "${LOG}"
fi

# ---- summary ----------------------------------------------------------------
echo ""                                            | tee -a "${LOG}"
echo "================ SUMMARY ================="   | tee -a "${LOG}"
for r in "${RESULTS[@]}"; do
  echo "  ${r}"                                     | tee -a "${LOG}"
done
echo "  ${PASS} passed, ${FAIL} failed"             | tee -a "${LOG}"
echo "=========================================="   | tee -a "${LOG}"

[ "${FAIL}" -eq 0 ]
