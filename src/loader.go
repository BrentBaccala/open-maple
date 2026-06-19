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

	// Run the package init proc. In a real Maple .lib build, build.sh wraps every
	// package function as
	//   pkg[f] := proc() `DT/initialized` <> 'true' and `DT/init`() <> 0: `DT/f`(args): end
	// so the first call to any package function lazily runs `DT/init`, which
	// builds `DifferentialThomasGlobalOptions` (the default options table:
	// CompareStrategy, Factor, ReduceQListInSystem="Inequations", ...). The
	// Phase-2 loading model deliberately skips the .lib archiving, so those
	// wrappers don't exist and init was never triggered — leaving the options
	// unset. ComputeRanking/ProcInput then merge an empty options table into the
	// ranking, so e.g. ReduceQListInSystem is missing and ReduceQListInSystem
	// takes the wrong branch (re-inserting half of Q every DoNextStep, doubling
	// Q without bound). Call init explicitly here to match Maple's lazy trigger.
	if _, err := it.Exec("`DifferentialThomas/initialized` <> 'true' and `DifferentialThomas/init`():"); err != nil {
		return fmt.Errorf("DifferentialThomas/init: %w", err)
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
