package compiler

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCompileAddGoldenIR(t *testing.T) {
	program := compileTestSource(t, `package main
func Add(a uint64, b uint64) uint64 { return a + b }
`)
	function, ok := program.Function("Add")
	if !ok {
		t.Fatal("Add function not found")
	}
	if got, want := len(function.Params), 2; got != want {
		t.Fatalf("parameter count = %d, want %d", got, want)
	}
	for index, parameterID := range function.Params {
		parameter, ok := function.Value(parameterID)
		if !ok || parameter.Type != TypeUint64 || parameter.Kind != ValueParameter {
			t.Fatalf("parameter %d = %+v", index, parameter)
		}
	}
	if got, want := len(function.Blocks), 1; got != want {
		t.Fatalf("block count = %d, want %d", got, want)
	}
	entry := function.Blocks[0]
	if got, want := len(entry.Instructions), 1; got != want {
		t.Fatalf("instruction count = %d, want %d", got, want)
	}
	add := entry.Instructions[0]
	if add.Op != OpAdd || add.X != function.Params[0] || add.Y != function.Params[1] {
		t.Fatalf("add instruction = %+v", add)
	}
	result, ok := function.Value(add.Dest)
	if !ok || result.Type != TypeUint64 || result.Kind != ValueTemporary {
		t.Fatalf("add destination = %+v", result)
	}
	if entry.Terminator.Kind != TermReturn || entry.Terminator.Result != add.Dest {
		t.Fatalf("terminator = %+v", entry.Terminator)
	}
}

func TestArithmeticAndComparisonLowering(t *testing.T) {
	tests := []struct {
		name       string
		resultType string
		expression string
		want       Op
	}{
		{name: "sub", resultType: "uint64", expression: "a - b", want: OpSub},
		{name: "mul", resultType: "uint64", expression: "a * b", want: OpMul},
		{name: "div", resultType: "uint64", expression: "a / b", want: OpDiv},
		{name: "eq", resultType: "bool", expression: "a == b", want: OpEqual},
		{name: "ne", resultType: "bool", expression: "a != b", want: OpNotEqual},
		{name: "lt", resultType: "bool", expression: "a < b", want: OpLess},
		{name: "le", resultType: "bool", expression: "a <= b", want: OpLessEqual},
		{name: "gt", resultType: "bool", expression: "a > b", want: OpGreater},
		{name: "ge", resultType: "bool", expression: "a >= b", want: OpGreaterEqual},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := fmt.Sprintf("package main\nfunc F(a uint64, b uint64) %s { return %s }\n", test.resultType, test.expression)
			function := compileFunction(t, source, "F")
			if got := function.Blocks[0].Instructions[0].Op; got != test.want {
				t.Fatalf("opcode = %s, want %s", got, test.want)
			}
		})
	}
}

func TestIfAndForProduceValidatedCFG(t *testing.T) {
	program := compileTestSource(t, `package main
func Sum(n uint64) uint64 {
	var sum uint64
	for i := uint64(0); i < n; i++ {
		if i == uint64(3) { sum = sum + i } else { sum += i }
	}
	return sum
}
`)
	if err := program.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	function, _ := program.Function("Sum")
	var branches, backEdges int
	for _, block := range function.Blocks {
		switch block.Terminator.Kind {
		case TermBranch:
			branches++
		case TermJump:
			if block.Terminator.Target <= block.ID {
				backEdges++
			}
		}
	}
	if branches < 2 {
		t.Fatalf("branch count = %d, want at least 2", branches)
	}
	if backEdges == 0 {
		t.Fatal("loop CFG has no back edge")
	}
}

func TestInternalCallAndScalarTypes(t *testing.T) {
	program := compileTestSource(t, `package main
func Twice(v int64) int64 { return v * int64(2) }
func NonNegative(v int64) bool {
	if v >= int64(0) { return true }
	return false
}
func Use(v int64) int64 { return Twice(v) }
`)
	use, _ := program.Function("Use")
	instruction := use.Blocks[0].Instructions[0]
	if instruction.Op != OpCall || instruction.Callee != "Twice" || len(instruction.Args) != 1 {
		t.Fatalf("call instruction = %+v", instruction)
	}
	result, _ := use.Value(instruction.Dest)
	if result.Type != TypeInt64 {
		t.Fatalf("call result type = %s, want int64", result.Type)
	}
	nonNegative, _ := program.Function("NonNegative")
	if nonNegative.Result != TypeBool {
		t.Fatalf("NonNegative result = %s", nonNegative.Result)
	}
}

