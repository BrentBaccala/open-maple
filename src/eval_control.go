package main

import (
	"fmt"
	"math/big"
	"strings"
)

// evalIf handles if/elif/else as both statement and expression. The ifNode's
// children are [cond, block, cond, block, ..., elseBlock?]. An odd trailing
// child is the else block. As an expression, the value is the taken block's
// last statement value.
func (it *Interp) evalIf(n *tree) (Value, error) {
	nodes := n.nodes
	i := 0
	for i+1 < len(nodes) {
		cond, err := it.eval(nodes[i])
		if err != nil {
			return nil, err
		}
		if truth(cond) == bTrue {
			return it.eval(nodes[i+1])
		}
		i += 2
	}
	// else block present?
	if i < len(nodes) {
		return it.eval(nodes[i])
	}
	return NULL(), nil
}

// evalLoop handles for/while/do. The forNode children are [header, body] where
// header is an exprseqNode whose children are tagged exprseqNodes:
// var/from/to/by/in/while.
func (it *Interp) evalLoop(n *tree) (Value, error) {
	header := n.nodes[0]
	body := n.nodes[1]

	var varName string
	var fromN, toN, byN, inN, whileN *tree
	for _, h := range header.nodes {
		if len(h.nodes) == 0 {
			continue
		}
		switch h.value {
		case "var":
			if h.nodes[0].group == variable {
				varName = stripBacktick(h.nodes[0].value)
			}
		case "from":
			fromN = h.nodes[0]
		case "to":
			toN = h.nodes[0]
		case "by":
			byN = h.nodes[0]
		case "in":
			inN = h.nodes[0]
		case "while":
			whileN = h.nodes[0]
		}
	}

	runBody := func() (bool, error) { // returns stop(break)
		_, err := it.eval(body)
		if err != nil {
			switch err.(type) {
			case breakSignal:
				return true, nil
			case nextSignal:
				return false, nil
			default:
				return false, err
			}
		}
		return false, nil
	}

	checkWhile := func() (bool, error) {
		if whileN == nil {
			return true, nil
		}
		v, err := it.eval(whileN)
		if err != nil {
			return false, err
		}
		return truth(v) == bTrue, nil
	}

	// for ... in <collection>
	if inN != nil {
		coll, err := it.eval(inN)
		if err != nil {
			return nil, err
		}
		items := iterItems(coll)
		for _, item := range items {
			if varName != "" {
				it.store(varName, item)
			}
			ok, err := checkWhile()
			if err != nil {
				return nil, err
			}
			if !ok {
				break
			}
			stop, err := runBody()
			if err != nil {
				return nil, err
			}
			if stop {
				break
			}
		}
		return NULL(), nil
	}

	// numeric for / while / bare do
	if fromN != nil || toN != nil || byN != nil {
		from := big.NewRat(1, 1)
		if fromN != nil {
			v, err := it.eval(fromN)
			if err != nil {
				return nil, err
			}
			r, ok := toNumRat(v)
			if !ok {
				return nil, fmt.Errorf("non-numeric loop 'from'")
			}
			from = new(big.Rat).Set(r)
		}
		by := big.NewRat(1, 1)
		if byN != nil {
			v, err := it.eval(byN)
			if err != nil {
				return nil, err
			}
			r, ok := toNumRat(v)
			if !ok {
				return nil, fmt.Errorf("non-numeric loop 'by'")
			}
			by = new(big.Rat).Set(r)
		}
		var to *big.Rat
		if toN != nil {
			v, err := it.eval(toN)
			if err != nil {
				return nil, err
			}
			r, ok := toNumRat(v)
			if !ok {
				return nil, fmt.Errorf("non-numeric loop 'to'")
			}
			to = new(big.Rat).Set(r)
		}
		cur := new(big.Rat).Set(from)
		bySign := by.Sign()
		for {
			if to != nil {
				c := cur.Cmp(to)
				if bySign >= 0 && c > 0 {
					break
				}
				if bySign < 0 && c < 0 {
					break
				}
			}
			if varName != "" {
				it.store(varName, normRat(new(big.Rat).Set(cur)))
			}
			ok, err := checkWhile()
			if err != nil {
				return nil, err
			}
			if !ok {
				break
			}
			stop, err := runBody()
			if err != nil {
				return nil, err
			}
			if stop {
				break
			}
			cur.Add(cur, by)
			if to == nil && whileN == nil {
				return nil, fmt.Errorf("unbounded numeric loop")
			}
		}
		return NULL(), nil
	}

	// while cond do / bare do (with break)
	for {
		ok, err := checkWhile()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if whileN == nil {
			// bare do — must rely on break/return inside
		}
		stop, err := runBody()
		if err != nil {
			return nil, err
		}
		if stop {
			break
		}
		if whileN == nil {
			// guard against infinite loop without break: handled by break/return
		}
	}
	return NULL(), nil
}

// iterItems yields the iterable elements of a collection for `for x in`.
func iterItems(v Value) []Value {
	switch c := v.(type) {
	case List:
		return c.Items
	case Set:
		return c.Items
	case Seq:
		return c.Items
	case *Table:
		ks := c.sortedKeys()
		out := make([]Value, 0, len(ks))
		for _, k := range ks {
			out = append(out, c.Vals[k])
		}
		return out
	default:
		return []Value{v}
	}
}

