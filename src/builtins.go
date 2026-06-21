package main

import (
	"fmt"
	"math/big"
	"strings"
)

// registerBuiltins installs all Go-implemented language builtins.
func registerBuiltins(it *Interp) {
	reg := func(name string, fn func(*Interp, []Value) (Value, error)) {
		it.builtins[name] = &Builtin{Name: name, Fn: fn}
	}

	reg("op", biOp)
	reg("nops", biNops)
	reg("type", biType)
	reg("map", biMap)
	reg("map2", biMap2)
	reg("select", biSelect)
	reg("remove", biRemove)
	reg("selectremove", biSelectRemove)
	reg("subs", biSubs)
	reg("subsop", biSubsop)
	reg("zip", biZip)
	reg("indices", biIndices)
	reg("entries", biEntries)
	reg("member", biMember)
	reg("has", biHas)
	reg("hastype", biHasType)
	reg("copy", biCopy)
	reg("eval", biEval)
	reg("lhs", biLhs)
	reg("rhs", biRhs)
	reg("evalb", biEvalb)
	reg("cat", biCat)
	reg("convert", biConvert)
	reg("nargs", biNargs)
	reg("args", biArgs)
	reg("ERROR", biERROR)
	reg("userinfo", biUserinfo)
	reg("ASSERT", biASSERT)
	reg("WARNING", biWarning)
	reg("kernelopts", biKernelopts)
	reg("print", biPrint)
	reg("printf", biPrintf)
	reg("sprintf", biSprintf)
	reg("time", biTime)
	reg("sort", biSort)
	reg("nprintf", biNprintf)
	reg("max", biMax)
	reg("min", biMin)
	reg("abs", biAbs)
	reg("igcd", biIgcd)
	reg("modp", biModp)
	reg("evalf", biEvalf)
	reg("table", biTable)
	reg("array", biArray)
	reg("with", biWith)
	reg("whattype", biWhattype)
	reg("subsindets", biSubsindets)
	reg("piecewise", biPiecewise)
	reg("ListTools:-Search", biListSearch)
	reg("ListTools:-Reverse", biListReverse)
	reg("ListTools:-FindMaximalElement", biFindMaximalElement)
	reg("StringTools:-Trim", biStringTrim)
	reg("add_function", biAddFunction)
	reg("add_type", biAddType)

	// Operator-as-function forms: Maple lets `+`(a,b,...), `*`(...), `-`(...)
	// be called like procedures (DT uses `+`(op(u)) to sum a jet-index list and
	// similar). Wire them to the same arithmetic the infix operators use.
	reg("+", biOpAdd)
	reg("*", biOpMul)
	reg("-", biOpSub)
}

// biOpAdd implements `+`(a,b,...) = a+b+...; `+`() = 0.
func biOpAdd(it *Interp, args []Value) (Value, error) {
	var acc Value = newInt(0)
	for _, a := range args {
		v, err := it.arithAdd(acc, a)
		if err != nil {
			return nil, err
		}
		acc = v
	}
	return acc, nil
}

// biOpMul implements `*`(a,b,...) = a*b*...; `*`() = 1.
func biOpMul(it *Interp, args []Value) (Value, error) {
	var acc Value = newInt(1)
	for _, a := range args {
		v, err := it.arithMul(acc, a)
		if err != nil {
			return nil, err
		}
		acc = v
	}
	return acc, nil
}

// biOpSub implements `-`(a) = -a and `-`(a,b) = a-b.
func biOpSub(it *Interp, args []Value) (Value, error) {
	if len(args) == 0 {
		return newInt(0), nil
	}
	if len(args) == 1 {
		return it.arithAdd(newInt(0), it.neg(args[0]))
	}
	acc := args[0]
	for _, a := range args[1:] {
		v, err := it.arithAdd(acc, it.neg(a))
		if err != nil {
			return nil, err
		}
		acc = v
	}
	return acc, nil
}

// ---- helpers ----------------------------------------------------------------

func need(args []Value, n int, name string) error {
	if len(args) < n {
		return newMapleError(fmt.Sprintf("%s expects %d arguments, got %d", name, n, len(args)))
	}
	return nil
}

// derefTable resolves a Name bound to a table into the *Table (so op/indets/etc.
// operate on the table contents, matching Maple where these functions evaluate
// the name). Non-table names and other values pass through unchanged.
func (it *Interp) derefTable(v Value) Value {
	if nm, ok := v.(Name); ok {
		if rv, bound := it.lookup(nm.Val); bound {
			if t, ok := rv.(*Table); ok {
				return t
			}
		}
	}
	return v
}

// operands returns the Maple op-list of a value (its top-level subparts).
func operands(v Value) []Value {
	switch x := v.(type) {
	case List:
		return x.Items
	case Set:
		return x.Items
	case Seq:
		return x.Items
	case *Sum:
		return x.Terms
	case *Prod:
		return x.Factors
	case *Power:
		return []Value{x.Base, x.Exp}
	case *Func:
		return x.Args
	case *Indexed:
		return x.Idx
	case *Range:
		return []Value{x.Lo, x.Hi}
	case *Equation:
		return []Value{x.Lhs, x.Rhs}
	case *Relation:
		return []Value{x.Lhs, x.Rhs}
	case Rational:
		return []Value{Integer{new(big.Int).Set(x.Val.Num())}, Integer{new(big.Int).Set(x.Val.Denom())}}
	case *Table:
		// Maple: op(t) -> an inert `table([idx=val, ...])`. The DT idiom
		// `table([op(op(op(t)))])` then peels three levels:
		//   op(t)        = table([ [eq1, eq2, ...] ])   (a Func head=table)
		//   op(op(t))    = [eq1, eq2, ...]              (the List operand)
		//   op(op(op(t)))= eq1, eq2, ...                (the Equation sequence)
		// and table([eq1, eq2, ...]) rebuilds the table.
		ks := x.sortedKeys()
		eqs := make([]Value, 0, len(ks))
		for _, k := range ks {
			eqs = append(eqs, &Equation{Lhs: x.Keys[k], Rhs: x.Vals[k]})
		}
		return []Value{&Func{Head: Name{"table"}, Args: []Value{List{eqs}}}}
	default:
		// atomic: op(x) == x
		return []Value{v}
	}
}

// ---- op / nops --------------------------------------------------------------

