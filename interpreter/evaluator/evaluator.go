// Package evaluator contains the core of our interpreter, which walks
// the AST produced by the parser and evaluates the user-submitted program.
package evaluator

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/kasworld/nonkey/config/builtinfunctions"
	"github.com/kasworld/nonkey/config/pragmas"
	"github.com/kasworld/nonkey/enum/objecttype"
	"github.com/kasworld/nonkey/enum/tokentype"
	"github.com/kasworld/nonkey/interpreter/ast"
	"github.com/kasworld/nonkey/interpreter/asti"
	"github.com/kasworld/nonkey/interpreter/object"
)

// Eval is our core function for evaluating nodes.
func Eval(node asti.NodeI, env *object.Environment) object.ObjectI {
	switch node := node.(type) {

	//Statements
	case *ast.Program:
		return evalProgram(node, env)
	case *ast.ExpressionStatement:
		return Eval(node.Expression, env)

	//Expressions
	case *ast.IntegerLiteral:
		return &object.Integer{Value: node.Value}
	case *ast.FloatLiteral:
		return &object.Float{Value: node.Value}
	case *ast.Boolean:
		return nativeBoolToBooleanObject(node.Value)
	case *ast.PrefixExpression:
		right := Eval(node.Right, env)
		if object.IsError(right) {
			return right
		}
		return evalPrefixExpression(node, node.Operator, right)
	case *ast.PostfixExpression:
		return evalPostfixExpression(env, node.Operator, node)
	case *ast.InfixExpression:
		left := Eval(node.Left, env)
		if object.IsError(left) {
			return left
		}
		right := Eval(node.Right, env)
		if object.IsError(right) {
			return right
		}
		res := evalInfixExpression(node, node.Operator, left, right, env)
		if object.IsError(res) {
			fmt.Fprintf(os.Stderr, "%s\n", res.Inspect())
			if pragmas.PRAGMAS["strict"] == 1 {
				os.Exit(1)
			}
		}
		return (res)

	case *ast.BlockStatement:
		return evalBlockStatement(node, env)
	case *ast.IfExpression:
		return evalIfExpression(node, env)
	case *ast.TernaryExpression:
		return evalTernaryExpression(node, env)
	case *ast.ForLoopExpression:
		return evalForLoopExpression(node, env)
	case *ast.ForeachStatement:
		return evalForeachExpression(node, env)
	case *ast.ReturnStatement:
		val := Eval(node.ReturnValue, env)
		if object.IsError(val) {
			return val
		}
		return &object.ReturnValue{Value: val}
	case *ast.LetStatement:
		val := Eval(node.Value, env)
		if object.IsError(val) {
			return val
		}
		env.Set(node.Name.Value, val)
		return val
	case *ast.ConstStatement:
		val := Eval(node.Value, env)
		if object.IsError(val) {
			return val
		}
		env.SetConst(node.Name.Value, val)
		return val
	case *ast.Identifier:
		return evalIdentifier(node, env)
	case *ast.FunctionLiteral:
		params := node.Parameters
		body := node.Body
		defaults := node.Defaults
		return &object.Function{Parameters: params, Env: env, Body: body, Defaults: defaults}
	case *ast.FunctionDefineLiteral:
		params := node.Parameters
		body := node.Body
		defaults := node.Defaults
		env.Set(node.GetToken().Literal, &object.Function{Parameters: params, Env: env, Body: body, Defaults: defaults})
		return object.NULL
	case *ast.ObjectCallExpression:
		res := evalObjectCallExpression(node, env)
		if object.IsError(res) {
			fmt.Fprintf(os.Stderr, "%s\n",
				res.Inspect())
			if pragmas.PRAGMAS["strict"] == 1 {
				os.Exit(1)
			}
		}
		return res
	case *ast.CallExpression:
		function := Eval(node.Function, env)
		if object.IsError(function) {
			return function
		}
		args := evalExpression(node.Arguments, env)
		if len(args) == 1 && object.IsError(args[0]) {
			return args[0]
		}
		res := applyFunction(node, env, function, args)
		if object.IsError(res) {
			fmt.Fprintf(os.Stderr, "%v %v\n", res.Inspect(), node.Function)
			if pragmas.PRAGMAS["strict"] == 1 {
				os.Exit(1)
			}
			return res
		}
		return res

	case *ast.ArrayLiteral:
		elements := evalExpression(node.Elements, env)
		if len(elements) == 1 && object.IsError(elements[0]) {
			return elements[0]
		}
		return &object.Array{Elements: elements}
	case *ast.StringLiteral:
		return &object.String{Value: node.Value}
	case *ast.RegexpLiteral:
		return &object.Regexp{Value: node.Value, Flags: node.Flags}
	case *ast.BacktickLiteral:
		return backTickOperation(node.Value)
	case *ast.IndexExpression:
		left := Eval(node.Left, env)
		if object.IsError(left) {
			return left
		}
		index := Eval(node.Index, env)
		if object.IsError(index) {
			return index
		}
		return evalIndexExpression(node, left, index)
	case *ast.AssignStatement:
		return evalAssignStatement(node, env)
	case *ast.HashLiteral:
		return evalHashLiteral(node, env)
	case *ast.SwitchExpression:
		return evalSwitchStatement(node, env)
	}
	return nil
}

