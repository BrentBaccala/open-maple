package main

import "fmt"

// nodeType enumerates the AST node kinds produced by the parser.
//
// The first five values (assign, operate, variable, constant, rootNode) are
// retained from the original arithmetic-only open_maple so the
// stringer-generated table and the (still arithmetic-only) tree-walker keep
// working. Everything after rootNode is new for the full Maple grammar.
type nodeType int

const (
	assign    nodeType = iota // a := b
	operate                   // binary/unary operator application
	variable                  // bare name / symbol
	constant                  // numeric literal
	rootNode                  // top-level statement sequence
	stringNode                // "..." string literal
	unevalNode                // '...' uneval-quoted literal
	callNode                  // f(args...)
	indexNode                 // a[args...]
	exprseqNode               // comma-separated expression sequence
	listNode                  // [ ... ]
	setNode                   // { ... }
	procNode                  // proc(params) ... end
	paramNode                 // a single proc parameter (optionally ::type)
	localsNode                // local a, b, ...;
	globalsNode               // global a, b, ...;
	optionNode                // option ...;
	descriptionNode           // description "...";
	ifNode                    // if/elif/else/fi
	forNode                   // for/while/do
	returnNode                // return expr
	errorNode                 // error "..."
	tryNode                   // try/catch/finally
	useNode                   // use ... in ... end use
	moduleNode                // module() ... end module
	rangeNode                 // a..b
	arrowNode                 // params -> body
	typeNode                  // a::type annotation
	memberNode                // module:-member
	unaryNode                 // unary -, not, etc.
	matrixNode                // <...|...> / <...,...>
	breakNode                 // break
	nextNode                  // next
	readNode                  // read "file"
	emptyNode                 // empty statement / NULL placeholder
)

type tree struct {
	nodes []*tree
	group nodeType
	value string
}

// parser is the entry point used by the rest of the interpreter. It builds a
// rootNode whose children are the top-level statements.
func parser(tokens []token) (tree, error) {
	p := &parser_t{tokens: tokens}
	root := tree{group: rootNode}
	for !p.atEnd() {
		// allow stray statement separators
		if p.cur().group == statementDelim {
			p.next()
			continue
		}
		stmt, err := p.parseStatement()
		if err != nil {
			return root, err
		}
		if stmt != nil {
			root.nodes = append(root.nodes, stmt)
		}
		// consume the trailing statement terminator if present
		if p.cur().group == statementDelim {
			p.next()
		}
	}
	return root, nil
}

type parser_t struct {
	tokens []token
	pos    int
}

func (p *parser_t) cur() token {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return token{group: eofTok}
}

func (p *parser_t) peekAt(n int) token {
	if p.pos+n < len(p.tokens) {
		return p.tokens[p.pos+n]
	}
	return token{group: eofTok}
}

func (p *parser_t) next() token {
	t := p.cur()
	p.pos++
	return t
}

func (p *parser_t) atEnd() bool {
	return p.cur().group == eofTok || p.pos >= len(p.tokens)
}

func (p *parser_t) errf(format string, a ...interface{}) error {
	return fmt.Errorf("parse error at token %d (%q): %s",
		p.pos, p.cur().value, fmt.Sprintf(format, a...))
}

func (p *parser_t) expect(g tokenType, what string) (token, error) {
	if p.cur().group != g {
		return token{}, p.errf("expected %s", what)
	}
	return p.next(), nil
}

