package main

import (
	"path/filepath"
	"testing"
)

// TestSaveReadRoundTrip pins that `save NAME, file` writes a re-readable text
// form and `read file` rebinds the saved names — the persistence the hydrogen
// worksheet relies on so its ~19-minute Thomas decomposition is computed once
// (save EI, cat(currentdir(), "/hydrogen_thomas_result.m")) and reloaded
// thereafter. The saved value re-parses to the identical structure.
func TestSaveReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "zz_roundtrip.m")

	it := NewInterp()
	prog := `EI := [[ [a[0,0,0], b], [c - d] ], [ [x^2 - y], [] ]]:
save EI, "` + file + `":
EI := 'EI':
read "` + file + `":
EI;`
	v, err := it.Exec(prog)
	if err != nil {
		t.Fatalf("save/read program errored: %v", err)
	}
	want := "[[[a[0, 0, 0], b], [c - d]], [[x^2 - y], []]]"
	if got := printValue(v); got != want {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, want)
	}
}

// TestSaveUnassignedErrors pins that saving an unassigned name is an error
// (Maple behaviour), rather than silently writing `NAME := NAME`.
func TestSaveUnassignedErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "zz_unassigned.m")
	it := NewInterp()
	_, err := it.Exec(`save nope, "` + file + `":`)
	if err == nil {
		t.Fatalf("expected error saving an unassigned name")
	}
}

// TestCurrentdir pins that currentdir() returns a non-empty string (the cwd),
// so cat(currentdir(), "/file") builds an absolute path. It used to evaluate to
// an inert currentdir() symbol.
func TestCurrentdir(t *testing.T) {
	it := NewInterp()
	v, err := it.Exec(`currentdir();`)
	if err != nil {
		t.Fatalf("currentdir errored: %v", err)
	}
	s, ok := v.(MString)
	if !ok || s.Val == "" {
		t.Fatalf("currentdir(): got %v, want a non-empty string", printValue(v))
	}
}

// TestNativeEvala pins that evala() of a polynomial is handled natively (no Sage
// process needed — this test runs in the default, Sage-less suite) and returns
// the expanded standard form. With no algebraic numbers in play, evala is the
// identity-up-to-normalization that DT calls as evala(expand(...)); routing it
// natively both fixes the hydrogen crash (a huge term-string overflowed Sage's
// parser recursion limit) and removes the round-trip.
func TestNativeEvala(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"evala((x + y)*(x - y));", "x^2 - y^2"},
		{"evala(2*u[1,0] - u[1,0]);", "u[1, 0]"},
		// native expand reconstructs descending by total degree; same-degree
		// tiebreak is the native canonical monomial key (not Sage's degrevlex),
		// so a*b sorts before a^2 — semantically identical, deterministic.
		{"evala((a + b)^2);", "2*a*b + a^2 + b^2"},
	}
	for _, c := range cases {
		v, err := it.Exec(c.expr)
		if err != nil {
			t.Fatalf("%s err: %v", c.expr, err)
		}
		if got := printValue(v); got != c.want {
			t.Fatalf("%s: got %q, want %q", c.expr, got, c.want)
		}
	}
}