// eval block statement
func evalBlockStatement(block *ast.BlockStatement, env *object.Environment) object.ObjectI {
	var result object.ObjectI
	for _, statement := range block.Statements {
		result = Eval(statement, env)
		if result != nil {
			rt := result.Type()
			if rt == objecttype.RETURN_VALUE || rt == objecttype.ERROR {
				return result
			}
		}
	}
	return result
}

// for performance, using single instance of boolean
func nativeBoolToBooleanObject(input bool) *object.Boolean {
	if input {
		return object.TRUE
	}
	return object.FALSE
}

// eval prefix expression
func evalPrefixExpression(node asti.NodeI, operator tokentype.TokenType, right object.ObjectI) object.ObjectI {
	switch operator {
	case tokentype.BANG:
		return evalBangOperatorExpression(right)
	case tokentype.MINUS:
		return evalMinusPrefixOperatorExpression(node, right)
	default:
		return object.NewError(node, "unknown operator: %s%s",
			operator.Literal(), right.Type())
	}
}

func evalPostfixExpression(env *object.Environment, operator tokentype.TokenType, node *ast.PostfixExpression) object.ObjectI {
	switch operator {
	case tokentype.PLUS_PLUS:
		val, ok := env.Get(node.Token.Literal)
		if !ok {
			return object.NewError(node, "%s is unknown", node.Token.Literal)
		}

		switch arg := val.(type) {
		case *object.Integer:
			v := arg.Value
			env.Set(node.Token.Literal, &object.Integer{Value: v + 1})
			return arg
		default:
			return object.NewError(node, "%s is not an int", node.Token.Literal)

		}
	case tokentype.MINUS_MINUS:
		val, ok := env.Get(node.Token.Literal)
		if !ok {
			return object.NewError(node, "%s is unknown", node.Token.Literal)
		}

		switch arg := val.(type) {
		case *object.Integer:
			v := arg.Value
			env.Set(node.Token.Literal, &object.Integer{Value: v - 1})
			return arg
		default:
			return object.NewError(node, "%s is not an int", node.Token.Literal)
		}
	default:
		return object.NewError(node, "unknown operator: %s",
			operator.Literal())
	}
}

func evalBangOperatorExpression(right object.ObjectI) object.ObjectI {
	switch right {
	case object.TRUE:
		return object.FALSE
	case object.FALSE:
		return object.TRUE
	case object.NULL:
		return object.TRUE
	default:
		return object.FALSE
	}
}

func evalMinusPrefixOperatorExpression(node asti.NodeI, right object.ObjectI) object.ObjectI {
	switch obj := right.(type) {
	case *object.Integer:
		return &object.Integer{Value: -obj.Value}
	case *object.Float:
		return &object.Float{Value: -obj.Value}
	default:
		return object.NewError(node, "unknown operator: -%s", right.Type())
	}
}

func evalInfixExpression(node asti.NodeI, operator tokentype.TokenType, left, right object.ObjectI, env *object.Environment) object.ObjectI {
	switch {
	case left.Type() == objecttype.INTEGER && right.Type() == objecttype.INTEGER:
		return evalIntegerInfixExpression(node, operator, left, right)
	case left.Type() == objecttype.FLOAT && right.Type() == objecttype.FLOAT:
		return evalFloatInfixExpression(node, operator, left, right)
	case left.Type() == objecttype.FLOAT && right.Type() == objecttype.INTEGER:
		return evalFloatIntegerInfixExpression(node, operator, left, right)
	case left.Type() == objecttype.INTEGER && right.Type() == objecttype.FLOAT:
		return evalIntegerFloatInfixExpression(node, operator, left, right)
	case left.Type() == objecttype.STRING && right.Type() == objecttype.STRING:
		return evalStringInfixExpression(node, operator, left, right)
	case operator == tokentype.AND:
		return nativeBoolToBooleanObject(objectToNativeBoolean(left) && objectToNativeBoolean(right))
	case operator == tokentype.OR:
		return nativeBoolToBooleanObject(objectToNativeBoolean(left) || objectToNativeBoolean(right))
	case operator == tokentype.NOT_CONTAINS:
		return notMatches(node, left, right)
	case operator == tokentype.CONTAINS:
		return matches(node, left, right, env)

	case operator == tokentype.EQ:
		return nativeBoolToBooleanObject(left == right)

	case operator == tokentype.NOT_EQ:
		return nativeBoolToBooleanObject(left != right)
	case left.Type() == objecttype.BOOLEAN && right.Type() == objecttype.BOOLEAN:
		return evalBooleanInfixExpression(node, operator, left, right)
	case left.Type() != right.Type():
		return object.NewError(node, "type mismatch: %s %s %s",
			left.Type(), operator.Literal(), right.Type())
	default:
		return object.NewError(node, "unknown operator: %s %s %s",
			left.Type(), operator.Literal(), right.Type())
	}
}

