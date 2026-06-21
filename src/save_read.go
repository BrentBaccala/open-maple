package main

import (
	"fmt"
	"os"
	"strings"
)

// save / read implement Maple's text-format object persistence. The hydrogen
// ansatz worksheet computes a ~19-minute Thomas decomposition and then does
//
//	save EI, cat(currentdir(), "/hydrogen_thomas_result.m"):
//
// so the result never has to be recomputed. Maple's `.m` is an internal binary
// format, but Maple also supports a re-readable text form, and since this port
// both writes and reads the file we use the text form unconditionally: each
// saved name is written as a Maple assignment statement (`NAME := <value>;`) in
// the same surface syntax `print`/`%a` produce, so a later `read` of the file is
// just executing those assignments back into the current scope.

// evalSave handles `save e1, ..., e(N-1), filenameExpr`. The trailing argument is
// evaluated to a filename string; each preceding argument must be a name, whose
// current binding is written in re-readable form.
func (it *Interp) evalSave(n *tree) (Value, error) {
	if len(n.nodes) < 2 {
		return nil, fmt.Errorf("save: expected at least one name and a filename")
	}
	fnNode := n.nodes[len(n.nodes)-1]
	fnVal, err := it.eval(fnNode)
	if err != nil {
		return nil, err
	}
	fname, ok := nameOrStr(fnVal)
	if !ok {
		return nil, fmt.Errorf("save: filename must be a string, got %s", printValue(fnVal))
	}

	var b strings.Builder
	for _, nameNode := range n.nodes[:len(n.nodes)-1] {
		if nameNode.group != variable {
			return nil, fmt.Errorf("save: can only save names, got %s",
				nameNode.group.String())
		}
		name := stripBacktick(nameNode.value)
		v, found := it.lookup(name)
		if !found {
			// Maple errors on saving an unassigned name; match that rather than
			// silently writing `NAME := NAME`.
			return nil, fmt.Errorf("save: %s is not assigned", name)
		}
		fmt.Fprintf(&b, "%s := %s:\n", name, printValue(v))
	}
	if err := os.WriteFile(fname, []byte(b.String()), 0644); err != nil {
		return nil, fmt.Errorf("save: %v", err)
	}
	return NULL(), nil
}

// evalRead handles `read filenameExpr`: it reads the file and executes its
// contents in the current scope. Paired with evalSave, this round-trips saved
// names back into the session.
func (it *Interp) evalRead(n *tree) (Value, error) {
	fnVal, err := it.eval(n.nodes[0])
	if err != nil {
		return nil, err
	}
	fname, ok := nameOrStr(fnVal)
	if !ok {
		return nil, fmt.Errorf("read: filename must be a string, got %s", printValue(fnVal))
	}
	data, err := os.ReadFile(fname)
	if err != nil {
		return nil, fmt.Errorf("read: %v", err)
	}
	_, err = it.Exec(string(data))
	return NULL(), err
}