func biOp(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "op"); err != nil {
		return nil, err
	}
	// op(expr): all operands; op(i,expr): i-th; op(i..j,expr): slice; op(0,expr): head
	if len(args) == 1 {
		return seqOrSingle(operands(it.derefTable(args[0]))), nil
	}
	sel := args[0]
	expr := it.derefTable(args[1])
	if i, ok := intVal(sel); ok {
		if i.Sign() == 0 {
			return op0(expr), nil
		}
		ops := operands(expr)
		idx := normIndex(int(i.Int64()), len(ops))
		if idx < 1 || idx > len(ops) {
			return nil, newMapleError("op: index out of range")
		}
		return ops[idx-1], nil
	}
	if rng, ok := sel.(*Range); ok {
		ops := operands(expr)
		lo, _ := intVal(rng.Lo)
		hi, hok := intVal(rng.Hi)
		loI := 1
		if lo != nil {
			loI = normIndex(int(lo.Int64()), len(ops))
		}
		hiI := len(ops)
		if hok {
			hiI = normIndex(int(hi.Int64()), len(ops))
		}
		if loI < 1 {
			loI = 1
		}
		if hiI > len(ops) {
			hiI = len(ops)
		}
		if loI > hiI {
			return NULL(), nil
		}
		return seqOrSingle(ops[loI-1 : hiI]), nil
	}
	if lst, ok := sel.(List); ok {
		// op([i,j],expr) nested op selection
		cur := expr
		for _, idxv := range lst.Items {
			r, err := biOp(it, []Value{idxv, cur})
			if err != nil {
				return nil, err
			}
			cur = r
		}
		return cur, nil
	}
	return nil, newMapleError("op: unsupported selector")
}

// op0 returns op(0, expr): the head/type indicator.
func op0(v Value) Value {
	switch x := v.(type) {
	case *Func:
		return x.Head
	case *Indexed:
		return x.Head
	case *Sum:
		return Name{"+"}
	case *Prod:
		return Name{"*"}
	case *Power:
		return Name{"^"}
	case List:
		return Name{"list"}
	case Set:
		return Name{"set"}
	default:
		return v
	}
}

func biNops(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "nops"); err != nil {
		return nil, err
	}
	switch x := args[0].(type) {
	case List:
		return newInt(int64(len(x.Items))), nil
	case Set:
		return newInt(int64(len(x.Items))), nil
	case Seq:
		return newInt(int64(len(x.Items))), nil
	case *Sum:
		return newInt(int64(len(x.Terms))), nil
	case *Prod:
		return newInt(int64(len(x.Factors))), nil
	case *Power:
		return newInt(2), nil
	case *Func:
		return newInt(int64(len(x.Args))), nil
	case *Indexed:
		return newInt(int64(len(x.Idx))), nil
	case *Equation, *Relation, *Range:
		return newInt(2), nil
	default:
		return newInt(1), nil
	}
}

// ---- type -------------------------------------------------------------------

func biType(it *Interp, args []Value) (Value, error) {
	// type(NULL, T): the NULL first operand is dropped by exprseq flattening, so
	// the call arrives with one arg (the type). Maple's type(NULL, T) is false
	// for every concrete type T (NULL is the empty sequence). DT relies on this
	// via `type(node['Degree'], table)` where Degree is stored as NULL.
	if len(args) == 1 {
		return vFalse, nil
	}
	if err := need(args, 2, "type"); err != nil {
		return nil, err
	}
	// the second argument is the (unevaluated) type expr; but here it has been
	// evaluated to a Name/String/structured value. Reconstruct a type check.
	ok, err := it.checkTypeValue(args[0], args[1])
	if err != nil {
		return nil, err
	}
	return mkBool(ok), nil
}

