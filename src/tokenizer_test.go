package main

import (
	"testing"
)

// TestTokenizerBasic checks the rewritten lexer produces the right token groups
// for a representative Maple snippet (proc header, brackets, operators, the
// '^' power, multi-char operators, and statement terminators).
func TestTokenizerBasic(test *testing.T) {
	input := "quadratic := proc (a, b, c) return [(-b - sqrt(b^2 - 4 * a * c))/(2 * a)] end proc;"
	tokens, err := tokenizer(input)
	if err != nil {
		test.Fatalf("tokenizer returned error: %v", err)
	}

	// Spot-check key tokens by (value, group).
	want := []token{
		{"quadratic", name},
		{":=", assignment},
		{"proc", keyword},
		{"(", lparen},
		{"a", name},
		{",", comma},
		{"b", name},
		{",", comma},
		{"c", name},
		{")", rparen},
		{"return", keyword},
		{"[", lbracket},
		{"(", lparen},
		{"-", operator},
		{"b", name},
		{"-", operator},
		{"sqrt", name},
		{"(", lparen},
		{"b", name},
		{"^", operator},
		{"2", number},
	}
	for i, w := range want {
		if tokens[i].value != w.value || tokens[i].group != w.group {
			test.Errorf("token %d: got {%q,%v}, want {%q,%v}",
				i, tokens[i].value, tokens[i].group, w.value, w.group)
		}
	}
	// last real token before EOF should be ';'
	if tokens[len(tokens)-2].group != statementDelim {
		test.Errorf("expected statementDelim before EOF, got %v", tokens[len(tokens)-2].group)
	}
	if tokens[len(tokens)-1].group != eofTok {
		test.Errorf("expected eofTok last, got %v", tokens[len(tokens)-1].group)
	}
}

// TestTokenizerLiterals checks backtick names, strings, uneval quotes, comments,
// the range vs decimal disambiguation, and the multi-char operators.
func TestTokenizerLiterals(test *testing.T) {
	cases := []struct {
		in    string
		value string
		group tokenType
	}{
		{"`DifferentialThomas/Foo`", "`DifferentialThomas/Foo`", name},
		{"\"hello world\"", "\"hello world\"", stringTok},
		{"'diff'", "'diff'", unevalTok},
		{"1.5", "1.5", number},
		{"100", "100", number},
		{"123456789012345678901234567890", "123456789012345678901234567890", number},
	}
	for _, c := range cases {
		toks, err := tokenizer(c.in)
		if err != nil {
			test.Errorf("tokenize %q: error %v", c.in, err)
			continue
		}
		if toks[0].value != c.value || toks[0].group != c.group {
			test.Errorf("tokenize %q: got {%q,%v}, want {%q,%v}",
				c.in, toks[0].value, toks[0].group, c.value, c.group)
		}
	}

	// Range "1..5" must lex as: number, "..", number (NOT 1. . .5).
	toks, err := tokenizer("1..5")
	if err != nil {
		test.Fatalf("tokenize range: %v", err)
	}
	if toks[0].value != "1" || toks[1].value != ".." || toks[2].value != "5" {
		test.Errorf("range lexing wrong: %q %q %q", toks[0].value, toks[1].value, toks[2].value)
	}

	// Comment is stripped.
	toks, _ = tokenizer("x := 1; # this is a comment\ny := 2;")
	for _, t := range toks {
		if t.group == name && t.value == "this" {
			test.Errorf("comment was not stripped")
		}
	}

	// Multi-char operators.
	for _, mc := range []string{":=", "<=", ">=", "<>", "->", "::", ":-", ".."} {
		toks, err := tokenizer("a " + mc + " b")
		if err != nil {
			test.Errorf("tokenize %q: %v", mc, err)
			continue
		}
		if toks[1].value != mc {
			test.Errorf("multi-char op %q lexed as %q", mc, toks[1].value)
		}
	}
}