func TestInt64ConstantKeepsTwosComplementBits(t *testing.T) {
	function := compileFunction(t, `package main
func MinusOne() int64 { return int64(-1) }
`, "MinusOne")
	instruction := function.Blocks[0].Instructions[0]
	if instruction.Op != OpConst || instruction.Imm != ^uint64(0) {
		t.Fatalf("constant = %+v, want all one bits", instruction)
	}
}

func TestUnaryShadowingVoidCallAndInfiniteLoop(t *testing.T) {
	program := compileTestSource(t, `package main
func Sink(v uint64) {}
func Neg(v int64) int64 { return -v }
func Shadow(v uint64) uint64 {
	if v > uint64(0) {
		v := uint64(2)
		Sink(v)
	}
	return v
}
func Spin() { for {} }
`)
	neg, _ := program.Function("Neg")
	if instructions := neg.Blocks[0].Instructions; len(instructions) != 2 ||
		instructions[0].Op != OpConst || instructions[0].Imm != 0 || instructions[1].Op != OpSub {
		t.Fatalf("unary negation IR = %+v", instructions)
	}
	shadow, _ := program.Function("Shadow")
	var namedV []Value
	var sawVoidCall bool
	for _, value := range shadow.Values {
		if value.Name == "v" {
			namedV = append(namedV, value)
		}
	}
	for _, block := range shadow.Blocks {
		for _, instruction := range block.Instructions {
			if instruction.Op == OpCall && instruction.Callee == "Sink" {
				sawVoidCall = instruction.Dest == NoValue
			}
		}
	}
	if len(namedV) != 2 || namedV[0].ID == namedV[1].ID {
		t.Fatalf("shadowed values = %+v, want distinct parameter and local", namedV)
	}
	if !sawVoidCall {
		t.Fatal("void Sink call with NoValue destination not found")
	}
	spin, _ := program.Function("Spin")
	if got := len(spin.Blocks); got != 3 {
		t.Fatalf("Spin block count = %d, want entry, condition, and body", got)
	}
	for _, block := range spin.Blocks {
		if block.Terminator.Kind != TermJump {
			t.Fatalf("Spin block %d terminator = %+v, want jump", block.ID, block.Terminator)
		}
	}
}

func TestContextualUntypedConstantsUseDeclaredScalarType(t *testing.T) {
	program := compileTestSource(t, `package main
func Signed() int64 { var v int64 = 1; return v + 2 }
func Unsigned() uint64 { return 3 }
func Boolean() bool { return true }
`)
	for name, want := range map[string]Type{"Signed": TypeInt64, "Unsigned": TypeUint64, "Boolean": TypeBool} {
		function, _ := program.Function(name)
		if function.Result != want {
			t.Fatalf("%s result = %s, want %s", name, function.Result, want)
		}
		for _, value := range function.Values {
			if value.Type != want {
				t.Fatalf("%s value %d type = %s, want %s", name, value.ID, value.Type, want)
			}
		}
	}
}

func TestParallelAssignmentSnapshotsRHS(t *testing.T) {
	function := compileFunction(t, `package main
func Swap(a uint64, b uint64) uint64 {
	a, b = b, a
	return a - b
}
`, "Swap")
	entry := function.Blocks[0]
	if len(entry.Instructions) < 5 {
		t.Fatalf("instructions = %+v", entry.Instructions)
	}
	firstSnapshot := entry.Instructions[0]
	secondSnapshot := entry.Instructions[1]
	if firstSnapshot.Op != OpMove || firstSnapshot.X != function.Params[1] ||
		secondSnapshot.Op != OpMove || secondSnapshot.X != function.Params[0] {
		t.Fatalf("RHS was not snapshotted before writes: %+v", entry.Instructions[:2])
	}
	if entry.Instructions[2].Dest != function.Params[0] || entry.Instructions[3].Dest != function.Params[1] {
		t.Fatalf("parallel assignment destinations = %+v", entry.Instructions[2:4])
	}
}

