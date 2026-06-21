package main

import (
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
)

// Interp is the Maple interpreter state.
type Interp struct {
	globals   map[string]Value // top-level / global symbol table
	scope     *scope           // current proc activation (nil at top level)
	builtins  map[string]*Builtin
	typeProcs map[string]Value // user type/X procedures (from add_type / `type/X`)
	// exports maps a package-exported short name (e.g. "JetList2Diff") to its
	// qualified global name ("DifferentialThomas/JetList2Diff"). Populated by
	// add_function. Maple lets a package proc call a sibling export by its short
	// name; DT relies on this in PrettyPrintDifferentialSystem (calls bare
	// JetList2Diff). evalCall consults this when a bare-name lookup fails.
	exports   map[string]string
	out       *strings.Builder // captured printf/print output
	assertLvl int
	cas       CAS // computer-algebra backend (stub in Phase 2)
	// history holds the last three computed results for the ditto operators
	// %, %%, %%% (history[0]=%, [1]=%%, [2]=%%%). Updated after each statement
	// in execBlock. DT uses `[op(%%),%]` in DiffVarToList.
	history [3]Value
	// inertParse, when set, makes dispatchUnknownCall keep a CAS-op function head
	// inert (an inert *Func) instead of routing it to the CAS backend. parseBack
	// sets it on the throwaway interp it uses to rebuild the Value tree from a
	// Sage output string: that string is already fully reduced, so re-dispatching
	// a CAS op in it (e.g. diff(u(x, y), x), the inert derivative of an unknown
	// function) is never needed and would loop forever (parse -> diff op -> Sage
	// returns the same string -> parse -> ...).
	inertParse bool
	// verifyNative, when set via OPENMAPLE_VERIFY_NATIVE, makes the native
	// polynomial fast paths (native_poly.go) also call Sage and assert the two
	// results agree — a correctness harness for the native ops, off by default.
	verifyNative bool
}

// NewInterp builds an interpreter with builtins registered.
func NewInterp() *Interp {
	it := &Interp{
		globals:   map[string]Value{},
		builtins:  map[string]*Builtin{},
		typeProcs: map[string]Value{},
		exports:   map[string]string{},
		out:       &strings.Builder{},
		cas:       selectCAS(),
	}
	registerBuiltins(it)
	it.verifyNative = os.Getenv("OPENMAPLE_VERIFY_NATIVE") != ""
	return it
}

// selectCAS picks the computer-algebra backend per the OPENMAPLE_CAS env var:
//
//	OPENMAPLE_CAS=sage   -> Sage subprocess backend (Phase 3+)
//	OPENMAPLE_CAS=stub   -> stub backend (errors on every op)
//	(unset)              -> stub (so existing tests run without Sage)
//
// The Sage backend is lazily started on first CAS call, so selecting it costs
// nothing until an op is actually delegated.
func selectCAS() CAS {
	switch os.Getenv("OPENMAPLE_CAS") {
	case "sage":
		sb, err := newSageBackend()
		if err != nil {
			// fall back to stub but record the reason on first call
			return &errCASBackend{err: err}
		}
		return sb
	default:
		return &stubCAS{}
	}
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
		it.pushHistory(v)
	}
	return last, nil
}

// pushHistory records a computed statement result for the ditto operators
// %, %%, %%%. Assignments and NULL don't update history in Maple, but DT's
// reliance is on plain expression statements (e.g. DiffVarToList computes
// DifferentialVariableDerivation(a) then FunctionToList(u) then reads %%/%),
// so we update on every non-assignment, non-NULL result.
func (it *Interp) histOrNull(i int) Value {
	if it.history[i] == nil {
		return NULL()
	}
	return it.history[i]
}

func (it *Interp) pushHistory(v Value) {
	if isNULL(v) {
		return
	}
	it.history[2] = it.history[1]
	it.history[1] = it.history[0]
	it.history[0] = v
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
	case "%":
		return it.histOrNull(0), nil
	case "%%":
		return it.histOrNull(1), nil
	case "%%%":
		return it.histOrNull(2), nil
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
		// a global package name always wins over a captured closure value (DT
		// package procs like `DifferentialThomas/Foo` are globals).
		if v, ok := it.globals[name]; ok {
			return v, ok
		}
		// lexical-closure fallback: a free name bound in the enclosing proc at
		// construction time (e.g. dvar/ivar inside IsDifferentialVariable2).
		if it.scope.captured != nil {
			if v, ok := it.scope.captured[name]; ok {
				return v, ok
			}
		}
		return nil, false
	}
	v, ok := it.globals[name]
	return v, ok
}

