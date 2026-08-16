package compiler

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
)

// Lower converts the checked Go frontend representation into typed,
// AST-independent control-flow IR.
func Lower(checked *CheckedPackage) (*Program, error) {
	if checked == nil || checked.Parsed == nil || checked.Package == nil || checked.Info == nil {
		return nil, fmt.Errorf("lower: nil or incomplete checked package")
	}
	program := &Program{Package: checked.Package.Name()}
	for _, declaration := range checked.Parsed.AST.Decls {
		functionDeclaration, ok := declaration.(*ast.FuncDecl)
		if !ok {
			return nil, lowerDiagnostic(checked, declaration, "unexpected non-function declaration")
		}
		if functionDeclaration.Body == nil {
			continue // validated explicit guest-memory or Solana syscall declaration
		}
		lowerer := &functionLowerer{
			checked:         checked,
			variables:       make(map[*types.Var]ValueID),
			memoryVariables: make(map[*types.Var]MemoryObjectID),
			addressTaken:    findAddressTakenVariables(checked, functionDeclaration),
		}
		function, err := lowerer.lowerFunction(functionDeclaration)
		if err != nil {
			return nil, err
		}
		program.Functions = append(program.Functions, function)
	}
	if len(program.Functions) == 0 {
		return nil, lowerDiagnostic(checked, checked.Parsed.AST, "source file declares no functions")
	}
	if err := program.Validate(); err != nil {
		return nil, fmt.Errorf("lower: generated invalid IR: %w", err)
	}
	return program, nil
}

type functionLowerer struct {
	checked         *CheckedPackage
	function        *Function
	current         *BasicBlock
	variables       map[*types.Var]ValueID
	memoryVariables map[*types.Var]MemoryObjectID
	addressTaken    map[*types.Var]bool
	temporary       uint32
}

func findAddressTakenVariables(checked *CheckedPackage, declaration *ast.FuncDecl) map[*types.Var]bool {
	result := make(map[*types.Var]bool)
	ast.Inspect(declaration.Body, func(node ast.Node) bool {
		unary, ok := node.(*ast.UnaryExpr)
		if !ok || unary.Op != token.AND {
			return true
		}
		identifier, ok := unary.X.(*ast.Ident)
		if !ok {
			return true
		}
		object := checked.Info.Uses[identifier]
		if object == nil {
			object = checked.Info.Defs[identifier]
		}
		if variable, ok := object.(*types.Var); ok {
			result[variable] = true
		}
		return true
	})
	return result
}

func (l *functionLowerer) lowerFunction(declaration *ast.FuncDecl) (*Function, error) {
	object, ok := l.checked.Info.Defs[declaration.Name].(*types.Func)
	if !ok {
		return nil, l.errorAt(declaration.Name, "cannot resolve function declaration")
	}
	signature, ok := object.Type().(*types.Signature)
	if !ok {
		return nil, l.errorAt(declaration.Name, "function has an invalid signature")
	}
	resultType := TypeVoid
	if signature.Results().Len() == 1 {
		var err error
		resultType, err = strictIRType(signature.Results().At(0).Type())
		if err != nil {
			return nil, l.errorAt(declaration.Type.Results, "%v", err)
		}
	}
	l.function = &Function{Name: declaration.Name.Name, Result: resultType, Entry: NoBlock}
	for index := 0; index < signature.Params().Len(); index++ {
		parameter := signature.Params().At(index)
		parameterType, err := strictIRType(parameter.Type())
		if err != nil {
			return nil, l.errorAt(declaration.Type.Params, "parameter %d: %v", index+1, err)
		}
		name := parameter.Name()
		if name == "" || name == "_" {
			name = fmt.Sprintf("arg%d", index+1)
		}
		id := l.newValue(name, parameterType, ValueParameter)
		l.function.Params = append(l.function.Params, id)
		if l.addressTaken[parameter] {
			object, objectErr := l.newMemoryVariable(parameter, name, parameterType)
			if objectErr != nil {
				return nil, l.errorAt(declaration.Type.Params, "parameter %s: %v", name, objectErr)
			}
			l.memoryVariables[parameter] = object
		} else {
			l.variables[parameter] = id
		}
	}
	entry := l.newBlock("entry")
	l.function.Entry = entry.ID
	l.current = entry
	for index := 0; index < signature.Params().Len(); index++ {
		parameter := signature.Params().At(index)
		if object, ok := l.memoryVariables[parameter]; ok {
			address := l.emitObjectAddress(object)
			parameterType, _ := strictIRType(parameter.Type())
			l.emit(Instruction{Op: OpStore, Dest: NoValue, X: address, Y: l.function.Params[index], Memory: memoryTypeFor(parameterType), Pos: l.position(declaration.Pos())})
		}
	}
	if err := l.lowerBlock(declaration.Body); err != nil {
		return nil, err
	}
	if l.current != nil {
		if resultType != TypeVoid {
			return nil, l.errorAt(declaration.Body, "function %s can reach its end without returning %s", declaration.Name.Name, resultType)
		}
		l.current.Terminator = Terminator{Kind: TermReturn, Result: NoValue, Pos: l.position(declaration.Body.End())}
	}
	return l.function, nil
}

func (l *functionLowerer) lowerBlock(block *ast.BlockStmt) error {
	for _, statement := range block.List {
		if l.current == nil {
			// go/types has already checked unreachable syntax and types. Omitting it
			// keeps the IR free of unterminated, predecessor-less blocks.
			break
		}
		if err := l.lowerStatement(statement); err != nil {
			return err
		}
	}
	return nil
}

func (l *functionLowerer) lowerStatement(statement ast.Stmt) error {
	switch current := statement.(type) {
	case *ast.BlockStmt:
		return l.lowerBlock(current)
	case *ast.EmptyStmt:
		return nil
	case *ast.ReturnStmt:
		return l.lowerReturn(current)
	case *ast.AssignStmt:
		return l.lowerAssignment(current)
	case *ast.DeclStmt:
		return l.lowerDeclaration(current)
	case *ast.IncDecStmt:
		return l.lowerIncDec(current)
	case *ast.ExprStmt:
		call, ok := current.X.(*ast.CallExpr)
		if !ok {
			return l.errorAt(current, "only calls may be expression statements")
		}
		_, err := l.lowerCall(call)
		return err
	case *ast.IfStmt:
		return l.lowerIf(current)
	case *ast.ForStmt:
		return l.lowerFor(current)
	default:
		return l.errorAt(current, "statement %T is not supported", current)
	}
}