func TestUnsupportedSyntaxFailsClearly(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		message string
	}{
		{name: "import", source: "package main\nimport \"fmt\"\nfunc F() {}", message: "imports"},
		{name: "string", source: "package main\nfunc F() { var x = \"bad\"; _ = x }", message: "STRING literals"},
		{name: "goroutine", source: "package main\nfunc F() { go F() }", message: "goroutines"},
		{name: "defer", source: "package main\nfunc F() { defer F() }", message: "defer"},
		{name: "map", source: "package main\nfunc F(v map[uint64]uint64) {}", message: "type map"},
		{name: "closure", source: "package main\nfunc F() { f := func() {}; f() }", message: "closures"},
		{name: "builtin", source: "package main\nfunc F() { panic(1) }", message: "builtin function panic"},
		{name: "wrong scalar", source: "package main\nfunc F(v int) int { return v }", message: "type int is not supported"},
		{name: "too many args", source: "package main\nfunc F(a,b,c,d,e,f uint64) uint64 { return a }", message: "at most 5"},
		{name: "break", source: "package main\nfunc F() { for { break } }", message: "break statements"},
		{name: "logical and", source: "package main\nfunc F(a,b bool) bool { return a && b }", message: "binary operator &&"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CompileSource(test.name+".go", []byte(test.source))
			if err == nil {
				t.Fatal("CompileSource succeeded, want error")
			}
			if !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error %q does not contain %q", err, test.message)
			}
			var diagnostics *DiagnosticError
			if !errors.As(err, &diagnostics) && test.name != "map" {
				t.Fatalf("error type = %T, want DiagnosticError", err)
			}
		})
	}
}

func TestMalformedSourceAndIRReturnErrors(t *testing.T) {
	if _, err := CompileSource("bad.go", []byte("package main\nfunc")); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("parse error = %v", err)
	}
	program := &Program{
		Package: "main",
		Functions: []*Function{{
			Name:   "Bad",
			Result: TypeVoid,
			Entry:  0,
			Blocks: []*BasicBlock{{
				ID:         0,
				Terminator: Terminator{Kind: TermJump, Target: 99},
			}},
		}},
	}
	if err := program.Validate(); err == nil || !strings.Contains(err.Error(), "invalid block 99") {
		t.Fatalf("IR validation error = %v", err)
	}
	invalidBool := &Program{
		Package: "main",
		Functions: []*Function{{
			Name:   "BadBool",
			Result: TypeBool,
			Values: []Value{{ID: 0, Type: TypeBool, Kind: ValueTemporary}},
			Entry:  0,
			Blocks: []*BasicBlock{{
				ID:           0,
				Instructions: []Instruction{{Op: OpConst, Dest: 0, Imm: 2}},
				Terminator:   Terminator{Kind: TermReturn, Result: 0},
			}},
		}},
	}
	if err := invalidBool.Validate(); err == nil || !strings.Contains(err.Error(), "bool constant") {
		t.Fatalf("invalid bool IR error = %v", err)
	}
}

func FuzzCompileUint64ConstantArithmetic(f *testing.F) {
	f.Add(uint64(0), uint64(0))
	f.Add(^uint64(0), uint64(1))
	f.Add(uint64(10), uint64(20))
	f.Fuzz(func(t *testing.T, a, b uint64) {
		// Keeping each constant behind a variable exercises runtime-style
		// wrapping addition; a direct constant expression is correctly rejected
		// by Go itself when the mathematical result exceeds uint64.
		source := fmt.Sprintf("package main\nfunc F() uint64 { var a uint64 = uint64(%d); var b uint64 = uint64(%d); return a + b }\n", a, b)
		program, err := CompileSource("fuzz.go", []byte(source))
		if err != nil {
			t.Fatalf("CompileSource: %v", err)
		}
		if err := program.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
}

func compileTestSource(t *testing.T, source string) *Program {
	t.Helper()
	program, err := CompileSource("test.go", []byte(source))
	if err != nil {
		t.Fatalf("CompileSource: %v", err)
	}
	return program
}

func compileFunction(t *testing.T, source, name string) *Function {
	t.Helper()
	program := compileTestSource(t, source)
	function, ok := program.Function(name)
	if !ok {
		t.Fatalf("function %s not found", name)
	}
	return function
}
