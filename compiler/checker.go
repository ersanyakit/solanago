package compiler

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

// CheckedPackage is the type-checked frontend input to Lower.
type CheckedPackage struct {
	Parsed       *ParsedPackage
	Package      *types.Package
	Info         *types.Info
	Intrinsics   map[*types.Func]memoryIntrinsic
	Syscalls     map[*types.Func]syscallIntrinsic
	AccountField map[*types.Func]accountFieldIntrinsic
}

type memoryIntrinsic struct {
	Memory  MemoryType
	Store   bool
	Address bool
}

var memoryIntrinsics = map[string]memoryIntrinsic{
	"LoadUint8": {Memory: MemoryUint8}, "StoreUint8": {Memory: MemoryUint8, Store: true},
	"LoadUint16": {Memory: MemoryUint16}, "StoreUint16": {Memory: MemoryUint16, Store: true},
	"LoadUint32": {Memory: MemoryUint32}, "StoreUint32": {Memory: MemoryUint32, Store: true},
	"LoadInt32": {Memory: MemoryInt32}, "StoreInt32": {Memory: MemoryInt32, Store: true},
	"LoadUint64": {Memory: MemoryUint64}, "StoreUint64": {Memory: MemoryUint64, Store: true},
	"LoadInt64": {Memory: MemoryInt64}, "StoreInt64": {Memory: MemoryInt64, Store: true},
	"LoadBool": {Memory: MemoryBool}, "StoreBool": {Memory: MemoryBool, Store: true},
	"AddressUint64": {Memory: MemoryUint64, Address: true},
}

// accountFieldIntrinsic describes one flat, fixed-offset accessor into an
// Agave ABIv1 account record (see runtime/abi.go for the pinned layout this
// mirrors: duplicate marker at 0, is_signer at 1, is_writable at 2,
// executable at 3, data_len at 80, key/owner/lamports/data at 8/40/72/88).
// Address==true returns record+Offset as a plain uint64 (like AddressUint64,
// but arithmetic only — no stack-pointer provenance, since these addresses
// point into the serialized input region, not a guest stack frame); if
// false, the intrinsic loads Memory's value at record+Offset (like the
// LoadX family, but with the offset baked in instead of hand-written).
type accountFieldIntrinsic struct {
	Offset  uint64
	Memory  MemoryType
	Address bool
}

var accountFieldIntrinsics = map[string]accountFieldIntrinsic{
	"AccountIsSigner":        {Offset: 1, Memory: MemoryBool},
	"AccountIsWritable":      {Offset: 2, Memory: MemoryBool},
	"AccountIsExecutable":    {Offset: 3, Memory: MemoryBool},
	"AccountKeyAddress":      {Offset: 8, Address: true},
	"AccountOwnerAddress":    {Offset: 40, Address: true},
	"AccountLamportsAddress": {Offset: 72, Address: true},
	"AccountDataLen":         {Offset: 80, Memory: MemoryUint64},
	"AccountDataAddress":     {Offset: 88, Address: true},
}

// AddressUint64 is a low-level primitive for constructing C ABI records in the
// current sBPF stack frame. It exposes only a guest virtual address (never a
// Go/host pointer). IR validation tracks that address through arithmetic,
// memory, and internal calls: synchronous Load/Store/syscall consumption is
// supported, while returning it or retaining it outside its owning frame is
// rejected. The VM additionally validates each access against mapped regions.

