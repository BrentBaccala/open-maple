package main

import (
	"os"
	"testing"
)

// These tests require Sage. They are skipped unless OPENMAPLE_CAS=sage is set
// (so the default `go test ./...` — which runs the stub backend — stays fast
// and Sage-independent). Run with:
//
//   OPENMAPLE_CAS=sage go test ./... -run TestSage -v
//
// Every expected value is computed independently (by hand or from the LGPL
// source / published literature), never from Maple — per the licensing posture.

func sageInterp(t *testing.T) *Interp {
	t.Helper()
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage to run Sage backend tests")
	}
	it := NewInterp()
	if _, ok := it.cas.(*SageBackend); !ok {
		t.Fatalf("expected SageBackend, got %T", it.cas)
	}
	return it
}

// execStr runs code and returns the printed value.
func execStr(t *testing.T, it *Interp, code string) string {
	t.Helper()
	v, err := it.Exec(code)
	if err != nil {
		t.Fatalf("%s -> error: %v", code, err)
	}
	return printValue(v)
}

func TestSageLandedOps(t *testing.T) {
	it := sageInterp(t)

	cases := []struct {
		code string
		want string
	}{
		// factor: x^2 - 1 = (x-1)(x+1).  factors() returns [unit,[[f,m],...]].
		{"factor(x^2-1);", "[1, [[x - 1, 1], [x + 1, 1]]]"},
		// gcd(x^2-1, x-1) = x-1
		{"gcd(x^2-1, x-1);", "x - 1"},
		// lcm(x, x^2) = x^2
		{"lcm(x, x^2);", "x^2"},
		// normal((x^2-1)/(x-1)) = x+1
		{"normal((x^2-1)/(x-1));", "x + 1"},
		// numer / denom of (x^2-1)/(x-1) -> after lowest terms x+1 / 1
		{"numer((x^2-1)/(x-1));", "x + 1"},
		{"denom((x^2-1)/(x-1));", "1"},
		// degree(x^3*y + 1, x) = 3
		{"degree(x^3*y+1, x);", "3"},
		// expand((x+1)^2) = x^2 + 2x + 1
		{"expand((x+1)^2);", "x^2 + 2*x + 1"},
		// rem(x^2-1, x-1, x) = 0 ; quo = x+1
		{"rem(x^2-1, x-1, x);", "0"},
		{"quo(x^2-1, x-1, x);", "x + 1"},
		// primpart(2*x+4) = x+2 ; content = 2
		{"primpart(2*x+4, x);", "x + 2"},
		{"content(2*x+4, x);", "2"},
		// binomial(5,2) = 10
		{"binomial(5,2);", "10"},
		// coeff(x^2 + 3*x + 5, x, 1) = 3
		{"coeff(x^2+3*x+5, x, 1);", "3"},
		// lcoeff(3*x^2+1, x) = 3
		{"lcoeff(3*x^2+1, x);", "3"},
	}
	for _, c := range cases {
		if got := execStr(t, it, c.code); got != c.want {
			t.Errorf("%s = %q, want %q", c.code, got, c.want)
		}
	}
}

// TestSageDiffSymbolic checks polynomial AND transcendental diff.
func TestSageDiffSymbolic(t *testing.T) {
	it := sageInterp(t)

	// d/dx (x^3) = 3x^2
	if got := execStr(t, it, "diff(x^3, x);"); got != "3*x^2" {
		t.Errorf("diff(x^3,x) = %q, want 3*x^2", got)
	}
	// d/dx (cos(phi)*x^2) = 2*x*cos(phi)  (transcendental — Sage SR).
	got := execStr(t, it, "diff(cos(phi)*x^2, x);")
	// Sage prints "2*x*cos(phi)"; accept either operand order after reparse.
	if got != "2*x*cos(phi)" && got != "2*cos(phi)*x" {
		t.Errorf("diff(cos(phi)*x^2,x) = %q, want 2*x*cos(phi)", got)
	}
}

// TestSageIndexedRoundTrip is the #1 correctness risk: jet/indexed names must
// round-trip through sanitization. factor of a polynomial in u[1,0].
func TestSageIndexedRoundTrip(t *testing.T) {
	it := sageInterp(t)

	// factor(u[1,0]^2 - 1) = (u[1,0]-1)(u[1,0]+1) — the indexed name must
	// come back as u[1,0], not the sanitized form.
	got := execStr(t, it, "factor(u[1,0]^2 - 1);")
	want := "[1, [[u[1, 0] - 1, 1], [u[1, 0] + 1, 1]]]"
	if got != want {
		t.Errorf("factor(u[1,0]^2-1) = %q, want %q", got, want)
	}

	// gcd over two indexed names u[0,1] and u[1,0].
	got2 := execStr(t, it, "gcd(u[1,0]*u[0,1], u[1,0]);")
	if got2 != "u[1, 0]" {
		t.Errorf("gcd = %q, want u[1, 0]", got2)
	}
}

// TestSanitizerRoundTrip is a pure (no-Sage) unit test of the name
// sanitization scheme: every sanitized name must map back to the original.
func TestSanitizerRoundTrip(t *testing.T) {
	san := newSanitizer()
	cases := []Value{
		&Indexed{Head: Name{"u"}, Idx: []Value{newInt(1), newInt(0)}},
		&Indexed{Head: Name{"u"}, Idx: []Value{newInt(0), newInt(1)}},
		&Indexed{Head: Name{"phi"}, Idx: []Value{newInt(0)}},
		&Indexed{Head: Name{"u"}, Idx: []Value{newInt(-2), newInt(3)}},
		Name{"x"},
		Name{"y"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		var san1 string
		switch n := c.(type) {
		case *Indexed:
			san1 = san.sanitizeIndexed(n)
		case Name:
			san1 = san.sanitizeName(n.Val)
		}
		if seen[san1] {
			t.Errorf("sanitized name collision: %s for %s", san1, printValue(c))
		}
		seen[san1] = true
		if !identRe.MatchString(san1) {
			t.Errorf("sanitized name %q (from %s) is not a valid identifier", san1, printValue(c))
		}
		back := san.unsanitizeName(san1)
		if printValue(back) != printValue(c) {
			t.Errorf("round-trip failed: %s -> %s -> %s", printValue(c), san1, printValue(back))
		}
	}
}

// TestSageMatrixLA exercises the small-matrix linear-algebra path.
func TestSageMatrixLA(t *testing.T) {
	it := sageInterp(t)
	// ColumnDimension / RowDimension of a 2x3 matrix via LinearAlgebra.
	// Construct the matrix as a list-of-rows and pass through Matrix op.
	if got := execStr(t, it, "LinearAlgebra[Rank](Matrix([[1,2],[2,4]]));"); got != "1" {
		t.Errorf("Rank([[1,2],[2,4]]) = %q, want 1", got)
	}
}