// checkTypeValue checks a value against a type given as a *value* (Name, String,
// Func like list(symbol), Set of alternatives). Used by the type() builtin.
func (it *Interp) checkTypeValue(v Value, typ Value) (bool, error) {
	switch t := typ.(type) {
	case Name:
		return it.checkNamedTypeV(v, t.Val, nil)
	case MString:
		return it.checkNamedTypeV(v, t.Val, nil)
	case *Func:
		hn, _ := nameOrStr(t.Head)
		return it.checkNamedTypeV(v, hn, t.Args)
	case *Indexed:
		hn, _ := nameOrStr(t.Head)
		return it.checkNamedTypeV(v, hn, t.Idx)
	case Set:
		for _, alt := range t.Items {
			ok, err := it.checkTypeValue(v, alt)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case *Equation:
		_, ok := v.(*Equation)
		return ok, nil
	default:
		return false, nil
	}
}

// checkNamedTypeV is checkNamedType but with value-form structured params.
func (it *Interp) checkNamedTypeV(v Value, name string, params []Value) (bool, error) {
	if proc, ok := it.typeProcs[name]; ok {
		res, err := it.applyValue(proc, []Value{v}, nil)
		if err != nil {
			return false, err
		}
		return truth(res) == bTrue, nil
	}
	if pv, ok := it.lookup("type/" + name); ok {
		if p, ok := pv.(*Proc); ok {
			res, err := it.callProc(p, []Value{v})
			if err != nil {
				return false, err
			}
			return truth(res) == bTrue, nil
		}
	}
	// structured collection types with value params: check elements
	switch name {
	case "list", "listlist":
		l, ok := v.(List)
		if !ok {
			return false, nil
		}
		if name == "listlist" {
			for _, e := range l.Items {
				if _, isL := e.(List); !isL {
					return false, nil
				}
			}
		}
		return it.checkElemsV(l.Items, params)
	case "set":
		s, ok := v.(Set)
		if !ok {
			return false, nil
		}
		return it.checkElemsV(s.Items, params)
	}
	// otherwise no structured params apply; reuse the tree-based atom checks via
	// a synthetic node.
	return it.checkNamedType(v, name, nil)
}

func (it *Interp) checkElemsV(items []Value, params []Value) (bool, error) {
	if len(params) == 0 {
		return true, nil
	}
	et := params[0]
	for _, e := range items {
		ok, err := it.checkTypeValue(e, et)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// ---- map / select / remove --------------------------------------------------

func (it *Interp) applyFn(fn Value, args []Value) (Value, error) {
	return it.applyValue(fn, args, nil)
}

func biMap(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "map"); err != nil {
		return nil, err
	}
	fn := args[0]
	coll := args[1]
	extra := args[2:]
	apply := func(e Value) (Value, error) {
		return it.applyFn(fn, append([]Value{e}, extra...))
	}
	return mapOver(coll, apply)
}

func biMap2(it *Interp, args []Value) (Value, error) {
	if err := need(args, 3, "map2"); err != nil {
		return nil, err
	}
	fn := args[0]
	first := args[1]
	coll := args[2]
	extra := args[3:]
	apply := func(e Value) (Value, error) {
		callArgs := append([]Value{first, e}, extra...)
		return it.applyFn(fn, callArgs)
	}
	return mapOver(coll, apply)
}

func mapOver(coll Value, apply func(Value) (Value, error)) (Value, error) {
	switch c := coll.(type) {
	case List:
		// Maple's map builds a container from f(item) results. A NULL result
		// (empty sequence) collapses during list construction, and a Seq result
		// flattens — so map(i->x$0,[1,2]) is [] (NOT [NULL,NULL]) and
		// map(i->(a,b),[1]) is [a,b]. JetList2Diff_Var depends on this:
		// map(i->IVar[i]$LD[i], ...) yields [] when all derivation orders are 0.
		var out []Value
		for _, e := range c.Items {
			r, err := apply(e)
			if err != nil {
				return nil, err
			}
			out = appendFlattenNonNull(out, r)
		}
		return List{out}, nil
	case Set:
		var out []Value
		for _, e := range c.Items {
			r, err := apply(e)
			if err != nil {
				return nil, err
			}
			out = appendFlattenNonNull(out, r)
		}
		return makeSet(out), nil
	case Seq:
		var out []Value
		for _, e := range c.Items {
			r, err := apply(e)
			if err != nil {
				return nil, err
			}
			out = appendFlattenNonNull(out, r)
		}
		return seqOrSingle(out), nil
	case *Sum:
		var out []Value
		for _, e := range c.Terms {
			r, err := apply(e)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return simplifySum(out), nil
	case *Prod:
		var out []Value
		for _, e := range c.Factors {
			r, err := apply(e)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return simplifyProd(out), nil
	case *Func:
		out := make([]Value, len(c.Args))
		for i, e := range c.Args {
			r, err := apply(e)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return &Func{Head: c.Head, Args: out}, nil
	default:
		// map over an atom applies once
		return apply(coll)
	}
}

func filterColl(it *Interp, fn Value, coll Value, extra []Value, keepTrue bool) (Value, []Value, error) {
	items, ok := itemsOf(coll)
	isList := false
	if l, isL := coll.(List); isL {
		items = l.Items
		isList = true
		ok = true
	}
	if !ok {
		return nil, nil, newMapleError("select/remove expects a list or set")
	}
	var kept, dropped []Value
	for _, e := range items {
		res, err := it.applyFn(fn, append([]Value{e}, extra...))
		if err != nil {
			return nil, nil, err
		}
		if truth(res) == bTrue {
			kept = append(kept, e)
		} else {
			dropped = append(dropped, e)
		}
	}
	wrap := func(xs []Value) Value {
		if isList {
			return List{xs}
		}
		return makeSet(xs)
	}
	if keepTrue {
		return wrap(kept), dropped, nil
	}
	return wrap(dropped), kept, nil
}

func biSelect(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "select"); err != nil {
		return nil, err
	}
	v, _, err := filterColl(it, args[0], args[1], args[2:], true)
	return v, err
}

func biRemove(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "remove"); err != nil {
		return nil, err
	}
	v, _, err := filterColl(it, args[0], args[1], args[2:], false)
	return v, err
}

func biSelectRemove(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "selectremove"); err != nil {
		return nil, err
	}
	items, ok := itemsOf(args[1])
	isList := false
	if l, isL := args[1].(List); isL {
		items = l.Items
		isList = true
		ok = true
	}
	if !ok {
		return nil, newMapleError("selectremove expects a list or set")
	}
	var sel, rem []Value
	for _, e := range items {
		res, err := it.applyFn(args[0], append([]Value{e}, args[2:]...))
		if err != nil {
			return nil, err
		}
		if truth(res) == bTrue {
			sel = append(sel, e)
		} else {
			rem = append(rem, e)
		}
	}
	wrap := func(xs []Value) Value {
		if isList {
			return List{xs}
		}
		return makeSet(xs)
	}
	return Seq{[]Value{wrap(sel), wrap(rem)}}, nil
}

// ---- subs / subsop ----------------------------------------------------------

func biSubs(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "subs"); err != nil {
		return nil, err
	}
	expr := args[len(args)-1]
	subsArgs := args[:len(args)-1]
	// build substitution pairs from equations / lists/sets of equations
	var pairs [][2]Value
	for _, s := range subsArgs {
		collectSubs(s, &pairs)
	}
	return substitute(expr, pairs), nil
}

func collectSubs(s Value, pairs *[][2]Value) {
	switch v := s.(type) {
	case *Equation:
		*pairs = append(*pairs, [2]Value{v.Lhs, v.Rhs})
	case List:
		for _, e := range v.Items {
			collectSubs(e, pairs)
		}
	case Set:
		for _, e := range v.Items {
			collectSubs(e, pairs)
		}
	case Seq:
		for _, e := range v.Items {
			collectSubs(e, pairs)
		}
	}
}

func substitute(expr Value, pairs [][2]Value) Value {
	for _, p := range pairs {
		if equalValues(expr, p[0]) {
			return p[1]
		}
	}
	switch x := expr.(type) {
	case List:
		return List{substList(x.Items, pairs)}
	case Set:
		return makeSet(substList(x.Items, pairs))
	case Seq:
		return seqOrSingle(substList(x.Items, pairs))
	case *Sum:
		return simplifySum(substList(x.Terms, pairs))
	case *Prod:
		return simplifyProd(substList(x.Factors, pairs))
	case *Power:
		return &Power{Base: substitute(x.Base, pairs), Exp: substitute(x.Exp, pairs)}
	case *Func:
		return &Func{Head: substitute(x.Head, pairs), Args: substList(x.Args, pairs)}
	case *Indexed:
		return &Indexed{Head: substitute(x.Head, pairs), Idx: substList(x.Idx, pairs)}
	case *Range:
		return &Range{Lo: substitute(x.Lo, pairs), Hi: substitute(x.Hi, pairs)}
	case *Equation:
		return &Equation{Lhs: substitute(x.Lhs, pairs), Rhs: substitute(x.Rhs, pairs)}
	case *Relation:
		return &Relation{Op: x.Op, Lhs: substitute(x.Lhs, pairs), Rhs: substitute(x.Rhs, pairs)}
	default:
		return expr
	}
}

func substList(items []Value, pairs [][2]Value) []Value {
	out := make([]Value, len(items))
	for i, e := range items {
		out[i] = substitute(e, pairs)
	}
	return out
}

func biSubsop(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "subsop"); err != nil {
		return nil, err
	}
	expr := args[len(args)-1]
	repls := args[:len(args)-1]
	ops := operands(expr)
	newOps := append([]Value{}, ops...)
	// deleted tracks operand positions (0-based) that subsop(i=NULL) removes.
	// In Maple, substituting an operand with NULL deletes it; all index
	// arguments refer to positions in the *original* operand list, so we
	// record substitutions/deletions first and apply the deletions after.
	deleted := make([]bool, len(newOps))
	var head Value
	for _, r := range repls {
		eq, ok := r.(*Equation)
		if !ok {
			return nil, newMapleError("subsop expects equations")
		}
		idx, ok := intVal(eq.Lhs)
		if !ok {
			return nil, newMapleError("subsop index must be an integer")
		}
		i := int(idx.Int64())
		if i == 0 {
			head = eq.Rhs
			continue
		}
		i = normIndex(i, len(newOps))
		if i < 1 || i > len(newOps) {
			return nil, newMapleError("subsop index out of range")
		}
		if isNULL(eq.Rhs) {
			deleted[i-1] = true
		} else {
			newOps[i-1] = eq.Rhs
			deleted[i-1] = false
		}
	}
	filtered := newOps[:0:0]
	for j, v := range newOps {
		if !deleted[j] {
			filtered = append(filtered, v)
		}
	}
	return rebuild(expr, head, filtered), nil
}

