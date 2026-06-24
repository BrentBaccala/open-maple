# open-maple — DifferentialThomas bring-up status

`~/open-maple` is a Go interpreter for a subset of the Maple language. Its load-
bearing use case is running Lange-Hegermann's LGPL **DifferentialThomas** package
(`~/DifferentialThomas/src`, untouched) to compute Thomas decompositions of
differential systems, delegating the computer-algebra primitives to a long-lived
**Sage** subprocess (`cas/sage_server.py`, `OPENMAPLE_CAS=sage`).

This file is the high-level map: what works, how it's verified, and where the
remaining frontiers are. Per-task detail lives in `~/project/reports/open-maple-*`.

## Running the example programs

`openmaple <file.mpl>` runs a Maple program directly. The `~/thomas-experiments/`
examples run end to end — using the **public** API (`with(DifferentialThomas)`,
`Ranking`, `ThomasDecomposition`, `Equations`, `Inequations`) and **differential
notation** input (`diff(u(x),x)`):

- `ex1_singular_ode` — `(u')^2 = 4u` → 2 simple systems
- `ex1b_discover`    — accessor/typing probes (whattype, op, lastexception[2..])
- `ex2_params`       — 3 parts incl. the parametric `a=0` vs `a<>0` split
- `ex3_ode1d`        — 4 jets + 7 params 1D ODE ansatz → 13 simple systems
- `ex4_hydrogen`     — the JOCA-paper hydrogen ansatz (3 ivar, 5 jets, 10 params,
  39 eqs), ~18–30 min wall. The largest system run to date. Its pass criterion is
  **completion without error** (`HYDROGEN_THOMAS_DONE` + exit 0), **not** a
  specific cell count — see the content/primpart-rational note below. It `save`s
  its result to `hydrogen_thomas_result.m` (reloads via `read` in ~0.4 s, vs the
  recompute).

## content / primpart over rational operands (361fbae root cause)

`content` and `primpart` are extended to **rational functions** in Sage's
`cas/sage_server.py`, matching Maple's documented multiplicative rule
(`~/open-maple/maple-help/content.md`): for `f` in {content, primpart},
`f(n/d, V) = f(n, V) / f(d, V)`. Previously both ops force-coerced their operand
into a polynomial ring and raised `primpart: fraction must have unit denominator`
on any genuine fraction.

The crash surfaced via ex4_hydrogen and was bisected to commit **361fbae**
("parenthesize a power's base when serializing for Sage"):

- DT forms the reciprocal `((b1^3*x^2*z)^1)^-1`, takes its `denom`, and calls
  `primpart` on the resulting rational function `(big num)·(x^2·z)^-1`.
- **Before 361fbae** the Go side emitted `(b1^3*x^2*z)^1^-1`. Sage's `^` is
  right-associative, so that parsed as `X^(1^-1)` = `X^1` = X — a *polynomial*
  (the reciprocal silently collapsed because `1^-1 = 1`). primpart only ever saw
  polynomials, so the old ex4 ran (and its old "29" cells were computed from this
  **wrong** intermediate — X instead of 1/X).
- **361fbae** correctly emits `((X)^1)^-1` = `X^-1` = 1/X — a genuine fraction.
  This is mathematically correct and is **not** reverted. It exposed the real,
  latent gap that this fix closes.

Because the old "29" came from a buggy intermediate and there is **no Maple
ground-truth output** to compare against, ex4's regression check is now
"completes without error", and the corrected cell count may legitimately differ.

Fix (in `cas/sage_server.py`): `decode_allow_frac` parses the operand over the
fraction field so a fraction survives (demoting unit-denominator values back to
the polynomial ring, and lifting bare scalars so the `degree(primpart→1)` fix
still holds). `op_content`/`op_primpart` keep the existing polynomial path
unchanged and add a rational branch applying the multiplicative rule to the
numerator and denominator separately via the existing `_content`/`_content_wrt`
helpers (sign carried by the content, per Maple). Tested by
`cas/test_content_primpart.py`.

