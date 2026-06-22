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
  39 eqs) → **29 simple systems**, ~18 min wall. The largest system run to date.
  It `save`s its result to `hydrogen_thomas_result.m` (reloads via `read` in
  ~0.4 s, vs the 18-min recompute).

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