// rebuild reconstructs a value of the same shape with new operands (and maybe a
// new head from subsop(0=...)).
func rebuild(orig, head Value, ops []Value) Value {
	switch x := orig.(type) {
	case List:
		return List{ops}
	case Set:
		return makeSet(ops)
	case Seq:
		return seqOrSingle(ops)
	case *Sum:
		return simplifySum(ops)
	case *Prod:
		return simplifyProd(ops)
	case *Power:
		if len(ops) == 2 {
			return &Power{Base: ops[0], Exp: ops[1]}
		}
	case *Func:
		h := x.Head
		if head != nil {
			h = head
		}
		return &Func{Head: h, Args: ops}
	case *Indexed:
		h := x.Head
		if head != nil {
			h = head
		}
		return &Indexed{Head: h, Idx: ops}
	case *Range:
		if len(ops) == 2 {
			return &Range{Lo: ops[0], Hi: ops[1]}
		}
	case *Equation:
		if len(ops) == 2 {
			return &Equation{Lhs: ops[0], Rhs: ops[1]}
		}
	}
	if len(ops) == 1 {
		return ops[0]
	}
	return orig
}

// ---- seq / add / mul --------------------------------------------------------

// seq/add/mul are special forms (they bind a loop variable), handled in
// evalSpecialForm — not in the value-form builtins map.

func biZip(it *Interp, args []Value) (Value, error) {
	if err := need(args, 3, "zip"); err != nil {
		return nil, err
	}
	fn := args[0]
	a, aok := positional(args[1])
	b, bok := positional(args[2])
	if !aok || !bok {
		return nil, newMapleError("zip expects two lists")
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	out := make([]Value, n)
	for i := 0; i < n; i++ {
		r, err := it.applyFn(fn, []Value{a[i], b[i]})
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	if _, ok := args[1].(List); ok {
		return List{out}, nil
	}
	return List{out}, nil
}

// ---- table accessors --------------------------------------------------------

func biIndices(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "indices"); err != nil {
		return nil, err
	}
	t, err := it.asTable(args[0])
	if err != nil {
		return nil, err
	}
	nolist := hasName(args[1:], "nolist")
	var out []Value
	for _, k := range t.sortedKeys() {
		if nolist {
			out = append(out, t.Keys[k])
		} else {
			out = append(out, List{[]Value{t.Keys[k]}})
		}
	}
	return seqOrSingle(out), nil
}

func biEntries(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "entries"); err != nil {
		return nil, err
	}
	t, err := it.asTable(args[0])
	if err != nil {
		return nil, err
	}
	nolist := hasName(args[1:], "nolist")
	var out []Value
	for _, k := range t.sortedKeys() {
		if nolist {
			out = append(out, t.Vals[k])
		} else {
			out = append(out, List{[]Value{t.Vals[k]}})
		}
	}
	return seqOrSingle(out), nil
}

func hasName(args []Value, n string) bool {
	for _, a := range args {
		if nm, ok := a.(Name); ok && nm.Val == n {
			return true
		}
		if s, ok := a.(MString); ok && s.Val == n {
			return true
		}
	}
	return false
}

// asTable dereferences a name to its table (last-name-eval means tables arrive
// as Names).
func (it *Interp) asTable(v Value) (*Table, error) {
	if t, ok := v.(*Table); ok {
		return t, nil
	}
	if nm, ok := v.(Name); ok {
		if val, bound := it.lookup(nm.Val); bound {
			if t, ok := val.(*Table); ok {
				return t, nil
			}
		}
	}
	return nil, newMapleError("expected a table")
}

// ---- member / has / hastype -------------------------------------------------

func biMember(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "member"); err != nil {
		return nil, err
	}
	items, ok := positional(args[1])
	if !ok {
		return vFalse, nil
	}
	for i, e := range items {
		if equalValues(e, args[0]) {
			// member(x,L,'p') assigns the position to p — needs special form;
			// the 2-arg form just returns true/false.
			if len(args) >= 3 {
				if nm, ok := args[2].(Name); ok {
					it.store(nm.Val, newInt(int64(i+1)))
				}
			}
			return vTrue, nil
		}
	}
	return vFalse, nil
}

func biHas(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "has"); err != nil {
		return nil, err
	}
	return mkBool(hasSub(args[0], args[1])), nil
}

func hasSub(expr, target Value) bool {
	if equalValues(expr, target) {
		return true
	}
	for _, o := range operandsDeep(expr) {
		if hasSub(o, target) {
			return true
		}
	}
	return false
}

func operandsDeep(v Value) []Value {
	switch v.(type) {
	case Integer, Rational, Float, Name, MString, Boolean:
		return nil
	}
	return operands(v)
}

