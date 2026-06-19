package main

import (
	"math/big"
	"sort"
	"strings"
)

// Value is the Maple runtime value model. Every evaluated expression is a
// Value. The concrete types model Maple's data structures:
//
//   Integer  - arbitrary precision integer (big.Int)
//   Rational - exact rational (big.Rat) — always kept normalised, never integral
//   Float    - hardware float (Maple's floating-point; rarely used in DT)
//   MString  - "..." string
//   Name     - a symbol / name (incl. backtick-quoted names; just a string)
//   Boolean  - true / false / FAIL
//   Seq      - expression sequence (auto-flattening); NULL == empty Seq
//   List     - [ ... ]
//   Set      - { ... } (sorted + deduplicated)
//   Table    - Maple table (the core data model)
//   Proc     - a procedure value
//   Func     - an unevaluated function application f(args) kept as data
//   Indexed  - an unevaluated indexed name a[i,j] kept as data (jet variables)
//   Sum/Prod/Power - symbolic arithmetic kept as data when operands are not
//                    fully numeric (so op/nops/printing work without a CAS)
//   Range    - a..b
//   Equation - a = b (relation kept as data, e.g. "Matrix"=A)
//   Relation - other relations kept as data (<, <=, <>, in, ...)
//
// Anything the CAS would normally simplify is represented structurally here so
// the language-level operators (op, nops, type, subs, printing) work in Phase 2
// without delegating to Sage.
type Value interface {
	isValue()
}

// ---- Numbers ----------------------------------------------------------------

type Integer struct{ Val *big.Int }
type Rational struct{ Val *big.Rat } // invariant: denominator != 1
type Float struct{ Val float64 }

func (Integer) isValue()  {}
func (Rational) isValue() {}
func (Float) isValue()    {}

func newInt(i int64) Integer        { return Integer{big.NewInt(i)} }
func bigInt(b *big.Int) Integer     { return Integer{b} }
func intVal(v Value) (*big.Int, bool) {
	if i, ok := v.(Integer); ok {
		return i.Val, true
	}
	return nil, false
}

// normRat returns an Integer if the rational is integral, else a Rational.
func normRat(r *big.Rat) Value {
	if r.IsInt() {
		return Integer{new(big.Int).Set(r.Num())}
	}
	return Rational{r}
}

// toRat coerces an Integer/Rational to *big.Rat. ok=false for non-rationals.
func toRat(v Value) (*big.Rat, bool) {
	switch n := v.(type) {
	case Integer:
		return new(big.Rat).SetInt(n.Val), true
	case Rational:
		return n.Val, true
	}
	return nil, false
}

// ---- Strings / names / booleans --------------------------------------------

type MString struct{ Val string }
type Name struct{ Val string } // the textual name, backticks already stripped

// Boolean models Maple's three-valued logic: true, false, FAIL.
type Boolean struct{ Kind boolKind }
type boolKind int

const (
	bFalse boolKind = iota
	bTrue
	bFAIL
)

func (MString) isValue() {}
func (Name) isValue()    {}
func (Boolean) isValue() {}

var (
	vTrue  = Boolean{bTrue}
	vFalse = Boolean{bFalse}
	vFAIL  = Boolean{bFAIL}
)

func mkBool(b bool) Boolean {
	if b {
		return vTrue
	}
	return vFalse
}

// ---- Sequences / collections ------------------------------------------------

// Seq is a flattened expression sequence. NULL is Seq{}.
type Seq struct{ Items []Value }
type List struct{ Items []Value }
type Set struct{ Items []Value } // kept sorted + deduplicated

func (Seq) isValue()  {}
func (List) isValue() {}
func (Set) isValue()  {}

// NULL is the empty expression sequence.
func NULL() Seq { return Seq{} }

// flattenSeq builds a Seq from a slice of values, flattening nested Seqs (Maple
// expression-sequence flattening) and dropping NULLs.
func flattenSeq(vals []Value) []Value {
	var out []Value
	for _, v := range vals {
		if s, ok := v.(Seq); ok {
			out = append(out, s.Items...)
		} else {
			out = append(out, v)
		}
	}
	return out
}

// seqOrSingle collapses a one-element sequence to the element (Maple: a 1-seq
// is the value; a 0-seq is NULL).
func seqOrSingle(vals []Value) Value {
	flat := flattenSeq(vals)
	switch len(flat) {
	case 0:
		return NULL()
	case 1:
		return flat[0]
	default:
		return Seq{flat}
	}
}

// makeSet sorts and deduplicates the items per Maple's canonical set ordering.
func makeSet(items []Value) Set {
	flat := flattenSeq(items)
	sort.SliceStable(flat, func(i, j int) bool {
		return compareValues(flat[i], flat[j]) < 0
	})
	var out []Value
	for i, v := range flat {
		if i == 0 || compareValues(out[len(out)-1], v) != 0 {
			out = append(out, v)
		}
	}
	return Set{out}
}

// ---- Table ------------------------------------------------------------------

// Table is Maple's table. Keys are canonical string forms of the index value
// (so t[1] and t[1.0] differ by their printed form, matching Maple closely
// enough for DT, which keys on names/integers/strings). We retain the original
// key Value for entries()/indices().
type Table struct {
	Keys map[string]Value // canonical-key -> original index value
	Vals map[string]Value // canonical-key -> stored value
}

func (*Table) isValue() {}

func newTable() *Table {
	return &Table{Keys: map[string]Value{}, Vals: map[string]Value{}}
}

func tableKey(idx Value) string { return canonicalKey(idx) }

