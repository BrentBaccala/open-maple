package main

import (
	"errors"
	"strconv"
)

// binaryEvaluate takes a root node and applies the given function to each
// subnode (arithmetic-only; Phase-2 will generalise to the full data model).
func binaryEvaluate(root tree, symbolTable map[string]interface{}, fn binaryFunc) (interface{}, error) {
	var err error
	var leftValue, rightValue, nodeValue interface{}

	leftValue, _, err = processNode(*root.nodes[0], symbolTable)
	if err == nil {
		rightValue, _, err = processNode(*root.nodes[1], symbolTable)
		if err == nil {
			lf, lok := leftValue.(float64)
			rf, rok := rightValue.(float64)
			if !lok || !rok {
				return nil, errors.New("non-numeric operand (evaluation is arithmetic-only in this phase)")
			}
			nodeValue = fn(lf, rf)
		}
	}
	return nodeValue, err
}

// processOperator returns the value of an operator subNode (arithmetic only).
func processOperator(root tree, symbolTable map[string]interface{}) (interface{}, error) {
	switch root.value {
	case "+":
		return binaryEvaluate(root, symbolTable, add)
	case "-":
		return binaryEvaluate(root, symbolTable, subtract)
	case "*":
		return binaryEvaluate(root, symbolTable, multiply)
	case "/":
		return binaryEvaluate(root, symbolTable, divide)
	default:
		// Other operators (^, relational, set ops, etc.) are not yet evaluated.
		return nil, errUnimplemented("operator " + root.value)
	}
}

// errUnimplemented is the explicit "this node parses but does not evaluate yet"
// signal. Phase-1 is parse-only; the evaluator must not crash on constructs it
// has not learned to execute.
func errUnimplemented(what string) error {
	return errors.New("unimplemented (parse-only phase): " + what)
}

// processNode evaluates a node where it can, and returns errUnimplemented for
// the full-grammar nodes that the Phase-2 evaluator will handle. It never
// panics on a well-formed parse tree.
func processNode(root tree, symbolTable map[string]interface{}) (interface{}, map[string]interface{}, error) {
	var err error
	var nodeValue interface{}

	switch root.group {
	case variable:
		var ok bool
		nodeValue, ok = symbolTable[root.value]
		if !ok {
			err = errors.New("Could not find variable " + root.value)
		}
	case constant:
		nodeValue, err = strconv.ParseFloat(root.value, 64)
	case assign:
		if len(root.nodes) == 2 && root.nodes[0].group == variable {
			symbolTable[root.nodes[0].value], _, err = processNode(*root.nodes[1], symbolTable)
		} else {
			err = errUnimplemented("structured assignment target")
		}
	case operate:
		nodeValue, err = processOperator(root, symbolTable)
	case unaryNode:
		if root.value == "-" && len(root.nodes) == 1 {
			var v interface{}
			v, _, err = processNode(*root.nodes[0], symbolTable)
			if err == nil {
				if f, ok := v.(float64); ok {
					nodeValue = -f
				} else {
					err = errUnimplemented("unary minus on non-numeric")
				}
			}
		} else {
			err = errUnimplemented("unary " + root.value)
		}
	case rootNode:
		// evaluate a block of statements
		for _, n := range root.nodes {
			_, symbolTable, err = processNode(*n, symbolTable)
			if err != nil {
				break
			}
		}
	default:
		// Everything else parses but is not yet executable.
		err = errUnimplemented(root.group.String())
	}

	return nodeValue, symbolTable, err
}

// treeWalker walks an AST. In this phase it still only evaluates arithmetic;
// any non-arithmetic construct returns an errUnimplemented error rather than
// crashing, so a parse-only run can call it safely on the simple samples.
func treeWalker(root tree, symbolTable map[string]interface{}) (map[string]interface{}, error) {
	for _, val := range root.nodes {
		_, symbolTable, err := processNode(*val, symbolTable)
		if err != nil {
			return symbolTable, err
		}
	}
	return symbolTable, nil
}