func biHasType(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "hastype"); err != nil {
		return nil, err
	}
	var found bool
	var walk func(Value) error
	walk = func(e Value) error {
		ok, err := it.checkTypeValue(e, args[1])
		if err != nil {
			return err
		}
		if ok {
			found = true
			return nil
		}
		for _, o := range operandsDeep(e) {
			if err := walk(o); err != nil {
				return err
			}
			if found {
				return nil
			}
		}
		return nil
	}
	if err := walk(args[0]); err != nil {
		return nil, err
	}
	return mkBool(found), nil
}

// ---- copy / eval / assigned -------------------------------------------------

func biCopy(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "copy"); err != nil {
		return nil, err
	}
	return deepCopy(it, args[0]), nil
}

func deepCopy(it *Interp, v Value) Value {
	switch x := v.(type) {
	case *Table:
		nt := newTable()
		for k, kv := range x.Keys {
			nt.Keys[k] = kv
			nt.Vals[k] = deepCopy(it, x.Vals[k])
		}
		return nt
	case Name:
		// copy of a name bound to a table copies the table
		if val, bound := it.lookup(x.Val); bound {
			if t, ok := val.(*Table); ok {
				return deepCopy(it, t)
			}
		}
		return v
	case List:
		out := make([]Value, len(x.Items))
		for i := range x.Items {
			out[i] = deepCopy(it, x.Items[i])
		}
		return List{out}
	default:
		return v
	}
}

func biEval(it *Interp, args []Value) (Value, error) {
	// eval() with no args: the operand evaluated to NULL and was dropped by
	// exprseq flattening (e.g. eval(q) where q holds NULL). Maple: eval(NULL) =
	// NULL.
	if len(args) == 0 {
		return NULL(), nil
	}
	// eval(name) -> full evaluation (dereference, including tables/procs)
	if len(args) == 1 {
		if nm, ok := args[0].(Name); ok {
			if val, bound := it.lookup(nm.Val); bound {
				return val, nil
			}
		}
		return args[0], nil
	}
	// eval(expr, eqs) -> substitute then evaluate
	var pairs [][2]Value
	collectSubs(args[1], &pairs)
	return substitute(args[0], pairs), nil
}

// assigned is a special form (see evalSpecialForm): it must not error on an
// unassigned table index.

// ---- lhs / rhs / evalb ------------------------------------------------------

func biLhs(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "lhs"); err != nil {
		return nil, err
	}
	switch x := args[0].(type) {
	case *Equation:
		return x.Lhs, nil
	case *Relation:
		return x.Lhs, nil
	case *Range:
		return x.Lo, nil
	}
	return nil, newMapleError("lhs: not a relation")
}

func biRhs(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "rhs"); err != nil {
		return nil, err
	}
	switch x := args[0].(type) {
	case *Equation:
		return x.Rhs, nil
	case *Relation:
		return x.Rhs, nil
	case *Range:
		return x.Hi, nil
	}
	return nil, newMapleError("rhs: not a relation")
}

func biEvalb(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "evalb"); err != nil {
		return nil, err
	}
	v := args[0]
	if b, ok := v.(Boolean); ok {
		return b, nil
	}
	if eq, ok := v.(*Equation); ok {
		return mkBool(equalValues(eq.Lhs, eq.Rhs)), nil
	}
	if rel, ok := v.(*Relation); ok {
		// numeric relations already reduced; leave others as-is
		r, err := it.compareRel(rel.Op, rel.Lhs, rel.Rhs)
		if err == nil {
			if b, ok := r.(Boolean); ok {
				return b, nil
			}
		}
	}
	switch truth(v) {
	case bTrue:
		return vTrue, nil
	case bFalse:
		return vFalse, nil
	}
	return v, nil
}

// ---- cat --------------------------------------------------------------------

func biCat(it *Interp, args []Value) (Value, error) {
	if len(args) == 0 {
		return Name{""}, nil
	}
	anyStr := false
	var b strings.Builder
	for _, a := range args {
		if _, ok := a.(MString); ok {
			anyStr = true
		}
		b.WriteString(plainText(a))
	}
	if anyStr {
		return MString{b.String()}, nil
	}
	return Name{b.String()}, nil
}

// ---- convert ----------------------------------------------------------------

func biConvert(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "convert"); err != nil {
		return nil, err
	}
	target, _ := nameOrStr(args[1])
	switch target {
	case "string":
		// 1-D linear form (no operator spacing). This is what DT's FactorSorter
		// byte-compares, so it must match Maple's lprint/convert-string surface.
		return MString{lprint1D(args[0])}, nil
	case "symbol":
		return Name{plainText(args[0])}, nil
	case "name":
		return Name{plainText(args[0])}, nil
	case "list":
		items, ok := positional(args[0])
		if !ok {
			if t, ok := args[0].(*Table); ok {
				var out []Value
				for _, k := range t.sortedKeys() {
					out = append(out, t.Vals[k])
				}
				return List{out}, nil
			}
			return List{[]Value{args[0]}}, nil
		}
		return List{append([]Value{}, items...)}, nil
	case "set":
		items, ok := positional(args[0])
		if !ok {
			return makeSet([]Value{args[0]}), nil
		}
		return makeSet(items), nil
	case "+", "`+`":
		// convert(L, `+`): sum the operands. The backtick name `+` arrives here
		// with backticks already stripped (Name{"+"}), so match the bare form;
		// keep the backticked label too for safety.
		items, ok := positional(args[0])
		if !ok {
			items = []Value{args[0]}
		}
		return simplifySum(items), nil
	case "*", "`*`":
		items, ok := positional(args[0])
		if !ok {
			items = []Value{args[0]}
		}
		return simplifyProd(items), nil
	default:
		// algebraic conversions (Matrix, Vector, polynom, ...) -> CAS
		if isCASOp("convert") || target == "Matrix" || target == "Vector" {
			return it.cas.Call("convert", args)
		}
		return nil, errCAS("convert/" + target)
	}
}

// ---- args / nargs -----------------------------------------------------------

func biNargs(it *Interp, args []Value) (Value, error) {
	if it.scope == nil {
		return newInt(0), nil
	}
	return newInt(int64(len(it.scope.args))), nil
}

func biArgs(it *Interp, args []Value) (Value, error) {
	if it.scope == nil {
		return NULL(), nil
	}
	return Seq{append([]Value{}, it.scope.args...)}, nil
}

// ---- errors / info ----------------------------------------------------------

func biERROR(it *Interp, args []Value) (Value, error) {
	return nil, mapleErrorFromArgs(args)
}