func matches(node asti.NodeI, left, right object.ObjectI, env *object.Environment) object.ObjectI {

	str := left.Inspect()

	if right.Type() != objecttype.REGEXP {
		return object.NewError(node, "regexp required for regexp-match, given %s", right.Type())
	}

	val := right.(*object.Regexp).Value
	if right.(*object.Regexp).Flags != "" {
		val = "(?" + right.(*object.Regexp).Flags + ")" + val
	}

	// Compile the regular expression.
	r, err := regexp.Compile(val)

	// Ensure it compiled
	if err != nil {
		return object.NewError(node, "error compiling regexp '%s': %s", right.Inspect(), err)
	}

	res := r.FindStringSubmatch(str)

	// Do we have any captures?
	if len(res) > 1 {
		for i := 1; i < len(res); i++ {
			env.Set(fmt.Sprintf("$%d", i), &object.String{Value: res[i]})
		}
	}

	// Test if it matched
	if len(res) > 0 {
		return object.TRUE
	}

	return object.FALSE
}

func notMatches(node asti.NodeI, left, right object.ObjectI) object.ObjectI {
	str := left.Inspect()

	if right.Type() != objecttype.REGEXP {
		return object.NewError(node, "regexp required for regexp-match, given %s", right.Type())
	}

	val := right.(*object.Regexp).Value
	if right.(*object.Regexp).Flags != "" {
		val = "(?" + right.(*object.Regexp).Flags + ")" + val
	}

	// Compile the regular expression.
	r, err := regexp.Compile(val)

	// Ensure it compiled
	if err != nil {
		return object.NewError(node, "error compiling regexp '%s': %s", right.Inspect(), err)
	}

	// Test if it matched
	if r.MatchString(str) {
		return object.FALSE
	}

	return object.TRUE
}

// boolean operations
func evalBooleanInfixExpression(node asti.NodeI, operator tokentype.TokenType, left, right object.ObjectI) object.ObjectI {
	// convert the bools to strings.
	l := &object.String{Value: string(left.Inspect())}
	r := &object.String{Value: string(right.Inspect())}

	switch operator {
	case tokentype.LT:
		return evalStringInfixExpression(node, operator, l, r)
	case tokentype.LT_EQUALS:
		return evalStringInfixExpression(node, operator, l, r)
	case tokentype.GT:
		return evalStringInfixExpression(node, operator, l, r)
	case tokentype.GT_EQUALS:
		return evalStringInfixExpression(node, operator, l, r)
	default:
		return object.NewError(node, "unknown operator: %s %s %s",
			left.Type(), operator.Literal(), right.Type())
	}
}

func evalIntegerInfixExpression(node asti.NodeI, operator tokentype.TokenType, left, right object.ObjectI) object.ObjectI {
	leftVal := left.(*object.Integer).Value
	rightVal := right.(*object.Integer).Value
	switch operator {
	case tokentype.PLUS:
		return &object.Integer{Value: leftVal + rightVal}
	case tokentype.PLUS_EQUALS:
		return &object.Integer{Value: leftVal + rightVal}
	case tokentype.MOD:
		return &object.Integer{Value: leftVal % rightVal}
	case tokentype.POW:
		return &object.Integer{Value: int64(math.Pow(float64(leftVal), float64(rightVal)))}
	case tokentype.MINUS:
		return &object.Integer{Value: leftVal - rightVal}
	case tokentype.MINUS_EQUALS:
		return &object.Integer{Value: leftVal - rightVal}
	case tokentype.ASTERISK:
		return &object.Integer{Value: leftVal * rightVal}
	case tokentype.ASTERISK_EQUALS:
		return &object.Integer{Value: leftVal * rightVal}
	case tokentype.SLASH:
		return &object.Integer{Value: leftVal / rightVal}
	case tokentype.SLASH_EQUALS:
		return &object.Integer{Value: leftVal / rightVal}
	case tokentype.LT:
		return nativeBoolToBooleanObject(leftVal < rightVal)
	case tokentype.LT_EQUALS:
		return nativeBoolToBooleanObject(leftVal <= rightVal)
	case tokentype.GT:
		return nativeBoolToBooleanObject(leftVal > rightVal)
	case tokentype.GT_EQUALS:
		return nativeBoolToBooleanObject(leftVal >= rightVal)
	case tokentype.EQ:
		return nativeBoolToBooleanObject(leftVal == rightVal)
	case tokentype.NOT_EQ:
		return nativeBoolToBooleanObject(leftVal != rightVal)
	case tokentype.DOTDOT:
		len := int(rightVal-leftVal) + 1
		array := make([]object.ObjectI, len)
		i := 0
		for i < len {
			array[i] = &object.Integer{Value: leftVal}
			leftVal++
			i++
		}
		return &object.Array{Elements: array}
	default:
		return object.NewError(node, "unknown operator: %s %s %s",
			left.Type(), operator.Literal(), right.Type())
	}
}
func evalFloatInfixExpression(node asti.NodeI, operator tokentype.TokenType, left, right object.ObjectI) object.ObjectI {
	leftVal := left.(*object.Float).Value
	rightVal := right.(*object.Float).Value
	switch operator {
	case tokentype.PLUS:
		return &object.Float{Value: leftVal + rightVal}
	case tokentype.PLUS_EQUALS:
		return &object.Float{Value: leftVal + rightVal}
	case tokentype.MINUS:
		return &object.Float{Value: leftVal - rightVal}
	case tokentype.MINUS_EQUALS:
		return &object.Float{Value: leftVal - rightVal}
	case tokentype.ASTERISK:
		return &object.Float{Value: leftVal * rightVal}
	case tokentype.ASTERISK_EQUALS:
		return &object.Float{Value: leftVal * rightVal}
	case tokentype.POW:
		return &object.Float{Value: math.Pow(leftVal, rightVal)}
	case tokentype.SLASH:
		return &object.Float{Value: leftVal / rightVal}
	case tokentype.SLASH_EQUALS:
		return &object.Float{Value: leftVal / rightVal}
	case tokentype.LT:
		return nativeBoolToBooleanObject(leftVal < rightVal)
	case tokentype.LT_EQUALS:
		return nativeBoolToBooleanObject(leftVal <= rightVal)
	case tokentype.GT:
		return nativeBoolToBooleanObject(leftVal > rightVal)
	case tokentype.GT_EQUALS:
		return nativeBoolToBooleanObject(leftVal >= rightVal)
	case tokentype.EQ:
		return nativeBoolToBooleanObject(leftVal == rightVal)
	case tokentype.NOT_EQ:
		return nativeBoolToBooleanObject(leftVal != rightVal)
	default:
		return object.NewError(node, "unknown operator: %s %s %s",
			left.Type(), operator.Literal(), right.Type())
	}
}

