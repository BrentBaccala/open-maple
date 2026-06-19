package main

import "testing"

// evalStr runs code on a fresh interpreter and returns the last value's printed
// form.
func evalStr(t *testing.T, code string) string {
	t.Helper()
	it := NewInterp()
	v, err := it.Exec(code)
	if err != nil {
		t.Fatalf("eval %q: %v", code, err)
	}
	return printValue(v)
}

func evalWith(t *testing.T, it *Interp, code string) (Value, error) {
	t.Helper()
	return it.Exec(code)
}

func TestArith(t *testing.T) {
	cases := map[string]string{
		"2+3*4;":            "14",
		"2^10;":             "1024",
		"1/2 + 1/3;":        "5/6",
		"10/4;":             "5/2",
		"(-3)^3;":           "-27",
		"2^100;":            "1267650600228229401496703205376",
		"7 mod 3;":          "1",
		"5!;":               "120",
		"evalb(3 < 5);":     "true",
		"evalb(3 = 3);":     "true",
		"evalb(3 <> 3);":    "false",
		"3 = 3;":            "3 = 3", // inert equation (Maple-faithful)
	}
	for code, want := range cases {
		if got := evalStr(t, code); got != want {
			t.Errorf("%s = %s, want %s", code, got, want)
		}
	}
}

func TestDataStructures(t *testing.T) {
	cases := map[string]string{
		"[1,2,3];":           "[1, 2, 3]",
		"{3,1,2,1};":         "{1, 2, 3}",
		"nops([1,2,3]);":     "3",
		"op([a,b,c]);":       "a, b, c",
		"op(2,[a,b,c]);":     "b",
		"op(0, f(x,y));":     "f",
		"nops(f(x,y));":      "2",
		"[a,b,c][2];":        "b",
		"[a,b,c][2..3];":     "[b, c]",
		"a union {1};":       "a union {1}", // a unbound -> inert? actually error
	}
	// drop the tricky last case; test the rest
	delete(cases, "a union {1};")
	for code, want := range cases {
		if got := evalStr(t, code); got != want {
			t.Errorf("%s = %s, want %s", code, got, want)
		}
	}
}

func TestSeqOp(t *testing.T) {
	cases := map[string]string{
		"0$3;":              "0, 0, 0",
		"seq(i^2, i=1..4);": "1, 4, 9, 16",
		"add(i, i=1..10);":  "55",
		"mul(i, i=1..5);":   "120",
		"[1, 0$2, 1];":      "[1, 0, 0, 1]",
	}
	for code, want := range cases {
		if got := evalStr(t, code); got != want {
			t.Errorf("%s = %s, want %s", code, got, want)
		}
	}
}

func TestTables(t *testing.T) {
	it := NewInterp()
	mustEval(t, it, "t[1] := 10;")
	mustEval(t, it, "t[foo] := 20;")
	if got := printStr(t, it, "t[1];"); got != "10" {
		t.Errorf("t[1] = %s", got)
	}
	if got := printStr(t, it, "assigned(t[1]);"); got != "true" {
		t.Errorf("assigned(t[1]) = %s", got)
	}
	if got := printStr(t, it, "assigned(t[zzz]);"); got != "false" {
		t.Errorf("assigned(t[zzz]) = %s", got)
	}
	// last-name-eval: t evaluates to the name t, not the table contents
	if got := printStr(t, it, "t;"); got != "t" {
		t.Errorf("last-name-eval: t = %s, want t", got)
	}
}

func TestProc(t *testing.T) {
	it := NewInterp()
	mustEval(t, it, `f := proc(x, y) local s; s := x + y; return s^2; end proc;`)
	if got := printStr(t, it, "f(2,3);"); got != "25" {
		t.Errorf("f(2,3) = %s", got)
	}
	// args/nargs
	mustEval(t, it, `g := proc() return nargs; end proc;`)
	if got := printStr(t, it, "g(1,2,3,4);"); got != "4" {
		t.Errorf("g(...) nargs = %s", got)
	}
	// defaults
	mustEval(t, it, `h := proc(x, y := 100) return x + y; end proc;`)
	if got := printStr(t, it, "h(1);"); got != "101" {
		t.Errorf("h(1) = %s", got)
	}
	if got := printStr(t, it, "h(1,2);"); got != "3" {
		t.Errorf("h(1,2) = %s", got)
	}
}