func biUserinfo(it *Interp, args []Value) (Value, error) {
	return NULL(), nil // userinfo prints diagnostics; NOP at infolevel 0
}

func biASSERT(it *Interp, args []Value) (Value, error) {
	if it.assertLvl == 0 {
		return NULL(), nil
	}
	if len(args) >= 1 && truth(args[0]) != bTrue {
		msg := "assertion failed"
		if len(args) >= 2 {
			if s, ok := strVal(args[1]); ok {
				msg = s
			}
		}
		return nil, newMapleError(msg)
	}
	return NULL(), nil
}

func biWarning(it *Interp, args []Value) (Value, error) {
	return NULL(), nil
}

func biKernelopts(it *Interp, args []Value) (Value, error) {
	for _, a := range args {
		if eq, ok := a.(*Equation); ok {
			if nm, ok := eq.Lhs.(Name); ok && nm.Val == "assertlevel" {
				if lv, ok := intVal(eq.Rhs); ok {
					it.assertLvl = int(lv.Int64())
				}
			}
		}
		if nm, ok := a.(Name); ok && nm.Val == "assertlevel" {
			return newInt(int64(it.assertLvl)), nil
		}
	}
	return NULL(), nil
}

// ---- print / printf ---------------------------------------------------------

func biPrint(it *Interp, args []Value) (Value, error) {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = printValue(a)
	}
	it.out.WriteString(strings.Join(parts, ", "))
	it.out.WriteString("\n")
	return NULL(), nil
}

func biPrintf(it *Interp, args []Value) (Value, error) {
	if len(args) == 0 {
		return NULL(), nil
	}
	format, _ := strVal(args[0])
	it.out.WriteString(mapleSprintf(format, args[1:]))
	return NULL(), nil
}

func biSprintf(it *Interp, args []Value) (Value, error) {
	if len(args) == 0 {
		return MString{""}, nil
	}
	format, _ := strVal(args[0])
	return MString{mapleSprintf(format, args[1:])}, nil
}

func biNprintf(it *Interp, args []Value) (Value, error) {
	if len(args) == 0 {
		return Name{""}, nil
	}
	format, _ := strVal(args[0])
	return Name{mapleSprintf(format, args[1:])}, nil
}

// mapleSprintf implements the subset of Maple printf format specifiers DT uses:
// %s, %d, %a, %g, %f, %%, \n.
func mapleSprintf(format string, args []Value) string {
	var b strings.Builder
	ai := 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c == '%' && i+1 < len(format) {
			// consume an optional flags/width/precision run, e.g. -5, 08, .1, +.3
			mods := ""
			j := i + 1
			for j < len(format) && strings.IndexByte("-+ 0#.0123456789", format[j]) >= 0 {
				mods += string(format[j])
				j++
			}
			if j >= len(format) {
				b.WriteByte('%')
				continue
			}
			i = j
			spec := format[i]
			switch spec {
			case '%':
				b.WriteByte('%')
			case 's':
				if ai < len(args) {
					b.WriteString(fmt.Sprintf("%"+mods+"s", plainText(args[ai])))
					ai++
				}
			case 'a', 'q', 'v':
				if ai < len(args) {
					b.WriteString(fmt.Sprintf("%"+mods+"s", printValue(args[ai])))
					ai++
				}
			case 'd':
				if ai < len(args) {
					if mods == "" {
						b.WriteString(plainText(args[ai]))
					} else if iv, ok := intVal(args[ai]); ok {
						b.WriteString(fmt.Sprintf("%"+mods+"s", iv.String()))
					} else {
						b.WriteString(plainText(args[ai]))
					}
					ai++
				}
			case 'g', 'f', 'e':
				if ai < len(args) {
					if f, ok := toFloat(args[ai]); ok {
						b.WriteString(fmt.Sprintf("%"+mods+string(spec), f))
					} else {
						b.WriteString(printValue(args[ai]))
					}
					ai++
				}
			default:
				b.WriteByte('%')
				b.WriteString(mods)
				b.WriteByte(spec)
			}
		} else if c == '\\' && i+1 < len(format) {
			i++
			switch format[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(format[i])
			}
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func biTime(it *Interp, args []Value) (Value, error) {
	return Float{0}, nil // stub
}

// ---- sort -------------------------------------------------------------------

func biSort(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "sort"); err != nil {
		return nil, err
	}
	items, ok := positional(args[0])
	if !ok {
		return args[0], nil
	}
	out := append([]Value{}, items...)
	if len(args) >= 2 {
		// custom comparator proc: returns true if a should come before b
		cmp := args[1]
		// guard: only treat as comparator if it's a proc/name; else (a string
		// option like "ascending") fall back to default.
		if isCallable(it, cmp) {
			var cmpErr error
			stableSort(out, func(a, b Value) bool {
				if cmpErr != nil {
					return false
				}
				r, err := it.applyFn(cmp, []Value{a, b})
				if err != nil {
					cmpErr = err
					return false
				}
				return truth(r) == bTrue
			})
			if cmpErr != nil {
				return nil, cmpErr
			}
			return rewrap(args[0], out), nil
		}
	}
	sortValuesDefault(out)
	return rewrap(args[0], out), nil
}

func isCallable(it *Interp, v Value) bool {
	switch x := v.(type) {
	case *Proc, *Builtin:
		return true
	case Name:
		if _, ok := it.builtins[x.Val]; ok {
			return true
		}
		if val, bound := it.lookup(x.Val); bound {
			if _, ok := val.(*Proc); ok {
				return true
			}
		}
	}
	return false
}

// stableSort is an insertion sort using a "less" predicate (stable, and works
// with a possibly-non-total user comparator).
func stableSort(items []Value, less func(a, b Value) bool) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && less(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// ---- numeric helpers --------------------------------------------------------

func biMax(it *Interp, args []Value) (Value, error) {
	return reduceNum(args, func(a, b *big.Rat) bool { return a.Cmp(b) >= 0 }, "max")
}

func biMin(it *Interp, args []Value) (Value, error) {
	return reduceNum(args, func(a, b *big.Rat) bool { return a.Cmp(b) <= 0 }, "min")
}

func reduceNum(args []Value, keep func(a, b *big.Rat) bool, name string) (Value, error) {
	// Maple's max/min flatten list/set arguments: max([a,b],c) ranges over a,b,c.
	// DT relies on this (e.g. `max(map(a->a[currentivar],leafs))` in
	// FactorModuleBasisFromTreeRecursive, where the map yields a one-element list
	// — without flattening, max returns the list itself and downstream
	// `maxdeg+1` is an unsimplified list+int Sum). Flatten one level.
	if len(args) > 0 {
		flat := make([]Value, 0, len(args))
		for _, a := range args {
			switch c := a.(type) {
			case List:
				flat = append(flat, c.Items...)
			case Set:
				flat = append(flat, c.Items...)
			default:
				flat = append(flat, a)
			}
		}
		args = flat
	}
	if len(args) == 0 {
		// Maple: max() = -infinity, min() = infinity (the identity for the
		// respective fold). DT relies on max() over an empty index list.
		if name == "max" {
			return &Prod{[]Value{newInt(-1), Name{"infinity"}}}, nil
		}
		return Name{"infinity"}, nil
	}
	best := args[0]
	br, ok := toNumRat(best)
	if !ok {
		return args[0], nil
	}
	for _, a := range args[1:] {
		ar, ok := toNumRat(a)
		if !ok {
			continue
		}
		if !keep(br, ar) {
			best = a
			br = ar
		}
	}
	return best, nil
}

func biAbs(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "abs"); err != nil {
		return nil, err
	}
	switch n := args[0].(type) {
	case Integer:
		return Integer{new(big.Int).Abs(n.Val)}, nil
	case Rational:
		return normRat(new(big.Rat).Abs(n.Val)), nil
	case Float:
		if n.Val < 0 {
			return Float{-n.Val}, nil
		}
		return n, nil
	}
	return &Func{Head: Name{"abs"}, Args: args}, nil
}

func biIgcd(it *Interp, args []Value) (Value, error) {
	g := big.NewInt(0)
	for _, a := range args {
		i, ok := intVal(a)
		if !ok {
			return nil, newMapleError("igcd: non-integer")
		}
		g.GCD(nil, nil, g, new(big.Int).Abs(i))
	}
	return Integer{g}, nil
}

func biModp(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "modp"); err != nil {
		return nil, err
	}
	return it.arithMod(args[0], args[1])
}