// store assigns a name honoring scope.
func (it *Interp) store(name string, val Value) {
	if it.scope != nil {
		if it.scope.isLocal[name] {
			it.scope.locals[name] = val
			// Maple reference-parameter write-through: if this parameter was bound
			// to a caller's table/proc passed as a bare name, reassigning the
			// parameter writes through to that caller name too. (See scope.paramWB.)
			if wb := it.scope.paramWB[name]; wb != nil {
				storeAt(it, wb, val)
			}
			return
		}
		it.globals[name] = val
		return
	}
	it.globals[name] = val
}

// storeAt writes val to a write-through target's name in its caller scope,
// chaining further write-throughs if that name is itself a forwarded parameter.
func storeAt(it *Interp, wb *wbTarget, val Value) {
	if wb.sc == nil {
		it.globals[wb.name] = val
		return
	}
	if wb.sc.isLocal[wb.name] {
		wb.sc.locals[wb.name] = val
		if next := wb.sc.paramWB[wb.name]; next != nil {
			storeAt(it, next, val)
		}
		return
	}
	it.globals[wb.name] = val
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
		// Resolve table/proc name-aliases to the underlying object *in the
		// current (caller) scope* before they cross into a callee scope or a
		// collection. Last-name-eval yields Name{x} for a table-valued x; a
		// local such name (e.g. ProcInput's `rankingtable`) is unresolvable in
		// the callee, so we pass the table reference instead. Tables/procs are
		// reference types in Maple, so sharing the object is faithful and keeps
		// mutation visible. (See also resolveRefForStore for the assignment
		// side of the same issue.)
		v = it.resolveRefForStore(v)
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
		nm := stripBacktick(lhs.value)
		// name an anonymous proc after the variable it's assigned to (aids
		// debugging and matches Maple's "the procedure is named X" convention).
		if p, ok := rhsVal.(*Proc); ok && p.name == "" {
			p.name = nm
		}
		it.store(nm, it.resolveRefForStore(rhsVal))
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

// resolveRefForStore handles Maple's reference-assignment for tables/procs.
// When the RHS is a Name that last-name-eval-resolves (in the current scope)
// to a *Table or *Proc, we store the underlying object rather than the name.
// This is required so that `GlobalRanking := rankingtable` (where rankingtable
// is a proc-local table) makes GlobalRanking reference the *same table object*
// — otherwise the alias would dangle once the proc's scope is gone (the local
// name is unbound at the call site). Tables/procs are reference types in Maple,
// so sharing the object is the faithful semantics; reading the global back
// still applies last-name-eval (returns the global's own name) and indexing
// resolves through the stored table.
func (it *Interp) resolveRefForStore(v Value) Value {
	nm, ok := v.(Name)
	if !ok {
		return v
	}
	bound, has := it.lookup(nm.Val)
	if !has {
		return v
	}
	switch bound.(type) {
	case *Table, *Proc:
		return bound
	}
	return v
}

// assignIndexed implements t[i] := v with auto-creation of the table. The base
// may itself be an indexed expression (t[i][j] := v); intermediate tables are
// auto-vivified, matching Maple (e.g. ProlongationConsidered does
// p['ConsideredProlongations'][x] := true).
func (it *Interp) assignIndexed(lhs *tree, rhsVal Value) error {
	base := lhs.nodes[0]
	idxVals, err := it.evalArgs(lhs.nodes[1:])
	if err != nil {
		return err
	}
	idx := seqOrSingle(idxVals)

	// List-element assignment: L[i] := v where L resolves to a list (or to a
	// table/name slot holding a list). Maple lists are value types, so L[i]:=v
	// produces a modified copy and rebinds L to it. DT relies on this in
	// RemoveMultiplicativeVariableInSubtree:
	//   node['MultiplicativeVariables'][indexofvar] := 0
	// where node['MultiplicativeVariables'] is a list [infinity, infinity].
	if handled, err := it.tryListElementAssign(base, idx, rhsVal); handled {
		return err
	}

	tbl, err := it.resolveAssignTable(base)
	if err != nil {
		return err
	}
	// Resolve a table/proc Name-alias on the RHS to the underlying object before
	// storing it into the table slot, mirroring assignTo's handling for plain
	// names (which calls resolveRefForStore). Without this, an indexed assignment
	// like `rankingtable['Compare'] := Compare2` (where Compare2 is a proc-local
	// `option remember` proc) stores the bare Name{"Compare2"}; once the building
	// proc's scope is gone that name is unbound, so a later `R['Compare'](a,b)`
	// falls through to dispatchUnknownCall and returns an inert Func instead of a
	// boolean. BiggestDiffVar then never updates its candidate (the inert Func is
	// truthy under `not`), so Leader is wrong and ReduceWRTJanetTrees spins.
	// Tables/procs are reference types in Maple, so storing the object is faithful
	// and keeps mutation visible. (See resolveRefForStore.)
	tbl.set(idx, it.resolveRefForStore(rhsVal))
	return nil
}

// tryListElementAssign handles `base[i] := v` when `base` evaluates to a list.
// Maple lists are value types: list-element assignment produces a modified copy
// and rebinds `base` to it. We read the current list (via normal evaluation of
// the base node), copy it with element i (1-based) replaced, and write the new
// list back to wherever `base` lives — a plain variable, or a table slot if the
// base is itself an indexed expression. Returns handled=false (and no error) if
// the base does not resolve to a list, so the caller falls through to the normal
// table path.
func (it *Interp) tryListElementAssign(base *tree, idx, rhsVal Value) (bool, error) {
	cur, err := it.eval(base)
	if err != nil {
		// Base may be an unassigned/auto-vivifying table slot; let the normal
		// table path handle (and report) it.
		return false, nil
	}
	lst, ok := it.derefTable(cur).(List)
	if !ok {
		return false, nil
	}
	bn, ok := intVal(idx)
	if !ok || !bn.IsInt64() {
		return false, nil
	}
	n := bn.Int64()
	if n < 1 || n > int64(len(lst.Items)) {
		// Out-of-range / non-integer index into a list isn't something DT does;
		// defer to the normal path's error.
		return false, nil
	}
	items := make([]Value, len(lst.Items))
	copy(items, lst.Items)
	items[n-1] = it.resolveRefForStore(rhsVal)
	newList := List{Items: items}

	switch base.group {
	case variable:
		it.store(stripBacktick(base.value), newList)
		return true, nil
	case indexNode:
		parent, perr := it.resolveAssignTable(base.nodes[0])
		if perr != nil {
			return true, perr
		}
		idxVals, ierr := it.evalArgs(base.nodes[1:])
		if ierr != nil {
			return true, ierr
		}
		parent.set(seqOrSingle(idxVals), newList)
		return true, nil
	}
	return false, nil
}

// resolveAssignTable resolves the base of an indexed assignment to a *Table,
// auto-creating tables as needed (Maple table auto-vivification). The base node
// is either a plain name (t[i] := v) or a nested index (t[i][j] := v); for the
// nested case it recurses, materialising the intermediate table slot.
func (it *Interp) resolveAssignTable(base *tree) (*Table, error) {
	switch base.group {
	case variable:
		name := stripBacktick(base.value)
		cur, bound := it.lookupRaw(name)
		if bound {
			if t, isT := it.derefTable(cur).(*Table); isT {
				return t, nil
			}
			return nil, fmt.Errorf("cannot index-assign into non-table %s", name)
		}
		tbl := newTable()
		it.store(name, tbl)
		return tbl, nil
	case indexNode:
		// The inner container may be a list whose element IS a table, as in DT's
		// InequationLCM: result[-1]['Q'] := v, where result is a list and
		// result[-1] selects the last element (a DeepCopy'd system table). Lists
		// are value types, but their table elements are reference types, so
		// mutating result[-1] in place is the correct Maple semantics. Evaluate the
		// inner container first; if it is a list, index it to reach the table.
		if innerVal, ierr := it.eval(base.nodes[0]); ierr == nil {
			if _, isList := it.derefTable(innerVal).(List); isList {
				idxVals, err := it.evalArgs(base.nodes[1:])
				if err != nil {
					return nil, err
				}
				elem, ok, err := indexCollection(it.derefTable(innerVal), idxVals)
				if err != nil {
					return nil, err
				}
				if ok {
					if t, isT := it.derefTable(elem).(*Table); isT {
						return t, nil
					}
				}
				return nil, fmt.Errorf("cannot index-assign into non-table list element")
			}
		}
		parent, err := it.resolveAssignTable(base.nodes[0])
		if err != nil {
			return nil, err
		}
		idxVals, err := it.evalArgs(base.nodes[1:])
		if err != nil {
			return nil, err
		}
		idx := seqOrSingle(idxVals)
		if cur, ok := parent.get(idx); ok {
			if t, isT := it.derefTable(cur).(*Table); isT {
				return t, nil
			}
			return nil, fmt.Errorf("cannot index-assign into non-table slot")
		}
		tbl := newTable()
		parent.set(idx, tbl)
		return tbl, nil
	default:
		return nil, errUnimplemented("indexed assignment to non-name base")
	}
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
