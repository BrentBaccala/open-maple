package main

import "testing"

// TestLprint1DCorpus pins the Maple 1-D / convert(,string) serialization rules
// against the starter corpus in ~/project/docs/maple-print-format-reference.md.
// These are the rules DT's FactorSorter byte-compares depend on: no operator
// spacing, comma+space sequences, coefficient-first monomials, indexed-name and
// function-call comma+space, -infinity as an atom.
//
// The corpus exercises the *serializer* directly on constructed values (the
// printer rules), not Maple's term-ordering sort — sort() is exercised
// end-to-end via the Sage decomposition suite. Where the doc's input is a
// pre-sorted polynomial, we build the value in that order and check the string.
func TestLprint1DCorpus(t *testing.T) {
	i := func(n int64) Value { return newInt(n) }
	name := func(s string) Value { return Name{s} }
	pow := func(b Value, e int64) Value { return &Power{Base: b, Exp: newInt(e)} }
	prod := func(fs ...Value) Value { return &Prod{Factors: fs} }
	sum := func(ts ...Value) Value { return &Sum{Terms: ts} }
	idx := func(h string, is ...Value) Value { return &Indexed{Head: Name{h}, Idx: is} }
	fn := func(h string, as ...Value) Value { return &Func{Head: Name{h}, Args: as} }
	neg := func(v Value) Value { return &Prod{Factors: []Value{newInt(-1), v}} }

	x, y, z := name("x"), name("y"), name("z")
	a, b, c, d := name("a"), name("b"), name("c"), name("d")

	cases := []struct {
		desc string
		in   Value
		want string
	}{
		// Sums / powers (no operator spacing, coeff-first) — sort/polynomial example.
		{"x^2+x+1", sum(pow(x, 2), x, i(1)), "x^2+x+1"},
		{"3*x^2", prod(i(3), pow(x, 2)), "3*x^2"},
		// Subtraction renders as a-b (not a+(-1)*b); leading negative as -x.
		{"a-b", sum(a, neg(b)), "a-b"},
		{"-x+1", sum(neg(x), i(1)), "-x+1"},
		// Product of sums with explicit *.
		{"c*d+a+b (internal order)", sum(prod(c, d), a, b), "c*d+a+b"},
		// Monomial with leading var (post-sort order) and *.
		{"x^2*y", prod(pow(x, 2), y), "x^2*y"},
		{"x*y*z", prod(x, y, z), "x*y*z"},
		// Lists / sets: comma+space.
		{"[1, 2, 3]", List{[]Value{i(1), i(2), i(3)}}, "[1, 2, 3]"},
		{"[1, 2, [3, 4]]", List{[]Value{i(1), i(2), List{[]Value{i(3), i(4)}}}}, "[1, 2, [3, 4]]"},
		{"{1, 2, 3}", makeSet([]Value{i(3), i(2), i(1)}), "{1, 2, 3}"},
		// Indexed names: comma+space inside the index tuple.
		{"u[1, 2]", idx("u", i(1), i(2)), "u[1, 2]"},
		{"u[0, 0]", idx("u", i(0), i(0)), "u[0, 0]"},
		// Function application: comma+space args (readme smoke target uses u(x, y)).
		{"u(x, y)", fn("u", x, y), "u(x, y)"},
		// Equation / relation spacing.
		{"u(x, y) = 0", &Equation{Lhs: fn("u", x, y), Rhs: i(0)}, "u(x, y) = 0"},
		{"a <> 0", &Relation{Op: "<>", Lhs: a, Rhs: i(0)}, "a <> 0"},
		// -infinity prints as the atom, not -1*infinity.
		{"-infinity", neg(name("infinity")), "-infinity"},
		// Nested structure: the smoke deliverable itself.
		{"[[u(x, y) = 0]]", List{[]Value{List{[]Value{&Equation{Lhs: fn("u", x, y), Rhs: i(0)}}}}}, "[[u(x, y) = 0]]"},
	}

	for _, tc := range cases {
		if got := lprint1D(tc.in); got != tc.want {
			t.Errorf("%s: lprint1D = %q, want %q", tc.desc, got, tc.want)
		}
	}
}

// TestConvertStringRoutesThroughLprint confirms convert(expr, string) uses the
// 1-D serializer (the FactorSorter path), so a sum byte-compares with no spaces.
func TestConvertStringRoutesThroughLprint(t *testing.T) {
	it := NewInterp()
	out, err := it.Exec("convert(x^2+x+1, string);")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	s, ok := out.(MString)
	if !ok {
		t.Fatalf("convert/string did not return a string: %T", out)
	}
	if s.Val != "x^2+x+1" {
		t.Errorf("convert(x^2+x+1, string) = %q, want \"x^2+x+1\" (no operator spaces)", s.Val)
	}
}
