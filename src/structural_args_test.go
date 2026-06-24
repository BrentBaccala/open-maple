package main

import (
	"os"
	"testing"
)

// TestStructuralArgs pins the fix for the indets-class bug across normal / expand
// / numer / denom: Maple applies these RECURSIVELY through lists, sets and
// equations (normal help page: "applies recursively to lists, sets, ranges,
// equations, relations"). open-maple wires a list/set as {"exprlist": ...} and an
// equation as {"poly": "lhs = rhs"}; before the fix those forms fell through to a
// Sage handler that raised on the exprlist / failed to parse the "=" — the same
// silent-mishandle class that made indets time out on the combined-hydrogen run.
//
// Sage-gated (the structure mapping lives in cas/sage_server.py).
func TestStructuralArgs(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" {
		t.Skip("set OPENMAPLE_CAS=sage to run the Sage-backed structural-arg test")
	}
	it := NewInterp()
	cases := []struct{ expr, want string }{
		// normal over a list: each element put in lowest terms.
		{"normal([(x^2 - 1)/(x - 1), (y^2 - 4)/(y - 2)]);", "[x + 1, y + 2]"},
		// normal of an equation: applied to each side.
		{"normal((x^2 - 1)/(x - 1) = (y^2 - 4)/(y - 2));", "x + 1 = y + 2"},
		// expand over a list.
		{"expand([(x + 1)^2, (x - 1)*(x + 1)]);", "[x^2 + 2*x + 1, x^2 - 1]"},
		// expand of an equation.
		{"expand((x + 1)^2 = 0);", "x^2 + 2*x + 1 = 0"},
		// numer / denom over a list.
		{"numer([(x + 1)/y, a/b]);", "[x + 1, a]"},
		{"denom([(x + 1)/y, a/b]);", "[y, b]"},
	}
	for _, c := range cases {
		v, err := it.Exec(c.expr)
		if err != nil {
			t.Fatalf("%s err: %v", c.expr, err)
		}
		if got := printValue(v); got != c.want {
			t.Errorf("%s: got %q, want %q", c.expr, got, c.want)
		}
	}
}
