package main

// parseProc parses a Maple procedure definition:
//
//	proc(p1, p2::type, ...) local a,b; global c; option ...; description "..";
//	    <body>
//	end proc        (or `end` or `end:`)
//
// The resulting procNode has the shape:
//
//	procNode
//	  ├─ exprseqNode "params"   → paramNode children
//	  ├─ localsNode | globalsNode | optionNode | descriptionNode  (zero or more)
//	  └─ rootNode "body"        → statement children
func (p *parser_t) parseProc() (*tree, error) {
	p.next() // 'proc'
	node := &tree{group: procNode}

	// parameter list
	if _, err := p.expect(lparen, "'(' after proc"); err != nil {
		return nil, err
	}
	params := &tree{group: exprseqNode, value: "params"}
	for p.cur().group != rparen {
		par, err := p.parseParam()
		if err != nil {
			return nil, err
		}
		params.nodes = append(params.nodes, par)
		if p.cur().group == comma {
			p.next()
			continue
		}
		break
	}
	if _, err := p.expect(rparen, "')' closing proc params"); err != nil {
		return nil, err
	}
	node.nodes = append(node.nodes, params)

	// Optional return-type annotation:  proc(...) ::T   (newer Maple).
	if p.cur().group == operator && p.cur().value == "::" {
		p.next()
		rt, err := p.parseExpr(bpType)
		if err != nil {
			return nil, err
		}
		node.nodes = append(node.nodes, &tree{group: typeNode, value: "rettype", nodes: []*tree{rt}})
	}

	// declaration section: local/global/option/description in any order,
	// each terminated by ';' or ':'.
	for {
		if p.cur().group != keyword {
			break
		}
		switch p.cur().value {
		case "local":
			d, err := p.parseNameDecl(localsNode)
			if err != nil {
				return nil, err
			}
			node.nodes = append(node.nodes, d)
		case "global":
			d, err := p.parseNameDecl(globalsNode)
			if err != nil {
				return nil, err
			}
			node.nodes = append(node.nodes, d)
		case "option", "options":
			d, err := p.parseOption()
			if err != nil {
				return nil, err
			}
			node.nodes = append(node.nodes, d)
		case "description":
			d, err := p.parseDescription()
			if err != nil {
				return nil, err
			}
			node.nodes = append(node.nodes, d)
		default:
			goto body
		}
		if p.cur().group == statementDelim {
			p.next()
		}
	}

body:
	bodyStmts, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	node.nodes = append(node.nodes, &tree{group: rootNode, value: "body", nodes: bodyStmts})

	// closing 'end' / 'end proc'
	if err := p.expectEnd("proc"); err != nil {
		return nil, err
	}
	return node, nil
}

// parseParam parses a single proc parameter: name, name::type, name:=default,
// or name::type:=default.
func (p *parser_t) parseParam() (*tree, error) {
	param := &tree{group: paramNode}
	// Parse the parameter expression (name, possibly with ::type) ABOVE comma
	// precedence so the comma separating parameters terminates this parse rather
	// than folding `x, y` into a single exprseq param. We also stop before ':='
	// so the default value is parsed separately below.
	e, err := p.parseExpr(bpComma)
	if err != nil {
		return nil, err
	}
	param.nodes = append(param.nodes, e)
	// optional default value `:= expr`
	if p.cur().group == assignment {
		p.next()
		def, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		param.value = "default"
		param.nodes = append(param.nodes, def)
	}
	return param, nil
}

// parseModule parses `module() ... end module`. DifferentialThomas does not
// define modules itself (it uses the build.sh table model), but `LinearAlgebra`
// and `CodeTools` are referenced as modules; supporting the definition form
// keeps the grammar complete and lets test fixtures exercise it.
func (p *parser_t) parseModule() (*tree, error) {
	p.next() // 'module'
	node := &tree{group: moduleNode}
	// optional () parameter list
	if p.cur().group == lparen {
		p.next()
		if p.cur().group != rparen {
			// modules take no positional params in practice; parse leniently.
			_, err := p.parseExprList(rparen)
			if err != nil {
				return nil, err
			}
		}
		if _, err := p.expect(rparen, "')' after module"); err != nil {
			return nil, err
		}
	}

	for {
		if p.cur().group != keyword {
			break
		}
		switch p.cur().value {
		case "local":
			d, err := p.parseNameDecl(localsNode)
			if err != nil {
				return nil, err
			}
			node.nodes = append(node.nodes, d)
		case "global":
			d, err := p.parseNameDecl(globalsNode)
			if err != nil {
				return nil, err
			}
			node.nodes = append(node.nodes, d)
		case "export":
			d, err := p.parseNameDecl(localsNode) // reuse decl shape
			if err != nil {
				return nil, err
			}
			d.value = "export"
			node.nodes = append(node.nodes, d)
		case "option", "options":
			d, err := p.parseOption()
			if err != nil {
				return nil, err
			}
			node.nodes = append(node.nodes, d)
		case "description":
			d, err := p.parseDescription()
			if err != nil {
				return nil, err
			}
			node.nodes = append(node.nodes, d)
		default:
			goto mbody
		}
		if p.cur().group == statementDelim {
			p.next()
		}
	}

mbody:
	bodyStmts, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	node.nodes = append(node.nodes, &tree{group: rootNode, value: "body", nodes: bodyStmts})
	if err := p.expectEnd("module"); err != nil {
		return nil, err
	}
	return node, nil
}