// Check validates the deliberately small language before returning go/types
// information. Syntax outside the subset is rejected even when the Go type
// checker itself would accept it.
func Check(parsed *ParsedPackage) (*CheckedPackage, error) {
	if parsed == nil || len(parsed.Files) == 0 || parsed.FileSet == nil {
		return nil, fmt.Errorf("check: nil or incomplete parsed package")
	}
	if diagnostics := validateSyntax(parsed); len(diagnostics) != 0 {
		return nil, diagnosticsError(diagnostics)
	}

	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Scopes:     make(map[ast.Node]*types.Scope),
		Implicits:  make(map[ast.Node]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	var diagnostics []Diagnostic
	configuration := types.Config{
		Error: func(err error) {
			position := SourcePosition{}
			message := err.Error()
			if typed, ok := err.(types.Error); ok {
				position = sourcePosition(parsed.FileSet, typed.Pos)
				message = typed.Msg
			}
			diagnostics = append(diagnostics, Diagnostic{Phase: "typecheck", Position: position, Message: message})
		},
	}
	checked, err := configuration.Check(parsed.Files[0].Name.Name, parsed.FileSet, parsed.Files, info)
	if len(diagnostics) != 0 {
		return nil, diagnosticsError(diagnostics)
	}
	if err != nil {
		return nil, fmt.Errorf("typecheck: %w", err)
	}
	result := &CheckedPackage{
		Parsed: parsed, Package: checked, Info: info,
		Intrinsics:   make(map[*types.Func]memoryIntrinsic),
		Syscalls:     make(map[*types.Func]syscallIntrinsic),
		AccountField: make(map[*types.Func]accountFieldIntrinsic),
	}
	for _, file := range parsed.Files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body != nil {
				continue
			}
			if intrinsic, ok := memoryIntrinsics[function.Name.Name]; ok {
				if object, ok := info.Defs[function.Name].(*types.Func); ok {
					result.Intrinsics[object] = intrinsic
				}
			}
			if intrinsic, ok := syscallIntrinsics[function.Name.Name]; ok {
				if object, ok := info.Defs[function.Name].(*types.Func); ok {
					result.Syscalls[object] = intrinsic
				}
			}
			if intrinsic, ok := accountFieldIntrinsics[function.Name.Name]; ok {
				if object, ok := info.Defs[function.Name].(*types.Func); ok {
					result.AccountField[object] = intrinsic
				}
			}
		}
	}
	if diagnostics = validateTypes(result); len(diagnostics) != 0 {
		return nil, diagnosticsError(diagnostics)
	}
	return result, nil
}

