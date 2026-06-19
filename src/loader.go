package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dtSourceOrder is the order in which the DifferentialThomas source files are
// read by build.sh (SRC list). init must come early (it defines the option
// machinery); the others define procs into global backtick names.
var dtSourceOrder = []string{
	"general", "history", "differentialvariables", "derivation",
	"polynomobjects", "algebraic", "factor", "reduction", "ranking",
	"sorting", "strategy", "tree", "passivity", "differentialsystems",
	"conversion", "solutions", "main", "walk", "benchmarking", "init",
}

// LoadDifferentialThomas macro-substitutes @@PACKAGE@@ and loads every source
// file into the interpreter, replicating the build.sh loading model (minus the
// .lib archiving). Returns the list of source files successfully read.
func (it *Interp) LoadDifferentialThomas(srcDir string) error {
	// initialise the accumulator tables build.sh sets up before reading sources.
	it.globals["functions_list"] = newTable()
	it.globals["packages_list"] = Set{}
	it.globals["types_list"] = List{}

	for _, f := range dtSourceOrder {
		path := filepath.Join(srcDir, f)
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		code := strings.ReplaceAll(string(raw), "@@PACKAGE@@", "DifferentialThomas")
		if _, err := it.Exec(code); err != nil {
			return fmt.Errorf("load %s: %w", f, err)
		}
	}
	return nil
}

// CountDefinedProcs counts global names bound to a Proc value (the package's
// procedures defined by the source files).
func (it *Interp) CountDefinedProcs() int {
	n := 0
	for _, v := range it.globals {
		if _, ok := v.(*Proc); ok {
			n++
		}
	}
	return n
}
