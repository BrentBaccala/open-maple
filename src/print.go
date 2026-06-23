package main

import (
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// printValue renders a Value in a Maple-faithful surface form. This is used by
// print/printf/convert(...,string) and — critically — by the FactorSorter
// (`convert(sort(...),string)`), so the decomposition's branch order depends on
// it. It does NOT yet reproduce every Maple typesetting nuance; documented gaps
// are noted in the Phase-2 report.
func printValue(v Value) string {
	return printPrec(v, 0)
}

// precedence levels for parenthesisation, matching Maple's surface grammar.
const (
	precSeq   = 1
	precEq    = 2
	precSum   = 3
	precProd  = 4
	precPow   = 5
	precUnary = 6
	precAtom  = 7
)

func printPrec(v Value, parent int) string {
	switch n := v.(type) {
	case *SageRef:
		// Printing needs the concrete expression: materialize (once) and recurse.
		return printPrec(concrete(n), parent)
	case Integer:
		return n.Val.String()
	case Rational:
		return n.Val.Num().String() + "/" + n.Val.Denom().String()
	case Float:
		return strconv.FormatFloat(n.Val, 'g', -1, 64)
	case MString:
		return "\"" + n.Val + "\""
	case Name:
		return printName(n.Val)
	case Boolean:
		switch n.Kind {
		case bTrue:
			return "true"
		case bFalse:
			return "false"
		default:
			return "FAIL"
		}
	case Seq:
		parts := make([]string, len(n.Items))
		for i, it := range n.Items {
			parts[i] = printPrec(it, precSeq)
		}
		return strings.Join(parts, ", ")
	case List:
		parts := make([]string, len(n.Items))
		for i, it := range n.Items {
			parts[i] = printPrec(it, precSeq)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case Set:
		parts := make([]string, len(n.Items))
		for i, it := range n.Items {
			parts[i] = printPrec(it, precSeq)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case *Table:
		return printTable(n)
	case *Proc:
		if n.name != "" {
			return n.name
		}
		return "proc"
	case *Builtin:
		return n.Name
	case *Func:
		return printPrec(n.Head, precAtom) + "(" + printArgs(n.Args) + ")"
	case *Indexed:
		return printPrec(n.Head, precAtom) + "[" + printArgs(n.Idx) + "]"
	case *Sum:
		return wrap(printSum(n), precSum, parent)
	case *Prod:
		// Maple stores -infinity as the product (-1)*infinity but PRINTS it as the
		// atom "-infinity" (not "-1*infinity"). Same for the literal -1*infinity
		// the user never sees. Render the canonical surface form.
		if isNegInfinityVal(n) {
			return wrap("-infinity", precUnary, parent)
		}
		return wrap(printProd(n), precProd, parent)
	case *Power:
		return wrap(printPrec(n.Base, precPow)+"^"+printPrec(n.Exp, precUnary), precPow, parent)
	case *Range:
		return printPrec(n.Lo, precEq) + ".." + printPrec(n.Hi, precEq)
	case *Equation:
		return wrap(printPrec(n.Lhs, precEq)+" = "+printPrec(n.Rhs, precEq), precEq, parent)
	case *Relation:
		return wrap(printPrec(n.Lhs, precEq)+" "+n.Op+" "+printPrec(n.Rhs, precEq), precEq, parent)
	case *Uneval:
		return printPrec(n.Expr, parent)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

func wrap(s string, self, parent int) string {
	if parent > self {
		return "(" + s + ")"
	}
	return s
}

func printArgs(args []Value) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = printPrec(a, precSeq)
	}
	return strings.Join(parts, ", ")
}

// printName quotes a name in backticks if it is not a plain Maple identifier.
func printName(s string) string {
	if s == "" {
		return "``"
	}
	plain := true
	for i, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if i > 0 {
			ok = ok || (r >= '0' && r <= '9')
		}
		if !ok {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "`" + s + "`"
}

func printSum(s *Sum) string {
	if len(s.Terms) == 0 {
		return "0"
	}
	var b strings.Builder
	for i, t := range s.Terms {
		ts := printPrec(t, precSum)
		if i == 0 {
			b.WriteString(ts)
			continue
		}
		// represent a leading '-' as " - " rather than " + -"
		if strings.HasPrefix(ts, "-") {
			b.WriteString(" - ")
			b.WriteString(ts[1:])
		} else {
			b.WriteString(" + ")
			b.WriteString(ts)
		}
	}
	return b.String()
}

func printProd(p *Prod) string {
	if len(p.Factors) == 0 {
		return "1"
	}
	// A leading coefficient of exactly -1 folds into a unary minus (-b, not
	// -1*b), so a subtracted term u(x,y)+(-1)*diff(...) renders as
	// u(x,y) - diff(...) — matching Maple. printSum keys on the leading '-'.
	factors := p.Factors
	neg := false
	if len(factors) > 1 && isMinusOne(factors[0]) {
		neg = true
		factors = factors[1:]
	}
	parts := make([]string, len(factors))
	for i, f := range factors {
		parts[i] = printPrec(f, precProd)
	}
	s := strings.Join(parts, "*")
	if neg {
		return "-" + s
	}
	return s
}

func printTable(t *Table) string {
	ks := t.sortedKeys()
	parts := make([]string, 0, len(ks))
	for _, k := range ks {
		parts = append(parts, printPrec(t.Keys[k], precSeq)+" = "+printPrec(t.Vals[k], precSeq))
	}
	return "table([" + strings.Join(parts, ", ") + "])"
}

// canonicalKey produces a stable string used as a table-index map key. It must
// be injective enough that distinct Maple indices map to distinct keys.
func canonicalKey(v Value) string {
	return "@" + reflectTag(v) + ":" + printValue(v)
}

// reflectTag gives a short type tag so e.g. Name "1" and Integer 1 don't alias.
func reflectTag(v Value) string {
	switch v.(type) {
	case Integer:
		return "i"
	case Rational:
		return "r"
	case Float:
		return "f"
	case MString:
		return "s"
	case Name:
		return "n"
	case Boolean:
		return "b"
	case List:
		return "L"
	case Set:
		return "S"
	case Seq:
		return "q"
	case *Indexed:
		return "x"
	case *Func:
		return "F"
	default:
		return "o"
	}
}

// typeOrder ranks value kinds for the canonical set/sort ordering. Maple's true
// ordering is its internal address order for many cases; we use a deterministic
// type-then-content order that is stable and good enough for set canonical*and*
// reproducible test output. Documented gap vs Maple in the report.
func typeOrder(v Value) int {
	switch v.(type) {
	case Integer, Rational, Float:
		return 0
	case Name:
		return 1
	case MString:
		return 2
	case Boolean:
		return 3
	case *Indexed:
		return 4
	case *Func:
		return 5
	case *Power:
		return 6
	case *Prod:
		return 7
	case *Sum:
		return 8
	case List:
		return 9
	case Set:
		return 10
	case Seq:
		return 11
	case *Range:
		return 12
	case *Equation, *Relation:
		return 13
	default:
		return 99
	}
}

// compareValues defines a total order used for set canonicalisation and as the
// default term comparator for sort(). Returns -1, 0, +1.
func compareValues(a, b Value) int {
	// A ref-backed value must be materialized before any structural comparison —
	// equality/membership/sort all look inside the expression.
	a = concrete(a)
	b = concrete(b)
	// Polynomial-aware comparison: when at least one side is a compound
	// polynomial (Sum/Prod/Power) and BOTH sides are plain polynomial
	// expressions, compare by expanded normal form. This makes polynomial
	// equality order- and shape-independent — x+y == y+x, and a Sum that cancels
	// to an atom (u[0,0]+u[1,1]-u[1,1]) == u[0,0] — regardless of which path
	// (Sage in one order, native code in another) produced each value. Without
	// this, equality/membership compare the printed string, so two equal
	// polynomials in different term order would test unequal — which is what made
	// a native expand()/coeff() unsafe. (Mixed/non-polynomial operands fall
	// through to the structural comparison below.)
	if isCompoundPoly(a) || isCompoundPoly(b) {
		if c, ok := comparePolyValues(a, b); ok {
			return c
		}
	}
	ta, tb := typeOrder(a), typeOrder(b)
	if ta != tb {
		return cmpInt(ta, tb)
	}
	switch x := a.(type) {
	case Integer, Rational, Float:
		ra, _ := toNumRat(a)
		rb, _ := toNumRat(b)
		return ra.Cmp(rb)
	case Name:
		return strings.Compare(x.Val, b.(Name).Val)
	case MString:
		return strings.Compare(x.Val, b.(MString).Val)
	case Boolean:
		return cmpInt(int(x.Kind), int(b.(Boolean).Kind))
	case List:
		return cmpSlices(x.Items, b.(List).Items)
	case Set:
		return cmpSlices(x.Items, b.(Set).Items)
	case Seq:
		return cmpSlices(x.Items, b.(Seq).Items)
	case *Indexed:
		y := b.(*Indexed)
		if c := compareValues(x.Head, y.Head); c != 0 {
			return c
		}
		return cmpSlices(x.Idx, y.Idx)
	case *Func:
		y := b.(*Func)
		if c := compareValues(x.Head, y.Head); c != 0 {
			return c
		}
		return cmpSlices(x.Args, y.Args)
	default:
		// fall back to string comparison for structural forms
		return strings.Compare(printValue(a), printValue(b))
	}
}

func toNumRat(v Value) (*big.Rat, bool) {
	switch n := v.(type) {
	case Integer:
		return new(big.Rat).SetInt(n.Val), true
	case Rational:
		return n.Val, true
	case Float:
		r := new(big.Rat)
		r.SetFloat64(n.Val)
		return r, true
	}
	return new(big.Rat), false
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func cmpSlices(a, b []Value) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareValues(a[i], b[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(a), len(b))
}

// sortValuesDefault sorts in place by the default comparator.
func sortValuesDefault(items []Value) {
	sort.SliceStable(items, func(i, j int) bool {
		return compareValues(items[i], items[j]) < 0
	})
}

// equalValues reports structural Maple-equality (used by =, member, set dedup).
func equalValues(a, b Value) bool {
	return compareValues(a, b) == 0
}
