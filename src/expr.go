package main

// Pratt expression parser for the Maple grammar.
//
// Operator precedence (lowest binds last). Maple's precedence, simplified to
// what DifferentialThomas uses, lowest-to-highest:
//
//	:=                      assignment (right)
//	->                      arrow proc (right)
//	or                      logical or
//	and                     logical and
//	not                     logical not (prefix, handled in nud)
//	= <> < <= > >= in       relational
//	..                      range
//	union minus             set union/diff
//	intersect               set intersect
//	+ -                     additive
//	* / mod                 multiplicative
//	&...                    neutral operators
//	^ **                    power (right)
//	@ @@                    composition
//	unary - +               prefix sign
//	::                      type annotation
//	$                       seq
//	. (cat) :- (member)     selection / concat
//	! (postfix)             factorial
//	f(...)  a[...]          call / index (highest, postfix)
//
// We implement this with binding powers.

// binding powers
const (
	bpNone   = 0
	bpAssign = 3
	bpComma  = 4 // expression-sequence comma binds tighter than ':=' so that
	//              `a, b := f()` parses as `(a, b) := f()` (multiple assign)
	//              while `x := a, b` parses as `x := (a, b)`.
	bpArrow  = 8
	bpOr     = 10
	bpAnd    = 12
	bpRel    = 20
	bpRange  = 25
	bpUnion  = 28
	bpInter  = 30
	bpAdd    = 40
	bpMul    = 50
	bpSeq    = 55
	bpType   = 58
	bpPow    = 70
	bpCompose = 75
	// bpNeg — prefix +/- bind looser than ^ so -b^2 parses as -(b^2), not
	// (-b)^2. (Just below bpPow, mirroring ^'s right operand at bpPow-1.)
	bpNeg    = bpPow - 1
	bpUnary  = 80
	bpDot    = 85
	bpPostfix = 90
)

// leftBindingPower returns the infix/postfix binding power of the current token.
func (p *parser_t) leftBindingPower() int {
	t := p.cur()
	switch t.group {
	case assignment:
		return bpAssign
	case operator:
		switch t.value {
		case "->":
			return bpArrow
		case "=", "<>", "<=", ">=":
			return bpRel
		case "..":
			return bpRange
		case "union", "minus":
			return bpUnion
		case "intersect":
			return bpInter
		case "+", "-":
			return bpAdd
		case "*", "/", "mod":
			return bpMul
		case "$":
			return bpSeq
		case "::":
			return bpType
		case "^", "**":
			return bpPow
		case "@", "@@":
			return bpCompose
		case ".":
			return bpDot
		case ":-":
			return bpDot
		case "!":
			return bpPostfix
		case "|":
			return bpNone // handled only inside <...>
		}
	case langle, rangle:
		// '<' / '>' acting as relational operators in infix position.
		return bpRel
	case name:
		// word operators in infix position
		switch t.value {
		case "and":
			return bpAnd
		case "or":
			return bpOr
		case "xor", "implies":
			return bpOr
		case "in", "subset":
			return bpRel
		case "mod":
			return bpMul
		case "union", "minus":
			return bpUnion
		case "intersect":
			return bpInter
		}
	case keyword:
		// `in` is a keyword (needed by the for-loop parser) but is also the
		// set-membership relational operator inside expressions.
		if t.value == "in" {
			return bpRel
		}
	case lparen:
		return bpPostfix // call
	case lbracket:
		return bpPostfix // index
	case comma:
		return bpComma // expression-sequence builder
	}
	return bpNone
}

// isExprStart reports whether a token can begin an expression (used to detect
// empty/NULL elements after a comma, e.g. `f(a, , b)` or a trailing comma).
func isExprStart(t token) bool {
	switch t.group {
	case number, stringTok, unevalTok, name, lparen, lbracket, lbrace, langle:
		return true
	case keyword:
		return t.value == "proc" || t.value == "module" || t.value == "if"
	case operator:
		switch t.value {
		case "-", "+", "$", "not":
			return true
		}
	}
	return false
}