func (p *parser_t) isKeyword(kw string) bool {
	return p.cur().group == keyword && p.cur().value == kw
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

// statementStopWords are keywords that close a statement block; parseStatement
// returns nil (no statement) when it sees one so the enclosing construct can
// consume it.
func isBlockTerminator(t token) bool {
	if t.group == eofTok {
		return true
	}
	if t.group == keyword {
		switch t.value {
		case "end", "fi", "od", "elif", "else", "then", "do",
			"catch", "finally", "in", "until":
			return true
		}
	}
	return false
}

func (p *parser_t) parseStatement() (*tree, error) {
	if p.cur().group == keyword {
		switch p.cur().value {
		case "if":
			return p.parseIf()
		case "for", "while":
			return p.parseLoop()
		case "do":
			return p.parseLoop() // bare do ... od
		case "return":
			return p.parseReturn()
		case "error":
			return p.parseError()
		case "try":
			return p.parseTry()
		case "use":
			return p.parseUse()
		case "local":
			return p.parseNameDecl(localsNode)
		case "global":
			return p.parseNameDecl(globalsNode)
		case "option", "options":
			return p.parseOption()
		case "description":
			return p.parseDescription()
		case "break":
			p.next()
			return &tree{group: breakNode}, nil
		case "next":
			p.next()
			return &tree{group: nextNode}, nil
		case "read":
			return p.parseRead()
		case "quit", "done", "stop":
			p.next()
			return &tree{group: emptyNode, value: "quit"}, nil
		}
	}
	// Otherwise it is an expression statement (which includes assignment).
	return p.parseExpr(0)
}

// parseNameDecl handles `local a, b::T, ...;` and `global a, b, ...;`.
func (p *parser_t) parseNameDecl(g nodeType) (*tree, error) {
	p.next() // local/global
	node := &tree{group: g}
	for {
		if p.cur().group == statementDelim || isBlockTerminator(p.cur()) {
			break
		}
		nameExpr, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		node.nodes = append(node.nodes, nameExpr)
		if p.cur().group == comma {
			p.next()
			continue
		}
		break
	}
	return node, nil
}

func (p *parser_t) parseOption() (*tree, error) {
	p.next() // option(s)
	node := &tree{group: optionNode}
	// options run to the statement terminator; collect them as raw exprs.
	for {
		if p.cur().group == statementDelim || isBlockTerminator(p.cur()) {
			break
		}
		e, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		node.nodes = append(node.nodes, e)
		if p.cur().group == comma {
			p.next()
			continue
		}
		break
	}
	return node, nil
}

func (p *parser_t) parseDescription() (*tree, error) {
	p.next() // description
	node := &tree{group: descriptionNode}
	for {
		if p.cur().group == statementDelim || isBlockTerminator(p.cur()) {
			break
		}
		e, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		node.nodes = append(node.nodes, e)
		if p.cur().group == comma {
			p.next()
			continue
		}
		break
	}
	return node, nil
}

func (p *parser_t) parseRead() (*tree, error) {
	p.next() // read
	arg, err := p.parseExpr(0)
	if err != nil {
		return nil, err
	}
	return &tree{group: readNode, nodes: []*tree{arg}}, nil
}

func (p *parser_t) parseReturn() (*tree, error) {
	p.next() // return
	node := &tree{group: returnNode}
	// `return;` or `return` with no value is legal.
	if p.cur().group == statementDelim || isBlockTerminator(p.cur()) {
		return node, nil
	}
	e, err := p.parseExpr(0)
	if err != nil {
		return nil, err
	}
	node.nodes = append(node.nodes, e)
	return node, nil
}

func (p *parser_t) parseError() (*tree, error) {
	p.next() // error
	node := &tree{group: errorNode}
	if p.cur().group == statementDelim || isBlockTerminator(p.cur()) {
		return node, nil
	}
	for {
		e, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		node.nodes = append(node.nodes, e)
		if p.cur().group == comma {
			p.next()
			continue
		}
		break
	}
	return node, nil
}

// parseBlock parses a sequence of statements until a block terminator keyword.
func (p *parser_t) parseBlock() ([]*tree, error) {
	var stmts []*tree
	for {
		// skip stray separators
		for p.cur().group == statementDelim {
			p.next()
		}
		if isBlockTerminator(p.cur()) {
			break
		}
		stmt, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		if stmt != nil {
			stmts = append(stmts, stmt)
		}
		if p.cur().group == statementDelim {
			p.next()
			continue
		}
		// no terminator: must be at a block boundary
		if isBlockTerminator(p.cur()) {
			break
		}
		// Otherwise the next token begins a new statement without a
		// separator (rare but legal at end of block); loop again.
	}
	return stmts, nil
}

func (p *parser_t) parseIf() (*tree, error) {
	p.next() // if
	node := &tree{group: ifNode}
	// clauses are stored as alternating [cond, block, cond, block, ..., elseBlock?]
	cond, err := p.parseExpr(0)
	if err != nil {
		return nil, err
	}
	if !p.isKeyword("then") {
		return nil, p.errf("expected 'then' in if-statement")
	}
	p.next()
	block, err := p.parseBlockTree()
	if err != nil {
		return nil, err
	}
	node.nodes = append(node.nodes, cond, block)

	for p.isKeyword("elif") {
		p.next()
		c, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		if !p.isKeyword("then") {
			return nil, p.errf("expected 'then' after elif condition")
		}
		p.next()
		b, err := p.parseBlockTree()
		if err != nil {
			return nil, err
		}
		node.nodes = append(node.nodes, c, b)
	}

	if p.isKeyword("else") {
		p.next()
		b, err := p.parseBlockTree()
		if err != nil {
			return nil, err
		}
		// else block stored as a lone trailing block (odd count signals else)
		node.nodes = append(node.nodes, b)
	}

	// closing: `fi` or `end if` or `end`
	if err := p.expectEnd("if"); err != nil {
		return nil, err
	}
	return node, nil
}

// expectEnd consumes a block close: `fi`/`od`/`end`/`end <kw>`/`end use`/`until`.
func (p *parser_t) expectEnd(kind string) error {
	switch {
	case p.isKeyword("fi") && kind == "if":
		p.next()
		return nil
	case p.isKeyword("od") && kind == "do":
		p.next()
		return nil
	case p.isKeyword("end"):
		p.next()
		// optional trailing keyword: `end proc`, `end if`, `end do`, `end use`,
		// `end module`. Consume it if it matches a closer word.
		if p.cur().group == keyword {
			switch p.cur().value {
			case "proc", "if", "do", "use", "module", "for", "while", "try":
				p.next()
			}
		}
		return nil
	default:
		return p.errf("expected closing for %s", kind)
	}
}

// parseBlockTree wraps parseBlock results into a rootNode-like subtree.
func (p *parser_t) parseBlockTree() (*tree, error) {
	stmts, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	return &tree{group: rootNode, nodes: stmts}, nil
}

// parseLoop handles for/from/to/by/in/while/do ... od / end do.
//
//	for v from a to b by c while cond do ... od
//	for v in expr while cond do ... od
//	while cond do ... od
//	do ... od
func (p *parser_t) parseLoop() (*tree, error) {
	node := &tree{group: forNode}
	// store control parts in node.value-tagged child trees via a small header.
	header := &tree{group: exprseqNode} // holds control clauses as tagged children

	if p.isKeyword("for") {
		p.next()
		// loop variable (a name/expr); optional.
		if !p.isKeyword("from") && !p.isKeyword("in") && !p.isKeyword("to") &&
			!p.isKeyword("by") && !p.isKeyword("while") && !p.isKeyword("do") {
			// Parse the loop variable above relational precedence so the `in`
			// keyword (also the membership relational operator) terminates the
			// variable rather than being swallowed as `x in [...]`.
			v, err := p.parseExpr(bpRange)
			if err != nil {
				return nil, err
			}
			header.nodes = append(header.nodes, &tree{group: exprseqNode, value: "var", nodes: []*tree{v}})
		}
		// from / to / by / in may appear in any order in Maple.
		for {
			var tag string
			switch {
			case p.isKeyword("from"):
				tag = "from"
			case p.isKeyword("in"):
				tag = "in"
			case p.isKeyword("to"):
				tag = "to"
			case p.isKeyword("by"):
				tag = "by"
			default:
				tag = ""
			}
			if tag == "" {
				break
			}
			p.next()
			e, err := p.parseExpr(0)
			if err != nil {
				return nil, err
			}
			header.nodes = append(header.nodes, &tree{group: exprseqNode, value: tag, nodes: []*tree{e}})
		}
	}

	if p.isKeyword("while") {
		p.next()
		e, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		header.nodes = append(header.nodes, &tree{group: exprseqNode, value: "while", nodes: []*tree{e}})
	}

	if !p.isKeyword("do") {
		return nil, p.errf("expected 'do' in loop")
	}
	p.next()
	body, err := p.parseBlockTree()
	if err != nil {
		return nil, err
	}
	if err := p.expectEnd("do"); err != nil {
		return nil, err
	}
	node.nodes = append(node.nodes, header, body)
	return node, nil
}

func (p *parser_t) parseTry() (*tree, error) {
	p.next() // try
	node := &tree{group: tryNode}
	body, err := p.parseBlockTree()
	if err != nil {
		return nil, err
	}
	node.nodes = append(node.nodes, &tree{group: exprseqNode, value: "try", nodes: []*tree{body}})

	for p.isKeyword("catch") {
		p.next()
		clause := &tree{group: exprseqNode, value: "catch"}
		// optional catch-string(s) before ':'
		if p.cur().group != statementDelim {
			for {
				if p.cur().group == statementDelim {
					break
				}
				e, err := p.parseExpr(0)
				if err != nil {
					return nil, err
				}
				clause.nodes = append(clause.nodes, e)
				if p.cur().group == comma {
					p.next()
					continue
				}
				break
			}
		}
		// the ':' separating catch-string from catch-body
		if p.cur().group == statementDelim {
			p.next()
		}
		cbody, err := p.parseBlockTree()
		if err != nil {
			return nil, err
		}
		clause.nodes = append(clause.nodes, cbody)
		node.nodes = append(node.nodes, clause)
	}

	if p.isKeyword("finally") {
		p.next()
		fbody, err := p.parseBlockTree()
		if err != nil {
			return nil, err
		}
		node.nodes = append(node.nodes, &tree{group: exprseqNode, value: "finally", nodes: []*tree{fbody}})
	}

	if err := p.expectEnd("try"); err != nil {
		return nil, err
	}
	return node, nil
}

func (p *parser_t) parseUse() (*tree, error) {
	p.next() // use
	node := &tree{group: useNode}
	// use <bindings> in <body> end use
	for {
		if p.isKeyword("in") {
			break
		}
		e, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		node.nodes = append(node.nodes, &tree{group: exprseqNode, value: "binding", nodes: []*tree{e}})
		if p.cur().group == comma {
			p.next()
			continue
		}
		break
	}
	if !p.isKeyword("in") {
		return nil, p.errf("expected 'in' in use-statement")
	}
	p.next()
	body, err := p.parseBlockTree()
	if err != nil {
		return nil, err
	}
	node.nodes = append(node.nodes, &tree{group: exprseqNode, value: "body", nodes: []*tree{body}})
	if err := p.expectEnd("use"); err != nil {
		return nil, err
	}
	return node, nil
}
