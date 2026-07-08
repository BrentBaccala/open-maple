//main.go contains main, the entry point of the program.
//
//With no arguments it runs the interactive REPL (execPrint, in debug_util.go).
//With a filename argument it runs that Maple (.mpl) program through the
//interpreter — the way the DifferentialThomas example programs are invoked.

package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
)

func main() {
	stopProfiles := startProfiles()
	if len(os.Args) > 1 {
		code := runFile(os.Args[1])
		stopProfiles()
		os.Exit(code)
	}
	execPrint()
	stopProfiles()
}

// startProfiles turns on pprof profiling when requested via the environment:
// OPENMAPLE_CPUPROFILE=<file> and/or OPENMAPLE_MEMPROFILE=<file>. Returns a
// stop function that finalizes the profiles; it must run before os.Exit.
func startProfiles() func() {
	stop := func() {}
	if path := os.Getenv("OPENMAPLE_CPUPROFILE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cpuprofile: "+err.Error())
		} else if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "cpuprofile: "+err.Error())
			f.Close()
		} else {
			stop = func() {
				pprof.StopCPUProfile()
				f.Close()
			}
		}
	}
	if path := os.Getenv("OPENMAPLE_MEMPROFILE"); path != "" {
		cpuStop := stop
		stop = func() {
			cpuStop()
			f, err := os.Create(path)
			if err != nil {
				fmt.Fprintln(os.Stderr, "memprofile: "+err.Error())
				return
			}
			runtime.GC()
			if err := pprof.WriteHeapProfile(f); err != nil {
				fmt.Fprintln(os.Stderr, "memprofile: "+err.Error())
			}
			f.Close()
		}
	}
	return stop
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