// parseExpr is the Pratt loop.
func (p *parser_t) parseExpr(minBP int) (*tree, error) {
	left, err := p.parseNud()
	if err != nil {
		return nil, err
	}
	for {
		bp := p.leftBindingPower()
		if bp <= minBP {
			break
		}
		left, err = p.parseLed(left, bp)
		if err != nil {
			return nil, err
		}
	}
	return left, nil
}

// parseNud handles prefix / atom positions.
func (p *parser_t) parseNud() (*tree, error) {
	t := p.cur()
	switch t.group {
	case number:
		p.next()
		return &tree{group: constant, value: t.value}, nil
	case stringTok:
		p.next()
		return &tree{group: stringNode, value: t.value}, nil
	case unevalTok:
		p.next()
		return &tree{group: unevalNode, value: t.value}, nil
	case name:
		// could be a word operator used in prefix position ("not")
		if t.value == "not" {
			p.next()
			operand, err := p.parseExpr(bpAnd)
			if err != nil {
				return nil, err
			}
			return &tree{group: unaryNode, value: "not", nodes: []*tree{operand}}, nil
		}
		p.next()
		return &tree{group: variable, value: t.value}, nil
	case keyword:
		// `proc`, `module`, and a few keywords are valid in expression position.
		switch t.value {
		case "proc":
			return p.parseProc()
		case "module":
			return p.parseModule()
		case "if":
			// Maple's if/then/else/fi is also an expression (used as an arrow
			// body, e.g. `a -> if a=0 then 1 else a fi`). Reuse the statement
			// parser; the ifNode is equally valid as an expression node.
			return p.parseIf()
		case "return", "error", "local", "global":
			return nil, p.errf("unexpected keyword %q in expression", t.value)
		default:
			// Some "keywords" (e.g. `done`) can also be names; be lenient.
			p.next()
			return &tree{group: variable, value: t.value}, nil
		}
	case lparen:
		p.next()
		// empty () — yields NULL/exprseq
		if p.cur().group == rparen {
			p.next()
			return &tree{group: exprseqNode}, nil
		}
		inner, err := p.parseExprSeq(rparen)
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(rparen, "')'"); err != nil {
			return nil, err
		}
		return inner, nil
	case lbracket:
		p.next()
		node := &tree{group: listNode}
		if p.cur().group == rbracket {
			p.next()
			return node, nil
		}
		elems, err := p.parseExprList(rbracket)
		if err != nil {
			return nil, err
		}
		node.nodes = elems
		if _, err := p.expect(rbracket, "']'"); err != nil {
			return nil, err
		}
		return node, nil
	case lbrace:
		p.next()
		node := &tree{group: setNode}
		if p.cur().group == rbrace {
			p.next()
			return node, nil
		}
		elems, err := p.parseExprList(rbrace)
		if err != nil {
			return nil, err
		}
		node.nodes = elems
		if _, err := p.expect(rbrace, "'}'"); err != nil {
			return nil, err
		}
		return node, nil
	case langle:
		// Matrix / Vector constructor: <a,b,c> or <a|b|c> or nested.
		return p.parseMatrix()
	case operator:
		switch t.value {
		case "-":
			p.next()
			operand, err := p.parseExpr(bpNeg)
			if err != nil {
				return nil, err
			}
			return &tree{group: unaryNode, value: "-", nodes: []*tree{operand}}, nil
		case "+":
			p.next()
			operand, err := p.parseExpr(bpNeg)
			if err != nil {
				return nil, err
			}
			return &tree{group: unaryNode, value: "+", nodes: []*tree{operand}}, nil
		case "$":
			// prefix $ (seq operator): `$ a..b` -> a, a+1, ..., b. Parse the
			// operand at the range binding power so the `..` is captured as the
			// operand (otherwise `$1..n` would parse as `($1)..n`).
			p.next()
			operand, err := p.parseExpr(bpRange - 1)
			if err != nil {
				return nil, err
			}
			return &tree{group: unaryNode, value: "$", nodes: []*tree{operand}}, nil
		case "..":
			// range with empty lower bound, e.g. array(0..n) handled via infix;
			// a leading ".." is unusual — surface as error.
			return nil, p.errf("unexpected '..'")
		}
	}
	return nil, p.errf("unexpected token in expression")
}

