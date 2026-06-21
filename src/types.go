package main

import (
	"math/big"
	"strings"
)

// checkType evaluates whether value v satisfies the type expression node t.
// Maple type expressions used by DT: symbol, name, string, integer, posint,
// nonnegint, nonnegative, boolean, list, list(T), set, set(T), table, indexed,
// Matrix, `=`, function, anything, And/Or, structured forms, and user types
// registered via add_type (`type/Foo`).
func (it *Interp) checkType(v Value, t *tree) (bool, error) {
	switch t.group {
	case variable:
		return it.checkNamedType(v, stripBacktick(t.value), nil)
	case stringNode:
		return it.checkNamedType(v, stripQuotes(t.value), nil)
	case unevalNode:
		return it.checkNamedType(v, stripBacktick(stripUneval(t.value)), nil)
	case callNode:
		// structured type T(arg) e.g. list(symbol), set(list)
		headName := nodeNameText(t.nodes[0])
		return it.checkNamedType(v, headName, t.nodes[1:])
	case indexNode:
		headName := nodeNameText(t.nodes[0])
		return it.checkNamedType(v, headName, t.nodes[1:])
	case operate:
		switch t.value {
		case "=":
			_, ok := v.(*Equation)
			return ok, nil
		}
		// `And`/`Or` come as calls; relational here is rare
		return false, nil
	case setNode:
		// type given as a set of admissible string/name values? DT uses a -> a in [...]
		return false, nil
	default:
		// arrow-proc type-check (a -> boolean) handled by callers via apply
		return false, nil
	}
}

