package main

import (
	"fmt"
	"math/big"
)

// evalOperator evaluates binary operator nodes (operate group).
func (it *Interp) evalOperator(n *tree) (Value, error) {
	op := n.value
	// '$' seq operator is special (lazy bounds handling)
	if op == "$" {
		return it.evalSeqOp(n)
	}
	left, err := it.eval(n.nodes[0])
	if err != nil {
		return nil, err
	}
	// short-circuit boolean operators
	switch op {
	case "and":
		return it.evalAnd(left, n.nodes[1])
	case "or":
		return it.evalOr(left, n.nodes[1])
	}
	right, err := it.eval(n.nodes[1])
	if err != nil {
		return nil, err
	}
	switch op {
	case "+":
		return it.arithAdd(left, right)
	case "-":
		return it.arithAdd(left, it.neg(right))
	case "*":
		return it.arithMul(left, right)
	case "/":
		return it.arithDiv(left, right)
	case "^", "**":
		return it.arithPow(left, right)
	case "mod":
		return it.arithMod(left, right)
	case "=":
		// Maple: `a = b` is an inert equation; boolean force happens in `if`
		// conditions and `evalb` (via truth()).
		return &Equation{Lhs: left, Rhs: right}, nil
	case "<>":
		return &Relation{Op: "<>", Lhs: left, Rhs: right}, nil
	case "<", "<=", ">", ">=":
		// inert relation; boolean force in if/evalb via truth()
		return &Relation{Op: op, Lhs: left, Rhs: right}, nil
	case "in":
		return it.memberOp(left, right)
	case "union", "intersect", "minus", "subset":
		return it.setOp(op, left, right)
	case ".":
		return it.catDot(left, right)
	case "||":
		return it.catBars(left, right)
	case "@":
		return it.compose(left, right), nil
	case "@@":
		// repeated composition f@@n — build a composition applied n times. If n
		// is not a known integer, keep it as an inert function.
		if cnt, ok := intVal(right); ok && cnt.IsInt64() {
			n := int(cnt.Int64())
			if n == 0 {
				return &Builtin{Name: "identity", Fn: func(_ *Interp, a []Value) (Value, error) { return seqOrSingle(a), nil }}, nil
			}
			res := left
			for i := 1; i < n; i++ {
				res = it.compose(left, res)
			}
			return res, nil
		}
		return &Func{Head: Name{"@@"}, Args: []Value{left, right}}, nil
	case "xor", "implies":
		return it.boolXorImplies(op, left, right)
	default:
		return nil, errUnimplemented("operator " + op)
	}
}

// ---- arithmetic -------------------------------------------------------------

// compose builds the function composition (f @ g): a callable that applies g to
// its arguments, then f to the result. f and g may be procs, builtins, or
// names (CAS ops / package members). The composition itself is not invoked on
// the ComputeRanking path; it is stored in the ranking table for later use.
func (it *Interp) compose(f, g Value) Value {
	name := printValue(f) + "@" + printValue(g)
	return &Builtin{
		Name: name,
		Fn: func(in *Interp, args []Value) (Value, error) {
			gv, err := in.applyValue(g, args, nil)
			if err != nil {
				return nil, err
			}
			return in.applyValue(f, flattenSeq([]Value{gv}), nil)
		},
	}
}

func (it *Interp) neg(v Value) Value {
	switch n := v.(type) {
	case Integer:
		return Integer{new(big.Int).Neg(n.Val)}
	case Rational:
		return normRat(new(big.Rat).Neg(n.Val))
	case Float:
		return Float{-n.Val}
	case List:
		out := make([]Value, len(n.Items))
		for i := range n.Items {
			out[i] = it.neg(n.Items[i])
		}
		return List{out}
	}
	// symbolic: -1 * v
	return &Prod{Factors: []Value{newInt(-1), v}}
}

func (it *Interp) arithAdd(a, b Value) (Value, error) {
	if r, ok := numAdd(a, b); ok {
		return r, nil
	}
	// Maple does element-wise arithmetic on equal-length lists: [1,2]+[3,4]=[4,6].
	if la, oka := a.(List); oka {
		if lb, okb := b.(List); okb {
			if len(la.Items) != len(lb.Items) {
				return nil, newMapleError("numeric exception: list lengths differ")
			}
			out := make([]Value, len(la.Items))
			for i := range la.Items {
				v, err := it.arithAdd(la.Items[i], lb.Items[i])
				if err != nil {
					return nil, err
				}
				out[i] = v
			}
			return List{out}, nil
		}
	}
	// symbolic sum (drop zeros)
	terms := append(sumTerms(a), sumTerms(b)...)
	return simplifySum(terms), nil
}