Reaching the ex[123] set surfaced a string of latent interpreter bugs (see the
§"latent bugs" list below and the git log): indets dropping function/derivative
terms, function-head and index-head name resolution, a leaking seq loop
variable, and lexical-scope-vs-global resolution. The hydrogen example surfaced
the `evala`-on-a-huge-polynomial crash (see §"the hydrogen wall" below) rather
than a correctness bug — the engine itself produced the right decomposition.

## What works (end-to-end, pinned by regression tests)

The decomposition engine handles every **standard-ranking** system tried, across
a wide range of shapes. Each is a `*_test.go` pinned against its exact pretty-
printed output (sage-gated):

| System | Ranking | Notes |
|---|---|---|
| readme smoke `[u[1,0]-u[0,0], u[0,1]-u[0,0]^2]` | DegRevLex | `[[u(x, y) = 0]]` |
| single eq `u_x - u = 0` | DegRevLex | first-order leader (inert derivative) |
| Cauchy–Riemann (2 dvar) | DegRevLex | sum-of-squares → 2-equation system |
| 3-ivar / 3-dvar Overview | DegRevLex | 4 components; ~14 s |
| Reduce worksheet | **EliminateFunction** | 1st case with **inequations** |
| heat, Laplace, wave | DegRevLex | 2nd-order in x and/or t |
| Burgers | DegRevLex | nonlinear |
| factoring `u_x^2 - u` | DegRevLex | multi-component split + inequation |
| overdetermined, two-eq split | DegRevLex | |
| hydrogen ansatz (JOCA paper) | DegRevLex | 3 ivar, 5 jets, 10 params, 39 eqs → 29 components; the largest, ~18 min |

Default suite (no Sage): ~50 tests pass (sage-gated tests skip). Full Sage suite:
green, and clean under `OPENMAPLE_VERIFY_NATIVE=1`.

## How correctness is guaranteed: the verify harness

`OPENMAPLE_VERIFY_NATIVE=1` (`Interp.verifyNative`) runs **both** the native
implementation and Sage on every native CA op and asserts they agree (tolerant
of inputs where Sage itself errors but native is correct, e.g. degree of a
constant). The full Sage suite passes clean under it — so native ≡ Sage in value
on every call the corpus exercises. This harness has caught every native bug and
several latent interpreter bugs (see below). Run it after any change touching
the interpreter or the native layer.

`OPENMAPLE_SAGE_TRACE=1` logs each Sage round-trip (op, vars, args) — used to find
hot ops and to tell an interpreter-bound computation from a CAS-bound one.

`OPENMAPLE_TRACE_PROCS=1` prints the Maple-level proc call chain on error.

## Independent decomposition verification (`verify/`)

