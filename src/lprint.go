package main

import (
	"strings"
)

// lprint1D renders a Value in Maple's 1-D / linear text form — the surface used
// by lprint, convert(expr, string), and sprintf("%a", expr). This is the form
// DifferentialThomas's FactorSorter byte-compares
// (convert(sort(p, vars), string) < convert(sort(q, vars), string)), so the
// rules here must match Maple's published 1-D conventions exactly (see
// ~/project/docs/maple-print-format-reference.md):
//
//   - No spaces around binary + - * / ^   (x^2+x+1, 3*x^2, a-b, a/b).
//   - Subtraction renders as a-b, leading negative as -x (NOT a+(-1)*b).
//   - Coefficient-first monomials (3*x^2).
//   - Sequences / lists / sets: comma+space (", ") between elements.
//   - Function-call args and indexed-name indices: comma+space (u(x, y),
//     u[1, 2]). The readme smoke target [[u(x, y) = 0]] fixes this convention.
//   - Equations / relations spaced: a = b, a <> b.
//   - -infinity prints as the atom "-infinity", not "-1*infinity".
//
// It differs from printValue only in operator spacing (printValue keeps the
// human-readable " + "/" - " for REPL / table dumps); the structural recursion
// is the same. Precedence levels reuse the print.go constants.
func lprint1D(v Value) string {
	return lprintPrec(v, 0)
}

func lprintPrec(v Value, parent int) string {
	switch n := v.(type) {
	case *Sum:
		return wrap(lprintSum(n), precSum, parent)
	case *Prod:
		if isNegInfinityVal(n) {
			return wrap("-infinity", precUnary, parent)
		}
		return wrap(lprintProd(n), precProd, parent)
	case *Power:
		return wrap(lprintPrec(n.Base, precPow)+"^"+lprintPrec(n.Exp, precUnary), precPow, parent)
	case Seq:
		return lprintJoin(n.Items, precSeq)
	case List:
		return "[" + lprintJoin(n.Items, precSeq) + "]"
	case Set:
		return "{" + lprintJoin(n.Items, precSeq) + "}"
	case *Func:
		return lprintPrec(n.Head, precAtom) + "(" + lprintJoin(n.Args, precSeq) + ")"
	case *Indexed:
		return lprintPrec(n.Head, precAtom) + "[" + lprintJoin(n.Idx, precSeq) + "]"
	case *Equation:
		return wrap(lprintPrec(n.Lhs, precEq)+" = "+lprintPrec(n.Rhs, precEq), precEq, parent)
	case *Relation:
		return wrap(lprintPrec(n.Lhs, precEq)+" "+n.Op+" "+lprintPrec(n.Rhs, precEq), precEq, parent)
	case *Range:
		return lprintPrec(n.Lo, precEq) + ".." + lprintPrec(n.Hi, precEq)
	case *Uneval:
		return lprintPrec(n.Expr, parent)
	default:
		// atoms (Integer, Rational, Float, Name, MString, Boolean, *Table, *Proc,
		// *Builtin, nil) have no operator spacing to adjust — defer to printValue.
		return printPrec(v, parent)
	}
}

func lprintJoin(items []Value, prec int) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = lprintPrec(it, prec)
	}
	return strings.Join(parts, ", ")
}

// lprintSum renders a+b+c with no spaces around + / -, folding a leading-minus
// term into "-": a-b (not a+-b), -x for a leading negative.
func lprintSum(s *Sum) string {
	if len(s.Terms) == 0 {
		return "0"
	}
	var b strings.Builder
	for i, t := range s.Terms {
		ts := lprintPrec(t, precSum)
		if i == 0 {
			b.WriteString(ts)
			continue
		}
		if strings.HasPrefix(ts, "-") {
			b.WriteByte('-')
			b.WriteString(ts[1:])
		} else {
			b.WriteByte('+')
			b.WriteString(ts)
		}
	}
	return b.String()
}

// lprintProd renders a*b*c with no spaces around *. A coefficient of exactly -1
// folds into a leading unary minus (-b, not -1*b) — Maple's 1-D surface for a
// negated term, which lprintSum relies on to render subtraction as a-b.
func lprintProd(p *Prod) string {
	if len(p.Factors) == 0 {
		return "1"
	}
	factors := p.Factors
	neg := false
	if len(factors) > 1 {
		if isMinusOne(factors[0]) {
			neg = true
			factors = factors[1:]
		}
	}
	parts := make([]string, len(factors))
	for i, f := range factors {
		parts[i] = lprintPrec(f, precProd)
	}
	s := strings.Join(parts, "*")
	if neg {
		return "-" + s
	}
	return s
}

// isMinusOne reports whether v is the integer/rational -1.
func isMinusOne(v Value) bool {
	switch n := v.(type) {
	case Integer:
		return n.Val.Sign() < 0 && n.Val.Int64() == -1
	case Rational:
		return n.Val.IsInt() && n.Val.Num().Int64() == -1
	}
	return false
}