func evalFloatIntegerInfixExpression(node asti.NodeI, operator tokentype.TokenType, left, right object.ObjectI) object.ObjectI {
	leftVal := left.(*object.Float).Value
	rightVal := float64(right.(*object.Integer).Value)
	switch operator {
	case tokentype.PLUS:
		return &object.Float{Value: leftVal + rightVal}
	case tokentype.PLUS_EQUALS:
		return &object.Float{Value: leftVal + rightVal}
	case tokentype.MINUS:
		return &object.Float{Value: leftVal - rightVal}
	case tokentype.MINUS_EQUALS:
		return &object.Float{Value: leftVal - rightVal}
	case tokentype.ASTERISK:
		return &object.Float{Value: leftVal * rightVal}
	case tokentype.ASTERISK_EQUALS:
		return &object.Float{Value: leftVal * rightVal}
	case tokentype.POW:
		return &object.Float{Value: math.Pow(leftVal, rightVal)}
	case tokentype.SLASH:
		return &object.Float{Value: leftVal / rightVal}
	case tokentype.SLASH_EQUALS:
		return &object.Float{Value: leftVal / rightVal}
	case tokentype.LT:
		return nativeBoolToBooleanObject(leftVal < rightVal)
	case tokentype.LT_EQUALS:
		return nativeBoolToBooleanObject(leftVal <= rightVal)
	case tokentype.GT:
		return nativeBoolToBooleanObject(leftVal > rightVal)
	case tokentype.GT_EQUALS:
		return nativeBoolToBooleanObject(leftVal >= rightVal)
	case tokentype.EQ:
		return nativeBoolToBooleanObject(leftVal == rightVal)
	case tokentype.NOT_EQ:
		return nativeBoolToBooleanObject(leftVal != rightVal)
	default:
		return object.NewError(node, "unknown operator: %s %s %s",
			left.Type(), operator.Literal(), right.Type())
	}
}

func evalIntegerFloatInfixExpression(node asti.NodeI, operator tokentype.TokenType, left, right object.ObjectI) object.ObjectI {
	leftVal := float64(left.(*object.Integer).Value)
	rightVal := right.(*object.Float).Value
	switch operator {
	case tokentype.PLUS:
		return &object.Float{Value: leftVal + rightVal}
	case tokentype.PLUS_EQUALS:
		return &object.Float{Value: leftVal + rightVal}
	case tokentype.MINUS:
		return &object.Float{Value: leftVal - rightVal}
	case tokentype.MINUS_EQUALS:
		return &object.Float{Value: leftVal - rightVal}
	case tokentype.ASTERISK:
		return &object.Float{Value: leftVal * rightVal}
	case tokentype.ASTERISK_EQUALS:
		return &object.Float{Value: leftVal * rightVal}
	case tokentype.POW:
		return &object.Float{Value: math.Pow(leftVal, rightVal)}
	case tokentype.SLASH:
		return &object.Float{Value: leftVal / rightVal}
	case tokentype.SLASH_EQUALS:
		return &object.Float{Value: leftVal / rightVal}
	case tokentype.LT:
		return nativeBoolToBooleanObject(leftVal < rightVal)
	case tokentype.LT_EQUALS:
		return nativeBoolToBooleanObject(leftVal <= rightVal)
	case tokentype.GT:
		return nativeBoolToBooleanObject(leftVal > rightVal)
	case tokentype.GT_EQUALS:
		return nativeBoolToBooleanObject(leftVal >= rightVal)
	case tokentype.EQ:
		return nativeBoolToBooleanObject(leftVal == rightVal)
	case tokentype.NOT_EQ:
		return nativeBoolToBooleanObject(leftVal != rightVal)
	default:
		return object.NewError(node, "unknown operator: %s %s %s",
			left.Type(), operator.Literal(), right.Type())
	}
}

