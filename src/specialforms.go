package main

import (
	"math/big"
)

// evalSpecialForm handles builtins that must inspect or delay their argument
// AST rather than receive fully-evaluated values: seq/add/mul (loop binder),
// assigned (must not evaluate an undefined index to an error), parse (returns a
// value to evaluate). Returns handled=true if it took the call.
func (it *Interp) evalSpecialForm(name string, argNodes []*tree) (Value, bool, error) {
	switch name {
	case "seq":
		v, err := it.sfSeq(argNodes)
		return v, true, err
	case "add":
		v, err := it.sfAddMul(argNodes, true)
		return v, true, err
	case "mul":
		v, err := it.sfAddMul(argNodes, false)
		return v, true, err
	case "assigned":
		v, err := it.sfAssigned(argNodes)
		return v, true, err
	case "parse":
		v, err := it.sfParse(argNodes)
		return v, true, err
	case "ASSERT":
		// ASSERT(cond, msg, ...) — Maple evaluates nothing unless assertions are
		// enabled (kernelopts(assertlevel)). Crucially the message/diagnostic
		// args (which DT fills with PrintDifferentialSystem(...) etc.) must NOT
		// be evaluated at the default level, or eager evaluation hits code paths
		// Maple skips. Lazy by design.
		if it.assertLvl == 0 {
			return NULL(), true, nil
		}
		cond, err := it.eval(argNodes[0])
		if err != nil {
			return nil, true, err
		}
		if truth(cond) != bTrue {
			msg := "assertion failed"
			if len(argNodes) >= 2 {
				if mv, e := it.eval(argNodes[1]); e == nil {
					if s, ok := strVal(mv); ok {
						msg = s
					}
				}
			}
			return nil, true, newMapleError(msg)
		}
		return NULL(), true, nil
	case "userinfo":
		// userinfo(level, pkg, msg...) — diagnostic output only emitted when
		// infolevel is high enough. Default: evaluate nothing (the msg args
		// include expensive/fragile Print* calls). NOP.
		return NULL(), true, nil
	case "timelimit":
		// timelimit(t, expr) — evaluate expr, returning its value. We do not
		// enforce the wall limit (Maple raises "time expired" on timeout; DT
		// passes -1 = no limit on the decomposition path). Special form so expr
		// is evaluated lazily here, not as a pre-evaluated builtin arg.
		if len(argNodes) < 2 {
			return NULL(), true, nil
		}
		v, err := it.eval(argNodes[1])
		return v, true, err
	}
	return nil, false, nil
}

// sfSeq implements seq(expr, i = a..b), seq(expr, i in coll), seq(expr, n)?,
// and the bare seq(expr) over an exprseq (rare).
func (it *Interp) sfSeq(argNodes []*tree) (Value, error) {
	if len(argNodes) == 1 {
		// seq(f(i) $ i=...) collapses; or seq over operands — evaluate the expr
		v, err := it.eval(argNodes[0])
		if err != nil {
			return nil, err
		}
		return seqOrSingle([]Value{v}), nil
	}
	body := argNodes[0]
	binder := argNodes[1]
	return it.iterateBinder(binder, body, func(results []Value) (Value, error) {
		return seqOrSingle(results), nil
	})
}

func (it *Interp) sfAddMul(argNodes []*tree, isAdd bool) (Value, error) {
	if len(argNodes) == 1 {
		// add/mul over an evaluated collection
		v, err := it.eval(argNodes[0])
		if err != nil {
			return nil, err
		}
		items, ok := positional(v)
		if !ok {
			return v, nil
		}
		return it.fold(items, isAdd)
	}
	body := argNodes[0]
	binder := argNodes[1]
	return it.iterateBinder(binder, body, func(results []Value) (Value, error) {
		return it.fold(results, isAdd)
	})
}

func (it *Interp) fold(items []Value, isAdd bool) (Value, error) {
	var acc Value
	if isAdd {
		acc = newInt(0)
	} else {
		acc = newInt(1)
	}
	for _, e := range items {
		var err error
		if isAdd {
			acc, err = it.arithAdd(acc, e)
		} else {
			acc, err = it.arithMul(acc, e)
		}
		if err != nil {
			return nil, err
		}
	}
	return acc, nil
}

