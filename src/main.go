//main.go contains main, the entry point of the program.
//
//With no arguments it runs the interactive REPL (execPrint, in debug_util.go).
//With a filename argument it runs that Maple (.mpl) program through the
//interpreter — the way the DifferentialThomas example programs are invoked.

package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 {
		os.Exit(runFile(os.Args[1]))
	}
	execPrint()
}

// runFile reads a Maple program from path and executes it. The program loads any
// package itself via with(DifferentialThomas). printf/print output accumulates in
// the interpreter's output buffer and is written to stdout when the program
// finishes (or errors). Returns a process exit code.
func runFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	it := NewInterp()
	_, execErr := it.ExecProgram(string(data))
	fmt.Fprint(os.Stdout, it.out.String())
	// Emit the ref-traffic / coercion-fallback summary so the example-suite
	// runner (and a human) can see the optimization's effect on this run.
	it.reportRefStats()
	if execErr != nil {
		fmt.Fprintln(os.Stderr, "error: "+execErr.Error())
		return 1
	}
	return 0
}
