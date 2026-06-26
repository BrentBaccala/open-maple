# open-maple design decisions (ADR ledger)

Append-only record of non-trivial architectural choices. Newest last. Each
entry: context, the decision, consequences, and the evidence that drove it.

---

## ADR-001 — Answer comparison and type predicates on the Sage side to keep big polynomials as refs

**Status:** Accepted. Equality / is-zero implemented (commit `a3ab695`); type
predicates implemented (commit `873baeb`). `canonicalKey` ordering and `subs`
remain scoped follow-ups (see Consequences).

### Context

A `SageRef` (`sage_cas.go`) stands in for a polynomial/rational expression kept
Sage-side behind an integer handle. Arithmetic preserves refs — `arithAdd`/`Mul`/
`neg`/`pow` route ref operands to `casArith`, so a chain of `+ - * ^` flows
server-side without ever serializing the expression (`op_add`/`op_sub`/… in
`sage_server.py`). The ref only collapses to a native Go AST when some code must
**inspect** its internals: `materialize()` (via `concrete()`) is the single
chokepoint, called by printing, comparison, `nops`/`op`/`type`, and the native
poly layer. Once materialized, `encodeArg`'s default branch ships the value as a
`{"poly": <string>}` on **every** subsequent Sage op — so one inspection turns a
server-side handle into a multi-MB string that re-crosses the wire repeatedly.

This surfaced on the hydrogen-ansatz cell-1 + Schrödinger-PDE reduction. After
the O(N²) sum-fold fix (`evalAddChain`, ADR background / commit `a356de2`), the
run reached a 21.5 MB intermediate and died on `error: sage call timed out after
2m0s (op=lcoeff)` — `lcoeff` re-marshalling 21.5 MB. The value was a native AST,
not a ref (`encodeArg` default `{"poly"}` path; the Sage side measured
`[indets-scan] 21562279 chars` of received *string*). So the question was *what
collapsed the ref*.

`OPENMAPLE_REF_TRACE` (commit `7186521`) logs every `materialize()` with the Go
call chain that forced it. On the cell-1 run, the 134 materializations broke down:

| Trigger | Count | Share | Nature |
|---|---|---|---|
| `compareValues ← equalValues ← truth` | 90 | 67% | equation `a=b` → boolean in `and`/`or`/`if` |
| `checkTypeValue ← biType` | 27 | 20% | `type(p, T)` test |
| `printPrec ← canonicalKey` | 12 | 9% | canonical key (equality/hashing for sets/sort) |
| `substitute ← substList` | 5 | 4% | `subs(eqs, p)` |

These inspections alone dragged **172.8 MB** across the wire (199 events) *before*
the run even reached the 21.5 MB poly. Cross-checking the DT source: **132**
`=0`/`<>0` conditions — the equality predicate is overwhelmingly **is-zero** — and
the `type(…)` names applied to *polynomials* are only the math ones (`polynom`,
`numeric`, `integer`); every structural type (`table`, `list`, `function`,
`Vector`, …) is tested on DT's data structures, which are never refs.

### Decision

**Simple comparison tests and type extraction are answered Sage-side on the ref,
never by materializing.** A predicate that returns a bool/type does not need the
polynomial in Go.

- **Equality / is-zero (~76%, done):** `refEqual` short-circuits `equalValues`
  when an operand is a live `SageRef`, calling the server-side `equal` / `is_zero`
  op (`op_is_zero`). It resolves ref args from the cache (`decode_arg`'s `{"ref"}`
  path) and returns `{"bool": …}`. Equality is `a − b == 0` in the fraction field,
  which matches `compareValues`' expanded-normal-form polynomial equality exactly.
  Any non-live-ref case / server error / non-bool result falls back to the
  materializing structural compare, so correctness is unchanged
  (`Sage+VERIFY_NATIVE` green; `TestSageRefEquality` pins that `refsMaterialized`
  stays put).

- **Type predicates (~20%, follow-up):** add a server-side `type(ref, T)` for the
  math types (`polynom`/`numeric`/`integer`/`constant`). `checkTypeValue` should
  consult it before `concrete()`. Structural type names won't reach this path
  (they're tested on non-poly values), so only the math predicates need wiring.

### Consequences

- Keeping the equality predicate off-wire cascades: a poly that stays a ref also
  passes `{"ref":N}` to the *legitimate* transforms that touch it next (`normal`,
  `numer`, `lcoeff`), instead of each re-stringifying it — which is what the
  21.5 MB events showed.
- Trade-off: a live ref compared N times now does N small round-trips instead of
  one materialize + N native compares. For big polys this wins decisively (no body
  crosses the wire); for small polys the extra round-trips are negligible, and
  once a ref is materialized by any path `refEqual` no longer fires.
- **Open follow-ups:** (1) the math `type` predicate above; (2) `canonicalKey`
  (9%) is a total-order key, not a boolean — harder to push server-side; where it
  is used purely for set membership it can call `equal`, but as a sort key it is a
  larger job; (3) `subs` (4%) is a *transform*, not a predicate — a server-side
  `subs(ref) → ref` would keep it off-wire but is separate work.

### References

- Commits: `a356de2` (evalAddChain O(N²) fix, the prerequisite), `7186521`
  (`OPENMAPLE_REF_TRACE` / `OPENMAPLE_MATH_TRACE`), `a3ab695` (this decision's
  equality implementation).
- Diagnose with `OPENMAPLE_REF_TRACE=1` (materialize sites + large poly-arg
  shipments) and `OPENMAPLE_MATH_TRACE=1` (per-op wire traffic: `ref:N` vs
  `poly:bytes`).