func (l *functionLowerer) lowerReturn(statement *ast.ReturnStmt) error {
	if l.function.Result == TypeVoid {
		if len(statement.Results) != 0 {
			return l.errorAt(statement, "void function cannot return a value")
		}
		l.current.Terminator = Terminator{Kind: TermReturn, Result: NoValue, Pos: l.position(statement.Pos())}
		l.current = nil
		return nil
	}
	if len(statement.Results) != 1 {
		return l.errorAt(statement, "function must return exactly one %s value", l.function.Result)
	}
	result, err := l.lowerExpression(statement.Results[0], l.function.Result)
	if err != nil {
		return err
	}
	l.current.Terminator = Terminator{Kind: TermReturn, Result: result, Pos: l.position(statement.Pos())}
	l.current = nil
	return nil
}

func (l *functionLowerer) lowerAssignment(statement *ast.AssignStmt) error {
	if statement.Tok == token.ADD_ASSIGN || statement.Tok == token.SUB_ASSIGN || statement.Tok == token.MUL_ASSIGN || statement.Tok == token.QUO_ASSIGN || statement.Tok == token.REM_ASSIGN {
		return l.lowerCompoundAssignment(statement)
	}
	if statement.Tok != token.ASSIGN && statement.Tok != token.DEFINE {
		return l.errorAt(statement, "assignment operator %s is not supported", statement.Tok)
	}
	if len(statement.Lhs) != len(statement.Rhs) {
		return l.errorAt(statement, "multiple-result assignments are not supported")
	}
	targets := make([]assignmentTarget, len(statement.Lhs))
	for index, expression := range statement.Lhs {
		target, err := l.lowerAssignmentTarget(expression, statement.Tok == token.DEFINE)
		if err != nil {
			return err
		}
		targets[index] = target
	}
	for index, target := range targets {
		if _, _, array := target.valueType.ArrayElement(); array {
			if len(targets) != 1 {
				return l.errorAt(statement, "fixed-array initialization cannot be mixed with parallel assignment")
			}
			return l.initializeArray(target.object, statement.Rhs[index], target.fresh)
		}
	}
	return l.assignValues(targets, statement.Rhs, statement.Pos())
}

func (l *functionLowerer) lowerCompoundAssignment(statement *ast.AssignStmt) error {
	if len(statement.Lhs) != 1 || len(statement.Rhs) != 1 {
		return l.errorAt(statement, "compound assignment requires one variable and one value")
	}
	target, err := l.lowerAssignmentTarget(statement.Lhs[0], false)
	if err != nil {
		return err
	}
	if !target.valueType.integer() {
		return l.errorAt(statement.Lhs[0], "compound arithmetic assignment requires an integer variable")
	}
	left, err := l.readAssignmentTarget(target)
	if err != nil {
		return err
	}
	right, err := l.lowerExpression(statement.Rhs[0], target.valueType)
	if err != nil {
		return err
	}
	op := mapAssignmentOp(statement.Tok)
	computed := l.newTemporary(target.valueType)
	l.emit(Instruction{Op: op, Dest: computed, X: left, Y: right, Pos: l.position(statement.Pos())})
	return l.writeAssignmentTarget(target, computed, statement.Pos())
}

type assignmentTarget struct {
	direct    ValueID
	address   ValueID
	object    MemoryObjectID
	valueType Type
	fresh     bool
}

func (l *functionLowerer) assignValues(targets []assignmentTarget, expressions []ast.Expr, position token.Pos) error {
	// Every RHS is snapshotted before any destination is modified. This
	// preserves Go's parallel assignment semantics for cases such as a, b=b, a.
	values := make([]ValueID, len(expressions))
	for index, expression := range expressions {
		value, err := l.lowerExpression(expression, targets[index].valueType)
		if err != nil {
			return err
		}
		snapshot := l.newTemporary(targets[index].valueType)
		l.emit(Instruction{Op: OpMove, Dest: snapshot, X: value, Pos: l.position(expression.Pos())})
		values[index] = snapshot
	}
	for index, target := range targets {
		if err := l.writeAssignmentTarget(target, values[index], position); err != nil {
			return err
		}
	}
	return nil
}