func evalStringInfixExpression(node asti.NodeI, operator tokentype.TokenType, left, right object.ObjectI) object.ObjectI {
	l := left.(*object.String)
	r := right.(*object.String)

	switch operator {
	case tokentype.EQ:
		return nativeBoolToBooleanObject(l.Value == r.Value)
	case tokentype.NOT_EQ:
		return nativeBoolToBooleanObject(l.Value != r.Value)
	case tokentype.GT_EQUALS:
		return nativeBoolToBooleanObject(l.Value >= r.Value)
	case tokentype.GT:
		return nativeBoolToBooleanObject(l.Value > r.Value)
	case tokentype.LT_EQUALS:
		return nativeBoolToBooleanObject(l.Value <= r.Value)
	case tokentype.LT:
		return nativeBoolToBooleanObject(l.Value < r.Value)
	case tokentype.PLUS:
		return &object.String{Value: l.Value + r.Value}
	case tokentype.PLUS_EQUALS:
		return &object.String{Value: l.Value + r.Value}
	}

	return object.NewError(node, "unknown operator: %s %s %s",
		left.Type(), operator.Literal(), right.Type())
}

// evalIfExpression handles an `if` expression, running the block
// if the condition matches, and running any optional else block
// otherwise.
func evalIfExpression(ie *ast.IfExpression, env *object.Environment) object.ObjectI {
	//
	// Create an environment for handling regexps
	//
	var permit []string
	i := 1
	for i < 32 {
		permit = append(permit, fmt.Sprintf("$%d", i))
		i++
	}
	nEnv := object.NewTemporaryScope(env, permit)
	condition := Eval(ie.Condition, nEnv)
	if object.IsError(condition) {
		return condition
	}
	if isTruthy(condition) {
		return Eval(ie.Consequence, nEnv)
	} else if ie.Alternative != nil {
		return Eval(ie.Alternative, nEnv)
	} else {
		return object.NULL
	}
}

// evalTernaryExpression handles a ternary-expression.  If the condition
// is true we return the contents of evaluating the true-branch, otherwise
// the false-branch.  (Unlike an `if` statement we know that we always have
// an alternative/false branch.)
func evalTernaryExpression(te *ast.TernaryExpression, env *object.Environment) object.ObjectI {

	condition := Eval(te.Condition, env)
	if object.IsError(condition) {
		return condition
	}

	if isTruthy(condition) {
		return Eval(te.IfTrue, env)
	}
	return Eval(te.IfFalse, env)
}

func evalAssignStatement(a *ast.AssignStatement, env *object.Environment) (val object.ObjectI) {
	evaluated := Eval(a.Value, env)
	if object.IsError(evaluated) {
		return evaluated
	}

	//
	// An assignment is generally:
	//
	//    variable = value
	//
	// But we cheat and reuse the implementation for:
	//
	//    i += 4
	//
	// In this case we record the "operator" as tokentype.PLUS_EQUALS
	//
	switch a.Operator {
	case tokentype.PLUS_EQUALS:
		// Get the current value
		current, ok := env.Get(a.Name.String())
		if !ok {
			return object.NewError(a, "%s is unknown", a.Name.String())
		}

		res := evalInfixExpression(a, tokentype.PLUS_EQUALS, current, evaluated, env)
		if object.IsError(res) {
			fmt.Fprintf(os.Stderr, "%v\n", res.Inspect())
			return res
		}

		env.Set(a.Name.String(), res)
		return res

	case tokentype.MINUS_EQUALS:

		// Get the current value
		current, ok := env.Get(a.Name.String())
		if !ok {
			return object.NewError(a, "%s is unknown", a.Name.String())
		}

		res := evalInfixExpression(a, tokentype.MINUS_EQUALS, current, evaluated, env)
		if object.IsError(res) {
			fmt.Fprintf(os.Stderr, "%v\n", res.Inspect())
			return res
		}

		env.Set(a.Name.String(), res)
		return res

	case tokentype.ASTERISK_EQUALS:
		// Get the current value
		current, ok := env.Get(a.Name.String())
		if !ok {
			return object.NewError(a, "%s is unknown", a.Name.String())
		}

		res := evalInfixExpression(a, tokentype.ASTERISK_EQUALS, current, evaluated, env)
		if object.IsError(res) {
			fmt.Fprintf(os.Stderr, "%v\n", res.Inspect())
			return res
		}

		env.Set(a.Name.String(), res)
		return res

	case tokentype.SLASH_EQUALS:

		// Get the current value
		current, ok := env.Get(a.Name.String())
		if !ok {
			return object.NewError(a, "%s is unknown", a.Name.String())
		}

		res := evalInfixExpression(a, tokentype.SLASH_EQUALS, current, evaluated, env)
		if object.IsError(res) {
			fmt.Fprintf(os.Stderr, "%v\n", res.Inspect())
			return res
		}

		env.Set(a.Name.String(), res)
		return res

	case tokentype.ASSIGN:
		// If we're running with the strict-pragma it is
		// a bug to set a variable which wasn't declared (via let).
		if pragmas.PRAGMAS["strict"] == 1 {
			_, ok := env.Get(a.Name.String())
			if !ok {
				fmt.Fprintf(os.Stderr,
					"Setting unknown variable '%s' is a bug under strict-pragma! %v\n",
					a.Name.String(), a)
				os.Exit(1)
			}
		}

		env.Set(a.Name.String(), evaluated)
	}
	return evaluated
}

