# open-maple — DifferentialThomas bring-up status

`~/open-maple` is a Go interpreter for a subset of the Maple language. Its load-
bearing use case is running Lange-Hegermann's LGPL **DifferentialThomas** package
(`~/DifferentialThomas/src`, untouched) to compute Thomas decompositions of
differential systems, delegating the computer-algebra primitives to a long-lived
**Sage** subprocess (`cas/sage_server.py`, `OPENMAPLE_CAS=sage`).

This file is the high-level map: what works, how it's verified, and where the
remaining frontiers are. Per-task detail lives in `~/project/reports/open-maple-*`.

## What works (end-to-end, pinned by regression tests)

The decomposition engine handles every **standard-ranking** system tried, across
a wide range of shapes. Each is a `*_test.go` pinned against its exact pretty-
printed output (sage-gated):

| System | Ranking | Notes |
|---|---|---|
| readme smoke `[u[1,0]-u[0,0], u[0,1]-u[0,0]^2]` | DegRevLex | `[[u(x, y) = 0]]` |
| single eq `u_x - u = 0` | DegRevLex | first-order leader (inert derivative) |
| Cauchy–Riemann (2 dvar) | DegRevLex | sum-of-squares → 2-equation system |
| 3-ivar / 3-dvar Overview | DegRevLex | 4 components; the largest, ~14 s |
| Reduce worksheet | **EliminateFunction** | 1st case with **inequations** |
| heat, Laplace, wave | DegRevLex | 2nd-order in x and/or t |
| Burgers | DegRevLex | nonlinear |
| factoring `u_x^2 - u` | DegRevLex | multi-component split + inequation |
| overdetermined, two-eq split | DegRevLex | |

Default suite (no Sage): 40 tests. Full Sage suite: green.

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

- **native**: degree, indets, expand, coeff, content, numer/denom & normal/simplify
  (scalar); `toPolyNF` also evaluates constant powers (`2^-1`) so division-form
  inputs stay native
- **order-independent polynomial equality** (`compareValues` via normal form) — the
  key enabler that made native expand/coeff safe regardless of term order
- This took the 3-ivar/3-dvar system from a 240 s timeout to ~14 s, and cut its
  Sage round-trips from ~592 to ~394 (content alone was ~198).

Native results carry NO term-ordering risk for equality (order-independent) and
reconstruct expand output in descending total degree to match Sage's printed
surface (which feeds DT's FactorSorter).

## Remaining frontiers (characterized, none a clear quick win)

1. **High-order systems are interpreter-CPU-bound, not CAS-bound.** The Juri–
   Vladimir example (`u[1,1,3]-u[4,0,0], u[5,1,0]-u[0,4,0], u[0,6,0], u[4,2,0]`,
   3 ivar) times out, but makes only ~92 Sage calls in 65 s — the cost is the Go
   interpreter executing DT's prolongation logic. A CPU profile of the 3-var shows
   allocation/GC (`mallocgc`, `scanobject`) and the Sage-Call encode/decode path as
   the top consumers. Speeding this up means **reducing interpreter allocations**
   (open-ended Go perf work), not faster CAS.

2. **`gcd` (and composite `numer`/`denom`/`normal`) still round-trip to Sage.**
   `content` is now native (it's just the coefficient gcd — no polynomial GCD).
   The rest all reduce to a real **multivariate polynomial GCD** over Q: `gcd`
   directly, and `numer`/`denom`/`normal` of a *rational function* (fraction of
   polynomials) via gcd of numerator/denominator. `factors` needs factoring
   (Sage-bound). After native content the 3-var's top remaining ops are denom/
   factors/normal/indets/numer/gcd (~40–80 each). A native multivariate GCD is
   the one substantial remaining polynomial-layer piece — high effort, verify-
   checkable; it would speed up systems that already complete but would NOT unlock
   the interpreter-bound high-order case.

3. **Matrix / LinearAlgebra rankings are unimplemented** — block dvar lists
   (`[[u,v],[w]]`) and custom `"Matrix"=A` rankings need the full Maple linear-
   algebra subsystem (`Matrix(n,m,fill)`, the `<A|B>` builder, `LinearAlgebra`
   ops). A distinct, large workstream; the standard rankings (DegRevLex,
   EliminateFunction) cover the common case. Deliberately deferred.

## Build / test

```bash
export PATH=~/.local/go-toolchain/go/bin:$PATH GOPATH=~/.local/gopath GOFLAGS=-mod=mod
cd ~/open-maple/src
go test ./...                                 # default suite (no Sage), 40 tests
OPENMAPLE_CAS=sage go test ./...              # full suite through the Sage backend
OPENMAPLE_CAS=sage OPENMAPLE_VERIFY_NATIVE=1 go test ./...   # native ≡ Sage check
```
