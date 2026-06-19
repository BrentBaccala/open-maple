package main

import (
	"fmt"
	"strings"
	"unicode"
)

// tokenType enumerates the lexical categories the Maple tokenizer produces.
//
// NOTE: the original open_maple tokenizer recognised only a handful of these
// (operator, statementDelim, name, number, assignment, nullToken). The Phase-1
// grammar work replaces it with a full Maple lexer. The first six values are
// kept in their original positions so the stringer-generated table in
// tokentype_string.go and any residual references stay valid; the new
// categories are appended.
type tokenType int

const (
	operator       tokenType = iota // arithmetic / relational / logical operators, ":=", etc.
	statementDelim                  // ';' or ':' (statement terminator)
	name                            // identifier / keyword / backtick-quoted name
	number                          // integer or decimal literal
	assignment                      // ":=" (kept distinct for back-compat)
	nullToken                       // unrecognised (error)
	stringTok                       // "..." double-quoted string literal
	unevalTok                       // '...' single-quoted (uneval / name) literal
	lparen                          // (
	rparen                          // )
	lbracket                        // [
	rbracket                        // ]
	lbrace                          // {
	rbrace                          // }
	langle                          // <  (Matrix/Vector constructor — only when not relational)
	rangle                          // >
	comma                           // ,
	keyword                         // reserved word: proc, end, if, then, etc.
	eofTok                          // end of input
)

type token struct {
	value string
	group tokenType
}

// mapleKeywords are the reserved words of the Maple language that the parser
// dispatches on. (Operator-like words such as mod/and/or/not/union/intersect/
// minus/in/xor/implies are handled as operators, not keywords, so they are NOT
// in this set.)
var mapleKeywords = map[string]bool{
	"proc": true, "end": true, "local": true, "global": true,
	"option": true, "options": true, "description": true,
	"if": true, "then": true, "elif": true, "else": true, "fi": true,
	"for": true, "from": true, "to": true, "by": true, "do": true,
	"od": true, "while": true, "in": true,
	"return": true, "error": true, "try": true, "catch": true, "finally": true,
	"use": true, "uses": true, "export": true, "module": true,
	"read": true, "save": true, "quit": true, "done": true, "stop": true,
	"break": true, "next": true,
}

// wordOperators are identifier-shaped tokens that act as operators.
var wordOperators = map[string]bool{
	"mod": true, "and": true, "or": true, "not": true, "xor": true,
	"implies": true, "union": true, "intersect": true, "minus": true,
	"subset": true,
}

// IsWordOperator reports whether an identifier should be treated as an operator
// rather than a name. Exposed for the parser.
func IsWordOperator(s string) bool { return wordOperators[s] }

// IsKeyword reports whether an identifier is a reserved keyword.
func IsKeyword(s string) bool { return mapleKeywords[s] }

type lexer struct {
	input []rune
	pos   int
	out   []token
}

func (l *lexer) peek() rune {
	if l.pos < len(l.input) {
		return l.input[l.pos]
	}
	return 0
}

func (l *lexer) peekAt(n int) rune {
	if l.pos+n < len(l.input) {
		return l.input[l.pos+n]
	}
	return 0
}

func (l *lexer) emit(v string, g tokenType) {
	l.out = append(l.out, token{value: v, group: g})
}

// isNameStart reports whether r can begin an unquoted Maple identifier.
func isNameStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

