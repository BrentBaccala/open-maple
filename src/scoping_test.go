package main

import "testing"

// TestFunctionHeadResolution: a bound name used as the HEAD of a function call
// resolves to its value. (s -> s(x))(Vf) = Vf(x); seq(p(x), p in [...])
// substitutes each. Mirrors the earlier index-head fix.
func TestFunctionHeadResolution(t *testing.T) {
	it := NewInterp()
	cases := []struct{ expr, want string }{
		{"(s -> s(x))(Vf);", "Vf(x)"},
		{"[seq(p(x), p in [a,b,c])];", "[a(x), b(x), c(x)]"},
		{"f := y -> y + 1: (s -> s(3))(f);", "4"},
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

// TestSeqVarNoLeak: a seq/add/mul loop variable is local — it does not leak to
// the enclosing scope, and a prior binding is restored.
func TestSeqVarNoLeak(t *testing.T) {
	it := NewInterp()
	it.Exec("L := [seq(p(x), p in [aa,bb,cc])]:")
	if v, _ := it.Exec("p;"); printValue(v) != "p" {
		t.Fatalf("seq leaked loop var: p = %s, want unassigned p", printValue(v))
	}
	it.Exec("q := 5: M := [seq(q^2, q=1..3)]:")
	if v, _ := it.Exec("q;"); printValue(v) != "5" {
		t.Fatalf("seq clobbered prior binding: q = %s, want 5", printValue(v))
	}
}

// TestLexicalShadowsGlobal: a captured lexical binding (an enclosing proc's
// parameter) shadows a plain-name global of the same name inside a lambda.
// DT's DiffVarList relies on this: its select lambda references the parameter
// `p` while a global `p` may linger from a seq loop variable.
func TestLexicalShadowsGlobal(t *testing.T) {
	it := NewInterp()
	it.Exec("p := someglobal:") // a lingering global p
	code := `
dvl := proc(p) return select(a -> p['k'], [aa]); end proc:
dvl(table(['k' = true]));
`
	v, err := it.Exec(code)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := printValue(v); got != "[aa]" {
		t.Fatalf("lambda captured global p, not param: got %q, want [aa]", got)
	}
}

// TestPrintfPrecision: printf handles width/precision modifiers (%.1f, %5d).
func TestPrintfPrecision(t *testing.T) {
	it := NewInterp()
	it.Exec("printf(\"%.1f|%d\", 3.14159, 7);")
	if got := it.out.String(); got != "3.1|7" {
		t.Fatalf("printf precision: got %q, want %q", got, "3.1|7")
	}
}