// parseLed handles infix / postfix operators.
func (p *parser_t) parseLed(left *tree, bp int) (*tree, error) {
	t := p.cur()

	// Postfix: function call
	if t.group == lparen {
		p.next()
		call := &tree{group: callNode, nodes: []*tree{left}}
		if p.cur().group == rparen {
			p.next()
			return call, nil
		}
		args, err := p.parseExprList(rparen)
		if err != nil {
			return nil, err
		}
		call.nodes = append(call.nodes, args...)
		if _, err := p.expect(rparen, "')'"); err != nil {
			return nil, err
		}
		return call, nil
	}

	// Postfix: indexing
	if t.group == lbracket {
		p.next()
		idx := &tree{group: indexNode, nodes: []*tree{left}}
		if p.cur().group == rbracket {
			p.next()
			return idx, nil
		}
		args, err := p.parseExprList(rbracket)
		if err != nil {
			return nil, err
		}
		idx.nodes = append(idx.nodes, args...)
		if _, err := p.expect(rbracket, "']'"); err != nil {
			return nil, err
		}
		return idx, nil
	}

	// Expression-sequence comma: a, b, c  ->  exprseqNode(a, b, c).
	// Left-associative with flattening so the sequence stays flat.
	if t.group == comma {
		p.next()
		// allow a trailing/empty element (NULL)
		var right *tree
		if isExprStart(p.cur()) {
			var err error
			right, err = p.parseExpr(bpComma)
			if err != nil {
				return nil, err
			}
		} else {
			right = &tree{group: exprseqNode}
		}
		seq := &tree{group: exprseqNode}
		if left.group == exprseqNode && left.value == "" {
			seq.nodes = append(seq.nodes, left.nodes...)
		} else {
			seq.nodes = append(seq.nodes, left)
		}
		if right.group == exprseqNode && right.value == "" {
			seq.nodes = append(seq.nodes, right.nodes...)
		} else {
			seq.nodes = append(seq.nodes, right)
		}
		return seq, nil
	}

	// Assignment (right-associative)
	if t.group == assignment {
		p.next()
		right, err := p.parseExpr(bpAssign - 1)
		if err != nil {
			return nil, err
		}
		return &tree{group: assign, value: ":=", nodes: []*tree{left, right}}, nil
	}

	// Postfix factorial
	if t.group == operator && t.value == "!" {
		p.next()
		return &tree{group: unaryNode, value: "!post", nodes: []*tree{left}}, nil
	}

	// Type annotation a::T
	if t.group == operator && t.value == "::" {
		p.next()
		right, err := p.parseExpr(bpType)
		if err != nil {
			return nil, err
		}
		return &tree{group: typeNode, value: "::", nodes: []*tree{left, right}}, nil
	}

	// Member selection a:-b
	if t.group == operator && t.value == ":-" {
		p.next()
		right, err := p.parseExpr(bpDot)
		if err != nil {
			return nil, err
		}
		return &tree{group: memberNode, value: ":-", nodes: []*tree{left, right}}, nil
	}

	// Range a..b
	if t.group == operator && t.value == ".." {
		p.next()
		right, err := p.parseExpr(bpRange)
		if err != nil {
			return nil, err
		}
		return &tree{group: rangeNode, value: "..", nodes: []*tree{left, right}}, nil
	}

	// Arrow proc params -> body (right-associative).
	if t.group == operator && t.value == "->" {
		p.next()
		body, err := p.parseExpr(bpArrow - 1)
		if err != nil {
			return nil, err
		}
		return &tree{group: arrowNode, value: "->", nodes: []*tree{left, body}}, nil
	}

	// Power right-associative.
	if t.group == operator && (t.value == "^" || t.value == "**") {
		p.next()
		right, err := p.parseExpr(bpPow - 1)
		if err != nil {
			return nil, err
		}
		return &tree{group: operate, value: "^", nodes: []*tree{left, right}}, nil
	}

	// '<' and '>' angle tokens used as relational operators in infix position.
	if t.group == langle || t.group == rangle {
		op := t.value
		p.next()
		right, err := p.parseExpr(bp)
		if err != nil {
			return nil, err
		}
		return &tree{group: operate, value: op, nodes: []*tree{left, right}}, nil
	}

	// `in` as the set-membership operator (lexed as keyword, used infix).
	if t.group == keyword && t.value == "in" {
		p.next()
		right, err := p.parseExpr(bp)
		if err != nil {
			return nil, err
		}
		return &tree{group: operate, value: "in", nodes: []*tree{left, right}}, nil
	}

	// Word operators in infix position (name group): and/or/in/mod/union/...
	if t.group == name && (IsWordOperator(t.value) || t.value == "and" || t.value == "or" || t.value == "in" || t.value == "xor" || t.value == "implies" || t.value == "subset") {
		op := t.value
		p.next()
		right, err := p.parseExpr(bp)
		if err != nil {
			return nil, err
		}
		return &tree{group: operate, value: op, nodes: []*tree{left, right}}, nil
	}

	// Generic binary operators (left-associative).
	if t.group == operator {
		op := t.value
		p.next()
		right, err := p.parseExpr(bp)
		if err != nil {
			return nil, err
		}
		// '$' seq operator gets its own node tag for clarity but is an operate.
		return &tree{group: operate, value: op, nodes: []*tree{left, right}}, nil
	}

	return nil, p.errf("no infix parse for token %q", t.value)
}

