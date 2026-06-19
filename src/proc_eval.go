package main

import (
	"fmt"
	"math/big"
	"os"
	"strings"
)

// traceProcs, when set via OPENMAPLE_TRACE_PROCS, prints the proc name on the
// way out of a proc that propagates an error — a cheap call-stack reconstruction
// for debugging the decomposition path.
var traceProcs = os.Getenv("OPENMAPLE_TRACE_PROCS") != ""

func stderrW() *os.File { return os.Stderr }

// makeProc builds a Proc value from a procNode, capturing whether `option
// remember` is set.
func (it *Interp) makeProc(n *tree, name string) *Proc {
	p := &Proc{def: n, name: name, env: it.captureEnv()}
	for _, c := range n.nodes {
		if c.group == optionNode {
			for _, o := range c.nodes {
				if nodeNameText(o) == "remember" {
					p.hasRemember = true
					p.remember = map[string]Value{}
				}
			}
		}
	}
	return p
}

// captureEnv snapshots the current proc scope's local bindings for a lexical
// closure. Returns nil at top level (no enclosing scope). The snapshot is a
// shallow copy of the locals map (table/proc values are references, so shared
// state still mutates through; scalar locals are captured by value as Maple's
// closures do for the values they reference at construction time).
func (it *Interp) captureEnv() map[string]Value {
	if it.scope == nil {
		return nil
	}
	env := make(map[string]Value, len(it.scope.locals))
	// inherit the enclosing proc's own captured env first (so nested closures
	// see grand-parent locals), then overlay this scope's locals.
	if it.scope.captured != nil {
		for k, v := range it.scope.captured {
			env[k] = v
		}
	}
	for k, v := range it.scope.locals {
		env[k] = v
	}
	return env
}

// makeArrow builds a Proc from an arrow expression `params -> body`. We
// synthesize a procNode so call handling is uniform.
func (it *Interp) makeArrow(n *tree) *Proc {
	paramsNode := n.nodes[0]
	body := n.nodes[1]

	params := &tree{group: exprseqNode, value: "params"}
	add := func(t *tree) {
		params.nodes = append(params.nodes, &tree{group: paramNode, nodes: []*tree{t}})
	}
	if paramsNode.group == exprseqNode && paramsNode.value == "" {
		for _, e := range paramsNode.nodes {
			add(e)
		}
	} else {
		add(paramsNode)
	}
	// body becomes a rootNode with a single return-less expression statement
	bodyRoot := &tree{group: rootNode, value: "body", nodes: []*tree{body}}
	proc := &tree{group: procNode, nodes: []*tree{params, bodyRoot}}
	return &Proc{def: proc, env: it.captureEnv()}
}

// evalCall evaluates f(args...). The head may be a builtin, a user proc, an
// indexed package access (LinearAlgebra[...]), or an unevaluated function.
func (it *Interp) evalCall(n *tree) (Value, error) {
	headNode := n.nodes[0]

	// Builtins and special forms keyed by head name.
	if headNode.group == variable {
		name := stripBacktick(headNode.value)
		// special-form builtins that need unevaluated args (checked before
		// regular builtins so e.g. ASSERT/userinfo stay lazy).
		if v, handled, err := it.evalSpecialForm(name, n.nodes[1:]); handled {
			return v, err
		}
		if b, ok := it.builtins[name]; ok {
			args, err := it.evalArgs(n.nodes[1:])
			if err != nil {
				return nil, err
			}
			return b.Fn(it, args)
		}
		// resolve the name to a proc/value
		val, bound := it.lookup(name)
		if bound {
			if p, ok := val.(*Proc); ok {
				args, err := it.evalArgs(n.nodes[1:])
				if err != nil {
					return nil, err
				}
				return it.callProc(p, args)
			}
			if b, ok := val.(*Builtin); ok {
				args, err := it.evalArgs(n.nodes[1:])
				if err != nil {
					return nil, err
				}
				return b.Fn(it, args)
			}
		}
		// CAS package call dispatch (e.g. LinearAlgebra not via index) or unknown
		args, err := it.evalArgs(n.nodes[1:])
		if err != nil {
			return nil, err
		}
		return it.dispatchUnknownCall(name, args)
	}

	// head is an expression (e.g. an indexed package: LinearAlgebra[Rank](M),
	// or a proc value computed by parse(...)).
	head, err := it.eval(headNode)
	if err != nil {
		return nil, err
	}
	args, err := it.evalArgs(n.nodes[1:])
	if err != nil {
		return nil, err
	}
	return it.applyValue(head, args, headNode)
}