// isNameCont reports whether r can continue an unquoted Maple identifier.
// Maple identifiers allow letters, digits and underscores.
func isNameCont(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// tokenizer scans a Maple source string into a slice of tokens. It is a
// hand-written lexer covering the full Maple surface grammar that
// DifferentialThomas uses: comments, backtick/double/single-quoted literals,
// numbers, the multi-character operators (:=, ..,  <=, >=, <>, ->, ::, :-, ||),
// the bracketing tokens, and identifiers (further classified into keyword /
// word-operator / name by the parser via IsKeyword / IsWordOperator).
func tokenizer(input string) ([]token, error) {
	l := &lexer{input: []rune(input)}

	for l.pos < len(l.input) {
		c := l.peek()

		switch {
		case unicode.IsSpace(c):
			l.pos++

		// Line comment: # ... to end of line.
		case c == '#':
			for l.pos < len(l.input) && l.peek() != '\n' {
				l.pos++
			}

		// Backtick-quoted name: `DifferentialThomas/Foo`. Backslash escapes.
		case c == '`':
			start := l.pos
			l.pos++ // consume opening backtick
			var sb strings.Builder
			sb.WriteRune('`')
			closed := false
			for l.pos < len(l.input) {
				r := l.peek()
				if r == '\\' && l.pos+1 < len(l.input) {
					sb.WriteRune(r)
					l.pos++
					sb.WriteRune(l.peek())
					l.pos++
					continue
				}
				if r == '`' {
					sb.WriteRune('`')
					l.pos++
					closed = true
					break
				}
				sb.WriteRune(r)
				l.pos++
			}
			if !closed {
				return l.out, fmt.Errorf("unterminated backtick name starting at offset %d", start)
			}
			l.emit(sb.String(), name)

		// Double-quoted string literal. Backslash escapes.
		case c == '"':
			start := l.pos
			l.pos++
			var sb strings.Builder
			sb.WriteRune('"')
			closed := false
			for l.pos < len(l.input) {
				r := l.peek()
				if r == '\\' && l.pos+1 < len(l.input) {
					sb.WriteRune(r)
					l.pos++
					sb.WriteRune(l.peek())
					l.pos++
					continue
				}
				if r == '"' {
					sb.WriteRune('"')
					l.pos++
					closed = true
					break
				}
				sb.WriteRune(r)
				l.pos++
			}
			if !closed {
				return l.out, fmt.Errorf("unterminated string starting at offset %d", start)
			}
			l.emit(sb.String(), stringTok)

		// Single-quoted uneval literal: 'diff', 'nolist'. Backslash escapes.
		case c == '\'':
			start := l.pos
			l.pos++
			var sb strings.Builder
			sb.WriteRune('\'')
			closed := false
			for l.pos < len(l.input) {
				r := l.peek()
				if r == '\\' && l.pos+1 < len(l.input) {
					sb.WriteRune(r)
					l.pos++
					sb.WriteRune(l.peek())
					l.pos++
					continue
				}
				if r == '\'' {
					sb.WriteRune('\'')
					l.pos++
					closed = true
					break
				}
				sb.WriteRune(r)
				l.pos++
			}
			if !closed {
				return l.out, fmt.Errorf("unterminated uneval quote starting at offset %d", start)
			}
			l.emit(sb.String(), unevalTok)

		// Numbers: integer or decimal. We must NOT greedily eat the ".." range
		// operator, so a '.' is only part of the number if it is not followed
		// by another '.'.
		case unicode.IsDigit(c) || (c == '.' && unicode.IsDigit(l.peekAt(1))):
			var sb strings.Builder
			for l.pos < len(l.input) {
				r := l.peek()
				if unicode.IsDigit(r) {
					sb.WriteRune(r)
					l.pos++
				} else if r == '.' && l.peekAt(1) != '.' {
					// decimal point (not the start of "..")
					sb.WriteRune(r)
					l.pos++
				} else if (r == 'e' || r == 'E') &&
					(unicode.IsDigit(l.peekAt(1)) ||
						((l.peekAt(1) == '+' || l.peekAt(1) == '-') && unicode.IsDigit(l.peekAt(2)))) {
					// scientific notation 1e10, 1.5e-3
					sb.WriteRune(r)
					l.pos++
					if l.peek() == '+' || l.peek() == '-' {
						sb.WriteRune(l.peek())
						l.pos++
					}
				} else {
					break
				}
			}
			l.emit(sb.String(), number)

		// Identifier / keyword / word-operator.
		case isNameStart(c):
			var sb strings.Builder
			for l.pos < len(l.input) && isNameCont(l.peek()) {
				sb.WriteRune(l.peek())
				l.pos++
			}
			word := sb.String()
			switch {
			case mapleKeywords[word]:
				l.emit(word, keyword)
			default:
				// word-operators and ordinary names both emit as `name`;
				// the parser distinguishes via IsWordOperator. Keeping a
				// single token group here keeps indexing/call detection simple.
				l.emit(word, name)
			}

		// Brackets and punctuation.
		case c == '(':
			l.emit("(", lparen)
			l.pos++
		case c == ')':
			l.emit(")", rparen)
			l.pos++
		case c == '[':
			l.emit("[", lbracket)
			l.pos++
		case c == ']':
			l.emit("]", rbracket)
			l.pos++
		case c == '{':
			l.emit("{", lbrace)
			l.pos++
		case c == '}':
			l.emit("}", rbrace)
			l.pos++
		case c == ',':
			l.emit(",", comma)
			l.pos++

		// Statement terminators. ':' may be the start of ':=' or ':-'.
		case c == ';':
			l.emit(";", statementDelim)
			l.pos++
		case c == ':':
			if l.peekAt(1) == '=' {
				l.emit(":=", assignment)
				l.pos += 2
			} else if l.peekAt(1) == '-' {
				l.emit(":-", operator) // module-member selector
				l.pos += 2
			} else if l.peekAt(1) == ':' {
				l.emit("::", operator) // type annotation
				l.pos += 2
			} else {
				l.emit(":", statementDelim)
				l.pos++
			}

		// '.' is either the range operator ".." or the (non-arithmetic) Maple
		// concatenation/decimal dot. A bare '.' becomes a name-cat operator.
		case c == '.':
			if l.peekAt(1) == '.' {
				l.emit("..", operator)
				l.pos += 2
			} else {
				l.emit(".", operator)
				l.pos++
			}

		// Arithmetic and the backtick-free operator words handled here.
		case c == '+':
			l.emit("+", operator)
			l.pos++
		case c == '-':
			if l.peekAt(1) == '>' {
				l.emit("->", operator) // arrow proc
				l.pos += 2
			} else {
				l.emit("-", operator)
				l.pos++
			}
		case c == '*':
			if l.peekAt(1) == '*' {
				l.emit("**", operator) // power (Maple alt for ^)
				l.pos += 2
			} else {
				l.emit("*", operator)
				l.pos++
			}
		case c == '/':
			l.emit("/", operator)
			l.pos++
		case c == '^':
			l.emit("^", operator)
			l.pos++
		case c == '$':
			l.emit("$", operator) // seq operator
			l.pos++
		case c == '!':
			l.emit("!", operator) // factorial (postfix)
			l.pos++
		case c == '@':
			if l.peekAt(1) == '@' {
				l.emit("@@", operator) // repeated composition
				l.pos += 2
			} else {
				l.emit("@", operator) // composition
				l.pos++
			}

		// Relational and the angle-bracket Matrix/Vector constructors.
		// '<' is relational when followed by a relational continuation or when
		// the parser is in expression-infix position; we tokenize the raw
		// operator and let the parser decide constructor-vs-relational. To keep
		// the lexer simple we emit "<"/">" as `operator` always EXCEPT we still
		// emit the multi-char forms.
		case c == '<':
			switch l.peekAt(1) {
			case '=':
				l.emit("<=", operator)
				l.pos += 2
			case '>':
				l.emit("<>", operator)
				l.pos += 2
			case '|':
				l.emit("<|", operator) // not real, but guard
				l.pos += 2
			default:
				l.emit("<", langle)
				l.pos++
			}
		case c == '>':
			if l.peekAt(1) == '=' {
				l.emit(">=", operator)
				l.pos += 2
			} else {
				l.emit(">", rangle)
				l.pos++
			}
		case c == '=':
			l.emit("=", operator)
			l.pos++

		case c == '|':
			if l.peekAt(1) == '|' {
				l.emit("||", operator) // name concatenation
				l.pos += 2
			} else {
				l.emit("|", operator) // Matrix column separator
				l.pos++
			}

		case c == '%':
			// history references: %, %%, %%%
			n := 0
			for l.peek() == '%' {
				n++
				l.pos++
			}
			l.emit(strings.Repeat("%", n), name)

		case c == '&':
			// neutral operator name &foo
			var sb strings.Builder
			sb.WriteRune('&')
			l.pos++
			for l.pos < len(l.input) && isNameCont(l.peek()) {
				sb.WriteRune(l.peek())
				l.pos++
			}
			l.emit(sb.String(), name)

		case c == '\\':
			// Backslash: line continuation (\<newline>) is whitespace; any
			// other backslash-escape outside a quote (e.g. the informal
			// `print(\n)` in the sample files) is folded into a name token so
			// parsing does not abort. Real Maple only uses '\' as continuation
			// or inside quotes; this keeps the lexer total.
			if l.peekAt(1) == '\n' {
				l.pos += 2
			} else {
				var sb strings.Builder
				sb.WriteRune('\\')
				l.pos++
				if l.pos < len(l.input) {
					sb.WriteRune(l.peek())
					l.pos++
				}
				l.emit(sb.String(), name)
			}

		case c == '?':
			// help query — treat the rest of the line as a help token.
			for l.pos < len(l.input) && l.peek() != '\n' {
				l.pos++
			}

		default:
			return l.out, fmt.Errorf("unrecognised character %q at offset %d", c, l.pos)
		}
	}

	l.emit("", eofTok)
	return l.out, nil
}