// parseExprList parses a comma-separated list of expressions, stopping at the
// given closing token group (but not consuming it). Each element is parsed at
// precedence above comma so the comma stays the list separator.
func (p *parser_t) parseExprList(closer tokenType) ([]*tree, error) {
	var elems []*tree
	for {
		if p.cur().group == closer {
			break
		}
		// An empty element (e.g. trailing comma producing NULL) — allow.
		if p.cur().group == comma {
			elems = append(elems, &tree{group: exprseqNode})
			p.next()
			continue
		}
		// Parse each element above comma precedence so the comma stays a
		// list separator here rather than folding into a single exprseq.
		e, err := p.parseExpr(bpComma)
		if err != nil {
			return nil, err
		}
		elems = append(elems, e)
		if p.cur().group == comma {
			p.next()
			continue
		}
		break
	}
	return elems, nil
}

// parseExprSeq parses a parenthesised sequence; a single element returns that
// element directly, multiple elements return an exprseqNode.
func (p *parser_t) parseExprSeq(closer tokenType) (*tree, error) {
	elems, err := p.parseExprList(closer)
	if err != nil {
		return nil, err
	}
	if len(elems) == 1 {
		return elems[0], nil
	}
	return &tree{group: exprseqNode, nodes: elems}, nil
}

// parseMatrix parses <...> Matrix/Vector constructors, where rows are
// separated by ',' and columns by '|'. We model it as a matrixNode whose
// children are row exprseqNodes.
func (p *parser_t) parseMatrix() (*tree, error) {
	p.next() // consume '<'
	node := &tree{group: matrixNode}
	if p.cur().group == rangle {
		p.next()
		return node, nil
	}
	// parse a sequence of entries separated by ',' or '|', tracking which.
	row := &tree{group: exprseqNode, value: "row"}
	flush := func() {
		node.nodes = append(node.nodes, row)
		row = &tree{group: exprseqNode, value: "row"}
	}
	for {
		if p.cur().group == rangle {
			break
		}
		// Parse each entry above relational precedence so the closing '>'
		// (a rangle token, otherwise a relational operator) and the column
		// separator '|' terminate the entry rather than being consumed.
		e, err := p.parseExpr(bpRel)
		if err != nil {
			return nil, err
		}
		row.nodes = append(row.nodes, e)
		switch {
		case p.cur().group == operator && p.cur().value == "|":
			p.next() // same row, next column
		case p.cur().group == comma:
			p.next()
			flush() // next row
		case p.cur().group == rangle:
			// done
		default:
			return nil, p.errf("expected '|', ',' or '>' in matrix constructor")
		}
		if p.cur().group == rangle {
			break
		}
	}
	flush()
	if _, err := p.expect(rangle, "'>'"); err != nil {
		return nil, err
	}
	return node, nil
}