func biEvalf(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "evalf"); err != nil {
		return nil, err
	}
	if f, ok := toFloat(args[0]); ok {
		return Float{f}, nil
	}
	return args[0], nil
}

func biTable(it *Interp, args []Value) (Value, error) {
	t := newTable()
	// table([k1=v1, k2=v2, ...]) or table(symmetric, [...]) etc.
	//
	// Each entry value goes through resolveRefForStore for the same reason the
	// indexed-assignment path does (see assignIndexed / resolveRefForStore): a
	// table/proc-valued RHS is a Maple reference type, but last-name-eval makes a
	// name bound to a table/proc evaluate to the bare name. Storing that bare
	// name into the new table drops the binding — once the constructing proc's
	// scope is gone the name is unbound, so a later index/call falls through to an
	// inert Func/Indexed. Concretely, CreateJanetTreesObject does
	// `table(['Ranking'=ranking])` with the proc-local `ranking` table; without
	// the deref the treeobject's Ranking ends up as the dangling name `ranking`,
	// and `treeobject['Ranking']['IsDifferentialVariable'](...)` evaluates to an
	// inert `ranking[IsDifferentialVariable](...)` instead of a boolean — tripping
	// the "no differential variable as leader" ASSERT in InsertIntoJanetTrees.
	set := func(k, v Value) { t.set(k, it.resolveRefForStore(v)) }
	for _, a := range args {
		switch x := a.(type) {
		case List:
			for _, e := range x.Items {
				if eq, ok := e.(*Equation); ok {
					set(eq.Lhs, eq.Rhs)
				}
			}
		case Set:
			for _, e := range x.Items {
				if eq, ok := e.(*Equation); ok {
					set(eq.Lhs, eq.Rhs)
				}
			}
		case *Equation:
			set(x.Lhs, x.Rhs)
		}
	}
	return t, nil
}

// biArray implements Maple's array(...) data-structure constructor. It is NOT a
// computer-algebra op: array(a..b) builds a mutable container indexed a..b whose
// entries are assigned and read by integer index (DT's InitializeResultant does
// ResultantData['SubResultant']:=array(0..n) then [i]:=...). A *Table models this
// exactly — arbitrary integer keys, assign/read in place. Dimension ranges are
// accepted (and ignored: entries materialise on assignment); an optional list
// initializer fills entries from the range's lower bound upward.
func biArray(it *Interp, args []Value) (Value, error) {
	t := newTable()
	var lo *big.Int // lower bound of the first range, for list-initializer indexing
	for _, a := range args {
		switch x := a.(type) {
		case *Range:
			if lo == nil {
				if l, ok := intVal(x.Lo); ok {
					lo = l
				}
			}
		case List:
			start := big.NewInt(1)
			if lo != nil {
				start = lo
			}
			for i, e := range x.Items {
				idx := new(big.Int).Add(start, big.NewInt(int64(i)))
				t.set(Integer{idx}, it.resolveRefForStore(e))
			}
		case *Equation:
			// array(0..n, [i=v, ...]) style or direct index=value
			t.set(x.Lhs, it.resolveRefForStore(x.Rhs))
		}
	}
	return t, nil
}

// biWith implements with(Package): load a package and bring its exports into
// scope. Only DifferentialThomas is supported — it loads the source (if not
// already) and registers the public API names. Returns NULL (Maple returns the
// export list; the example programs end the statement with ':' so it's unused).
func biWith(it *Interp, args []Value) (Value, error) {
	if len(args) == 0 {
		return NULL(), nil
	}
	name, _ := nameOrStr(args[0])
	if name == "DifferentialThomas" {
		// load only once: ComputeRanking already bound means it's loaded.
		if _, ok := it.globals["DifferentialThomas/ComputeRanking"]; !ok {
			if err := it.LoadDifferentialThomas(defaultDTSrcDir()); err != nil {
				return nil, err
			}
		}
		it.registerDTPublicAPI()
		return NULL(), nil
	}
	return nil, newMapleError("with: unsupported package " + name)
}

// biWhattype returns Maple's structural type name of its argument (the head /
// constructor): integer, fraction, string, list, set, exprseq, `+`, `*`, `^`,
// function, indexed, `=`, symbol, etc.
func biWhattype(it *Interp, args []Value) (Value, error) {
	if len(args) != 1 {
		return nil, newMapleError("whattype expects one argument")
	}
	return Name{whatType(args[0])}, nil
}