func evalSwitchStatement(se *ast.SwitchExpression, env *object.Environment) object.ObjectI {

	// Get the value.
	obj := Eval(se.Value, env)

	// Try all the choices
	for _, opt := range se.Choices {

		// skipping the default-case, which we'll
		// handle later.
		if opt.Default {
			continue
		}

		// Look at any expression we've got in this case.
		for _, val := range opt.Expr {

			// Get the value of the case
			out := Eval(val, env)

			// Is it a literal match?
			if obj.Type() == out.Type() &&
				(obj.Inspect() == out.Inspect()) {

				// Evaluate the block and return the value
				out := evalBlockStatement(opt.Block, env)
				return out
			}

			// Is it a regexp-match?
			if out.Type() == objecttype.REGEXP {

				m := matches(se, obj, out, env)
				if m == object.TRUE {

					// Evaluate the block and return the value
					out := evalBlockStatement(opt.Block, env)
					return out

				}
			}
		}
	}

	// No match?  Handle default if present
	for _, opt := range se.Choices {

		// skip default
		if opt.Default {

			out := evalBlockStatement(opt.Block, env)
			return out
		}
	}

	return nil
}

func evalForLoopExpression(fle *ast.ForLoopExpression, env *object.Environment) object.ObjectI {
	rt := &object.Boolean{Value: true}
	for {
		condition := Eval(fle.Condition, env)
		if object.IsError(condition) {
			return condition
		}
		if isTruthy(condition) {
			rt := Eval(fle.Consequence, env)
			if !object.IsError(rt) && (rt.Type() == objecttype.RETURN_VALUE || rt.Type() == objecttype.ERROR) {
				return rt
			}
		} else {
			break
		}
	}
	return rt
}

// handle "for x [,y] in .."
func evalForeachExpression(fle *ast.ForeachStatement, env *object.Environment) object.ObjectI {

	// expression
	val := Eval(fle.Value, env)

	helper, ok := val.(object.IterableI)
	if !ok {
		return object.NewError(fle,
			"%s object doesn't implement the Iterable interface", val.Type())
	}

	// The one/two values we're going to permit
	var permit []string
	permit = append(permit, fle.Ident)
	if fle.Index != "" {
		permit = append(permit, fle.Index)
	}

	// Create a new environment for the block
	//
	// This will allow writing EVERYTHING to the parent scope,
	// except the two variables named in the permit-array
	child := object.NewTemporaryScope(env, permit)

	// Reset the state of any previous iteration.
	helper.Reset()

	// Get the initial values.
	ret, idx, ok := helper.Next()

	for ok {

		// Set the index + name
		child.Set(fle.Ident, ret)

		idxName := fle.Index
		if idxName != "" {
			child.Set(fle.Index, idx)
		}

		// Eval the block
		rt := Eval(fle.Body, child)

		//
		// If we got an error/return then we handle it.
		//
		if !object.IsError(rt) && (rt.Type() == objecttype.RETURN_VALUE || rt.Type() == objecttype.ERROR) {
			return rt
		}

		// Loop again
		ret, idx, ok = helper.Next()
	}

	return &object.Null{}
}

func isTruthy(obj object.ObjectI) bool {
	switch obj {
	case object.NULL:
		return false
	case object.TRUE:
		return true
	case object.FALSE:
		return false
	default:
		return true
	}
}

func evalProgram(program *ast.Program, env *object.Environment) object.ObjectI {
	var result object.ObjectI
	for _, statement := range program.Statements {
		result = Eval(statement, env)
		switch result := result.(type) {
		case *object.ReturnValue:
			return result.Value
		case *object.Error:
			return result
		}
	}
	return result
}