func validateSyntax(parsed *ParsedPackage) []Diagnostic {
	var diagnostics []Diagnostic
	add := func(node ast.Node, format string, arguments ...any) {
		position := token.NoPos
		if node != nil {
			position = node.Pos()
		}
		diagnostics = append(diagnostics, Diagnostic{
			Phase:    "check",
			Position: sourcePosition(parsed.FileSet, position),
			Message:  fmt.Sprintf(format, arguments...),
		})
	}
	for _, file := range parsed.Files {
		for _, importSpec := range file.Imports {
			add(importSpec, "imports and the standard library are not supported")
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				add(declaration, "package-level declarations are not supported; only functions are allowed")
				continue
			}
			if function.Recv != nil {
				add(function.Recv, "methods are not supported")
			}
			if function.Type.TypeParams != nil {
				add(function.Type.TypeParams, "generic functions are not supported")
			}
			if function.Body == nil {
				_, memoryIntrinsic := memoryIntrinsics[function.Name.Name]
				_, syscallIntrinsic := syscallIntrinsics[function.Name.Name]
				_, accountFieldIntrinsic := accountFieldIntrinsics[function.Name.Name]
				if !memoryIntrinsic && !syscallIntrinsic && !accountFieldIntrinsic {
					add(function, "functions without a body are not supported; only explicit guest-memory, Solana syscall, and account-field intrinsics may omit a body")
				}
				continue
			}
			if _, reserved := syscallIntrinsics[function.Name.Name]; reserved {
				add(function, "Solana syscall intrinsic name %s is reserved and must be a bodyless declaration with its exact signature", function.Name.Name)
			}
			if intrinsic, reserved := memoryIntrinsics[function.Name.Name]; reserved && intrinsic.Address {
				add(function, "guest-address intrinsic name %s is reserved and must be a bodyless declaration with its exact signature", function.Name.Name)
			}
			if _, reserved := accountFieldIntrinsics[function.Name.Name]; reserved {
				add(function, "account-field intrinsic name %s is reserved and must be a bodyless declaration with its exact signature", function.Name.Name)
			}
			if results := function.Type.Results; results != nil {
				resultCount := 0
				for _, field := range results.List {
					if len(field.Names) != 0 {
						add(field, "named result parameters are not supported; return an explicit value")
					}
					if len(field.Names) == 0 {
						resultCount++
					} else {
						resultCount += len(field.Names)
					}
				}
				if resultCount > 1 {
					add(results, "multiple return values are not supported")
				}
			}

			ast.Inspect(function.Body, func(node ast.Node) bool {
				if node == nil {
					return true
				}
				switch current := node.(type) {
				case *ast.BlockStmt, *ast.EmptyStmt, *ast.ReturnStmt, *ast.IfStmt, *ast.ForStmt,
					*ast.ParenExpr, *ast.Ident, *ast.ValueSpec, *ast.IndexExpr, *ast.CompositeLit,
					*ast.KeyValueExpr, *ast.StarExpr:
					return true
				case *ast.AssignStmt:
					switch current.Tok {
					case token.ASSIGN, token.DEFINE, token.ADD_ASSIGN, token.SUB_ASSIGN, token.MUL_ASSIGN, token.QUO_ASSIGN, token.REM_ASSIGN:
					default:
						add(current, "assignment operator %s is not supported", current.Tok)
					}
					for _, left := range current.Lhs {
						switch target := left.(type) {
						case *ast.Ident:
							if target.Name == "_" {
								add(left, "blank assignment targets are not supported")
							}
						case *ast.IndexExpr, *ast.StarExpr:
						default:
							add(left, "assignment target must be a local variable, fixed-array element, or pointer dereference")
						}
					}
					return true
				case *ast.DeclStmt:
					return true
				case *ast.GenDecl:
					if current.Tok != token.VAR {
						add(current, "%s declarations are not supported; use local var declarations", current.Tok)
						return false
					}
					return true
				case *ast.IncDecStmt:
					switch current.X.(type) {
					case *ast.Ident, *ast.IndexExpr, *ast.StarExpr:
					default:
						add(current.X, "increment and decrement require an assignable integer")
					}
					return true
				case *ast.ExprStmt:
					if _, ok := current.X.(*ast.CallExpr); !ok {
						add(current, "only function calls may be used as expression statements")
					}
					return true
				case *ast.BasicLit:
					if current.Kind != token.INT {
						add(current, "%s literals are not supported", current.Kind)
					}
					return true
				case *ast.BinaryExpr:
					switch current.Op {
					case token.ADD, token.SUB, token.MUL, token.QUO, token.REM,
						token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
					default:
						add(current, "binary operator %s is not supported", current.Op)
					}
					return true
				case *ast.UnaryExpr:
					if current.Op != token.ADD && current.Op != token.SUB && current.Op != token.AND {
						add(current, "unary operator %s is not supported", current.Op)
					}
					return true
				case *ast.CallExpr:
					if current.Ellipsis.IsValid() {
						add(current, "variadic calls are not supported")
					}
					return true
				case *ast.BranchStmt:
					add(current, "%s statements are not supported", current.Tok)
					return false
				case *ast.GoStmt:
					add(current, "goroutines are not supported")
					return false
				case *ast.DeferStmt:
					add(current, "defer is not supported")
					return false
				case *ast.SendStmt:
					add(current, "channel sends are not supported")
					return false
				case *ast.RangeStmt:
					add(current, "range loops are not supported; use a three-clause for loop")
					return false
				case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
					add(current, "%T control flow is not supported", current)
					return false
				case *ast.LabeledStmt:
					add(current, "labels are not supported")
					return false
				case *ast.FuncLit:
					add(current, "closures are not supported")
					return false
				case *ast.SelectorExpr:
					add(current, "selectors, imported packages, and methods are not supported")
					return false
				case *ast.IndexListExpr, *ast.SliceExpr:
					add(current, "generic indexing and slices are not supported")
					return false
				case *ast.TypeAssertExpr:
					add(current, "interfaces and type assertions are not supported")
					return false
				case *ast.ArrayType:
					if current.Len == nil {
						add(current, "slices are not supported; use a fixed-length array")
					}
					return true
				case *ast.MapType:
					add(current, "maps are not supported")
					return false
				case *ast.ChanType:
					add(current, "channels are not supported")
					return false
				case *ast.InterfaceType:
					add(current, "interfaces are not supported")
					return false
				case *ast.StructType:
					add(current, "structs are not supported")
					return false
				case *ast.BadExpr, *ast.BadStmt, *ast.Ellipsis, *ast.FuncType:
					add(current, "syntax node %T is not supported", current)
					return false
				default:
					add(current, "syntax node %T is not supported", current)
					return false
				}
			})
		}
	}
	return diagnostics
}

