package main

// scope is a procedure activation record. The interpreter holds the single
// global symbol table separately (Interp.globals). Maple scoping used by
// DifferentialThomas:
//
//   - parameters and `local` names live in the activation's locals map.
//   - `global` names (always declared in DT) resolve to the interpreter's
//     global table.
//   - an undeclared name read inside a proc falls through to the global table
//     (last-name-eval / global functions like `DifferentialThomas/Foo`).
//   - an undeclared name *assigned* inside a proc: Maple would auto-localise,
//     but DT declares every such name, so we treat an undeclared assignment
//     target as global (matches DT's explicit-declaration style and the
//     loading model where procs write into global package names). This is a
//     documented simplification.
//
// At the top level there is no scope (scope==nil); names live in globals.
type scope struct {
	locals   map[string]Value
	isLocal  map[string]bool // declared local or a parameter
	isGlobal map[string]bool // declared global
	args     []Value         // actual call arguments (for args/nargs/_passed)
	nparams  int             // number of declared positional params
	procName string
	captured map[string]Value // lexical-closure env captured at proc construction

	// paramWB records, per parameter name, a write-through target in the
	// caller's scope. Maple binds a reference-type argument (a table/proc passed
	// as a bare assignable name) such that reassigning the *parameter* inside the
	// proc writes through to the caller's name — this is what makes DT's
	// `ReduceWithSideEffects` (which does `q := PseudoRemainder(...)` on its
	// parameter and is called for its side effect, the caller ignoring the return
	// value) actually reduce the caller's polynomial object. Without this, the
	// rebind only updates the callee's local and the reduction is lost (the poly
	// then mis-reduces in the tail pass, the leader changes, and the system is
	// falsely declared Inconsistent — dropping the surviving component). Only set
	// for arguments that were bare names bound to a *Table/*Proc in the caller.
	paramWB map[string]*wbTarget
}

// wbTarget is a write-through destination: a name in a caller scope (sc==nil
// means the interpreter's global table).
type wbTarget struct {
	sc   *scope
	name string
}

func newScope() *scope {
	return &scope{
		locals:   map[string]Value{},
		isLocal:  map[string]bool{},
		isGlobal: map[string]bool{},
	}
}