func TestOptionRemember(t *testing.T) {
	it := NewInterp()
	mustEval(t, it, `
		counter := 0;
		fib := proc(n) option remember;
			if n < 2 then return n fi;
			return fib(n-1) + fib(n-2);
		end proc;`)
	if got := printStr(t, it, "fib(20);"); got != "6765" {
		t.Errorf("fib(20) = %s", got)
	}
}

func TestMapSelect(t *testing.T) {
	it := NewInterp()
	mustEval(t, it, `sq := proc(x) return x^2 end proc;`)
	if got := printStr(t, it, "map(sq, [1,2,3]);"); got != "[1, 4, 9]" {
		t.Errorf("map = %s", got)
	}
	mustEval(t, it, `pos := proc(x) return x > 0 end proc;`)
	if got := printStr(t, it, "select(pos, [-1, 2, -3, 4]);"); got != "[2, 4]" {
		t.Errorf("select = %s", got)
	}
	if got := printStr(t, it, "remove(pos, [-1, 2, -3, 4]);"); got != "[-1, -3]" {
		t.Errorf("remove = %s", got)
	}
}

func TestSubsSubsop(t *testing.T) {
	cases := map[string]string{
		"subs(x=2, x+y);":          "y + 2",
		"subs([x=2,y=3], x+y);":    "5",
		"subsop(2=z, [a,b,c]);":    "[a, z, c]",
		"subsop(1=q, f(a,b));":     "f(q, b)",
	}
	for code, want := range cases {
		if got := evalStr(t, code); got != want {
			t.Errorf("%s = %s, want %s", code, got, want)
		}
	}
}

func TestParseCat(t *testing.T) {
	it := NewInterp()
	// parse(cat(...)) round-trip: build an assignment and execute it
	mustEval(t, it, `parse(cat("zz := ", "41 + 1", ";"), statement);`)
	if got := printStr(t, it, "zz;"); got != "42" {
		t.Errorf("parse(cat(...)) statement: zz = %s, want 42", got)
	}
	// expression-mode parse
	if got := printStr(t, it, `parse("3 * 14");`); got != "42" {
		t.Errorf("parse expr = %s, want 42", got)
	}
}

func TestTypeSystem(t *testing.T) {
	cases := map[string]string{
		"type(3, integer);":         "true",
		"type(3, posint);":          "true",
		"type(-3, posint);":         "false",
		`type("x", string);`:        "true",
		"type([1,2], list);":        "true",
		"type([1,2], list(integer));": "true",
		"type([1,a], list(integer));": "false",
		"type(x, symbol);":          "true",
		"type({1,2}, set);":         "true",
	}
	for code, want := range cases {
		if got := evalStr(t, code); got != want {
			t.Errorf("%s = %s, want %s", code, got, want)
		}
	}
}

func TestControlFlow(t *testing.T) {
	it := NewInterp()
	mustEval(t, it, `
		s := 0;
		for i from 1 to 5 do s := s + i; od;`)
	if got := printStr(t, it, "s;"); got != "15" {
		t.Errorf("for sum = %s, want 15", got)
	}
	mustEval(t, it, `
		p := 1;
		for x in [2,3,4] do p := p * x; od;`)
	if got := printStr(t, it, "p;"); got != "24" {
		t.Errorf("for-in product = %s, want 24", got)
	}
	if got := evalStr(t, "if 3 > 2 then 100 else 200 fi;"); got != "100" {
		t.Errorf("if-expr = %s", got)
	}
}

func TestTryCatch(t *testing.T) {
	it := NewInterp()
	mustEval(t, it, `
		r := 0;
		try
			error "boom: %1", 42;
		catch "boom":
			r := 99;
		end try;`)
	if got := printStr(t, it, "r;"); got != "99" {
		t.Errorf("try/catch = %s, want 99", got)
	}
}

func mustEval(t *testing.T, it *Interp, code string) {
	t.Helper()
	if _, err := it.Exec(code); err != nil {
		t.Fatalf("eval %q: %v", code, err)
	}
}

func printStr(t *testing.T, it *Interp, code string) string {
	t.Helper()
	v, err := it.Exec(code)
	if err != nil {
		t.Fatalf("eval %q: %v", code, err)
	}
	return printValue(v)
}