// applyValue applies an already-evaluated head value to args.
func (it *Interp) applyValue(head Value, args []Value, headNode *tree) (Value, error) {
	switch h := head.(type) {
	case *Proc:
		return it.callProc(h, args)
	case *Builtin:
		return h.Fn(it, args)
	case Name:
		// name could be a bound proc, a CAS op, or an inert function application
		if b, ok := it.builtins[h.Val]; ok {
			return b.Fn(it, args)
		}
		if val, bound := it.lookup(h.Val); bound {
			if p, ok := val.(*Proc); ok {
				return it.callProc(p, args)
			}
		}
		return it.dispatchUnknownCall(h.Val, args)
	}
	// indexed package member as a Name like "LinearAlgebra:-Rank"
	return &Func{Head: head, Args: args}, nil
}

// dispatchUnknownCall routes a call whose head is an unbound name: it is either
// a CAS op, or an inert function application (jet variable / symbol).
func (it *Interp) dispatchUnknownCall(name string, args []Value) (Value, error) {
	if isCASOp(name) {
		return it.cas.Call(name, args)
	}
	// inert function application (e.g. u(x,y), cos(phi[0]))
	return &Func{Head: Name{name}, Args: args}, nil
}

// callProc binds args and runs a procedure body in a fresh scope.
func (it *Interp) callProc(p *Proc, args []Value) (Value, error) {
	// option remember cache
	var cacheKey string
	if p.hasRemember {
		cacheKey = rememberKey(args)
		if v, ok := p.remember[cacheKey]; ok {
			return v, nil
		}
	}

	def := p.def
	params := def.nodes[0]

	sc := newScope()
	sc.args = args
	sc.procName = p.name
	sc.nparams = len(params.nodes)
	sc.captured = p.env // lexical closure: free names fall back here

	// gather declarations
	var body *tree
	for _, c := range def.nodes[1:] {
		switch c.group {
		case localsNode:
			for _, d := range c.nodes {
				nm := declName(d)
				sc.isLocal[nm] = true
			}
		case globalsNode:
			for _, d := range c.nodes {
				sc.isGlobal[nm(d)] = true
			}
		case rootNode:
			if c.value == "body" {
				body = c
			}
		}
	}

	// bind positional parameters
	if err := it.bindParams(params, args, sc); err != nil {
		return nil, err
	}

	// activate scope
	prev := it.scope
	it.scope = sc
	defer func() { it.scope = prev }()

	var result Value = NULL()
	if body != nil {
		v, err := it.execBlock(body.nodes)
		if err != nil {
			if rs, ok := err.(returnSignal); ok {
				result = rs.val
			} else {
				if traceProcs {
					argStrs := make([]string, len(args))
					for i, a := range args {
						argStrs[i] = printValue(a)
					}
					fmt.Fprintf(stderrW(), "[proc-trace] error in %q(args=%v): %v\n", p.name, argStrs, err)
				}
				return nil, err
			}
		} else {
			result = v
		}
	}

	// Resolve table/proc name-aliases in the return value to the underlying
	// objects *while this scope is still active*. A proc that returns a local
	// table (last-name-eval gives Name{localTable}) would otherwise hand back a
	// name that is unbound at the call site. Recurse into lists/sets/seqs so a
	// returned [sys1, sys2] of local-table systems resolves too. (Same
	// reference-semantics rationale as resolveRefForStore / evalArgs.)
	result = it.resolveRefDeep(result)

	if p.hasRemember {
		p.remember[cacheKey] = result
	}
	return result, nil
}