// evalError implements `error "msg", arg1, ...` and ERROR(...).
func (it *Interp) evalError(n *tree) (Value, error) {
	if len(n.nodes) == 0 {
		return nil, newMapleError("")
	}
	vals, err := it.evalArgs(n.nodes)
	if err != nil {
		return nil, err
	}
	return nil, mapleErrorFromArgs(vals)
}

// mapleErrorFromArgs formats a Maple error from a message string + args. The
// message may contain %1, %2 placeholders; we also support trailing-arg
// concatenation (Maple appends args with spaces when no placeholder).
func mapleErrorFromArgs(vals []Value) *mapleError {
	if len(vals) == 0 {
		return newMapleError("")
	}
	msg, ok := strVal(vals[0])
	if !ok {
		msg = printValue(vals[0])
	}
	args := vals[1:]
	formatted := formatMapleMsg(msg, args)
	return &mapleError{Msg: formatted, Args: args}
}

func formatMapleMsg(msg string, args []Value) string {
	if strings.Contains(msg, "%") {
		out := msg
		for i, a := range args {
			ph := fmt.Sprintf("%%%d", i+1)
			out = strings.ReplaceAll(out, ph, printValue(a))
		}
		return out
	}
	// no placeholders: append args separated by ", " (Maple-ish)
	parts := []string{msg}
	for _, a := range args {
		parts = append(parts, printValue(a))
	}
	return strings.Join(parts, ", ")
}

// evalTry implements try/catch "str"/finally with string-matched catch. The
// tryNode children: [ {try,body}, {catch, str?, body}*, {finally, body}? ].
func (it *Interp) evalTry(n *tree) (Value, error) {
	var tryBody, finallyBody *tree
	type catchClause struct {
		matches []string // catch strings; empty => catch-all
		body    *tree
	}
	var catches []catchClause

	for _, c := range n.nodes {
		switch c.value {
		case "try":
			tryBody = c.nodes[0]
		case "finally":
			finallyBody = c.nodes[0]
		case "catch":
			// last node is the body; preceding are catch-strings
			cc := catchClause{body: c.nodes[len(c.nodes)-1]}
			for _, s := range c.nodes[:len(c.nodes)-1] {
				v, err := it.eval(s)
				if err != nil {
					return nil, err
				}
				if str, ok := strVal(v); ok {
					cc.matches = append(cc.matches, str)
				}
			}
			catches = append(catches, cc)
		}
	}

	result, tryErr := it.eval(tryBody)

	// control-flow signals propagate through try (but finally still runs)
	if tryErr != nil {
		if _, isReturn := tryErr.(returnSignal); isReturn {
			it.runFinally(finallyBody)
			return result, tryErr
		}
		if me, ok := tryErr.(*mapleError); ok {
			for _, cc := range catches {
				if catchMatches(cc.matches, me.Msg) {
					it.store("lasterror", MString{me.Msg})
					cres, cerr := it.eval(cc.body)
					ferr := it.runFinally(finallyBody)
					if cerr != nil {
						return nil, cerr
					}
					if ferr != nil {
						return nil, ferr
					}
					return cres, nil
				}
			}
		}
		// no catch matched
		it.runFinally(finallyBody)
		return nil, tryErr
	}
	if ferr := it.runFinally(finallyBody); ferr != nil {
		return nil, ferr
	}
	return result, nil
}

func (it *Interp) runFinally(body *tree) error {
	if body == nil {
		return nil
	}
	_, err := it.eval(body)
	return err
}

// catchMatches reports whether a catch clause matches a maple error message.
// An empty matches list is catch-all. A catch string matches if the error
// message starts with it (Maple prefix-matches on the message string).
func catchMatches(matches []string, msg string) bool {
	if len(matches) == 0 {
		return true
	}
	for _, m := range matches {
		if m == msg || strings.HasPrefix(msg, m) {
			return true
		}
	}
	return false
}

// evalTypeAnnotationExpr handles `expr :: type` used in expression position
// (a membership test).
func (it *Interp) evalTypeAnnotationExpr(n *tree) (Value, error) {
	v, err := it.eval(n.nodes[0])
	if err != nil {
		return nil, err
	}
	ok, err := it.checkType(v, n.nodes[1])
	if err != nil {
		return nil, err
	}
	return mkBool(ok), nil
}

// evalMember handles module:-member. DT references LinearAlgebra[X] (index) and
// occasionally pkg:-name. We resolve `A:-B` to the name "A:-B" if unbound.
func (it *Interp) evalMember(n *tree) (Value, error) {
	// Represent module member access as a Name "Mod:-member" symbol; CAS-bound
	// modules (LinearAlgebra) are handled at call sites via index/call dispatch.
	left := nodeNameText(n.nodes[0])
	right := nodeNameText(n.nodes[1])
	return Name{left + ":-" + right}, nil
}

func nodeNameText(n *tree) string {
	switch n.group {
	case variable:
		return stripBacktick(n.value)
	case stringNode:
		return stripQuotes(n.value)
	default:
		return n.value
	}
}
