package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// dtSrcDir is the DifferentialThomas source directory. Overridable via env so
// the harness is not pinned to one checkout location.
func dtSrcDir() string {
	if d := os.Getenv("DT_SRC"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "DifferentialThomas", "src")
}

// macroSubstitute replicates update.sh's only transformation:
//
//	sed 's|@@PACKAGE@@|DifferentialThomas|g'
//
// It is the exact build-time pass that turns the shipped sources into the form
// Maple actually reads.
func macroSubstitute(src string) string {
	return strings.ReplaceAll(src, "@@PACKAGE@@", "DifferentialThomas")
}

// isSourceFile filters the src/ directory down to the 22 Maple source files
// (excluding the GPL/LGPL license texts).
func isSourceFile(name string) bool {
	switch name {
	case "gpl.txt", "lgpl.txt":
		return false
	}
	if strings.HasSuffix(name, ".tmp") {
		return false
	}
	return true
}

// TestParseDifferentialThomas parses every DifferentialThomas source file after
// the @@PACKAGE@@ macro pass and reports a pass/fail tally. This is the Phase-1
// milestone: target 22/22 parse clean.
func TestParseDifferentialThomas(test *testing.T) {
	dir := dtSrcDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		test.Skipf("DifferentialThomas src not found at %s: %v", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !isSourceFile(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	pass := 0
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			test.Errorf("%-22s READ ERROR: %v", n, err)
			continue
		}
		src := macroSubstitute(string(data))
		tokens, err := tokenizer(src)
		if err != nil {
			test.Errorf("%-22s LEX FAIL: %v", n, err)
			continue
		}
		_, err = parser(tokens)
		if err != nil {
			test.Errorf("%-22s PARSE FAIL: %v", n, err)
			continue
		}
		pass++
		test.Logf("%-22s OK (%d tokens)", n, len(tokens))
	}

	test.Logf("DifferentialThomas parse result: %d/%d files clean", pass, len(names))
	if pass < 20 {
		test.Errorf("Phase-1 milestone not met: only %d/%d files parse (need >=20)", pass, len(names))
	}
}
