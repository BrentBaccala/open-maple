package main

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Interp is the Maple interpreter state.
type Interp struct {
	globals   map[string]Value // top-level / global symbol table
	scope     *scope           // current proc activation (nil at top level)
	builtins  map[string]*Builtin
	typeProcs map[string]Value // user type/X procedures (from add_type / `type/X`)
	out       *strings.Builder // captured printf/print output
	assertLvl int
	cas       CAS // computer-algebra backend (stub in Phase 2)
}

// NewInterp builds an interpreter with builtins registered.
func NewInterp() *Interp {
	it := &Interp{
		globals:   map[string]Value{},
		builtins:  map[string]*Builtin{},
		typeProcs: map[string]Value{},
		out:       &strings.Builder{},
		cas:       &stubCAS{},
	}
	registerBuiltins(it)
	return it
}

// control-flow signals carried via Go errors.
type returnSignal struct{ val Value }
type breakSignal struct{}
type nextSignal struct{}

func (returnSignal) Error() string { return "return outside proc" }
func (breakSignal) Error() string  { return "break outside loop" }
func (nextSignal) Error() string   { return "next outside loop" }

// mapleError is a user-level Maple error (from error/ERROR). The Msg is the
// formatted message string used for string-matched catch.
type mapleError struct {
	Msg  string
	Args []Value
}

func (e *mapleError) Error() string { return e.Msg }

func newMapleError(msg string, args ...Value) *mapleError {
	return &mapleError{Msg: msg, Args: args}
}

// ---------------------------------------------------------------------------
// Top-level execution
// ---------------------------------------------------------------------------

// Exec parses and runs a chunk of Maple source at top level. Returns the value
// of the last statement (for REPL/printing).
func (it *Interp) Exec(code string) (Value, error) {
	root, err := frontEnd(code)
	if err != nil {
		return nil, err
	}
	return it.execBlock(root.nodes)
}

// execBlock runs a sequence of statements, returning the last value.
func (it *Interp) execBlock(stmts []*tree) (Value, error) {
	var last Value = NULL()
	for _, s := range stmts {
		v, err := it.eval(s)
		if err != nil {
			return last, err
		}
		last = v
	}
	return last, nil
}

// ---------------------------------------------------------------------------
// Evaluation
// ---------------------------------------------------------------------------

// eval evaluates an AST node to a Value with full (top-level) evaluation rules.
func (it *Interp) eval(n *tree) (Value, error) {
	if n == nil {
		return NULL(), nil
	}
	switch n.group {
	case constant:
		return parseNumber(n.value)
	case stringNode:
		return MString{stripQuotes(n.value)}, nil
	case unevalNode:
		// 'name' literal: the uneval-quoted token. In DT these are names like
		// 'diff', 'true', 'Compare'. Evaluate the inner token once as a name.
		inner := stripUneval(n.value)
		return Name{stripBacktick(inner)}, nil
	case variable:
		return it.evalName(stripBacktick(n.value))
	case assign:
		return it.evalAssign(n)
	case operate:
		return it.evalOperator(n)
	case unaryNode:
		return it.evalUnary(n)
	case exprseqNode:
		return it.evalSeq(n)
	case listNode:
		items, err := it.evalArgs(n.nodes)
		if err != nil {
			return nil, err
		}
		return List{items}, nil
	case setNode:
		items, err := it.evalArgs(n.nodes)
		if err != nil {
			return nil, err
		}
		return makeSet(items), nil
	case rangeNode:
		lo, err := it.eval(n.nodes[0])
		if err != nil {
			return nil, err
		}
		hi, err := it.eval(n.nodes[1])
		if err != nil {
			return nil, err
		}
		return &Range{lo, hi}, nil
	case callNode:
		return it.evalCall(n)
	case indexNode:
		return it.evalIndex(n)
	case procNode:
		return it.makeProc(n, ""), nil
	case arrowNode:
		return it.makeArrow(n), nil
	case ifNode:
		return it.evalIf(n)
	case forNode:
		return it.evalLoop(n)
	case returnNode:
		var v Value = NULL()
		if len(n.nodes) > 0 {
			var err error
			v, err = it.eval(n.nodes[0])
			if err != nil {
				return nil, err
			}
		}
		return nil, returnSignal{v}
	case errorNode:
		return it.evalError(n)
	case tryNode:
		return it.evalTry(n)
	case breakNode:
		return nil, breakSignal{}
	case nextNode:
		return nil, nextSignal{}
	case typeNode:
		// a::T appearing as an expression: treat as a membership test (rare).
		return it.evalTypeAnnotationExpr(n)
	case memberNode:
		return it.evalMember(n)
	case rootNode:
		return it.execBlock(n.nodes)
	case emptyNode:
		return NULL(), nil
	case localsNode, globalsNode, optionNode, descriptionNode:
		// declarations are handled at proc-build time; as statements they are NOPs.
		return NULL(), nil
	case matrixNode:
		return nil, errCAS("Matrix/Vector constructor")
	case moduleNode:
		return nil, errUnimplemented("module evaluation")
	case readNode:
		return nil, errUnimplemented("read")
	default:
		return nil, errUnimplemented("eval " + n.group.String())
	}
}