func evalIdentifier(node *ast.Identifier, env *object.Environment) object.ObjectI {
	if val, ok := env.Get(node.Value); ok {
		return val
	}
	if builtin, ok := builtinfunctions.BuiltinFunctions[node.Value]; ok {
		return builtin
	}
	fmt.Fprintf(os.Stderr, "identifier not found: %v\n", node.Token)
	if pragmas.PRAGMAS["strict"] == 1 {
		os.Exit(1)
	}
	return object.NewError(node, "identifier not found: "+node.Value)
}

func evalExpression(exps []asti.ExpressionI, env *object.Environment) []object.ObjectI {
	var result []object.ObjectI
	for _, e := range exps {
		evaluated := Eval(e, env)
		if object.IsError(evaluated) {
			return []object.ObjectI{evaluated}
		}
		result = append(result, evaluated)
	}
	return result
}

// Split a line of text into tokens, but keep anything "quoted"
// together..
//
// So this input:
//
//   /bin/sh -c "ls /etc"
//
// Would give output of the form:
//   /bin/sh
//   -c
//   ls /etc
//
func splitCommand(input string) []string {

	//
	// This does the split into an array
	//
	r := regexp.MustCompile(`[^\s"']+|"([^"]*)"|'([^']*)`)
	res := r.FindAllString(input, -1)

	//
	// However the resulting pieces might be quoted.
	// So we have to remove them, if present.
	//
	var result []string
	for _, e := range res {
		result = append(result, trimQuotes(e, '"'))
	}
	return (result)
}

// Remove balanced characters around a string.
func trimQuotes(in string, c byte) string {
	if len(in) >= 2 {
		if in[0] == c && in[len(in)-1] == c {
			return in[1 : len(in)-1]
		}
	}
	return in
}

// Run a command and return a hash containing the result.
// `stderr`, `stdout`, and `error` will be the fields
func backTickOperation(command string) object.ObjectI {

	// split the command
	toExec := splitCommand(command)
	cmd := exec.Command(toExec[0], toExec[1:]...)

	// get the result
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()

	// If the command exits with a non-zero exit-code it
	// is regarded as a failure.  Here we test for ExitError
	// to regard that as a non-failure.
	if err != nil && err != err.(*exec.ExitError) {
		fmt.Fprintf(os.Stderr, "Failed to run '%s' -> %s\n", command, err.Error())
		return object.NULL
	}

	//
	// The result-objects to store in our hash.
	//
	stdout := &object.String{Value: outb.String()}
	stderr := &object.String{Value: errb.String()}

	// Create keys
	stdoutKey := &object.String{Value: "stdout"}
	stdoutHash := object.HashPair{Key: stdoutKey, Value: stdout}

	stderrKey := &object.String{Value: "stderr"}
	stderrHash := object.HashPair{Key: stderrKey, Value: stderr}

	// Make a new hash, and populate it
	newHash := make(map[object.HashKey]object.HashPair)
	newHash[stdoutKey.HashKey()] = stdoutHash
	newHash[stderrKey.HashKey()] = stderrHash

	return &object.Hash{Pairs: newHash}
}

func evalIndexExpression(node asti.NodeI, left, index object.ObjectI) object.ObjectI {
	switch {
	case left.Type() == objecttype.ARRAY && index.Type() == objecttype.INTEGER:
		return evalArrayIndexExpression(left, index)
	case left.Type() == objecttype.HASH:
		return evalHashIndexExpression(node, left, index)
	case left.Type() == objecttype.STRING:
		return evalStringIndexExpression(left, index)
	default:
		return object.NewError(node, "index operator not support:%s", left.Type())

	}
}

func evalArrayIndexExpression(array, index object.ObjectI) object.ObjectI {
	arrayObject := array.(*object.Array)
	idx := index.(*object.Integer).Value
	max := int64(len(arrayObject.Elements) - 1)
	if idx < 0 || idx > max {
		return object.NULL
	}
	return arrayObject.Elements[idx]
}
func evalHashIndexExpression(node asti.NodeI, hash, index object.ObjectI) object.ObjectI {
	hashObject := hash.(*object.Hash)
	key, ok := index.(object.HashableI)
	if !ok {
		return object.NewError(node, "unusable as hash key: %s", index.Type())
	}
	pair, ok := hashObject.Pairs[key.HashKey()]
	if !ok {
		return object.NULL
	}
	return pair.Value
}

func evalStringIndexExpression(input, index object.ObjectI) object.ObjectI {
	str := input.(*object.String).Value
	idx := index.(*object.Integer).Value
	max := int64(len(str))
	if idx < 0 || idx > max {
		return object.NULL
	}

	// Get the characters as an array of runes
	chars := []rune(str)

	// Now index
	ret := chars[idx]

	// And return as a string.
	return &object.String{Value: string(ret)}
}

func evalHashLiteral(node *ast.HashLiteral, env *object.Environment) object.ObjectI {
	pairs := make(map[object.HashKey]object.HashPair)
	for keyNode, valueNode := range node.Pairs {
		key := Eval(keyNode, env)
		if object.IsError(key) {
			return key
		}
		hashKey, ok := key.(object.HashableI)
		if !ok {
			return object.NewError(node, "unusable as hash key: %s", key.Type())
		}
		value := Eval(valueNode, env)
		if object.IsError(value) {
			return value
		}
		hashed := hashKey.HashKey()
		pairs[hashed] = object.HashPair{Key: key, Value: value}

	}
	return &object.Hash{Pairs: pairs}

}