// iterateBinder evaluates a binder (i = a..b, i in coll) and runs the body for
// each binding, collecting the body values, then calls combine.
func (it *Interp) iterateBinder(binder, body *tree, combine func([]Value) (Value, error)) (Value, error) {
	// binder is either `i = a..b` (operate "=") or `i in coll`.
	var varName string
	var results []Value

	runOne := func() error {
		v, err := it.eval(body)
		if err != nil {
			return err
		}
		if s, ok := v.(Seq); ok {
			results = append(results, s.Items...)
		} else {
			results = append(results, v)
		}
		return nil
	}

	switch binder.group {
	case operate:
		if binder.value == "=" {
			varName = stripBacktick(binder.nodes[0].value)
			rng, err := it.eval(binder.nodes[1])
			if err != nil {
				return nil, err
			}
			r, ok := rng.(*Range)
			if !ok {
				return nil, newMapleError("seq/add binder range expected")
			}
			lo, lok := intVal(r.Lo)
			hi, hok := intVal(r.Hi)
			if !lok || !hok {
				return nil, errCAS("seq over non-integer range")
			}
			for i := new(big.Int).Set(lo); i.Cmp(hi) <= 0; i.Add(i, big.NewInt(1)) {
				it.store(varName, Integer{new(big.Int).Set(i)})
				if err := runOne(); err != nil {
					return nil, err
				}
			}
			return combine(results)
		}
		if binder.value == "in" {
			varName = stripBacktick(binder.nodes[0].value)
			coll, err := it.eval(binder.nodes[1])
			if err != nil {
				return nil, err
			}
			for _, item := range iterItems(coll) {
				it.store(varName, item)
				if err := runOne(); err != nil {
					return nil, err
				}
			}
			return combine(results)
		}
	}
	// binder is a plain expression: seq(expr, coll)?  Maple: seq(f, x) iterates
	// x's operands without a var (the body refers to nothing). Evaluate binder
	// as a collection and run body once per element with no binding.
	coll, err := it.eval(binder)
	if err != nil {
		return nil, err
	}
	for range iterItems(coll) {
		if err := runOne(); err != nil {
			return nil, err
		}
	}
	return combine(results)
}

// sfAssigned implements assigned(x) and assigned(t[k]) without erroring on an
// undefined index.
func (it *Interp) sfAssigned(argNodes []*tree) (Value, error) {
	if len(argNodes) != 1 {
		return nil, newMapleError("assigned expects one argument")
	}
	n := argNodes[0]
	switch n.group {
	case variable:
		name := stripBacktick(n.value)
		_, ok := it.lookup(name)
		return mkBool(ok), nil
	case indexNode:
		base := n.nodes[0]
		bn, ok := baseName(base)
		if !ok {
			return vFalse, nil
		}
		bn = stripBacktick(bn)
		cur, bound := it.lookup(bn)
		if !bound {
			return vFalse, nil
		}
		t, ok := cur.(*Table)
		if !ok {
			return vFalse, nil
		}
		idxVals, err := it.evalArgs(n.nodes[1:])
		if err != nil {
			return nil, err
		}
		_, has := t.get(seqOrSingle(idxVals))
		return mkBool(has), nil
	default:
		v, err := it.eval(n)
		if err != nil {
			return vFalse, nil
		}
		// assigned of a non-name expression: true if it evaluated to a value
		_, isName := v.(Name)
		return mkBool(!isName), nil
	}
}

// sfParse implements parse(string) and parse(string, statement): re-enter the
// tokenizer+parser on the string and evaluate the result. This is the
// metaprogramming primitive DT relies on (parse(cat("`Pkg/`",f,...))).
func (it *Interp) sfParse(argNodes []*tree) (Value, error) {
	if len(argNodes) == 0 {
		return nil, newMapleError("parse expects a string")
	}
	sv, err := it.eval(argNodes[0])
	if err != nil {
		return nil, err
	}
	src, ok := strVal(sv)
	if !ok {
		return nil, newMapleError("parse expects a string argument")
	}
	statementMode := false
	for _, opt := range argNodes[1:] {
		ov, err := it.eval(opt)
		if err != nil {
			return nil, err
		}
		if nm, ok := ov.(Name); ok && nm.Val == "statement" {
			statementMode = true
		}
	}
	root, err := frontEnd(src)
	if err != nil {
		return nil, newMapleError("parse error: " + err.Error())
	}
	if statementMode {
		// execute the statement(s); return NULL (parse(...,statement) is used
		// for its side effects: assignments).
		_, err := it.execBlock(root.nodes)
		return NULL(), err
	}
	// expression mode: return the (unevaluated-then-evaluated) value of the
	// single expression.
	return it.execBlock(root.nodes)
}
