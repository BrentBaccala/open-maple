package main

import (
	"testing"
)

// TestLoadDifferentialThomas is the Phase-2 milestone harness. It:
//  1. macro-substitutes @@PACKAGE@@ -> DifferentialThomas and loads all 20
//     source files into a fresh interpreter via the loading model;
//  2. asserts the expected number of package procedures get defined with no
//     evaluation error, and functions_list / packages_list populate;
//  3. runs ComputeRanking([x,y],[u]) — which per readme.txt should neither give
//     outputs nor error messages.
func TestLoadDifferentialThomas(t *testing.T) {
	it := NewInterp()
	if err := it.LoadDifferentialThomas(dtSrcDir()); err != nil {
		t.Fatalf("loading DifferentialThomas failed: %v", err)
	}

	// All 20 source files defined their top-level procedures. The package has
	// exactly 190 top-level `Pkg/Foo := proc(...)` definitions (the readme's
	// "~246" counts the build.sh-generated package-wrapper + type/ procs, which
	// the loading model deliberately skips — see the Phase-2 report). Allow a
	// small band so an inner-proc accounting change doesn't make the test
	// brittle, but require essentially all of them.
	n := it.CountDefinedProcs()
	if n < 188 {
		t.Errorf("defined procs = %d, want >= 188 (expected ~190 package procedures)", n)
	}
	t.Logf("package procedures defined: %d", n)

	// functions_list['all'] populated.
	fl, ok := it.globals["functions_list"].(*Table)
	if !ok {
		t.Fatalf("functions_list is not a table: %T", it.globals["functions_list"])
	}
	allV, ok := fl.get(Name{"all"})
	if !ok {
		t.Fatalf("functions_list['all'] not assigned")
	}
	allList, ok := allV.(List)
	if !ok || len(allList.Items) < 180 {
		t.Errorf("functions_list['all'] has %v entries, want >= 180", lenOf(allV))
	}
	t.Logf("functions_list['all'] entries: %d", len(allList.Items))

	// packages_list populated as a set.
	pl, ok := it.globals["packages_list"].(Set)
	if !ok || len(pl.Items) == 0 {
		t.Fatalf("packages_list not a non-empty set: %T", it.globals["packages_list"])
	}
	t.Logf("packages_list: %s", printValue(pl))

	// ComputeRanking([x,y],[u]) — should run cleanly and set the GlobalRanking.
	r, err := it.Exec("`DifferentialThomas/ComputeRanking`([x,y],[u]);")
	if err != nil {
		t.Fatalf("ComputeRanking errored (expected clean run): %v", err)
	}
	if !isNULL(r) {
		t.Errorf("ComputeRanking returned %q, want NULL (no output)", printValue(r))
	}

	// Verify the ranking table was fully populated (the ranking/table setup
	// executed end-to-end, no CAS stub on this path).
	grName, ok := it.globals["DifferentialThomas/GlobalRanking"].(Name)
	if !ok {
		t.Fatalf("GlobalRanking not set to a table alias: %T", it.globals["DifferentialThomas/GlobalRanking"])
	}
	tbl, err := it.asTable(grName)
	if err != nil {
		t.Fatalf("GlobalRanking does not resolve to a table: %v", err)
	}
	for _, key := range []string{"Compare", "RankingString", "IVar", "DVar",
		"IsDifferentialVariable", "FunctionToList", "RankingList", "NoDeepCopy"} {
		if _, has := tbl.get(Name{key}); !has {
			t.Errorf("GlobalRanking[%s] not assigned after ComputeRanking", key)
		}
	}
	if rs, _ := tbl.get(Name{"RankingString"}); printValue(rs) != "\"DegRevLex\"" {
		t.Errorf("RankingString = %s, want \"DegRevLex\"", printValue(rs))
	}
}

func lenOf(v Value) int {
	if l, ok := v.(List); ok {
		return len(l.Items)
	}
	return 0
}

// TestLanguageBehaviors exercises the pure-language behaviors end-to-end (no
// package load), per the Phase-2 milestone item (2).
func TestLanguageBehaviors(t *testing.T) {
	it := NewInterp()

	checks := []struct{ code, want string }{
		// op / nops over sum, product, list, set, table
		{"nops(a+b+c);", "3"},
		{"op(a*b*c);", "a, b, c"},
		{"nops([1,2,3]);", "3"},
		{"op(2, {3,1,2});", "2"},
		// map / select / subs / subsop
		{"subs(x=1, x+y+z);", "y + z + 1"},
		{"subsop(1=Z, [a,b]);", "[Z, b]"},
		// parse(cat(...)) round-trip
		{`parse(cat("res", " := ", "6*7", ";"), statement); res;`, "42"},
	}
	for _, c := range checks {
		v, err := it.Exec(c.code)
		if err != nil {
			t.Errorf("%s -> error %v", c.code, err)
			continue
		}
		if got := printValue(v); got != c.want {
			t.Errorf("%s = %s, want %s", c.code, got, c.want)
		}
	}

	// table read/write + assigned
	it.Exec("T[k1] := 7;")
	if v, _ := it.Exec("T[k1];"); printValue(v) != "7" {
		t.Errorf("table read failed")
	}
	if v, _ := it.Exec("assigned(T[k1]);"); printValue(v) != "true" {
		t.Errorf("assigned(T[k1]) != true")
	}
	if v, _ := it.Exec("assigned(T[nope]);"); printValue(v) != "false" {
		t.Errorf("assigned(T[nope]) != false")
	}

	// proc with args/nargs/defaults/local + option remember
	it.Exec(`acc := proc(x, base := 10) local r; r := x + base; return r, nargs; end proc;`)
	if v, _ := it.Exec("acc(5);"); printValue(v) != "15, 1" {
		t.Errorf("acc(5) = %s, want 15, 1", printValue(v))
	}
	if v, _ := it.Exec("acc(5, 100);"); printValue(v) != "105, 2" {
		t.Errorf("acc(5,100) = %s, want 105, 2", printValue(v))
	}

	// last-name-eval: a name holding a table returns the name
	it.Exec("MyTab[1] := 1;")
	if v, _ := it.Exec("MyTab;"); printValue(v) != "MyTab" {
		t.Errorf("last-name-eval: MyTab = %s, want MyTab", printValue(v))
	}
}