// resolveRefDeep resolves table/proc Name-aliases to their objects, recursing
// through list/set/seq containers. Used on proc return values so local-table
// references survive the scope teardown.
func (it *Interp) resolveRefDeep(v Value) Value {
	switch n := v.(type) {
	case Name:
		return it.resolveRefForStore(n)
	case List:
		items := make([]Value, len(n.Items))
		for i, e := range n.Items {
			items[i] = it.resolveRefDeep(e)
		}
		return List{items}
	case Set:
		items := make([]Value, len(n.Items))
		for i, e := range n.Items {
			items[i] = it.resolveRefDeep(e)
		}
		return makeSet(items)
	case Seq:
		items := make([]Value, len(n.Items))
		for i, e := range n.Items {
			items[i] = it.resolveRefDeep(e)
		}
		return Seq{items}
	default:
		return v
	}
}

func nm(d *tree) string { return declName(d) }

// declName extracts the bare name from a local/global declaration entry.
func declName(d *tree) string {
	switch d.group {
	case variable:
		return stripBacktick(d.value)
	case typeNode:
		return declName(d.nodes[0])
	case assign:
		return declName(d.nodes[0])
	default:
		return d.value
	}
}

// bindParams binds positional parameters, applying ::type checks and defaults.
func (it *Interp) bindParams(params *tree, args []Value, sc *scope) error {
	for i, par := range params.nodes {
		pname, ptype, pdefault := parseParam(par)
		sc.isLocal[pname] = true
		if i < len(args) {
			v := args[i]
			if ptype != nil {
				ok, err := it.checkType(v, ptype)
				if err != nil {
					return err
				}
				if !ok {
					return newMapleError(fmt.Sprintf("invalid input: %s expects its %d-th argument to be of type %s, but received %s",
						procDisplayName(sc), i+1, printValue2(ptype), printValue(v)))
				}
			}
			sc.locals[pname] = v
		} else if pdefault != nil {
			// evaluate default in the scope being built (with prior params bound)
			prev := it.scope
			it.scope = sc
			dv, err := it.eval(pdefault)
			it.scope = prev
			if err != nil {
				return err
			}
			sc.locals[pname] = dv
		} else {
			// unsupplied, no default: leave the parameter as the name symbol
			sc.locals[pname] = Name{pname}
		}
	}
	return nil
}

func procDisplayName(sc *scope) string {
	if sc.procName != "" {
		return sc.procName
	}
	return "proc"
}

func printValue2(t *tree) string {
	return typeText(t)
}

// parseParam extracts (name, typeNode, defaultNode) from a paramNode.
func parseParam(par *tree) (name string, ptype *tree, pdefault *tree) {
	expr := par.nodes[0]
	if par.value == "default" && len(par.nodes) > 1 {
		pdefault = par.nodes[1]
	}
	if expr.group == typeNode {
		name = declName(expr.nodes[0])
		ptype = expr.nodes[1]
		return
	}
	name = declName(expr)
	return
}

// rememberKey builds a cache key from the argument values.
func rememberKey(args []Value) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = canonicalKey(a)
	}
	return strings.Join(parts, "\x00")
}

// ---------------------------------------------------------------------------
// Indexing
// ---------------------------------------------------------------------------