func (it *Interp) arithMul(a, b Value) (Value, error) {
	if r, ok := numMul(a, b); ok {
		return r, nil
	}
	// Maple scales a list by a scalar element-wise: 2*[1,2]=[2,4].
	// (list*list is element-wise too, e.g. for equal-length lists.)
	if la, oka := a.(List); oka {
		if lb, okb := b.(List); okb {
			if len(la.Items) != len(lb.Items) {
				return nil, newMapleError("numeric exception: list lengths differ")
			}
			out := make([]Value, len(la.Items))
			for i := range la.Items {
				v, err := it.arithMul(la.Items[i], lb.Items[i])
				if err != nil {
					return nil, err
				}
				out[i] = v
			}
			return List{out}, nil
		}
		return it.scaleList(la, b)
	}
	if lb, okb := b.(List); okb {
		return it.scaleList(lb, a)
	}
	factors := append(prodFactors(a), prodFactors(b)...)
	return simplifyProd(factors), nil
}

// scaleList multiplies every element of l by the scalar s (Maple element-wise
// scalar*list semantics).
func (it *Interp) scaleList(l List, s Value) (Value, error) {
	out := make([]Value, len(l.Items))
	for i := range l.Items {
		v, err := it.arithMul(s, l.Items[i])
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return List{out}, nil
}

func (it *Interp) arithDiv(a, b Value) (Value, error) {
	if ra, ok := toRat(a); ok {
		if rb, ok := toRat(b); ok {
			if rb.Sign() == 0 {
				return nil, newMapleError("numeric exception: division by zero")
			}
			return normRat(new(big.Rat).Quo(ra, rb)), nil
		}
	}
	// symbolic a * b^(-1)
	inv := &Power{Base: b, Exp: newInt(-1)}
	return it.arithMul(a, inv)
}

func (it *Interp) arithPow(a, b Value) (Value, error) {
	if ai, ok := a.(Integer); ok {
		if bi, ok := b.(Integer); ok {
			if bi.Val.Sign() >= 0 && bi.Val.IsInt64() {
				return Integer{new(big.Int).Exp(ai.Val, bi.Val, nil)}, nil
			}
			if bi.Val.Sign() < 0 && bi.Val.IsInt64() {
				p := new(big.Int).Exp(ai.Val, new(big.Int).Neg(bi.Val), nil)
				return normRat(new(big.Rat).SetFrac(big.NewInt(1), p)), nil
			}
		}
	}
	if ar, ok := toRat(a); ok {
		if bi, ok := b.(Integer); ok && bi.Val.IsInt64() {
			e := bi.Val.Int64()
			res := new(big.Rat).SetInt64(1)
			base := new(big.Rat).Set(ar)
			neg := e < 0
			if neg {
				e = -e
			}
			for ; e > 0; e-- {
				res.Mul(res, base)
			}
			if neg {
				res.Inv(res)
			}
			return normRat(res), nil
		}
	}
	return &Power{Base: a, Exp: b}, nil
}

func (it *Interp) arithMod(a, b Value) (Value, error) {
	ai, aok := intVal(a)
	bi, bok := intVal(b)
	if aok && bok {
		if bi.Sign() == 0 {
			return nil, newMapleError("numeric exception: division by zero")
		}
		m := new(big.Int).Mod(ai, bi)
		return Integer{m}, nil
	}
	return nil, errUnimplemented("mod on non-integers")
}

// numAdd/numMul return ok=false when operands are not both numeric.
func numAdd(a, b Value) (Value, bool) {
	if af, ok := a.(Float); ok {
		if bf, fok := toFloat(b); fok {
			return Float{af.Val + bf}, true
		}
	}
	if bf, ok := b.(Float); ok {
		if af, fok := toFloat(a); fok {
			return Float{af + bf.Val}, true
		}
	}
	ra, ok1 := toRat(a)
	rb, ok2 := toRat(b)
	if ok1 && ok2 {
		return normRat(new(big.Rat).Add(ra, rb)), true
	}
	return nil, false
}

func numMul(a, b Value) (Value, bool) {
	if af, ok := a.(Float); ok {
		if bf, fok := toFloat(b); fok {
			return Float{af.Val * bf}, true
		}
	}
	if bf, ok := b.(Float); ok {
		if af, fok := toFloat(a); fok {
			return Float{af * bf.Val}, true
		}
	}
	ra, ok1 := toRat(a)
	rb, ok2 := toRat(b)
	if ok1 && ok2 {
		return normRat(new(big.Rat).Mul(ra, rb)), true
	}
	return nil, false
}

func toFloat(v Value) (float64, bool) {
	switch n := v.(type) {
	case Float:
		return n.Val, true
	case Integer:
		f := new(big.Float).SetInt(n.Val)
		r, _ := f.Float64()
		return r, true
	case Rational:
		r, _ := n.Val.Float64()
		return r, true
	}
	return 0, false
}

// ---- symbolic helpers -------------------------------------------------------

func sumTerms(v Value) []Value {
	if s, ok := v.(*Sum); ok {
		return s.Terms
	}
	return []Value{v}
}

func prodFactors(v Value) []Value {
	if p, ok := v.(*Prod); ok {
		return p.Factors
	}
	return []Value{v}
}

func isZero(v Value) bool {
	if i, ok := v.(Integer); ok {
		return i.Val.Sign() == 0
	}
	if r, ok := v.(Rational); ok {
		return r.Val.Sign() == 0
	}
	if f, ok := v.(Float); ok {
		return f.Val == 0
	}
	return false
}

func isOne(v Value) bool {
	if i, ok := v.(Integer); ok {
		return i.Val.Cmp(big.NewInt(1)) == 0
	}
	return false
}

func simplifySum(terms []Value) Value {
	// Maple's extended-real arithmetic on +/-infinity: a sum containing +infinity
	// absorbs every finite term -> infinity (and likewise -infinity -> -infinity).
	// Mixing +infinity and -infinity is undefined. DT relies on this when it does
	// element-wise list arithmetic on MultiplicativeVariables lists whose entries
	// are `infinity` (JanetDivisorInTrees: `[infinity,infinity] - [0,1]` must give
	// `[infinity,infinity]`, not `[infinity, infinity-1]`).
	pos, neg := false, false
	for _, t := range terms {
		if isInfinityVal(t) {
			if isNegInfinityVal(t) {
				neg = true
			} else {
				pos = true
			}
		}
	}
	if pos || neg {
		if pos && neg {
			return Name{"undefined"}
		}
		if pos {
			return Name{"infinity"}
		}
		return &Prod{[]Value{newInt(-1), Name{"infinity"}}}
	}

	var num *big.Rat
	var rest []Value
	for _, t := range terms {
		if r, ok := toRat(t); ok {
			if num == nil {
				num = new(big.Rat).Set(r)
			} else {
				num.Add(num, r)
			}
			continue
		}
		rest = append(rest, t)
	}
	var out []Value
	out = append(out, rest...)
	if num != nil && num.Sign() != 0 {
		out = append(out, normRat(num))
	}
	switch len(out) {
	case 0:
		return newInt(0)
	case 1:
		return out[0]
	default:
		return &Sum{out}
	}
}

func simplifyProd(factors []Value) Value {
	var coef *big.Rat
	var rest []Value
	for _, f := range factors {
		if r, ok := toRat(f); ok {
			if coef == nil {
				coef = new(big.Rat).Set(r)
			} else {
				coef.Mul(coef, r)
			}
			continue
		}
		rest = append(rest, f)
	}
	if coef != nil && coef.Sign() == 0 {
		return newInt(0)
	}
	var out []Value
	if coef != nil && coef.Cmp(big.NewRat(1, 1)) != 0 {
		out = append(out, normRat(coef))
	}
	out = append(out, rest...)
	switch len(out) {
	case 0:
		return newInt(1)
	case 1:
		return out[0]
	default:
		return &Prod{out}
	}
}

// ---- relational / boolean ---------------------------------------------------

func (it *Interp) compareRel(op string, a, b Value) (Value, error) {
	ra, ok1 := toNumRat(a)
	rb, ok2 := toNumRat(b)
	if ok1 && ok2 {
		c := ra.Cmp(rb)
		var res bool
		switch op {
		case "<":
			res = c < 0
		case "<=":
			res = c <= 0
		case ">":
			res = c > 0
		case ">=":
			res = c >= 0
		}
		return mkBool(res), nil
	}
	// non-numeric: keep as an inert relation (Maple does this).
	return &Relation{Op: op, Lhs: a, Rhs: b}, nil
}

func (it *Interp) evalAnd(left Value, rightNode *tree) (Value, error) {
	lb := truth(left)
	if lb == bFalse {
		return vFalse, nil
	}
	right, err := it.eval(rightNode)
	if err != nil {
		return nil, err
	}
	rb := truth(right)
	if lb == bTrue && rb == bTrue {
		return vTrue, nil
	}
	if lb == bFalse || rb == bFalse {
		return vFalse, nil
	}
	return vFAIL, nil
}

func (it *Interp) evalOr(left Value, rightNode *tree) (Value, error) {
	lb := truth(left)
	if lb == bTrue {
		return vTrue, nil
	}
	right, err := it.eval(rightNode)
	if err != nil {
		return nil, err
	}
	rb := truth(right)
	if lb == bTrue || rb == bTrue {
		return vTrue, nil
	}
	if lb == bFalse && rb == bFalse {
		return vFalse, nil
	}
	return vFAIL, nil
}

func (it *Interp) boolXorImplies(op string, a, b Value) (Value, error) {
	la, lb := truth(a), truth(b)
	if la == bFAIL || lb == bFAIL {
		return vFAIL, nil
	}
	switch op {
	case "xor":
		return mkBool((la == bTrue) != (lb == bTrue)), nil
	case "implies":
		return mkBool(la != bTrue || lb == bTrue), nil
	}
	return vFAIL, nil
}

// truth maps a value to a three-valued boolean kind, evaluating inert
// equations/relations (Maple's implicit evalb in boolean contexts).
func truth(v Value) boolKind {
	switch x := v.(type) {
	case Boolean:
		return x.Kind
	case Name:
		switch x.Val {
		case "true":
			return bTrue
		case "false":
			return bFalse
		case "FAIL":
			return bFAIL
		}
	case *Equation:
		return mkBool(equalValues(x.Lhs, x.Rhs)).Kind
	case *Relation:
		switch x.Op {
		case "<>":
			return mkBool(!equalValues(x.Lhs, x.Rhs)).Kind
		case "<", "<=", ">", ">=":
			ra, ok1 := toNumRat(x.Lhs)
			rb, ok2 := toNumRat(x.Rhs)
			if ok1 && ok2 {
				c := ra.Cmp(rb)
				switch x.Op {
				case "<":
					return mkBool(c < 0).Kind
				case "<=":
					return mkBool(c <= 0).Kind
				case ">":
					return mkBool(c > 0).Kind
				case ">=":
					return mkBool(c >= 0).Kind
				}
			}
		}
	}
	return bFAIL
}

// ---- sets / membership ------------------------------------------------------

func (it *Interp) memberOp(x, container Value) (Value, error) {
	switch c := container.(type) {
	case Set:
		for _, e := range c.Items {
			if equalValues(e, x) {
				return vTrue, nil
			}
		}
		return vFalse, nil
	case List:
		for _, e := range c.Items {
			if equalValues(e, x) {
				return vTrue, nil
			}
		}
		return vFalse, nil
	}
	return &Relation{Op: "in", Lhs: x, Rhs: container}, nil
}

func itemsOf(v Value) ([]Value, bool) {
	switch c := v.(type) {
	case Set:
		return c.Items, true
	case List:
		return c.Items, true
	}
	return nil, false
}

func (it *Interp) setOp(op string, a, b Value) (Value, error) {
	ai, aok := itemsOf(a)
	bi, bok := itemsOf(b)
	if !aok || !bok {
		return nil, errUnimplemented("set op " + op + " on non-collections")
	}
	contains := func(items []Value, x Value) bool {
		for _, e := range items {
			if equalValues(e, x) {
				return true
			}
		}
		return false
	}
	switch op {
	case "union":
		return makeSet(append(append([]Value{}, ai...), bi...)), nil
	case "intersect":
		var out []Value
		for _, x := range ai {
			if contains(bi, x) {
				out = append(out, x)
			}
		}
		return makeSet(out), nil
	case "minus":
		var out []Value
		for _, x := range ai {
			if !contains(bi, x) {
				out = append(out, x)
			}
		}
		return makeSet(out), nil
	case "subset":
		for _, x := range ai {
			if !contains(bi, x) {
				return vFalse, nil
			}
		}
		return vTrue, nil
	}
	return nil, errUnimplemented("set op " + op)
}

// ---- string / name concatenation -------------------------------------------

func (it *Interp) catDot(a, b Value) (Value, error) {
	// '.' is Maple's concatenation when applied to names/strings.
	return it.catValues(a, b)
}

func (it *Interp) catBars(a, b Value) (Value, error) {
	return it.catValues(a, b)
}

// catValues concatenates names/strings/integers Maple-style. If either is a
// string the result is a string; otherwise a name.
func (it *Interp) catValues(a, b Value) (Value, error) {
	_, aStr := a.(MString)
	_, bStr := b.(MString)
	sa := plainText(a)
	sb := plainText(b)
	if aStr || bStr {
		return MString{sa + sb}, nil
	}
	return Name{sa + sb}, nil
}

// plainText renders a value as its unquoted textual content for cat().
func plainText(v Value) string {
	switch n := v.(type) {
	case MString:
		return n.Val
	case Name:
		return n.Val
	case Integer:
		return n.Val.String()
	default:
		return printValue(v)
	}
}

// ---- unary ------------------------------------------------------------------

func (it *Interp) evalUnary(n *tree) (Value, error) {
	switch n.value {
	case "-":
		v, err := it.eval(n.nodes[0])
		if err != nil {
			return nil, err
		}
		return it.neg(v), nil
	case "+":
		return it.eval(n.nodes[0])
	case "not":
		v, err := it.eval(n.nodes[0])
		if err != nil {
			return nil, err
		}
		switch truth(v) {
		case bTrue:
			return vFalse, nil
		case bFalse:
			return vTrue, nil
		default:
			return vFAIL, nil
		}
	case "$":
		// prefix $ — `$ a..b` produces the integer sequence a, a+1, ..., b
		// (empty if a>b). `$ expr` on a non-range yields the single value.
		v, err := it.eval(n.nodes[0])
		if err != nil {
			return nil, err
		}
		if rng, ok := v.(*Range); ok {
			return expandIntRange(rng)
		}
		return v, nil
	case "!post":
		v, err := it.eval(n.nodes[0])
		if err != nil {
			return nil, err
		}
		i, ok := intVal(v)
		if !ok {
			return nil, errUnimplemented("factorial of non-integer")
		}
		return Integer{factorial(i)}, nil
	}
	return nil, errUnimplemented("unary " + n.value)
}

func factorial(n *big.Int) *big.Int {
	res := big.NewInt(1)
	one := big.NewInt(1)
	i := big.NewInt(1)
	for i.Cmp(n) <= 0 {
		res.Mul(res, i)
		i.Add(i, one)
	}
	return res
}

// ---- $ seq operator ---------------------------------------------------------

// evalSeqOp handles  a $ b  forms:
//   expr $ i = lo..hi   (range form, handled in call/seq builtins normally)
//   x $ n               (n copies of x)
//   lo .. hi expressed via range
// expandIntRange expands an integer Range a..b into the sequence a,a+1,...,b
// (NULL if a>b). Used by the prefix `$ a..b` operator.
func expandIntRange(rng *Range) (Value, error) {
	lo, lok := intVal(rng.Lo)
	hi, hok := intVal(rng.Hi)
	if !lok || !hok {
		return nil, fmt.Errorf("$ range bounds must be integers")
	}
	var items []Value
	for i := lo.Int64(); i <= hi.Int64(); i++ {
		items = append(items, newInt(i))
	}
	return Seq{items}, nil
}

func (it *Interp) evalSeqOp(n *tree) (Value, error) {
	left := n.nodes[0]
	right, err := it.eval(n.nodes[1])
	if err != nil {
		return nil, err
	}
	// case: i $ i=lo..hi handled when left is an equation? In DT, `0$(i-1)` and
	// `1,0$(n-i)` are the dominant forms: value $ count.
	if cnt, ok := intVal(right); ok {
		lv, err := it.eval(left)
		if err != nil {
			return nil, err
		}
		c := int(cnt.Int64())
		if c < 0 {
			c = 0
		}
		items := make([]Value, c)
		for i := range items {
			items[i] = lv
		}
		return Seq{items}, nil
	}
	// range form: expr $ (var = lo..hi) — left is expr, right is Range only if
	// the equation was stripped. DT uses `seq` for that, so this is rare.
	if rng, ok := right.(*Range); ok {
		lo, lok := intVal(rng.Lo)
		hi, hok := intVal(rng.Hi)
		if lok && hok {
			var items []Value
			for i := lo.Int64(); i <= hi.Int64(); i++ {
				lv, err := it.eval(left)
				if err != nil {
					return nil, err
				}
				items = append(items, lv)
			}
			return Seq{items}, nil
		}
	}
	return nil, fmt.Errorf("unsupported $ sequence form")
}
