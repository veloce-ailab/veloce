package service

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"
	"strings"
)

// evalRiskFormula evaluates a deliberately small Go-expression subset. The AST
// walker never evaluates selectors, indexing, statements, or arbitrary calls.
func evalRiskFormula(expression string, variables map[string]float64) (float64, error) {
	parsed, err := parser.ParseExpr(strings.TrimSpace(expression))
	if err != nil {
		return 0, fmt.Errorf("invalid formula: %w", err)
	}
	value, err := evalRiskNode(parsed, variables)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("formula must return a number")
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("formula returned a non-finite number")
	}
	return number, nil
}

func evalRiskNode(node ast.Expr, variables map[string]float64) (interface{}, error) {
	switch value := node.(type) {
	case *ast.BasicLit:
		if value.Kind != token.INT && value.Kind != token.FLOAT {
			return nil, fmt.Errorf("only numeric literals are allowed")
		}
		number, err := strconv.ParseFloat(value.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q", value.Value)
		}
		return number, nil
	case *ast.Ident:
		if value.Name == "true" {
			return true, nil
		}
		if value.Name == "false" {
			return false, nil
		}
		number, ok := variables[value.Name]
		if !ok {
			return nil, fmt.Errorf("unknown formula variable %q", value.Name)
		}
		return number, nil
	case *ast.ParenExpr:
		return evalRiskNode(value.X, variables)
	case *ast.UnaryExpr:
		operand, err := evalRiskNode(value.X, variables)
		if err != nil {
			return nil, err
		}
		switch value.Op {
		case token.ADD:
			return riskNumber(operand)
		case token.SUB:
			number, err := riskNumber(operand)
			return -number, err
		case token.NOT:
			boolean, ok := operand.(bool)
			if !ok {
				return nil, fmt.Errorf("operator ! requires a boolean")
			}
			return !boolean, nil
		default:
			return nil, fmt.Errorf("operator %s is not allowed", value.Op)
		}
	case *ast.BinaryExpr:
		left, err := evalRiskNode(value.X, variables)
		if err != nil {
			return nil, err
		}
		if value.Op == token.LAND {
			leftBool, ok := left.(bool)
			if !ok {
				return nil, fmt.Errorf("operator && requires booleans")
			}
			if !leftBool {
				return false, nil
			}
		}
		if value.Op == token.LOR {
			leftBool, ok := left.(bool)
			if !ok {
				return nil, fmt.Errorf("operator || requires booleans")
			}
			if leftBool {
				return true, nil
			}
		}
		right, err := evalRiskNode(value.Y, variables)
		if err != nil {
			return nil, err
		}
		return evalRiskBinary(value.Op, left, right)
	case *ast.CallExpr:
		function, ok := value.Fun.(*ast.Ident)
		if !ok {
			return nil, fmt.Errorf("only whitelisted formula functions are allowed")
		}
		if function.Name == "choose" {
			if len(value.Args) != 3 {
				return nil, fmt.Errorf("choose(condition, trueValue, falseValue) requires three arguments")
			}
			conditionValue, err := evalRiskNode(value.Args[0], variables)
			if err != nil {
				return nil, err
			}
			condition, ok := conditionValue.(bool)
			if !ok {
				return nil, fmt.Errorf("choose condition must be boolean")
			}
			branch := value.Args[2]
			if condition {
				branch = value.Args[1]
			}
			return evalRiskNode(branch, variables)
		}
		arguments := make([]interface{}, len(value.Args))
		for index, argument := range value.Args {
			result, err := evalRiskNode(argument, variables)
			if err != nil {
				return nil, err
			}
			arguments[index] = result
		}
		return evalRiskFunction(function.Name, arguments)
	default:
		return nil, fmt.Errorf("expression node %T is not allowed", node)
	}
}

func evalRiskBinary(operator token.Token, left, right interface{}) (interface{}, error) {
	if operator == token.LAND || operator == token.LOR {
		leftBool, leftOK := left.(bool)
		rightBool, rightOK := right.(bool)
		if !leftOK || !rightOK {
			return nil, fmt.Errorf("operators && and || require booleans")
		}
		if operator == token.LAND {
			return leftBool && rightBool, nil
		}
		return leftBool || rightBool, nil
	}
	leftNumber, err := riskNumber(left)
	if err != nil {
		return nil, err
	}
	rightNumber, err := riskNumber(right)
	if err != nil {
		return nil, err
	}
	switch operator {
	case token.ADD:
		return leftNumber + rightNumber, nil
	case token.SUB:
		return leftNumber - rightNumber, nil
	case token.MUL:
		return leftNumber * rightNumber, nil
	case token.QUO:
		if rightNumber == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		return leftNumber / rightNumber, nil
	case token.REM:
		if rightNumber == 0 {
			return nil, fmt.Errorf("remainder by zero")
		}
		return math.Mod(leftNumber, rightNumber), nil
	case token.EQL:
		return leftNumber == rightNumber, nil
	case token.NEQ:
		return leftNumber != rightNumber, nil
	case token.LSS:
		return leftNumber < rightNumber, nil
	case token.LEQ:
		return leftNumber <= rightNumber, nil
	case token.GTR:
		return leftNumber > rightNumber, nil
	case token.GEQ:
		return leftNumber >= rightNumber, nil
	default:
		return nil, fmt.Errorf("operator %s is not allowed", operator)
	}
}

func evalRiskFunction(name string, arguments []interface{}) (interface{}, error) {
	if name == "choose" {
		if len(arguments) != 3 {
			return nil, fmt.Errorf("choose(condition, trueValue, falseValue) requires three arguments")
		}
		condition, ok := arguments[0].(bool)
		if !ok {
			return nil, fmt.Errorf("choose condition must be boolean")
		}
		if condition {
			return riskNumber(arguments[1])
		}
		return riskNumber(arguments[2])
	}
	if name == "abs" {
		if len(arguments) != 1 {
			return nil, fmt.Errorf("abs requires one argument")
		}
		number, err := riskNumber(arguments[0])
		return math.Abs(number), err
	}
	if name == "min" || name == "max" {
		if len(arguments) < 1 {
			return nil, fmt.Errorf("%s requires at least one argument", name)
		}
		number, err := riskNumber(arguments[0])
		if err != nil {
			return nil, err
		}
		for _, argument := range arguments[1:] {
			candidate, err := riskNumber(argument)
			if err != nil {
				return nil, err
			}
			if (name == "min" && candidate < number) || (name == "max" && candidate > number) {
				number = candidate
			}
		}
		return number, nil
	}
	return nil, fmt.Errorf("formula function %q is not allowed", name)
}

func riskNumber(value interface{}) (float64, error) {
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("numeric value required")
	}
	return number, nil
}