// checkNamedType checks a named type, possibly with structured parameters.
func (it *Interp) checkNamedType(v Value, name string, params []*tree) (bool, error) {
	// user type/X registered via add_type or `type/X` proc
	if proc, ok := it.typeProcs[name]; ok {
		args := []Value{v}
		res, err := it.applyValue(proc, args, nil)
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

	switch name {
	case "anything":
		return true, nil
	case "integer":
		_, ok := v.(Integer)
		return ok, nil
	case "posint":
		i, ok := v.(Integer)
		return ok && i.Val.Sign() > 0, nil
	case "negint":
		i, ok := v.(Integer)
		return ok && i.Val.Sign() < 0, nil
	case "nonnegint":
		i, ok := v.(Integer)
		return ok && i.Val.Sign() >= 0, nil
	case "posint0", "nonnegative":
		r, ok := toNumRat(v)
		return ok && r.Sign() >= 0, nil
	case "positive":
		r, ok := toNumRat(v)
		return ok && r.Sign() > 0, nil
	case "rational", "fraction":
		switch v.(type) {
		case Integer, Rational:
			return true, nil
		}
		return false, nil
	case "numeric", "realcons", "constant":
		switch v.(type) {
		case Integer, Rational, Float:
			return true, nil
		}
		return false, nil
	case "extended_numeric":
		// Maple: numeric plus infinity, -infinity, and undefined.
		switch v.(type) {
		case Integer, Rational, Float:
			return true, nil
		}
		return isInfinityVal(v) || isUndefinedVal(v), nil
	case "infinity":
		return isInfinityVal(v), nil
	case "float":
		_, ok := v.(Float)
		return ok, nil
	case "string":
		_, ok := v.(MString)
		return ok, nil
	case "symbol":
		n, ok := v.(Name)
		if !ok {
			return false, nil
		}
		// a symbol is a plain name with no index/structure
		return !strings.Contains(n.Val, ":-"), nil
	case "name":
		switch v.(type) {
		case Name:
			return true, nil
		case *Indexed:
			return true, nil
		}
		return false, nil
	case "indexed":
		_, ok := v.(*Indexed)
		return ok, nil
	case "boolean", "truefalse":
		_, ok := v.(Boolean)
		if ok {
			return true, nil
		}
		if n, ok := v.(Name); ok {
			return n.Val == "true" || n.Val == "false" || n.Val == "FAIL", nil
		}
		return false, nil
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
		return it.checkElems(l.Items, params)
	case "set":
		s, ok := v.(Set)
		if !ok {
			return false, nil
		}
		return it.checkElems(s.Items, params)
	case "table":
		if _, ok := v.(*Table); ok {
			return true, nil
		}
		// a name holding a table satisfies type(.,table) in Maple (type
		// evaluates the name). Resolve last-name-eval aliases.
		if nm, ok := v.(Name); ok {
			if rv, bound := it.lookup(nm.Val); bound {
				_, isT := rv.(*Table)
				return isT, nil
			}
		}
		return false, nil
	case "function", "procedure":
		switch v.(type) {
		case *Proc, *Builtin:
			return name == "procedure", nil
		case *Func:
			return name == "function", nil
		}
		return false, nil
	case "Matrix", "matrix", "Vector", "array":
		// not represented yet; treat as never-matching (documented gap)
		return false, nil
	case "equation", "=", "`=`":
		_, ok := v.(*Equation)
		return ok, nil
	case "range":
		_, ok := v.(*Range)
		return ok, nil
	// The type names `+`/`*`/`^` arrive backtick-stripped (checkType does
	// stripBacktick on the type node), so the cases must be the bare operator
	// strings. Same backtick-stripping pitfall as `convert(L, `+`)` in
	// builtins.biConvert. DT's PartialDerivativeInternal branches on
	// type(p,`+`) / type(p,`*`) / type(p,`^`); when these silently returned false
	// the sum/product rules were skipped, so e.g. d/dy (u[0,0]-u[1,0]) fell
	// through to a scalar `diff` of the whole sum and came back 0 — producing a
	// zero reductor and the spurious "division by zero" in PseudoRemainder.
	case "+", "`+`":
		_, ok := v.(*Sum)
		return ok, nil
	case "*", "`*`":
		_, ok := v.(*Prod)
		return ok, nil
	case "^", "`^`":
		_, ok := v.(*Power)
		return ok, nil
	case "polynom", "ratpoly", "algebraic":
		// permissive for Phase 2: any algebraic-ish value
		switch v.(type) {
		case Integer, Rational, Float, Name, *Sum, *Prod, *Power, *Indexed, *Func:
			return true, nil
		}
		// A DifferentialThomas polynom-object (a table with a 'Polynom' slot) is a
		// differential polynomial: DT recognises one everywhere via
		// `type(p,table)` + p['Polynom'], and passes polynom-objects to procs typed
		// `polynom(anything,function)` (DifferentialSystemReduce's gate, reached
		// from SplitByInitial -> DifferentialSystemInequationImplied). A system
		// table has no top-level 'Polynom' slot, so this does not misclassify one.
		if t, ok := v.(*Table); ok {
			if _, has := t.get(Name{"Polynom"}); has {
				return true, nil
			}
		}
		return false, nil
	case "even":
		i, ok := v.(Integer)
		return ok && new(big.Int).And(i.Val, big.NewInt(1)).Sign() == 0, nil
	case "odd":
		i, ok := v.(Integer)
		return ok && new(big.Int).And(i.Val, big.NewInt(1)).Sign() != 0, nil
	default:
		// unknown type name: be permissive only for a few catch-all, else false
		return false, nil
	}
}

// checkElems verifies every element matches the structured parameter type (if
// any). list(symbol) -> params=[symbol]; list({a,b}) handled via set type node.
func (it *Interp) checkElems(items []Value, params []*tree) (bool, error) {
	if len(params) == 0 {
		return true, nil
	}
	elemType := params[0]
	for _, e := range items {
		ok, err := it.checkTypeElem(e, elemType)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// checkTypeElem allows the element type to be a set literal {a,b} meaning "one
// of these named types" — DT writes list({symbol,indexed}).
func (it *Interp) checkTypeElem(v Value, t *tree) (bool, error) {
	if t.group == setNode {
		for _, alt := range t.nodes {
			ok, err := it.checkType(v, alt)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	return it.checkType(v, t)
}

// typeText renders a type node to a human string (for error messages).
func typeText(t *tree) string {
	if t == nil {
		return "anything"
	}
	switch t.group {
	case variable:
		return stripBacktick(t.value)
	case stringNode:
		return stripQuotes(t.value)
	case callNode:
		var parts []string
		for _, p := range t.nodes[1:] {
			parts = append(parts, typeText(p))
		}
		return typeText(t.nodes[0]) + "(" + strings.Join(parts, ",") + ")"
	case setNode:
		var parts []string
		for _, p := range t.nodes {
			parts = append(parts, typeText(p))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		return t.value
	}
}

// isPackageName reports whether a name is a known Maple package whose [member]
// indexing resolves to a CAS op rather than a table read.
func isPackageName(s string) bool {
	switch s {
	case "LinearAlgebra", "ListTools", "StringTools", "ArrayTools",
		"PolynomialTools", "FileTools", "CodeTools", "Involutive",
		"combinat", "numtheory", "RootFinding":
		return true
	}
	return false
}