func validateTypes(checked *CheckedPackage) []Diagnostic {
	var diagnostics []Diagnostic
	add := func(node ast.Node, format string, arguments ...any) {
		diagnostics = append(diagnostics, Diagnostic{
			Phase:    "check",
			Position: sourcePosition(checked.Parsed.FileSet, node.Pos()),
			Message:  fmt.Sprintf(format, arguments...),
		})
	}
	for _, file := range checked.Parsed.Files {
		for _, declaration := range file.Decls {
			function := declaration.(*ast.FuncDecl) // validateSyntax guarantees this.
			object, ok := checked.Info.Defs[function.Name].(*types.Func)
			if !ok {
				add(function.Name, "cannot resolve function declaration")
				continue
			}
			signature, ok := object.Type().(*types.Signature)
			if !ok {
				add(function.Name, "function has an invalid type")
				continue
			}
			if function.Body == nil {
				if intrinsic, exists := checked.Intrinsics[object]; exists {
					validateMemoryIntrinsicSignature(function, signature, intrinsic, add)
					continue
				}
				if intrinsic, exists := checked.Syscalls[object]; exists {
					validateSyscallIntrinsicSignature(function, signature, intrinsic, add)
					continue
				}
				if intrinsic, exists := checked.AccountField[object]; exists {
					validateAccountFieldIntrinsicSignature(function, signature, intrinsic, add)
					continue
				}
				add(function, "invalid bodyless function declaration")
				continue
			}
			if signature.Params().Len() > 5 {
				add(function.Type.Params, "function %s has %d parameters; sBPF calls support at most 5", function.Name.Name, signature.Params().Len())
			}
			for index := 0; index < signature.Params().Len(); index++ {
				parameter := signature.Params().At(index)
				parameterType, err := strictIRType(parameter.Type())
				if err != nil {
					add(function.Type.Params, "parameter %s: %v", parameter.Name(), err)
				} else if _, _, array := parameterType.ArrayElement(); array {
					add(function.Type.Params, "parameter %s: fixed arrays cannot cross the sBPF register ABI; pass a pointer to an element", parameter.Name())
				}
			}
			if signature.Results().Len() > 1 {
				add(function.Type.Results, "multiple return values are not supported")
			} else if signature.Results().Len() == 1 {
				resultType, err := strictIRType(signature.Results().At(0).Type())
				if err != nil {
					add(function.Type.Results, "result: %v", err)
				} else if _, pointer := resultType.PointerElement(); pointer {
					add(function.Type.Results, "pointer results are not supported; sBPF stack addresses must not escape a call frame")
				} else if _, _, array := resultType.ArrayElement(); array {
					add(function.Type.Results, "fixed-array results cannot cross the sBPF register ABI")
				}
			}

			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch current := node.(type) {
				case *ast.CallExpr:
					validateCall(checked, current, add)
				case *ast.Ident:
					if variable, ok := checked.Info.Defs[current].(*types.Var); ok {
						if _, err := strictIRType(variable.Type()); err != nil {
							add(current, "variable %s: %v", current.Name, err)
						}
					}
				}
				return true
			})
		}
	}
	return diagnostics
}

func validateSyscallIntrinsicSignature(declaration *ast.FuncDecl, signature *types.Signature, intrinsic syscallIntrinsic, add func(ast.Node, string, ...any)) {
	valid := signature.Params().Len() == int(intrinsic.ParamCount)
	if valid {
		for index, want := range intrinsic.parameterTypes() {
			got, err := strictIRType(signature.Params().At(index).Type())
			if err != nil || got != want {
				valid = false
				break
			}
		}
	}
	if valid && intrinsic.Result == TypeVoid {
		valid = signature.Results().Len() == 0
	} else if valid {
		if signature.Results().Len() != 1 {
			valid = false
		} else {
			got, err := strictIRType(signature.Results().At(0).Type())
			valid = err == nil && got == intrinsic.Result
		}
	}
	if !valid {
		add(declaration, "%s intrinsic for %s must have signature %s", declaration.Name.Name, intrinsic.Symbol, intrinsic.signature(declaration.Name.Name))
	}
}

func validateMemoryIntrinsicSignature(declaration *ast.FuncDecl, signature *types.Signature, intrinsic memoryIntrinsic, add func(ast.Node, string, ...any)) {
	wantValue := intrinsic.Memory.ValueType()
	if intrinsic.Address {
		valid := signature.Params().Len() == 1 && signature.Results().Len() == 1
		if valid {
			parameterType, parameterErr := strictIRType(signature.Params().At(0).Type())
			resultType, resultErr := strictIRType(signature.Results().At(0).Type())
			valid = parameterErr == nil && resultErr == nil && parameterType == PointerTo(wantValue) && resultType == TypeUint64
		}
		if !valid {
			add(declaration, "%s intrinsic must have signature func(pointer *%s) uint64", declaration.Name.Name, wantValue)
		}
		return
	}
	wantParams := 1
	if intrinsic.Store {
		wantParams = 2
	}
	valid := signature.Params().Len() == wantParams
	if valid {
		addressType, err := strictIRType(signature.Params().At(0).Type())
		valid = err == nil && addressType == TypeUint64
	}
	if valid && intrinsic.Store {
		valueType, err := strictIRType(signature.Params().At(1).Type())
		valid = err == nil && valueType == wantValue && signature.Results().Len() == 0
	} else if valid {
		if signature.Results().Len() != 1 {
			valid = false
		} else {
			resultType, err := strictIRType(signature.Results().At(0).Type())
			valid = err == nil && resultType == wantValue
		}
	}
	if !valid {
		if intrinsic.Store {
			add(declaration, "%s intrinsic must have signature func(address uint64, value %s)", declaration.Name.Name, wantValue)
		} else {
			add(declaration, "%s intrinsic must have signature func(address uint64) %s", declaration.Name.Name, wantValue)
		}
	}
}

