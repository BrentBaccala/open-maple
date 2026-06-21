package main

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// TestOverview3Var runs the 3-independent-variable, 3-dependent-variable
// nonlinear system from DifferentialThomas's doc/DifferentialThomasPrintDifferential
// System worksheet:
//
//	ComputeRanking([x,y,z],[u,v,w]):
//	DifferentialThomasDecomposition(
//	  [u[1,0,0]-2*u[1,0,0]*v[0,1,0]+v[0,1,0],
//	   u[0,1,0]*w[0,0,1]+2*u[0,1,0]*v[1,0,0]+v[1,0,0],
//	   w[0,0,0]-u[0,0,0]*u[0,1,0]], [])
//
// This is the largest system exercised end to end. It only completes in
// reasonable time because expand/coeff/degree/indets run natively (native_poly.go)
// instead of round-tripping to Sage on every call; with the Sage bridge it ran
// past a 240s timeout still deep in subresultant-PRS arithmetic.
//
// We pin the structure (a 4-component decomposition) and the simplest component
// (u=0, v_x=0, v_y=0, w=0) exactly, not the full ~1.5 KB of all four components
// (whose surface form is sensitive to native vs Sage term ordering).
//
// Skipped unless OPENMAPLE_CAS=sage.
func TestOverview3Var(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage to run the 3-variable Overview system")
	}
	it := NewInterp()
	if err := it.LoadDifferentialThomas(dtSrcDir()); err != nil {
		t.Fatalf("loading DifferentialThomas failed: %v", err)
	}
	if _, err := it.Exec("`DifferentialThomas/ComputeRanking`([x,y,z],[u,v,w]);"); err != nil {
		t.Fatalf("ComputeRanking errored: %v", err)
	}
	r, err := it.Exec("`DifferentialThomas/DifferentialThomasDecomposition`(" +
		"[u[1,0,0]-2*u[1,0,0]*v[0,1,0]+v[0,1,0], " +
		"u[0,1,0]*w[0,0,1]+2*u[0,1,0]*v[1,0,0]+v[1,0,0], " +
		"w[0,0,0]-u[0,0,0]*u[0,1,0]], []);")
	if err != nil {
		t.Fatalf("decomposition errored: %v", err)
	}
	lst, ok := r.(List)
	if !ok || len(lst.Items) != 4 {
		t.Fatalf("expected a 4-component decomposition, got %s", printValue(r))
	}
	// Every component must PrettyPrint without error; collect the strings.
	var pps []string
	for i := range lst.Items {
		it.globals["__c3"] = lst.Items[i]
		pp, err := it.Exec("`DifferentialThomas/PrettyPrintDifferentialSystem`(__c3);")
		if err != nil {
			t.Fatalf("component %d PrettyPrint errored: %v", i+1, err)
		}
		pps = append(pps, printValue(pp))
	}
	// The fully-collapsed component u=0, v_x=0, v_y=0, w=0 must be present.
	want := "[u(x, y, z) = 0, diff(v(x, y, z), x) = 0, diff(v(x, y, z), y) = 0, w(x, y, z) = 0]"
	sort.Strings(pps)
	found := false
	for _, s := range pps {
		if strings.Contains(s, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a component containing %q; got:\n%s", want, strings.Join(pps, "\n"))
	}
}