func (t *Table) get(idx Value) (Value, bool) {
	k := tableKey(idx)
	v, ok := t.Vals[k]
	return v, ok
}

func (t *Table) set(idx, val Value) {
	k := tableKey(idx)
	t.Keys[k] = idx
	t.Vals[k] = val
}

func (t *Table) deleteKey(idx Value) {
	k := tableKey(idx)
	delete(t.Keys, k)
	delete(t.Vals, k)
}

// sortedKeys returns the canonical keys in a deterministic order (sorted by the
// original index value) for entries/indices.
func (t *Table) sortedKeys() []string {
	ks := make([]string, 0, len(t.Keys))
	for k := range t.Keys {
		ks = append(ks, k)
	}
	sort.SliceStable(ks, func(i, j int) bool {
		return compareValues(t.Keys[ks[i]], t.Keys[ks[j]]) < 0
	})
	return ks
}

// ---- Procedures -------------------------------------------------------------

// Proc is a procedure value: the AST plus memoization state. DifferentialThomas
// procedures reference package globals (not lexical upvalues), so no closure
// environment is captured — globals resolve through the interpreter's global
// table at call time.
type Proc struct {
	def         *tree            // procNode
	name        string
	remember    map[string]Value // option remember cache (nil if not enabled)
	hasRemember bool
	// env captures the enclosing proc's local bindings when this proc is
	// constructed inside another proc body (lexical closure). DifferentialThomas
	// builds inner procs — e.g. IsDifferentialVariable2 inside ComputeRanking —
	// that reference the outer proc's locals (dvar, ivar). nil for top-level
	// procs (no enclosing scope). Read-only snapshot at construction time.
	env map[string]Value
}

func (*Proc) isValue() {}

// Builtin is a Go-implemented procedure.
type Builtin struct {
	Name string
	Fn   func(in *Interp, args []Value) (Value, error)
}

func (*Builtin) isValue() {}

// ---- Unevaluated structural forms ------------------------------------------

// Func is an unevaluated function application kept as data, e.g. f(x), diff(y,x)
// where f is not a known proc. DifferentialThomas relies on these (u(x,y) in
// the expected output).
type Func struct {
	Head Value   // usually a Name
	Args []Value // arguments (already a flattened sequence of args)
}

// Indexed is an unevaluated indexed name a[i,j] kept as data (jet variables).
type Indexed struct {
	Head Value
	Idx  []Value
}

func (*Func) isValue()    {}
func (*Indexed) isValue() {}

// Symbolic arithmetic forms (kept as data when not fully numeric).
type Sum struct{ Terms []Value }     // a + b + c  (subtraction encoded via Prod with -1)
type Prod struct{ Factors []Value }  // a * b * c
type Power struct{ Base, Exp Value } // a^b

func (*Sum) isValue()   {}
func (*Prod) isValue()  {}
func (*Power) isValue() {}

// ---- Relations / ranges -----------------------------------------------------

type Range struct{ Lo, Hi Value }
type Equation struct{ Lhs, Rhs Value }            // a = b
type Relation struct{ Op string; Lhs, Rhs Value } // <, <=, <>, in, subset
type Uneval struct{ Expr Value }                  // 'expr' (one-level delay marker; rarely persists)

func (*Range) isValue()    {}
func (*Equation) isValue() {}
func (*Relation) isValue() {}
func (*Uneval) isValue()   {}

// ---- helpers ----------------------------------------------------------------

func isNULL(v Value) bool {
	s, ok := v.(Seq)
	return ok && len(s.Items) == 0
}

// isInfinityVal reports whether v is Maple's infinity or -infinity. infinity is
// the bare Name{"infinity"}; -infinity is the product (-1)*infinity.
func isInfinityVal(v Value) bool {
	if n, ok := v.(Name); ok {
		return n.Val == "infinity"
	}
	if p, ok := v.(*Prod); ok {
		for _, f := range p.Factors {
			if n, ok := f.(Name); ok && n.Val == "infinity" {
				return true
			}
		}
	}
	return false
}

// isNegInfinityVal reports whether v is Maple's -infinity, represented as the
// product (-1)*infinity (a Prod containing the infinity name and a negative
// rational coefficient). Plain Name{"infinity"} is +infinity.
func isNegInfinityVal(v Value) bool {
	p, ok := v.(*Prod)
	if !ok {
		return false
	}
	hasInf := false
	neg := false
	for _, f := range p.Factors {
		if n, ok := f.(Name); ok && n.Val == "infinity" {
			hasInf = true
			continue
		}
		if r, ok := toRat(f); ok && r.Sign() < 0 {
			neg = true
		}
	}
	return hasInf && neg
}

// isUndefinedVal reports whether v is Maple's undefined.
func isUndefinedVal(v Value) bool {
	n, ok := v.(Name)
	return ok && n.Val == "undefined"
}

func nameVal(v Value) (string, bool) {
	if n, ok := v.(Name); ok {
		return n.Val, true
	}
	return "", false
}

func strVal(v Value) (string, bool) {
	if s, ok := v.(MString); ok {
		return s.Val, true
	}
	return "", false
}

// stripBacktick removes surrounding backticks from a name literal token.
func stripBacktick(s string) string {
	if len(s) >= 2 && s[0] == '`' && s[len(s)-1] == '`' {
		return s[1 : len(s)-1]
	}
	return s
}

// stripQuotes removes surrounding double quotes from a string literal token and
// processes simple backslash escapes.
func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return unescape(s)
}

// stripUneval removes surrounding single quotes from an uneval literal token.
func stripUneval(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}

func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '`':
				b.WriteByte('`')
			default:
				b.WriteByte(s[i])
			}
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