// evalIndex evaluates a[i,j]. If a is a table -> table read (or inert if
// unassigned). If a is a list/set/string -> positional/slice access. If a is an
// unbound name -> an inert Indexed value (jet variable). Package indexing
// (LinearAlgebra[Rank]) yields a Name "Pkg:-member".
func (it *Interp) evalIndex(n *tree) (Value, error) {
	baseNode := n.nodes[0]
	idxVals, err := it.evalArgs(n.nodes[1:])
	if err != nil {
		return nil, err
	}

	// args[i] / args[i..j]: index the current activation's argument sequence.
	if baseNode.group == variable && stripBacktick(baseNode.value) == "args" && it.scope != nil {
		argSeq := Seq{append([]Value{}, it.scope.args...)}
		if v, ok, err := indexCollection(argSeq, idxVals); ok || err != nil {
			return v, err
		}
	}

	// package index: PackageName[member] where base is a known package name.
	if baseNode.group == variable {
		bn := stripBacktick(baseNode.value)
		if isPackageName(bn) && len(idxVals) == 1 {
			if mn, ok := nameOrStr(idxVals[0]); ok {
				return Name{bn + ":-" + mn}, nil
			}
		}
		// table or jet-variable
		cur, bound := it.lookup(bn)
		if bound {
			if t, ok := cur.(*Table); ok {
				return it.tableIndex(t, idxVals, bn)
			}
		}
		// list/set/string indexing handled below only if bound to one
		if bound {
			if v, ok, err := indexCollection(cur, idxVals); ok || err != nil {
				return v, err
			}
		}
		// unbound name -> inert indexed (jet variable u[1,0])
		return &Indexed{Head: Name{bn}, Idx: idxVals}, nil
	}

	base, err := it.eval(baseNode)
	if err != nil {
		return nil, err
	}
	base = it.derefTable(base)
	if t, ok := base.(*Table); ok {
		return it.tableIndex(t, idxVals, "")
	}
	if v, ok, err := indexCollection(base, idxVals); ok || err != nil {
		return v, err
	}
	return &Indexed{Head: base, Idx: idxVals}, nil
}

func (it *Interp) tableIndex(t *Table, idxVals []Value, name string) (Value, error) {
	idx := seqOrSingle(idxVals)
	if v, ok := t.get(idx); ok {
		return v, nil
	}
	// unassigned table entry -> inert indexed name
	if name != "" {
		return &Indexed{Head: Name{name}, Idx: idxVals}, nil
	}
	return &Indexed{Head: t, Idx: idxVals}, nil
}

// indexCollection handles list/set/string indexing and range slicing. Returns
// handled=false if base is not an indexable collection.
func indexCollection(base Value, idxVals []Value) (Value, bool, error) {
	items, ok := positional(base)
	if !ok {
		return nil, false, nil
	}
	if len(idxVals) != 1 {
		return nil, false, fmt.Errorf("collection indexing expects one index")
	}
	switch ix := idxVals[0].(type) {
	case Integer:
		i, v, err := resolveIndex(ix.Val, len(items))
		if err != nil {
			return nil, true, err
		}
		_ = i
		return v(items), true, nil
	case *Range:
		lo, lok := intVal(ix.Lo)
		hi, hok := intVal(ix.Hi)
		var loI, hiI int
		if lok {
			loI = normIndex(int(lo.Int64()), len(items))
		} else {
			loI = 1
		}
		if hok {
			hiI = normIndex(int(hi.Int64()), len(items))
		} else {
			hiI = len(items)
		}
		if loI < 1 {
			loI = 1
		}
		if hiI > len(items) {
			hiI = len(items)
		}
		var sub []Value
		if loI <= hiI {
			sub = append(sub, items[loI-1:hiI]...)
		}
		return rewrap(base, sub), true, nil
	}
	return nil, true, fmt.Errorf("unsupported index type: base=%s idx=%s", printValue(base), printValue(idxVals[0]))
}

func positional(v Value) ([]Value, bool) {
	switch c := v.(type) {
	case List:
		return c.Items, true
	case Set:
		return c.Items, true
	case Seq:
		return c.Items, true
	case *Func:
		return c.Args, true
	case *Indexed:
		return c.Idx, true
	}
	return nil, false
}

func rewrap(orig Value, items []Value) Value {
	switch orig.(type) {
	case List:
		return List{items}
	case Set:
		return makeSet(items)
	case Seq:
		return seqOrSingle(items)
	default:
		return List{items}
	}
}

func normIndex(i, n int) int {
	if i < 0 {
		return n + i + 1
	}
	return i
}

func resolveIndex(bi *big.Int, n int) (int, func([]Value) Value, error) {
	i := int(bi.Int64())
	i = normIndex(i, n)
	if i < 1 || i > n {
		return 0, nil, fmt.Errorf("index %d out of range 1..%d", i, n)
	}
	return i, func(items []Value) Value { return items[i-1] }, nil
}

func nameOrStr(v Value) (string, bool) {
	if n, ok := v.(Name); ok {
		return n.Val, true
	}
	if s, ok := v.(MString); ok {
		return s.Val, true
	}
	return "", false
}