func validateAccountFieldIntrinsicSignature(declaration *ast.FuncDecl, signature *types.Signature, intrinsic accountFieldIntrinsic, add func(ast.Node, string, ...any)) {
	wantResult := TypeUint64
	if !intrinsic.Address {
		wantResult = intrinsic.Memory.ValueType()
	}
	valid := signature.Params().Len() == 1 && signature.Results().Len() == 1
	if valid {
		parameterType, parameterErr := strictIRType(signature.Params().At(0).Type())
		resultType, resultErr := strictIRType(signature.Results().At(0).Type())
		valid = parameterErr == nil && resultErr == nil && parameterType == TypeUint64 && resultType == wantResult
	}
	if !valid {
		add(declaration, "%s intrinsic must have signature func(record uint64) %s", declaration.Name.Name, wantResult)
	}
}

func validateCall(checked *CheckedPackage, call *ast.CallExpr, add func(ast.Node, string, ...any)) {
	identifier, ok := call.Fun.(*ast.Ident)
	if !ok {
		add(call.Fun, "call target must be an internal function name or scalar type conversion")
		return
	}
	switch object := checked.Info.Uses[identifier].(type) {
	case *types.Func:
		if object.Pkg() != checked.Package {
			add(identifier, "calls to external functions are not supported")
			return
		}
		if len(call.Args) > 5 {
			add(call, "call to %s has %d arguments; sBPF calls support at most 5", identifier.Name, len(call.Args))
		}
	case *types.TypeName:
		if identifier.Name != "uint64" && identifier.Name != "int64" && identifier.Name != "uint32" && identifier.Name != "int32" && identifier.Name != "uint8" {
			add(identifier, "only uint64, int64, uint32, int32, and uint8 scalar conversions are supported")
		}
		if len(call.Args) != 1 {
			add(call, "scalar conversion requires exactly one argument")
		}
		if _, err := strictIRType(object.Type()); err != nil {
			add(identifier, "conversion target: %v", err)
		}
	case *types.Builtin:
		add(identifier, "builtin function %s is not supported", object.Name())
	default:
		add(identifier, "call target %s is not a supported internal function", identifier.Name)
	}
}

func strictIRType(goType types.Type) (Type, error) {
	switch underlying := goType.Underlying().(type) {
	case *types.Basic:
		switch underlying.Kind() {
		case types.Uint64:
			return TypeUint64, nil
		case types.Int64:
			return TypeInt64, nil
		case types.Uint32:
			return TypeUint32, nil
		case types.Int32:
			return TypeInt32, nil
		case types.Uint8:
			return TypeUint8, nil
		case types.Bool:
			return TypeBool, nil
		default:
			return TypeInvalid, fmt.Errorf("type %s is not supported; use uint64, int64, uint32, int32, uint8, bool, pointers, or fixed arrays", goType)
		}
	case *types.Pointer:
		element, err := strictIRType(underlying.Elem())
		if err != nil || !element.memoryScalar() {
			return TypeInvalid, fmt.Errorf("pointer element type %s is not a supported explicit memory type", underlying.Elem())
		}
		return PointerTo(element), nil
	case *types.Array:
		element, err := strictIRType(underlying.Elem())
		if err != nil || !element.memoryScalar() {
			return TypeInvalid, fmt.Errorf("array element type %s is not a supported explicit memory type", underlying.Elem())
		}
		if underlying.Len() <= 0 || underlying.Len() > 0x000fffff {
			return TypeInvalid, fmt.Errorf("array length %d is outside the supported range", underlying.Len())
		}
		array := ArrayOf(element, uint32(underlying.Len()))
		if uint64(element.memoryBytes())*uint64(underlying.Len()) > stackFrameSize {
			return TypeInvalid, fmt.Errorf("array %s exceeds the %d-byte sBPF frame", goType, stackFrameSize)
		}
		return array, nil
	default:
		return TypeInvalid, fmt.Errorf("type %s is not supported; use explicit fixed-width scalars, pointers, or fixed arrays", goType)
	}
}

func sourcePosition(fileSet *token.FileSet, position token.Pos) SourcePosition {
	if fileSet == nil || !position.IsValid() {
		return SourcePosition{}
	}
	resolved := fileSet.Position(position)
	return SourcePosition{Filename: resolved.Filename, Line: resolved.Line, Column: resolved.Column}
}