A second, engine-independent harness (`verify/`, uses only Sage Gröbner/ideal/
saturation, never DT's reducer) certifies a *computed decomposition* is sound,
disjoint, square-free, passive, and **complete**. For the hydrogen 29-cell result
the full verdict is now **A** triangular 29/29, **B** soundness 29/29, **C**
disjoint 406/406, **E** passive 29/29, square-free 29/29, **F** all 412 pruned
branches certified empty, and **D′ cover** PASS — the 29 cells + 412 empty
branches provably tile the whole parameter space (`⋃V(cells)=V(input)`), via
split-exhaustiveness: every branch comes from one of seven known DT split
operators (six tautological `p=0 ⊔ p≠0`, the `Factorize` equation split
variety-checked `V(q)=V(fak1·fak2)`). So the 29-cell decomposition is the
canonical Thomas decomposition for the ranking; Maple's ~70–80 is a DT
version/granularity difference, not missing solutions. See
`~/project/reports/open-maple-decomposition-verification.md` (Addenda 2–3) and
`verify/README.md`.

## Latent bugs found and fixed (all were silent corruption, not crashes)

Running real DT systems + the verify harness surfaced a string of pre-existing
interpreter bugs, each fixed with a pinned test:

- **`-b^2` parsed as `(-b)^2`** — prefix +/- bound tighter than `^`. Flipped signs
  whenever a Sage result led with a negative power term.
- **`(a->a[0])(c1)` returned `a[0]`** — a bound name used as an index *head* wasn't
  resolved; DT's SubstituteDVar leaked a phantom jet variable into the polynomials.
- **`diff(f, x, x)` dropped all but the first variable** — every 2nd-or-higher-order
  derivative pretty-printed at the wrong order (heat → u_x instead of u_xx).
- Earlier: product/sum index binding, inert-derivative re-eval loop, polynom-object
  type gate, list-element table assign, index-assign auto-viv, `-1` print fold.

## Performance: the native polynomial layer

DT calls cheap polynomial primitives in tight loops; each Sage round-trip costs a
full JSON + parse + re-eval. `native_poly.go` computes the cheap ops directly on
the Value AST (an expanded monomial→QQ-coeff normal form), reserving Sage for the
hard ones:

- **native**: degree, indets, expand, **evala**, coeff, content, gcd (univariate +
  integer-constant), numer/denom & normal/simplify (scalar); `toPolyNF` also
  evaluates constant powers (`2^-1`) so division-form inputs stay native.
  `evala` is native because, with no algebraic numbers (`RootOf`) in play — the
  only domain this port supports — `evala(p)` is just expand-to-standard-form;
  DT calls it as `evala(expand(...))` and `evala(StandardForm(p)/c)`, both
  polynomial. (A genuine rational function / `RootOf` input still falls to Sage.)
- **order-independent polynomial equality** (`compareValues` via normal form) — the
  key enabler that made native expand/coeff safe regardless of term order
- This took the 3-ivar/3-dvar system from a 240 s timeout to ~14 s, and cut its
  Sage round-trips from ~592 to ~363 (content ~198, gcd ~31 univariate).

Native results carry NO term-ordering risk for equality (order-independent) and
reconstruct expand output in descending total degree to match Sage's printed
surface (which feeds DT's FactorSorter).

## The hydrogen wall: a Sage-parser crash, fixed

The hydrogen ansatz crashed after ~7 min with `sage evala: maximum recursion
depth exceeded during compilation`. Root cause: DT calls `evala(expand(...))` on
a **fully expanded** polynomial; before native `evala`, that huge flat
term-string (thousands of `+`-joined terms) round-tripped to Sage, where the
string was parsed via `sage_eval` → CPython `compile()`, whose bytecode compiler
recurses once per AST node and overflowed its default 1000-deep limit just
*parsing* the sum (reproduced synthetically at ~3000 terms). Two fixes:

1. **Native `evala`** removes the round-trip entirely (see the native layer
   above) — the giant string never reaches Sage. This is the real fix.
2. **Backstop in `cas/sage_server.py`**: `sys.setrecursionlimit(100000)` on the
   main thread, so the ops that *genuinely* need Sage on a big expression
   (`normal`/`numer`/`denom` of a large rational function) don't hit the same
   wall. It must stay on the main thread — running the server loop off-thread
   (the textbook deep-stack workaround) **segfaults Sage's cysignals SIGSEGV
   handling**. Probed safe to 30000-term sums on the default stack.

## Worksheet result persistence: save / read

The hydrogen worksheet `save`s its 18-min result so it never recomputes. `save`,
`read`, `currentdir`, and `time` were stubs/no-ops; they now work:

- `save NAME, ..., "file"` writes each name as a re-readable `NAME := value:`
  assignment (text form, same surface as `%a`/`print`). The `.m` extension is
  Maple's *internal* binary format; we use the text form unconditionally since
  this port both writes and reads the file. Reloads via `read` in ~0.4 s.
- `read "file"` executes the file's statements in the current scope.
- `currentdir([dir])` returns the cwd (was an inert symbol); optional chdir.
- `time()` returns real CPU seconds via `getrusage` (was a `0.0` stub), so the
  worksheets' `time() - st` elapsed prints are meaningful.

Pinned by `save_read_test.go`.

## CAS expression handles (refs): cutting the string round-trips

The Go↔Sage bridge was fully **stateless**: every request shipped the whole
expression as a string (`{"poly": "<entire string>"}`), Sage re-parsed it,
computed, and returned another full string that Go re-parsed. For the combined
hydrogen system (no end reduction) those strings reach ~200 KB / ~47k terms —
the direct cause of the astfold_expr C-stack SIGSEGV (commits d31b89e, 9e54eed).

### Combined-run astfold crash #2: deep MULTIPLICATIVE chains

The combined hydrogen run (`ex4_hydrogen_combined.mpl`, no end reduction) still
crashed in `astfold_expr` ~39 min in (24958 stacked frames, all at the same call
site = a single flat left-deep chain). The original `rebalance` only balanced
**additive** (`+`/`-`) chains: it split a sum into terms and recursed into
parentheses, but left each term's top-level **multiplicative** chain flat. The
combined system's pseudo-remainders (`op=prem`) and `degree`/`coeff` operands
include single monomials that are flat products of thousands of factors
(`x*x*…*x`); a flat `a*b*c*…` compiles to a left-deep `BinOp(Mult)` AST that
overflows `compile()`/astfold exactly like a flat sum. Confirmed against the real
parse path: a 30000-factor product `RecursionError`s during compilation (off the
main thread / under cysignals this surfaces as the astfold SIGSEGV).

Fix (this session): `rebalance` now also balances multiplicative chains.
`_split_top_multiplicative` splits each additive term into factors at top
paren-depth, keeping `^` glued to its base (`x^4` is one factor); `_balanced_mult`
reassociates maximal runs of `*`-connected factors into a balanced binary tree
(exact over QQ — multiplication is associative). Division is left-associative and
shares `*`'s precedence, so a `/` is *never* swallowed into a balanced group:
`a/b*c` stays `(a/b)*c`, `a/b/c` stays `(a/b)/c`. Value-preservation is fuzzed
(500 random exprs with powers/signs/division) and pinned by `test_rebalance.py`
(30000-factor product parses; mult/div samples are value-preserving). The
combined frontier still has other open issues — this fix only removes the
deep-AST overflow.

### Combined-run wall #3: `_back_to_R` result coercion (task 428)

After the astfold fixes, the combined run hit the Go-side 120 s per-call timeout
on `op=prem`. DIAGNOSIS (task 428, `cas/bench_prem.py` on the captured failing
operand `cas/fixtures/prem_combined_hydrogen.json`): the wall was NEITHER parse
nor compute. On that 56 KB operand —

    parse  (decode_arg + coerce into Frac(other)[x]) :   1.1 s
    compute (pseudo_quo_rem)                          :   0.3 s
    _back_to_R (coerce result into R)                 : 432.3 s   <-- the wall

So caching the operands (refs) would not have helped, and the algorithm is fine.
The bottleneck was result coercion. `op_prem` divides in `Frac(QQ[other])[x]`;
the `d^e` pseudo-division blowup makes the remainder numerator a ~2.9 MB / 47k-
term polynomial. `_back_to_R` tried `R(e)` first, which raises `RecursionError`
during `compile()` (the SAME deep-AST issue `rebalance` fixes for *parse*, but on
the *result*), then fell through to `R(SR(str(e)))` — a 2.9 MB-string → symbolic-
ring → coerce round trip that took >400 s.

Fix: `_back_to_R(R, e, x)` now rebuilds the multivariate element STRUCTURALLY
from the univariate-in-x coefficient list (`_back_to_R_struct`): for each
`coeff_i` (a `FractionFieldElement` over `QQ[other]`), coerce its numerator and
denominator into R via libSingular and sum `coeff_i * x^i`. No str()/SR round
trip, no `compile()`. The big operand now coerces in **0.36 s** (≈1200×), and the
structural result is bit-equal to the old SR path on every mid-size captured
operand (`test_back_to_R.py`). `op_rem`/`op_quo`/`op_pquo` were updated to pass
`x` too; genuinely-rational coefficients fall back to `Frac(R)` (covered by
`test_back_to_R.test_rational_coefficient_via_rem`). The 2-arg paths (no main
var) keep the old behavior.

This removes the combined run's prem wall — the frontier is now bounded by the
*intrinsic* size of the no-end-reduction intermediates (2.9 MB prem results that
downstream `factor`/`denom`/`degree` must then chew through), not by a bridge
inefficiency. Refs caching (would have addressed a parse wall) and the
division-chain rebalance extension were therefore NOT needed for this wall and
were left for if/when a future operand is shown to be parse-bound.

**Refs** are an optimization layered on top of the string protocol (correctness
identical with refs off — disable via `OPENMAPLE_DISABLE_REFS=1`, a bisection
switch like `OPENMAPLE_DISABLE_NATIVE`):

- A poly/rational result is kept Sage-side in a cache and returned as an opaque
  `{"ref": N}` handle (the Go client sets `want_ref` on every request). The
  giant result string is materialized only on demand.
- A handle fed back as an op argument is sent as `{"ref": N}` and resolved from
  the cache — the string never crosses the wire. So a chain of Sage→Sage ops
  (arithmetic/simplify/factor flowing into each other) avoids serialize+parse on
  both ends.
- **Lazy materialization** (Go side, `SageRef` in `sage_cas.go`): any Go code
  that must look inside the expression — printing, equality/`compareValues`,
  `op`/`nops`/`whattype`/`type`, `subs`/`map`, arithmetic, `save`, the native
  poly layer — calls `concrete()`, which does a single `materialize` round-trip
  and caches the result on the ref (fetched at most once). DT inspects structure
  frequently, so the win is concentrated in the arithmetic/simplify chains that
  flow Sage→Sage without Go peeking; that is expected and fine.
- **SR results are never refs.** The diff/expand/simplify symbolic-ring fallback
  paths return strings, because an SR expression's str() carries unsanitized
  function heads (`diff(a(x), x)`, `cos(phi)`) that would break sanitization if
  cached and fed back. Only genuine polynomial/rational ring elements become
  refs (`enc_poly` / `_is_symbolic_ring_elt` in `sage_server.py`).

**Where `clear` is emitted.** The cache has **no automatic eviction**, so it
must be dropped at a coarse boundary. There is no Go-visible per-cell seam inside
DT's decomposition (the cells are produced by the LGPL Maple source running
through the interpreter), so the clear fires at the **end of each top-level
program statement** (`Interp.ExecProgram` in `interp.go`, used by `runFile`).
Before each clear, every `SageRef` still reachable from the globals and the ditto
history is **deep-materialized** (`materializeLiveRefs`/`materializeDeep`), so a
handle that survives the statement already holds its concrete value in Go and
clearing the server-side cache can never strand it. (A subsequent `encodeArg` of
an already-materialized ref sends its string, not the now-stale handle.)

**Reading the logs.**
- `[ref-coerce-fallback] op=… ref=N from=<parent ring> to=<target ring> err=…`
  (stderr): a ref's fast-path `R(obj)` coercion into the consuming op's ring
  failed and we fell back to the string/rebalance path. Sage's by-name coercion
  turns out to be very robust, so in practice this rarely fires — refs fit every
  ring regime tried (poly↔frac, subset/superset vars, Frac-coefficient rings).
  A `[ref-coerce-fallback] total=N by-op=…` summary prints at each `clear` and at
  process exit.
- `[ref-stats] issued=… materialized=… ref-args=… poly-args=…` (stderr, end of
  each `openmaple` program): how many result handles were issued vs how many had
  to be materialized, and the `{"ref"}` vs `{"poly"}` arg split on the wire — the
  measure of how much string traffic refs removed.

**Tests.** `cas/test_refs.py` (run under `sage -python`) covers ref round-trip,
cross-ring consumption, the coercion fallback path, clear (whole + by-id), and
the unknown-ref hard error. The Go side is covered by the full sage-gated suite
(green with refs on) and the example-suite runner below.

## Example-suite regression runner

`tests/run_examples.sh` is the canonical regression suite for the
`~/thomas-experiments` examples (there is no formal Go test for the full
end-to-end decompositions). It runs each example through `openmaple` on the Sage
backend, asserts the expected simple-system count, prints per-example PASS/FAIL +
a summary, exits nonzero on any failure, and streams to a log (each example via
`tee` so the task-runner stream watchdog stays alive during ex4's ~30 min run):

```
tests/run_examples.sh            # ex1b smoke + ex1..ex4 (ex4 ~30+ min)
tests/run_examples.sh --quick    # ex1b + ex1..ex3 only (skip ex4)
tests/run_examples.sh --log FILE # stream to FILE (default: $TASK_LIVE_LOG or temp)
```

Expected: ex1_singular_ode → 2, ex2_params → 3 labeled parts, ex3_ode1d → 13,
ex4_hydrogen → 29. ex1b_discover is a typing/accessor probe (smoke run, no count).

## Remaining frontiers (characterized, none a clear quick win)

1. **High-order / large systems are interpreter-CPU-bound, not CAS-bound.** Now
   that native `evala` removed the last big Sage round-trip, the **hydrogen**
   case (18 min, ~2040 s CPU) is the canonical example: its time is the Go
   interpreter executing DT's prolongation logic, not the CAS. Same story for the
   Juri–Vladimir example (`u[1,1,3]-u[4,0,0], u[5,1,0]-u[0,4,0], u[0,6,0],
   u[4,2,0]`, 3 ivar), which makes only ~92 Sage calls in 65 s. A CPU profile of
   the 3-var shows allocation/GC (`mallocgc`, `scanobject`) and the Sage-Call
   encode/decode path as the top consumers. Speeding this up means **reducing
   interpreter allocations** (open-ended Go perf work), not faster CAS.

2. **Composite `numer`/`denom`/`normal`, `factors`, and multivariate `gcd` still
   round-trip to Sage.** `content` and the univariate / integer-constant slice of
   `gcd` are now native. What remains: (a) `numer`/`denom`/`normal` of a *rational
   function* (fraction of polynomials) — would need rational-function support
   (polynomial division + gcd of numerator/denominator, building on the native
   univariate gcd); (b) genuinely **multivariate** gcd — needs a recursive
   content/primitive-part algorithm; (c) `factors` — factoring, effectively
   Sage-bound (`evala` is now native for the no-`RootOf` case). The 3-var's top
   remaining ops are
   denom/factors/normal/indets/numer (~50–80 each). All verify-checkable but each
   is its own chunk; none would unlock the interpreter-bound high-order case.

3. **Matrix / LinearAlgebra rankings are unimplemented** — block dvar lists
   (`[[u,v],[w]]`) and custom `"Matrix"=A` rankings need the full Maple linear-
   algebra subsystem (`Matrix(n,m,fill)`, the `<A|B>` builder, `LinearAlgebra`
   ops). A distinct, large workstream; the standard rankings (DegRevLex,
   EliminateFunction) cover the common case. Deliberately deferred.

## Build / test

```bash
export PATH=~/.local/go-toolchain/go/bin:$PATH GOPATH=~/.local/gopath GOFLAGS=-mod=mod
cd ~/open-maple/src
go test ./...                                 # default suite (no Sage), ~50 tests
OPENMAPLE_CAS=sage go test ./...              # full suite through the Sage backend
OPENMAPLE_CAS=sage OPENMAPLE_VERIFY_NATIVE=1 go test ./...   # native ≡ Sage check
```
