# Independent decomposition verifier (`open-maple/verify`)

A Sage verification harness that checks a computed **differential Thomas
decomposition** for soundness, disjointness, completeness, structural validity,
and passivity — **independently of the open-maple / DifferentialThomas engine**.

The whole point is independence: every reduction / membership test uses Sage's
own polynomial machinery (Gröbner bases, ideal membership, saturation). We never
call DT's reducer to perform a check (that would be circular). open-maple is used
only *upstream*, to produce the decomposition data (`hydrogen_thomas_result.m`).

## What it certifies

| Check | What it proves | Cost |
|-------|----------------|------|
| **A** | Each cell is a valid **triangular set** — distinct leaders under the ranking, no identically-zero initial/separant | cheap (structural) |
| **B** | **Soundness**: each input equation reduces to 0 modulo each cell's equations + prolongations (so `Sol(cell) ⊆ Sol(input)`); each input inequation is a unit on the cell; each cell is **non-vacuous** (consistent) | heavy (Gröbner per cell) |
| **C** | **Disjointness**: every pair of cells shares no solution (`1 ∈` saturated combined ideal) | moderate (one saturation per pair) |
| **D** | **Cover / completeness** (bounded prolongation): `V(input_N) ⊆ ⋃ V(cell_i)` — settles whether a branch was dropped. Tractable only for small systems; reports INTRACTABLE and defers to E on large ones | exponential in falsification combinations |
| **E** | **Per-cell passivity**: Δ-polynomial integrability conditions reduce to 0 mod the (prolonged) cell — the smoking-gun check for a premature-finish / missing-prolongation bug | heavy |
| **F** | **Inconsistency certificates**: certifies that every branch the engine *pruned* (set `Inconsistent`) is genuinely empty — a rejection whose certificate fails is a wrongly-pruned non-empty branch | cheap, per record |

## Representation

Jet variables `name[i,j,k]` are flattened to algebraic indeterminates
`name__i_j_k`. Everything lives in a single ring `QQ[x, y, z, <jets>]` with
DegRevLex order; the independent variables `x,y,z` are ordinary ring variables,
and the fact that they are *units in the coefficient field* (the basis for content
removal) is modelled by **saturating every ideal by the product of the ivars** in
addition to the cell's inequations.

## Files

- `maple_parse.py` — parse the `EI := [[eqs,ineqs],...]` result file and jet
  notation (pure Python, no Sage).
- `build_input.py` — re-derive each example's **input** system in jet notation,
  independent of the engine (differentiation = jet-index raising, done in sympy).
- `known_cells.py` — hand-checked reference decompositions for the small systems
  (used to validate the harness before trusting it on hydrogen).
- `verify_core.py` — the Sage primitives (ring construction, ranking/leader/
  initial/separant, saturation/membership, prolongation).
- `run_verify.py` — entry point; runs A–E and prints PASS/FAIL with witnesses.
- `check_f.py` — reads an OMRI_RECORD log and certifies each rejection empty.
- `make_instrumented.sh` + `dt-instr.patch` — build the instrumented DT source
  copy at `/tmp/dt-instr` (the canonical `~/DifferentialThomas/src` stays
  untouched, per the LGPL policy).
- `record_inconsistent.sh` — run a `.mpl` through the instrumented source and
  capture the `OMRI_RECORD` lines (check F instrumentation; driven by
  `OPENMAPLE_RECORD_INCONSISTENT`).

## Running

```bash
export PATH=~/miniforge3/envs/sage/bin:$PATH

# Small known-correct systems (validate the harness — must all PASS):
sage -python run_verify.py alg_xu2        --checks ABCDE
sage -python run_verify.py alg_factored   --checks ABCDE
sage -python run_verify.py readme_smoke   --checks ABCDE
sage -python run_verify.py cauchy_riemann --checks ABCDE

# Hydrogen (cells read from ~/open-maple/hydrogen_thomas_result.m):
sage -python run_verify.py hydrogen --checks A      # ~2 s
sage -python run_verify.py hydrogen --checks C      # ~150 s, 406 pairs
sage -python run_verify.py hydrogen --checks B      # heavy; per-cell
sage -python run_verify.py hydrogen --checks E

# Check F — record + certify the engine's inconsistency rejections:
verify/make_instrumented.sh
OPENMAPLE_RECORD_INCONSISTENT=/tmp/omri.log \
  verify/record_inconsistent.sh ~/thomas-experiments/ex4_hydrogen.mpl   # ~18 min
sage -python check_f.py /tmp/omri.log --ivars x,y,z
```

## Methodology

The harness is validated on decompositions with **known-correct counts** before
any hydrogen conclusion is drawn (it must PASS on `alg_xu2`→2, `alg_factored`→2,
`readme_smoke`→1, `cauchy_riemann`→1), and it is checked against **negative
controls** (a dropped branch must FAIL cover D; an overlapping pair must FAIL
disjointness C; a vacuous cell must FAIL non-vacuity) so it is not vacuously
passing.

## Note on the original spec

The prompt's "reverse soundness" check (every cell equation must be implied by the
input) is **not valid for a splitting decomposition** — a cell legitimately adds
branch constraints (e.g. the `{u=0}` cell of `x*u²-u` is not implied by the input
alone). It is replaced by a **non-vacuity** check (each cell must be consistent),
which is the genuine corruption guard; soundness `cell ⊆ input` is covered by the
forward direction of B.