// evalName resolves a bare name with Maple's evaluation rules. Last-name-eval:
// a name bound to a table or procedure evaluates to the *name* itself (the
// caller passes such things around by name). Otherwise it returns the value.
// An unbound name evaluates to itself (a symbol).
func (it *Interp) evalName(name string) (Value, error) {
	// special literals
	switch name {
	case "true":
		return vTrue, nil
	case "false":
		return vFalse, nil
	case "FAIL":
		return vFAIL, nil
	case "NULL":
		return NULL(), nil
	case "Pi", "infinity", "gamma", "I":
		return Name{name}, nil
	case "nargs":
		if it.scope != nil {
			return newInt(int64(len(it.scope.args))), nil
		}
		return newInt(0), nil
	case "args":
		if it.scope != nil {
			return Seq{append([]Value{}, it.scope.args...)}, nil
		}
		return NULL(), nil
	}
	v, ok := it.lookup(name)
	if !ok {
		// unassigned name -> the symbol itself
		return Name{name}, nil
	}
	// last-name-eval: tables and procs return the name, not their contents.
	switch v.(type) {
	case *Table, *Proc, *Builtin:
		return Name{name}, nil
	}
	return v, nil
}

// fullEval performs full evaluation of a value: if it is a Name bound to a
// non-table/proc value, dereference it (used at top level and by eval()).
func (it *Interp) fullEval(v Value) Value {
	for i := 0; i < 100; i++ {
		nm, ok := v.(Name)
		if !ok {
			return v
		}
		nv, ok := it.lookup(nm.Val)
		if !ok {
			return v
		}
		switch nv.(type) {
		case *Table, *Proc, *Builtin:
			return nv // one-level for these (but eval() returns name; here we deref once)
		}
		if equalValues(nv, v) {
			return v
		}
		v = nv
	}
	return v
}

// lookup resolves a name to a stored value, honoring scope.
func (it *Interp) lookup(name string) (Value, bool) {
	if it.scope != nil {
		if it.scope.isLocal[name] {
			v, ok := it.scope.locals[name]
			return v, ok
		}
		// not a declared local -> global (declared global, or fall-through)
		v, ok := it.globals[name]
		return v, ok
	}
	v, ok := it.globals[name]
	return v, ok
}

// store assigns a name honoring scope.
func (it *Interp) store(name string, val Value) {
	if it.scope != nil {
		if it.scope.isLocal[name] {
			it.scope.locals[name] = val
			return
		}
		it.globals[name] = val
		return
	}
	it.globals[name] = val
}

// ---------------------------------------------------------------------------
// Sequences
// ---------------------------------------------------------------------------

func (it *Interp) evalSeq(n *tree) (Value, error) {
	if len(n.nodes) == 0 {
		return NULL(), nil
	}
	items, err := it.evalArgs(n.nodes)
	if err != nil {
		return nil, err
	}
	return seqOrSingle(items), nil
}