func applyFunction(node asti.NodeI, env *object.Environment, fn object.ObjectI, args []object.ObjectI) object.ObjectI {
	switch fn := fn.(type) {
	case *object.Function:
		extendEnv := extendFunctionEnv(fn, args)
		evaluated := Eval(fn.Body, extendEnv)
		return upwrapReturnValue(evaluated)
	case *object.Builtin:
		return fn.Fn(node, env, args...)
	default:
		return object.NewError(node, "not a function: %s", fn.Type())
	}

}

func extendFunctionEnv(fn *object.Function, args []object.ObjectI) *object.Environment {
	env := object.NewEnclosedEnvironment(fn.Env)

	// Set the defaults
	for key, val := range fn.Defaults {
		env.Set(key, Eval(val, env))
	}
	for paramIdx, param := range fn.Parameters {
		if paramIdx < len(args) {
			env.Set(param.Value, args[paramIdx])
		}
	}
	return env
}

func upwrapReturnValue(obj object.ObjectI) object.ObjectI {
	if returnValue, ok := obj.(*object.ReturnValue); ok {
		return returnValue.Value
	}
	return obj
}

// evalObjectCallExpression invokes methods against objects.
func evalObjectCallExpression(call *ast.ObjectCallExpression, env *object.Environment) object.ObjectI {

	obj := Eval(call.Object, env)
	if method, ok := call.Call.(*ast.CallExpression); ok {

		//
		// Here we try to invoke the object.method() call which has
		// been implemented in go.
		//
		// We do this by forwarding the call to the appropriate
		// `invokeMethod` interface on the object.
		//
		args := evalExpression(call.Call.(*ast.CallExpression).Arguments, env)
		ret := obj.InvokeMethod(method.Function.String(), *env, args...)
		if ret != nil {
			return ret
		}

		//
		// If we reach this point then the invokation didn't
		// succeed, that probably means that the function wasn't
		// implemented in go.
		//
		// So now we want to look for it in monkey, and we have
		// enough details to find the appropriate function.
		//
		//  * We have the object involved.
		//
		//  * We have the type of that object.
		//
		//  * We have the name of the function.
		//
		//  * We have the arguments.
		//
		// We'll use the type + name to lookup the (global) function
		// to invoke.  For example in this case we'll invoke
		// `string.len()` - because the type of the object we're
		// invoking-against is string:
		//
		//  "steve".len();
		//
		// For this case we'll be looking for `array.foo()`.
		//
		//   let a = [ 1, 2, 3 ];
		//   puts( a.foo() );
		//
		// As a final fall-back we'll look for "object.foo()"
		// if "array.foo()" isn't defined.
		//
		//
		//
		attempts := []string{}
		attempts = append(attempts, strings.ToLower(string(obj.Type())))
		attempts = append(attempts, "object")

		//
		// Look for "$type.name", or "object.name"
		//
		for _, prefix := range attempts {

			//
			// What we're attempting to execute.
			//
			name := prefix + "." + method.Function.String()

			//
			// Try to find that function in our environment.
			//
			if fn, ok := env.Get(name); ok {

				//
				// Extend our environment with the functional-args.
				//
				extendEnv := extendFunctionEnv(fn.(*object.Function), args)

				//
				// Now set "self" to be the implicit object, against
				// which the function-call will be operating.
				//
				extendEnv.Set("self", obj)

				//
				// Finally invoke & return.
				//
				evaluated := Eval(fn.(*object.Function).Body, extendEnv)
				obj = upwrapReturnValue(evaluated)
				return obj
			} else {
				//fmt.Fprintf(os.Stderr,"fail to exec %v %v\n", name, env)
			}
		}

	}

	//
	// If we hit this point we have had a method invoked which
	// was neither defined in go nor monkey.
	//
	// e.g. "steve".md5sum()
	//
	// So we've got no choice but to return an error.
	//
	return object.NewError(call, "Failed to invoke method: %s", call.Call.(*ast.CallExpression).Function.String())
}

func objectToNativeBoolean(o object.ObjectI) bool {
	if r, ok := o.(*object.ReturnValue); ok {
		o = r.Value
	}
	switch obj := o.(type) {
	case *object.Boolean:
		return obj.Value
	case *object.String:
		return obj.Value != ""
	case *object.Regexp:
		return obj.Value != ""
	case *object.Null:
		return false
	case *object.Integer:
		if obj.Value == 0 {
			return false
		}
		return true
	case *object.Float:
		if obj.Value == 0.0 {
			return false
		}
		return true
	case *object.Array:
		if len(obj.Elements) == 0 {
			return false
		}
		return true
	case *object.Hash:
		if len(obj.Pairs) == 0 {
			return false
		}
		return true
	default:
		return true
	}
}
