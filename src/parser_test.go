package main

import (
	"io/ioutil"
	"testing"
)

// parseString is a small helper: tokenize + parse, returning the root tree.
func parseString(t *testing.T, src string) tree {
	t.Helper()
	tokens, err := tokenizer(src)
	if err != nil {
		t.Fatalf("tokenizer error on %q: %v", src, err)
	}
	root, err := parser(tokens)
	if err != nil {
		t.Fatalf("parser error on %q: %v", src, err)
	}
	return root
}

// findFirst walks the tree depth-first and returns the first node of group g.
func findFirst(root tree, g nodeType) *tree {
	if root.group == g {
		return &root
	}
	for _, n := range root.nodes {
		if found := findFirst(*n, g); found != nil {
			return found
		}
	}
	return nil
}

// TestParseArithmetic checks the original arithmetic still parses and evaluates.
func TestParseArithmetic(test *testing.T) {
	file, err := ioutil.ReadFile("../samples/test_samples/multi-statement-assign.txt")
	if err != nil {
		test.Skip("sample file missing")
	}
	root := parseString(test, string(file))
	if len(root.nodes) != 2 {
		test.Fatalf("expected 2 statements, got %d", len(root.nodes))
	}
	for _, n := range root.nodes {
		if n.group != assign {
			test.Errorf("top-level statement is %v, want assign", n.group)
		}
	}
}

// TestParseArithmeticEval confirms the tree-walker still computes the documented
// Phase-0 result (x := 6*5/4/3 -> 2.5).
func TestParseArithmeticEval(test *testing.T) {
	st := map[string]interface{}{}
	st, err := run("x := 6*5/4/3;", st)
	if err != nil {
		test.Fatalf("eval error: %v", err)
	}
	if v, ok := st["x"].(float64); !ok || !floatEqual(v, 2.5) {
		test.Errorf("x = %v, want 2.5", st["x"])
	}
	st, err = run("y := 2 + x*10;", st)
	if err != nil {
		test.Fatalf("eval error: %v", err)
	}
	if v, ok := st["y"].(float64); !ok || !floatEqual(v, 27.0) {
		test.Errorf("y = %v, want 27", st["y"])
	}
}

// TestParseConstructs is a table-driven test that every targeted Maple construct
// parses without error and produces the expected top-level node kind.
func TestParseConstructs(test *testing.T) {
	cases := []struct {
		name string
		src  string
		want nodeType // expected group of the first top-level statement
	}{
		{"assign", "x := 1;", assign},
		{"call", "f(x, y, z);", callNode},
		{"index", "a[i];", indexNode},
		{"index-multi", "a[i, j];", indexNode},
		{"list", "[1, 2, 3];", listNode},
		{"set", "{1, 2, 3};", setNode},
		{"range", "1..n;", rangeNode},
		{"power", "x^2;", operate},
		{"power-alt", "x**2;", operate},
		{"relational", "a <= b;", operate},
		{"neq", "a <> b;", operate},
		{"boolean", "a and b or not c;", operate},
		{"setops", "A union B intersect C minus D;", operate},
		{"mod", "i mod p;", operate},
		{"arrow", "x -> x^2;", arrowNode},
		{"backtick", "`DifferentialThomas/Foo`;", variable},
		{"string", "\"abc\";", stringNode},
		{"uneval", "'diff';", unevalNode},
		{"member", "LinearAlgebra:-Rank;", memberNode},
		{"index-pkg", "LinearAlgebra[Rank];", indexNode},
		{"typed", "x::integer;", typeNode},
		{"return", "return x;", returnNode},
		{"error", "error \"bad\";", errorNode},
		{"if", "if a then b; fi;", ifNode},
		{"if-end", "if a then b; end if;", ifNode},
		{"if-elif-else", "if a then b; elif c then d; else e; fi;", ifNode},
		{"for", "for i from 1 to 5 do x; od;", forNode},
		{"for-in", "for x in L do y; od;", forNode},
		{"for-by", "for i from 3 by 2 to n do z; end do;", forNode},
		{"while", "while c do d; od;", forNode},
		{"do", "do x; od;", forNode},
		{"proc", "proc(a, b) a+b; end proc;", procNode},
		{"proc-end", "proc(a) local x; x := a; x; end;", procNode},
		{"proc-typed", "proc(a::integer, b::list) a; end proc;", procNode},
		{"proc-global", "proc() global g; g; end proc;", procNode},
		{"proc-option", "proc() option remember; 1; end proc;", procNode},
		{"try", "try f(); catch \"err\": g(); end try;", tryNode},
		{"matrix", "<1, 2, 3>;", matrixNode},
		{"matrix-cols", "<a | b | c>;", matrixNode},
		{"seq", "[$1..n];", listNode},
		{"dollar", "[0$n];", listNode},
		{"unary-minus", "-x;", unaryNode},
		{"empty-index", "a[];", indexNode},
		{"break", "break;", breakNode},
		{"next", "next;", nextNode},
	}

	for _, c := range cases {
		test.Run(c.name, func(t *testing.T) {
			root := parseString(t, c.src)
			if len(root.nodes) == 0 {
				t.Fatalf("no statements parsed from %q", c.src)
			}
			got := root.nodes[0].group
			if got != c.want {
				t.Errorf("%q: first statement group = %v, want %v", c.src, got, c.want)
			}
		})
	}
}

// TestParseSampleFiles parses the real-Maple personal samples that ship with
// open_maple (procs with loops, recursion, nested procs).
func TestParseSampleFiles(test *testing.T) {
	files := []string{
		"../samples/personal_samples/loops.txt",
		"../samples/personal_samples/primes.txt",
		"../samples/personal_samples/maclaurin_e.txt",
		"../samples/personal_samples/functionception.txt",
	}
	for _, f := range files {
		data, err := ioutil.ReadFile(f)
		if err != nil {
			test.Logf("skip %s: %v", f, err)
			continue
		}
		tokens, err := tokenizer(string(data))
		if err != nil {
			test.Errorf("%s: tokenizer error: %v", f, err)
			continue
		}
		if _, err := parser(tokens); err != nil {
			test.Errorf("%s: parser error: %v", f, err)
		}
	}
}