// evalArgs evaluates a list of nodes, flattening sequences (Maple arg/list
// flattening).
func (it *Interp) evalArgs(nodes []*tree) ([]Value, error) {
	var out []Value
	for _, nd := range nodes {
		v, err := it.eval(nd)
		if err != nil {
			return nil, err
		}
		if s, ok := v.(Seq); ok {
			out = append(out, s.Items...)
		} else {
			out = append(out, v)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Assignment
// ---------------------------------------------------------------------------

func (it *Interp) evalAssign(n *tree) (Value, error) {
	lhs := n.nodes[0]
	rhsVal, err := it.eval(n.nodes[1])
	if err != nil {
		return nil, err
	}
	if err := it.assignTo(lhs, rhsVal); err != nil {
		return nil, err
	}
	return rhsVal, nil
}

// assignTo handles assignment to a name, an indexed name (table/auto-create),
// or a multiple-assignment (a,b := ...).
func (it *Interp) assignTo(lhs *tree, rhsVal Value) error {
	switch lhs.group {
	case variable:
		it.store(stripBacktick(lhs.value), rhsVal)
		return nil
	case typeNode:
		// x::T := v  (typed local in proc body, rare at statement level) — assign x
		return it.assignTo(lhs.nodes[0], rhsVal)
	case indexNode:
		return it.assignIndexed(lhs, rhsVal)
	case exprseqNode:
		// multiple assignment: (a, b, c) := seq
		flat := flattenSeq([]Value{rhsVal})
		for i, target := range lhs.nodes {
			var v Value = NULL()
			if i < len(flat) {
				v = flat[i]
			}
			if err := it.assignTo(target, v); err != nil {
				return err
			}
		}
		return nil
	case callNode:
		// f(x) := v  — defines an "indexed procedure"/remember entry. DT does
		// not use this in a way that affects Phase-2; treat as unimplemented.
		return errUnimplemented("assignment to function call")
	default:
		return errUnimplemented("assignment target " + lhs.group.String())
	}
}

// assignIndexed implements t[i] := v with auto-creation of the table.
func (it *Interp) assignIndexed(lhs *tree, rhsVal Value) error {
	base := lhs.nodes[0]
	idxVals, err := it.evalArgs(lhs.nodes[1:])
	if err != nil {
		return err
	}
	idx := seqOrSingle(idxVals)

	// resolve the base to a table, auto-creating one if the name is unbound or
	// bound to a name (Maple: assigning to t[i] auto-creates the table t).
	name, ok := baseName(base)
	if !ok {
		return errUnimplemented("indexed assignment to non-name base")
	}
	name = stripBacktick(name)
	cur, bound := it.lookupRaw(name)
	var tbl *Table
	if bound {
		if t, isT := cur.(*Table); isT {
			tbl = t
		} else {
			// bound to a non-table: Maple would error; auto-create only if name
			return fmt.Errorf("cannot index-assign into non-table %s", name)
		}
	}
	if tbl == nil {
		tbl = newTable()
		it.store(name, tbl)
	}
	tbl.set(idx, rhsVal)
	return nil
}

// lookupRaw resolves a name to its stored value WITHOUT last-name-eval (used by
// assignment to find an existing table).
func (it *Interp) lookupRaw(name string) (Value, bool) {
	return it.lookup(name)
}

// baseName extracts the underlying name from an index/call base node.
func baseName(n *tree) (string, bool) {
	switch n.group {
	case variable:
		return n.value, true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Numbers
// ---------------------------------------------------------------------------

func parseNumber(s string) (Value, error) {
	if strings.ContainsAny(s, ".eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q: %v", s, err)
		}
		return Float{f}, nil
	}
	i := new(big.Int)
	if _, ok := i.SetString(s, 10); !ok {
		return nil, fmt.Errorf("bad integer %q", s)
	}
	return Integer{i}, nil
}