func (l *functionLowerer) lowerDeclaration(statement *ast.DeclStmt) error {
	declaration, ok := statement.Decl.(*ast.GenDecl)
	if !ok || declaration.Tok != token.VAR {
		return l.errorAt(statement, "only local var declarations are supported")
	}
	for _, specification := range declaration.Specs {
		valueSpec, ok := specification.(*ast.ValueSpec)
		if !ok {
			return l.errorAt(specification, "declaration %T is not supported", specification)
		}
		targets := make([]assignmentTarget, len(valueSpec.Names))
		for index, name := range valueSpec.Names {
			target, err := l.lowerAssignmentTarget(name, true)
			if err != nil {
				return err
			}
			targets[index] = target
		}
		if len(valueSpec.Values) == 0 {
			for _, target := range targets {
				if _, _, array := target.valueType.ArrayElement(); array {
					if err := l.zeroMemoryObject(target.object, valueSpec.Pos()); err != nil {
						return err
					}
					continue
				}
				zero := l.newTemporary(target.valueType)
				l.emit(Instruction{Op: OpConst, Dest: zero, Imm: 0, Pos: l.position(valueSpec.Pos())})
				if err := l.writeAssignmentTarget(target, zero, valueSpec.Pos()); err != nil {
					return err
				}
			}
			continue
		}
		if len(targets) != len(valueSpec.Values) {
			return l.errorAt(valueSpec, "multiple-result variable initialization is not supported")
		}
		for index, target := range targets {
			if _, _, array := target.valueType.ArrayElement(); array {
				if len(targets) != 1 {
					return l.errorAt(valueSpec, "fixed-array initialization cannot be mixed with parallel declaration")
				}
				if err := l.initializeArray(target.object, valueSpec.Values[index], target.fresh); err != nil {
					return err
				}
				continue
			}
		}
		if _, _, array := targets[0].valueType.ArrayElement(); !array {
			if err := l.assignValues(targets, valueSpec.Values, valueSpec.Pos()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *functionLowerer) lowerIncDec(statement *ast.IncDecStmt) error {
	target, err := l.lowerAssignmentTarget(statement.X, false)
	if err != nil {
		return err
	}
	if !target.valueType.integer() {
		return l.errorAt(statement.X, "increment and decrement require an integer variable")
	}
	current, err := l.readAssignmentTarget(target)
	if err != nil {
		return err
	}
	one := l.newTemporary(target.valueType)
	l.emit(Instruction{Op: OpConst, Dest: one, Imm: 1, Pos: l.position(statement.Pos())})
	computed := l.newTemporary(target.valueType)
	op := OpAdd
	if statement.Tok == token.DEC {
		op = OpSub
	}
	l.emit(Instruction{Op: op, Dest: computed, X: current, Y: one, Pos: l.position(statement.Pos())})
	return l.writeAssignmentTarget(target, computed, statement.Pos())
}

func (l *functionLowerer) lowerIf(statement *ast.IfStmt) error {
	if statement.Init != nil {
		if err := l.lowerStatement(statement.Init); err != nil {
			return err
		}
	}
	condition, err := l.lowerExpression(statement.Cond, TypeBool)
	if err != nil {
		return err
	}
	branch := l.current
	thenBlock := l.newBlock("if.then")
	elseBlock := l.newBlock("if.else")
	branch.Terminator = Terminator{
		Kind:  TermBranch,
		Cond:  condition,
		True:  thenBlock.ID,
		False: elseBlock.ID,
		Pos:   l.position(statement.If),
	}

	l.current = thenBlock
	if err := l.lowerBlock(statement.Body); err != nil {
		return err
	}
	thenEnd := l.current

	l.current = elseBlock
	if statement.Else != nil {
		if err := l.lowerStatement(statement.Else); err != nil {
			return err
		}
	}
	elseEnd := l.current

	if thenEnd == nil && elseEnd == nil {
		l.current = nil
		return nil
	}
	join := l.newBlock("if.join")
	if thenEnd != nil {
		thenEnd.Terminator = Terminator{Kind: TermJump, Target: join.ID, Result: NoValue, Pos: l.position(statement.End())}
	}
	if elseEnd != nil {
		elseEnd.Terminator = Terminator{Kind: TermJump, Target: join.ID, Result: NoValue, Pos: l.position(statement.End())}
	}
	l.current = join
	return nil
}

func (l *functionLowerer) lowerFor(statement *ast.ForStmt) error {
	if statement.Init != nil {
		if err := l.lowerStatement(statement.Init); err != nil {
			return err
		}
	}
	preheader := l.current
	conditionBlock := l.newBlock("for.cond")
	bodyBlock := l.newBlock("for.body")
	preheader.Terminator = Terminator{Kind: TermJump, Target: conditionBlock.ID, Result: NoValue, Pos: l.position(statement.For)}

	l.current = conditionBlock
	var exitBlock *BasicBlock
	if statement.Cond == nil {
		conditionBlock.Terminator = Terminator{Kind: TermJump, Target: bodyBlock.ID, Result: NoValue, Pos: l.position(statement.For)}
	} else {
		condition, err := l.lowerExpression(statement.Cond, TypeBool)
		if err != nil {
			return err
		}
		exitBlock = l.newBlock("for.exit")
		conditionBlock.Terminator = Terminator{
			Kind:  TermBranch,
			Cond:  condition,
			True:  bodyBlock.ID,
			False: exitBlock.ID,
			Pos:   l.position(statement.Cond.Pos()),
		}
	}

	l.current = bodyBlock
	if err := l.lowerBlock(statement.Body); err != nil {
		return err
	}
	if l.current != nil {
		if statement.Post != nil {
			postBlock := l.newBlock("for.post")
			l.current.Terminator = Terminator{Kind: TermJump, Target: postBlock.ID, Result: NoValue, Pos: l.position(statement.Post.Pos())}
			l.current = postBlock
			if err := l.lowerStatement(statement.Post); err != nil {
				return err
			}
		}
		if l.current != nil {
			l.current.Terminator = Terminator{Kind: TermJump, Target: conditionBlock.ID, Result: NoValue, Pos: l.position(statement.End())}
		}
	}
	// A condition-less loop has no supported break statement, hence no
	// reachable continuation. A conditional loop continues through for.exit.
	l.current = exitBlock
	return nil
}

func (l *functionLowerer) lowerExpression(expression ast.Expr, expected Type) (ValueID, error) {
	if typeAndValue, ok := l.checked.Info.Types[expression]; ok && typeAndValue.Value != nil {
		effective, err := l.effectiveExpressionType(expression, expected)
		if err != nil {
			return NoValue, err
		}
		bits, err := constantBits(typeAndValue.Value, effective)
		if err != nil {
			return NoValue, l.errorAt(expression, "%v", err)
		}
		destination := l.newTemporary(effective)
		l.emit(Instruction{Op: OpConst, Dest: destination, Imm: bits, Pos: l.position(expression.Pos())})
		return destination, nil
	}

	switch current := expression.(type) {
	case *ast.ParenExpr:
		return l.lowerExpression(current.X, expected)
	case *ast.Ident:
		if current.Name == "nil" {
			if _, pointer := expected.PointerElement(); !pointer {
				return NoValue, l.errorAt(current, "nil requires an explicit pointer context")
			}
			destination := l.newTemporary(expected)
			l.emit(Instruction{Op: OpConst, Dest: destination, Imm: 0, Pos: l.position(current.Pos())})
			return destination, nil
		}
		object, ok := l.checked.Info.Uses[current].(*types.Var)
		if !ok {
			return NoValue, l.errorAt(current, "identifier %s is not a local variable", current.Name)
		}
		if memoryObject, exists := l.memoryVariables[object]; exists {
			objectType, err := strictIRType(object.Type())
			if err != nil {
				return NoValue, l.errorAt(current, "%v", err)
			}
			if _, _, array := objectType.ArrayElement(); array {
				return NoValue, l.errorAt(current, "fixed array %s must be indexed; arrays are not register values", current.Name)
			}
			address := l.emitObjectAddress(memoryObject)
			return l.emitLoad(address, objectType, current.Pos()), nil
		}
		id, exists := l.variables[object]
		if !exists {
			return NoValue, l.errorAt(current, "variable %s is not available in this function", current.Name)
		}
		value, _ := l.function.Value(id)
		if expected.scalar() && value.Type != expected {
			return NoValue, l.errorAt(current, "variable %s has type %s, want %s", current.Name, value.Type, expected)
		}
		return id, nil
	case *ast.UnaryExpr:
		return l.lowerUnary(current, expected)
	case *ast.StarExpr:
		address, element, err := l.lowerPointerDereference(current)
		if err != nil {
			return NoValue, err
		}
		if expected.scalar() && expected != element {
			return NoValue, l.errorAt(current, "pointer dereference has type %s, want %s", element, expected)
		}
		return l.emitLoad(address, element, current.Pos()), nil
	case *ast.IndexExpr:
		address, element, err := l.lowerArrayElementAddress(current)
		if err != nil {
			return NoValue, err
		}
		if expected.scalar() && expected != element {
			return NoValue, l.errorAt(current, "array element has type %s, want %s", element, expected)
		}
		return l.emitLoad(address, element, current.Pos()), nil
	case *ast.BinaryExpr:
		return l.lowerBinary(current)
	case *ast.CallExpr:
		return l.lowerCall(current)
	default:
		return NoValue, l.errorAt(current, "expression %T is not supported", current)
	}
}

func (l *functionLowerer) lowerUnary(expression *ast.UnaryExpr, expected Type) (ValueID, error) {
	if expression.Op == token.AND {
		address, element, err := l.lowerAddressExpression(expression.X)
		if err != nil {
			return NoValue, err
		}
		pointerType := PointerTo(element)
		if expected.scalar() && expected != pointerType {
			return NoValue, l.errorAt(expression, "address has type %s, want %s", pointerType, expected)
		}
		return address, nil
	}
	effective, err := l.effectiveExpressionType(expression, expected)
	if err != nil {
		return NoValue, err
	}
	value, err := l.lowerExpression(expression.X, effective)
	if err != nil {
		return NoValue, err
	}
	if expression.Op == token.ADD {
		return value, nil
	}
	if expression.Op != token.SUB || !effective.integer() {
		return NoValue, l.errorAt(expression, "unary operator %s is not supported for %s", expression.Op, effective)
	}
	zero := l.newTemporary(effective)
	l.emit(Instruction{Op: OpConst, Dest: zero, Imm: 0, Pos: l.position(expression.Pos())})
	destination := l.newTemporary(effective)
	l.emit(Instruction{Op: OpSub, Dest: destination, X: zero, Y: value, Pos: l.position(expression.Pos())})
	return destination, nil
}

func (l *functionLowerer) lowerBinary(expression *ast.BinaryExpr) (ValueID, error) {
	op := mapBinaryOp(expression.Op)
	if op == OpInvalid {
		return NoValue, l.errorAt(expression, "binary operator %s is not supported", expression.Op)
	}
	comparison := op >= OpEqual && op <= OpGreaterEqual
	operandType, err := l.binaryOperandType(expression)
	if err != nil {
		return NoValue, err
	}
	left, err := l.lowerExpression(expression.X, operandType)
	if err != nil {
		return NoValue, err
	}
	right, err := l.lowerExpression(expression.Y, operandType)
	if err != nil {
		return NoValue, err
	}
	resultType := operandType
	if comparison {
		resultType = TypeBool
	}
	destination := l.newTemporary(resultType)
	l.emit(Instruction{Op: op, Dest: destination, X: left, Y: right, Pos: l.position(expression.Pos())})
	return destination, nil
}

func (l *functionLowerer) lowerCall(call *ast.CallExpr) (ValueID, error) {
	identifier, ok := call.Fun.(*ast.Ident)
	if !ok {
		return NoValue, l.errorAt(call.Fun, "call target must be an internal function name or scalar type conversion")
	}
	switch object := l.checked.Info.Uses[identifier].(type) {
	case *types.TypeName:
		if len(call.Args) != 1 {
			return NoValue, l.errorAt(call, "scalar conversion requires exactly one argument")
		}
		targetType, err := strictIRType(object.Type())
		if err != nil || !targetType.integer() {
			return NoValue, l.errorAt(identifier, "conversion target %s is not supported", object.Type())
		}
		sourceType, err := l.effectiveExpressionType(call.Args[0], TypeInvalid)
		if err != nil {
			return NoValue, err
		}
		value, err := l.lowerExpression(call.Args[0], sourceType)
		if err != nil {
			return NoValue, err
		}
		destination := l.newTemporary(targetType)
		l.emit(Instruction{Op: OpMove, Dest: destination, X: value, Pos: l.position(call.Pos())})
		return destination, nil
	case *types.Func:
		if intrinsic, ok := l.checked.Intrinsics[object]; ok {
			return l.lowerMemoryIntrinsic(call, intrinsic)
		}
		if intrinsic, ok := l.checked.Syscalls[object]; ok {
			return l.lowerSyscallIntrinsic(call, intrinsic)
		}
		if object.Pkg() != l.checked.Package {
			return NoValue, l.errorAt(identifier, "external function calls are not supported")
		}
		signature := object.Type().(*types.Signature)
		if len(call.Args) != signature.Params().Len() {
			return NoValue, l.errorAt(call, "call to %s has %d arguments, want %d", identifier.Name, len(call.Args), signature.Params().Len())
		}
		arguments := make([]ValueID, len(call.Args))
		for index, argument := range call.Args {
			parameterType, err := strictIRType(signature.Params().At(index).Type())
			if err != nil {
				return NoValue, l.errorAt(argument, "call argument %d: %v", index+1, err)
			}
			arguments[index], err = l.lowerExpression(argument, parameterType)
			if err != nil {
				return NoValue, err
			}
		}
		destination := NoValue
		if signature.Results().Len() == 1 {
			resultType, err := strictIRType(signature.Results().At(0).Type())
			if err != nil {
				return NoValue, l.errorAt(call, "call result: %v", err)
			}
			destination = l.newTemporary(resultType)
		}
		l.emit(Instruction{
			Op:     OpCall,
			Dest:   destination,
			Callee: object.Name(),
			Args:   arguments,
			Pos:    l.position(call.Pos()),
		})
		return destination, nil
	default:
		return NoValue, l.errorAt(identifier, "call target %s is not supported", identifier.Name)
	}
}

func (l *functionLowerer) lowerSyscallIntrinsic(call *ast.CallExpr, intrinsic syscallIntrinsic) (ValueID, error) {
	if len(call.Args) != int(intrinsic.ParamCount) {
		return NoValue, l.errorAt(call, "Solana syscall intrinsic %s requires %d arguments", intrinsic.Symbol, intrinsic.ParamCount)
	}
	arguments := make([]ValueID, len(call.Args))
	for index, argument := range call.Args {
		value, err := l.lowerExpression(argument, intrinsic.Parameters[index])
		if err != nil {
			return NoValue, err
		}
		arguments[index] = value
	}
	destination := NoValue
	if intrinsic.Result != TypeVoid {
		destination = l.newTemporary(intrinsic.Result)
	}
	l.emit(Instruction{
		Op:           OpSyscall,
		Dest:         destination,
		X:            NoValue,
		Y:            NoValue,
		Callee:       intrinsic.Symbol,
		Args:         arguments,
		SyscallID:    intrinsic.ID,
		Object:       NoMemoryObject,
		SourceObject: NoMemoryObject,
		Pos:          l.position(call.Pos()),
	})
	return destination, nil
}

func (l *functionLowerer) lowerMemoryIntrinsic(call *ast.CallExpr, intrinsic memoryIntrinsic) (ValueID, error) {
	if intrinsic.Address {
		if len(call.Args) != 1 {
			return NoValue, l.errorAt(call, "guest-address intrinsic requires 1 argument")
		}
		pointer, err := l.lowerExpression(call.Args[0], PointerTo(intrinsic.Memory.ValueType()))
		if err != nil {
			return NoValue, err
		}
		destination := l.newTemporary(TypeUint64)
		l.emit(Instruction{Op: OpPointerAddress, Dest: destination, X: pointer, Pos: l.position(call.Pos())})
		return destination, nil
	}
	wantArguments := 1
	if intrinsic.Store {
		wantArguments = 2
	}
	if len(call.Args) != wantArguments {
		return NoValue, l.errorAt(call, "guest-memory intrinsic requires %d arguments", wantArguments)
	}
	address, err := l.lowerExpression(call.Args[0], TypeUint64)
	if err != nil {
		return NoValue, err
	}
	if intrinsic.Store {
		value, valueErr := l.lowerExpression(call.Args[1], intrinsic.Memory.ValueType())
		if valueErr != nil {
			return NoValue, valueErr
		}
		l.emit(Instruction{Op: OpStore, Dest: NoValue, X: address, Y: value,
			Memory: intrinsic.Memory, Pos: l.position(call.Pos())})
		return NoValue, nil
	}
	destination := l.newTemporary(intrinsic.Memory.ValueType())
	l.emit(Instruction{Op: OpLoad, Dest: destination, X: address,
		Memory: intrinsic.Memory, Pos: l.position(call.Pos())})
	return destination, nil
}

func (l *functionLowerer) binaryOperandType(expression *ast.BinaryExpr) (Type, error) {
	if expression.Op != token.EQL && expression.Op != token.NEQ && expression.Op != token.LSS &&
		expression.Op != token.LEQ && expression.Op != token.GTR && expression.Op != token.GEQ {
		return l.effectiveExpressionType(expression, TypeInvalid)
	}
	if expressionType, err := strictIRType(l.checked.Info.TypeOf(expression.X)); err == nil {
		return expressionType, nil
	}
	if expressionType, err := strictIRType(l.checked.Info.TypeOf(expression.Y)); err == nil {
		return expressionType, nil
	}
	return TypeInvalid, l.errorAt(expression, "comparison operands do not have a supported scalar type")
}

func (l *functionLowerer) effectiveExpressionType(expression ast.Expr, expected Type) (Type, error) {
	goType := l.checked.Info.TypeOf(expression)
	if goType != nil {
		if result, err := strictIRType(goType); err == nil {
			return result, nil
		}
		if basic, ok := goType.Underlying().(*types.Basic); ok {
			switch basic.Kind() {
			case types.UntypedBool:
				return TypeBool, nil
			case types.UntypedInt, types.UntypedRune:
				if expected.integer() {
					return expected, nil
				}
			}
		}
	}
	if expected.scalar() {
		return expected, nil
	}
	return TypeInvalid, l.errorAt(expression, "expression has unsupported type %v", goType)
}

func constantBits(value constant.Value, valueType Type) (uint64, error) {
	switch valueType {
	case TypeBool:
		if value.Kind() != constant.Bool {
			return 0, fmt.Errorf("constant %s is not bool", value.ExactString())
		}
		if constant.BoolVal(value) {
			return 1, nil
		}
		return 0, nil
	case TypeUint64:
		integer := constant.ToInt(value)
		if integer.Kind() == constant.Unknown {
			return 0, fmt.Errorf("constant %s is not an integer", value.ExactString())
		}
		result, exact := constant.Uint64Val(integer)
		if !exact {
			return 0, fmt.Errorf("constant %s does not fit uint64", value.ExactString())
		}
		return result, nil
	case TypeUint32:
		integer := constant.ToInt(value)
		if integer.Kind() == constant.Unknown {
			return 0, fmt.Errorf("constant %s is not an integer", value.ExactString())
		}
		result, exact := constant.Uint64Val(integer)
		if !exact || result > uint64(^uint32(0)) {
			return 0, fmt.Errorf("constant %s does not fit uint32", value.ExactString())
		}
		return result, nil
	case TypeUint8:
		integer := constant.ToInt(value)
		if integer.Kind() == constant.Unknown {
			return 0, fmt.Errorf("constant %s is not an integer", value.ExactString())
		}
		result, exact := constant.Uint64Val(integer)
		if !exact || result > uint64(^uint8(0)) {
			return 0, fmt.Errorf("constant %s does not fit uint8", value.ExactString())
		}
		return result, nil
	case TypeInt64:
		integer := constant.ToInt(value)
		if integer.Kind() == constant.Unknown {
			return 0, fmt.Errorf("constant %s is not an integer", value.ExactString())
		}
		result, exact := constant.Int64Val(integer)
		if !exact {
			return 0, fmt.Errorf("constant %s does not fit int64", value.ExactString())
		}
		return uint64(result), nil
	case TypeInt32:
		integer := constant.ToInt(value)
		if integer.Kind() == constant.Unknown {
			return 0, fmt.Errorf("constant %s is not an integer", value.ExactString())
		}
		result, exact := constant.Int64Val(integer)
		if !exact || result < -1<<31 || result > 1<<31-1 {
			return 0, fmt.Errorf("constant %s does not fit int32", value.ExactString())
		}
		return uint64(int64(int32(result))), nil
	default:
		if _, pointer := valueType.PointerElement(); pointer {
			return 0, fmt.Errorf("pointer constants are not supported")
		}
		return 0, fmt.Errorf("constant has unsupported type %s", valueType)
	}
}

func (l *functionLowerer) lowerAssignmentTarget(expression ast.Expr, defining bool) (assignmentTarget, error) {
	target := assignmentTarget{direct: NoValue, address: NoValue, object: NoMemoryObject}
	switch current := expression.(type) {
	case *ast.Ident:
		var object types.Object
		if defining {
			object = l.checked.Info.Defs[current]
		}
		if object == nil {
			object = l.checked.Info.Uses[current]
		}
		variable, ok := object.(*types.Var)
		if !ok || current.Name == "_" {
			return target, l.errorAt(current, "%s is not a named local variable", current.Name)
		}
		valueType, err := strictIRType(variable.Type())
		if err != nil {
			return target, l.errorAt(current, "variable %s: %v", current.Name, err)
		}
		target.valueType = valueType
		_, _, array := valueType.ArrayElement()
		if array || l.addressTaken[variable] {
			memoryObject, exists := l.memoryVariables[variable]
			if !exists {
				if !defining {
					return target, l.errorAt(current, "variable %s has not been declared", current.Name)
				}
				memoryObject, err = l.newMemoryVariable(variable, current.Name, valueType)
				if err != nil {
					return target, l.errorAt(current, "variable %s: %v", current.Name, err)
				}
				l.memoryVariables[variable] = memoryObject
				target.fresh = true
			}
			target.object = memoryObject
			if !array {
				target.address = l.emitObjectAddress(memoryObject)
			}
			return target, nil
		}
		id, exists := l.variables[variable]
		if !exists {
			if !defining {
				return target, l.errorAt(current, "variable %s has not been declared", current.Name)
			}
			id = l.newValue(current.Name, valueType, ValueLocal)
			l.variables[variable] = id
			target.fresh = true
		}
		target.direct = id
		return target, nil
	case *ast.IndexExpr:
		if defining {
			return target, l.errorAt(current, "short declaration requires named variables")
		}
		address, element, err := l.lowerArrayElementAddress(current)
		if err != nil {
			return target, err
		}
		target.address, target.valueType = address, element
		return target, nil
	case *ast.StarExpr:
		if defining {
			return target, l.errorAt(current, "short declaration requires named variables")
		}
		address, element, err := l.lowerPointerDereference(current)
		if err != nil {
			return target, err
		}
		target.address, target.valueType = address, element
		return target, nil
	default:
		return target, l.errorAt(expression, "assignment target must be a local variable, fixed-array element, or pointer dereference")
	}
}

func (l *functionLowerer) readAssignmentTarget(target assignmentTarget) (ValueID, error) {
	if target.direct != NoValue {
		return target.direct, nil
	}
	if target.address != NoValue {
		return l.emitLoad(target.address, target.valueType, token.NoPos), nil
	}
	return NoValue, fmt.Errorf("lower: fixed arrays are not scalar assignment targets")
}

func (l *functionLowerer) writeAssignmentTarget(target assignmentTarget, value ValueID, position token.Pos) error {
	if target.direct != NoValue {
		l.emit(Instruction{Op: OpMove, Dest: target.direct, X: value, Pos: l.position(position)})
		return nil
	}
	if target.address != NoValue {
		l.emit(Instruction{Op: OpStore, Dest: NoValue, X: target.address, Y: value,
			Memory: memoryTypeFor(target.valueType), Pos: l.position(position)})
		return nil
	}
	return l.errorAt(nil, "fixed arrays cannot be assigned as register values")
}

func (l *functionLowerer) newMemoryVariable(variable *types.Var, name string, valueType Type) (MemoryObjectID, error) {
	element, length, array := valueType.ArrayElement()
	if !array {
		element, length = valueType, 1
	}
	if !element.memoryScalar() || length == 0 {
		return NoMemoryObject, fmt.Errorf("type %s is not addressable in explicit sBPF memory", valueType)
	}
	id := MemoryObjectID(len(l.function.Memory))
	l.function.Memory = append(l.function.Memory, MemoryObject{ID: id, Name: name, Element: element, Length: length})
	_ = variable
	return id, nil
}

func (l *functionLowerer) emitObjectAddress(objectID MemoryObjectID) ValueID {
	object, _ := l.function.MemoryObject(objectID)
	destination := l.newTemporary(PointerTo(object.Element))
	l.emit(Instruction{Op: OpAddress, Dest: destination, Object: objectID})
	return destination
}

func (l *functionLowerer) emitLoad(address ValueID, valueType Type, position token.Pos) ValueID {
	destination := l.newTemporary(valueType)
	l.emit(Instruction{Op: OpLoad, Dest: destination, X: address, Memory: memoryTypeFor(valueType), Pos: l.position(position)})
	return destination
}

func (l *functionLowerer) lowerPointerDereference(expression *ast.StarExpr) (ValueID, Type, error) {
	pointerType, err := strictIRType(l.checked.Info.TypeOf(expression.X))
	if err != nil {
		return NoValue, TypeInvalid, l.errorAt(expression, "pointer expression: %v", err)
	}
	element, ok := pointerType.PointerElement()
	if !ok {
		return NoValue, TypeInvalid, l.errorAt(expression, "cannot dereference non-pointer type %s", pointerType)
	}
	address, err := l.lowerExpression(expression.X, pointerType)
	return address, element, err
}

func (l *functionLowerer) lowerAddressExpression(expression ast.Expr) (ValueID, Type, error) {
	switch current := expression.(type) {
	case *ast.Ident:
		variable, ok := l.checked.Info.Uses[current].(*types.Var)
		if !ok {
			return NoValue, TypeInvalid, l.errorAt(current, "address operand must be a local variable")
		}
		objectID, exists := l.memoryVariables[variable]
		if !exists {
			return NoValue, TypeInvalid, l.errorAt(current, "variable %s was not allocated in sBPF memory", current.Name)
		}
		valueType, err := strictIRType(variable.Type())
		if err != nil {
			return NoValue, TypeInvalid, err
		}
		if _, _, array := valueType.ArrayElement(); array {
			return NoValue, TypeInvalid, l.errorAt(current, "take the address of an array element, not the whole fixed array")
		}
		return l.emitObjectAddress(objectID), valueType, nil
	case *ast.IndexExpr:
		return l.lowerArrayElementAddress(current)
	case *ast.StarExpr:
		return l.lowerPointerDereference(current)
	default:
		return NoValue, TypeInvalid, l.errorAt(expression, "address operand must be a local variable, array element, or pointer dereference")
	}
}

func (l *functionLowerer) lowerArrayElementAddress(expression *ast.IndexExpr) (ValueID, Type, error) {
	identifier, ok := unparenthesized(expression.X).(*ast.Ident)
	if !ok {
		return NoValue, TypeInvalid, l.errorAt(expression.X, "only named fixed arrays may be indexed")
	}
	variable, ok := l.checked.Info.Uses[identifier].(*types.Var)
	if !ok {
		return NoValue, TypeInvalid, l.errorAt(identifier, "%s is not a fixed array", identifier.Name)
	}
	arrayType, err := strictIRType(variable.Type())
	if err != nil {
		return NoValue, TypeInvalid, l.errorAt(identifier, "%v", err)
	}
	element, length, array := arrayType.ArrayElement()
	if !array {
		return NoValue, TypeInvalid, l.errorAt(identifier, "%s has type %s, not a fixed array", identifier.Name, arrayType)
	}
	objectID, exists := l.memoryVariables[variable]
	if !exists {
		return NoValue, TypeInvalid, l.errorAt(identifier, "fixed array %s is not available in this function", identifier.Name)
	}
	indexType, err := l.effectiveExpressionType(expression.Index, TypeUint64)
	if err != nil || !indexType.integer() {
		return NoValue, TypeInvalid, l.errorAt(expression.Index, "array index must use uint64, int64, uint32, int32, or uint8")
	}
	index, err := l.lowerExpression(expression.Index, indexType)
	if err != nil {
		return NoValue, TypeInvalid, err
	}
	l.emit(Instruction{Op: OpBoundsCheck, Dest: NoValue, X: index, Imm: uint64(length), Pos: l.position(expression.Index.Pos())})
	base := l.emitObjectAddress(objectID)
	address := l.newTemporary(PointerTo(element))
	l.emit(Instruction{Op: OpPointerAdd, Dest: address, X: base, Y: index, Scale: element.memoryBytes(), Pos: l.position(expression.Pos())})
	return address, element, nil
}

func unparenthesized(expression ast.Expr) ast.Expr {
	for {
		parentheses, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parentheses.X
	}
}

func (l *functionLowerer) zeroMemoryObject(objectID MemoryObjectID, position token.Pos) error {
	if _, ok := l.function.MemoryObject(objectID); !ok {
		return fmt.Errorf("lower: invalid memory object %d", objectID)
	}
	l.emit(Instruction{Op: OpZeroMemory, Dest: NoValue, Object: objectID, Pos: l.position(position)})
	return nil
}

func (l *functionLowerer) initializeArray(objectID MemoryObjectID, expression ast.Expr, fresh bool) error {
	object, ok := l.function.MemoryObject(objectID)
	if !ok {
		return fmt.Errorf("lower: invalid memory object %d", objectID)
	}
	if identifier, identifierOK := unparenthesized(expression).(*ast.Ident); identifierOK {
		variable, variableOK := l.checked.Info.Uses[identifier].(*types.Var)
		if !variableOK {
			return l.errorAt(identifier, "%s is not a fixed array", identifier.Name)
		}
		sourceID, sourceOK := l.memoryVariables[variable]
		source, sourceObjectOK := l.function.MemoryObject(sourceID)
		if !sourceOK || !sourceObjectOK || source.Element != object.Element || source.Length != object.Length {
			return l.errorAt(identifier, "array %s does not match [%d]%s", identifier.Name, object.Length, object.Element)
		}
		l.emit(Instruction{Op: OpCopyMemory, Dest: NoValue, Object: objectID, SourceObject: sourceID, Pos: l.position(expression.Pos())})
		return nil
	}
	composite, ok := unparenthesized(expression).(*ast.CompositeLit)
	if !ok {
		return l.errorAt(expression, "fixed arrays currently require a fixed-array composite literal initializer")
	}
	arrayType, err := strictIRType(l.checked.Info.TypeOf(composite))
	if err != nil {
		return l.errorAt(composite, "%v", err)
	}
	element, length, array := arrayType.ArrayElement()
	if !array || element != object.Element || length != object.Length {
		return l.errorAt(composite, "array initializer type %s does not match [%d]%s", arrayType, object.Length, object.Element)
	}
	initializationObjectID := objectID
	if !fresh {
		initializationObjectID, err = l.newMemoryVariable(nil, "$array.literal", arrayType)
		if err != nil {
			return l.errorAt(composite, "array literal temporary: %v", err)
		}
	}
	if err := l.zeroMemoryObject(initializationObjectID, composite.Pos()); err != nil {
		return err
	}
	next := uint64(0)
	for _, item := range composite.Elts {
		valueExpression := item
		index := next
		if keyed, keyedOK := item.(*ast.KeyValueExpr); keyedOK {
			constantValue := l.checked.Info.Types[keyed.Key].Value
			if constantValue == nil {
				return l.errorAt(keyed.Key, "array initializer index must be constant")
			}
			resolved, exact := constant.Uint64Val(constant.ToInt(constantValue))
			if !exact {
				return l.errorAt(keyed.Key, "array initializer index is invalid")
			}
			index = resolved
			valueExpression = keyed.Value
		}
		if index >= uint64(object.Length) {
			return l.errorAt(item, "array initializer index %d is outside [0,%d)", index, object.Length)
		}
		address := l.emitConstantElementAddress(initializationObjectID, uint32(index), item.Pos())
		value, valueErr := l.lowerExpression(valueExpression, object.Element)
		if valueErr != nil {
			return valueErr
		}
		l.emit(Instruction{Op: OpStore, Dest: NoValue, X: address, Y: value,
			Memory: memoryTypeFor(object.Element), Pos: l.position(item.Pos())})
		next = index + 1
	}
	if initializationObjectID != objectID {
		l.emit(Instruction{Op: OpCopyMemory, Dest: NoValue, Object: objectID,
			SourceObject: initializationObjectID, Pos: l.position(composite.Pos())})
	}
	return nil
}

func (l *functionLowerer) emitConstantElementAddress(objectID MemoryObjectID, index uint32, position token.Pos) ValueID {
	object, _ := l.function.MemoryObject(objectID)
	base := l.emitObjectAddress(objectID)
	indexValue := l.newTemporary(TypeUint64)
	l.emit(Instruction{Op: OpConst, Dest: indexValue, Imm: uint64(index), Pos: l.position(position)})
	address := l.newTemporary(PointerTo(object.Element))
	l.emit(Instruction{Op: OpPointerAdd, Dest: address, X: base, Y: indexValue, Scale: object.Element.memoryBytes(), Pos: l.position(position)})
	return address
}

func (l *functionLowerer) variableForIdentifier(identifier *ast.Ident, defining bool) (ValueID, error) {
	var object types.Object
	if defining {
		object = l.checked.Info.Defs[identifier]
	}
	if object == nil {
		object = l.checked.Info.Uses[identifier]
	}
	variable, ok := object.(*types.Var)
	if !ok || identifier.Name == "_" {
		return NoValue, l.errorAt(identifier, "%s is not a named local variable", identifier.Name)
	}
	if id, exists := l.variables[variable]; exists {
		return id, nil
	}
	if !defining {
		return NoValue, l.errorAt(identifier, "variable %s has not been declared", identifier.Name)
	}
	variableType, err := strictIRType(variable.Type())
	if err != nil {
		return NoValue, l.errorAt(identifier, "variable %s: %v", identifier.Name, err)
	}
	id := l.newValue(identifier.Name, variableType, ValueLocal)
	l.variables[variable] = id
	return id, nil
}

func (l *functionLowerer) newValue(name string, valueType Type, kind ValueKind) ValueID {
	id := ValueID(len(l.function.Values))
	l.function.Values = append(l.function.Values, Value{ID: id, Name: name, Type: valueType, Kind: kind})
	return id
}

func (l *functionLowerer) newTemporary(valueType Type) ValueID {
	l.temporary++
	return l.newValue(fmt.Sprintf("t%d", l.temporary), valueType, ValueTemporary)
}

func (l *functionLowerer) newBlock(name string) *BasicBlock {
	id := BlockID(len(l.function.Blocks))
	block := &BasicBlock{ID: id, Name: fmt.Sprintf("%s.%d", name, id)}
	l.function.Blocks = append(l.function.Blocks, block)
	return block
}

func (l *functionLowerer) emit(instruction Instruction) {
	l.current.Instructions = append(l.current.Instructions, instruction)
}

func (l *functionLowerer) position(position token.Pos) SourcePosition {
	return sourcePosition(l.checked.Parsed.FileSet, position)
}

func (l *functionLowerer) errorAt(node ast.Node, format string, arguments ...any) error {
	return lowerDiagnostic(l.checked, node, format, arguments...)
}

func lowerDiagnostic(checked *CheckedPackage, node ast.Node, format string, arguments ...any) error {
	position := SourcePosition{}
	if checked != nil && checked.Parsed != nil && node != nil {
		position = sourcePosition(checked.Parsed.FileSet, node.Pos())
	}
	return &DiagnosticError{Diagnostics: []Diagnostic{{
		Phase:    "lower",
		Position: position,
		Message:  fmt.Sprintf(format, arguments...),
	}}}
}

func mapBinaryOp(operator token.Token) Op {
	switch operator {
	case token.ADD:
		return OpAdd
	case token.SUB:
		return OpSub
	case token.MUL:
		return OpMul
	case token.QUO:
		return OpDiv
	case token.REM:
		return OpMod
	case token.EQL:
		return OpEqual
	case token.NEQ:
		return OpNotEqual
	case token.LSS:
		return OpLess
	case token.LEQ:
		return OpLessEqual
	case token.GTR:
		return OpGreater
	case token.GEQ:
		return OpGreaterEqual
	default:
		return OpInvalid
	}
}

func mapAssignmentOp(operator token.Token) Op {
	switch operator {
	case token.ADD_ASSIGN:
		return OpAdd
	case token.SUB_ASSIGN:
		return OpSub
	case token.MUL_ASSIGN:
		return OpMul
	case token.QUO_ASSIGN:
		return OpDiv
	case token.REM_ASSIGN:
		return OpMod
	default:
		return OpInvalid
	}
}