func whatType(v Value) string {
	switch v.(type) {
	case Integer:
		return "integer"
	case Rational:
		return "fraction"
	case Float:
		return "float"
	case MString:
		return "string"
	case Boolean:
		return "symbol" // true/false are symbols in Maple's whattype
	case Name:
		return "symbol"
	case List:
		return "list"
	case Set:
		return "set"
	case Seq:
		return "exprseq"
	case *Sum:
		return "+"
	case *Prod:
		return "*"
	case *Power:
		return "^"
	case *Func:
		return "function"
	case *Indexed:
		return "indexed"
	case *Equation:
		return "="
	case *Range:
		return ".."
	case *Table:
		return "table"
	case *Proc:
		return "procedure"
	}
	return "symbol"
}

func biSubsindets(it *Interp, args []Value) (Value, error) {
	return nil, errCAS("subsindets")
}

func biPiecewise(it *Interp, args []Value) (Value, error) {
	// piecewise(cond1, val1, cond2, val2, ..., default?)
	i := 0
	for i+1 < len(args) {
		if truth(args[i]) == bTrue {
			return args[i+1], nil
		}
		i += 2
	}
	if i < len(args) {
		return args[i], nil // default
	}
	return newInt(0), nil
}

// ---- ListTools / StringTools ------------------------------------------------

func biListSearch(it *Interp, args []Value) (Value, error) {
	if err := need(args, 2, "ListTools:-Search"); err != nil {
		return nil, err
	}
	items, ok := positional(args[1])
	if !ok {
		return newInt(0), nil
	}
	for i, e := range items {
		if equalValues(e, args[0]) {
			return newInt(int64(i + 1)), nil
		}
	}
	return newInt(0), nil
}

// biFindMaximalElement implements ListTools:-FindMaximalElement(L) and
// FindMaximalElement(L, 'position'). Returns the maximal element; with the
// `position` option it returns the sequence `maxElement, index` (1-based index
// of the first maximal element). DT uses
// `[ListTools[FindMaximalElement](subivar,position)][2]` to get the index of the
// largest sub-independent-variable degree (tree:247).
func biFindMaximalElement(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "ListTools:-FindMaximalElement"); err != nil {
		return nil, err
	}
	items, ok := positional(args[0])
	if !ok || len(items) == 0 {
		// Maple errors on an empty list; mirror with a Maple-style error so the
		// failure is legible rather than a Go panic.
		return nil, newMapleError("ListTools:-FindMaximalElement: empty or non-list argument")
	}
	wantPos := false
	for _, a := range args[1:] {
		if nm, ok := a.(Name); ok && nm.Val == "position" {
			wantPos = true
		}
	}
	maxIdx := 0
	for i := 1; i < len(items); i++ {
		if compareValues(items[i], items[maxIdx]) > 0 {
			maxIdx = i
		}
	}
	if wantPos {
		return Seq{Items: []Value{items[maxIdx], newInt(int64(maxIdx + 1))}}, nil
	}
	return items[maxIdx], nil
}

func biListReverse(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "ListTools:-Reverse"); err != nil {
		return nil, err
	}
	items, ok := positional(args[0])
	if !ok {
		return args[0], nil
	}
	out := make([]Value, len(items))
	for i, e := range items {
		out[len(items)-1-i] = e
	}
	return rewrap(args[0], out), nil
}

func biStringTrim(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "StringTools:-Trim"); err != nil {
		return nil, err
	}
	s, _ := strVal(args[0])
	return MString{strings.TrimSpace(s)}, nil
}

// ---- add_function / add_type (loading model) --------------------------------

func biAddFunction(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "add_function"); err != nil {
		return nil, err
	}
	f, ok := strVal(args[0])
	if !ok {
		return nil, newMapleError("add_function expects a string")
	}
	// functions_list['all'] := [op(functions_list['all']), f]
	fl := it.getOrCreateTable("functions_list")
	appendToTableList(fl, Name{"all"}, MString{f})

	if len(args) >= 2 {
		var pkgs []string
		switch x := args[1].(type) {
		case MString:
			pkgs = []string{x.Val}
		case Set:
			for _, e := range x.Items {
				if s, ok := strVal(e); ok {
					pkgs = append(pkgs, s)
				}
			}
		default:
			return nil, newMapleError("add_function: second arg must be string or set")
		}
		pl := it.getOrCreateSet("packages_list")
		for _, pkg := range pkgs {
			appendToTableList(fl, MString{pkg}, MString{f})
			it.globals["packages_list"] = setUnionName(pl, MString{pkg})
			pl = it.globals["packages_list"].(Set)
			// Record the short-name → qualified-name export so a package proc can
			// call a sibling by its bare name (Maple package-export semantics). The
			// qualified global is `<pkg>/<f>` (e.g. DifferentialThomas/JetList2Diff).
			// "all"/"<pkg>All" are bookkeeping pseudo-packages, not real prefixes —
			// skip them; the real package binds the canonical target.
			if it.exports != nil && pkg != "all" && !strings.HasSuffix(pkg, "All") {
				qualified := pkg + "/" + f
				if _, only := it.exports[f]; !only {
					it.exports[f] = qualified
				}
			}
		}
	}
	return NULL(), nil
}

func biAddType(it *Interp, args []Value) (Value, error) {
	if err := need(args, 1, "add_type"); err != nil {
		return nil, err
	}
	f, ok := strVal(args[0])
	if !ok {
		return nil, newMapleError("add_type expects a string")
	}
	tl, _ := it.globals["types_list"].(List)
	tl.Items = append(append([]Value{}, tl.Items...), MString{f})
	it.globals["types_list"] = tl
	return NULL(), nil
}

func (it *Interp) getOrCreateTable(name string) *Table {
	if v, ok := it.globals[name]; ok {
		if t, ok := v.(*Table); ok {
			return t
		}
	}
	t := newTable()
	it.globals[name] = t
	return t
}

func (it *Interp) getOrCreateSet(name string) Set {
	if v, ok := it.globals[name]; ok {
		if s, ok := v.(Set); ok {
			return s
		}
	}
	s := Set{}
	it.globals[name] = s
	return s
}

func appendToTableList(t *Table, key Value, val Value) {
	cur, ok := t.get(key)
	if ok {
		if l, isL := cur.(List); isL {
			t.set(key, List{append(append([]Value{}, l.Items...), val)})
			return
		}
	}
	t.set(key, List{[]Value{val}})
}

func setUnionName(s Set, v Value) Set {
	return makeSet(append(append([]Value{}, s.Items...), v))
}
