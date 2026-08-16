package vm

import (
	"errors"
	"math"
	"testing"

	"github.com/ersany/go-solana/sbpf"
)

func run(t *testing.T, program []sbpf.Instruction, args ...uint64) uint64 {
	t.Helper()
	machine, err := New(program)
	if err != nil {
		t.Fatal(err)
	}
	result, err := machine.Run(args...)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestArithmetic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		op   sbpf.Opcode
		a, b uint64
		want uint64
	}{
		{"add", sbpf.ADD64_REG, 10, 20, 30},
		{"sub", sbpf.SUB64_REG, 100, 40, 60},
		{"mul", sbpf.MUL64_REG, 5, 6, 30},
		{"div", sbpf.DIV64_REG, 100, 4, 25},
		{"add wraps", sbpf.ADD64_REG, math.MaxUint64, 1, 0},
		{"sub wraps", sbpf.SUB64_REG, 0, 1, math.MaxUint64},
		{"mul wraps", sbpf.MUL64_REG, 1 << 63, 2, 0},
		{"xor", sbpf.XOR64_REG, 0xff00, 0x0ff0, 0xf0f0},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			program := []sbpf.Instruction{
				sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R0, sbpf.R1),
				sbpf.ALUReg(test.op, sbpf.R0, sbpf.R2),
				sbpf.Return(),
			}
			if got := run(t, program, test.a, test.b); got != test.want {
				t.Fatalf("got %d, want %d", got, test.want)
			}
		})
	}
}

func TestImmediateSignExtensionAndLoad64(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{
		sbpf.LoadImm64(sbpf.R0, 0x8000000000000000),
		sbpf.ALUImm(sbpf.ADD64_IMM, sbpf.R0, -1),
		sbpf.Return(),
	}
	if got := run(t, program); got != 0x7fffffffffffffff {
		t.Fatalf("got %#x", got)
	}
}

func TestUnsignedAndSignedBranches(t *testing.T) {
	t.Parallel()
	unsignedLess := []sbpf.Instruction{
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 0),
		sbpf.JumpReg(sbpf.JLT64_REG, sbpf.R1, sbpf.R2, 1),
		sbpf.Return(),
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 1),
		sbpf.Return(),
	}
	if got := run(t, unsignedLess, 2, 3); got != 1 {
		t.Fatalf("2 < 3 returned %d", got)
	}
	if got := run(t, unsignedLess, 3, 2); got != 0 {
		t.Fatalf("3 < 2 returned %d", got)
	}

	signedLess := append([]sbpf.Instruction(nil), unsignedLess...)
	signedLess[1] = sbpf.JumpReg(sbpf.JSLT64_REG, sbpf.R1, sbpf.R2, 1)
	if got := run(t, signedLess, uint64(math.MaxInt64)+1, 0); got != 1 {
		t.Fatalf("MinInt64 < 0 returned %d", got)
	}
	if got := run(t, unsignedLess, uint64(math.MaxInt64)+1, 0); got != 0 {
		t.Fatalf("unsigned high-bit value < 0 returned %d", got)
	}
}

func TestAllRegisterComparisonSemantics(t *testing.T) {
	t.Parallel()
	minusOne := ^uint64(0)
	minusTwo := minusOne - 1
	tests := []struct {
		name       string
		op         sbpf.Opcode
		a, b       uint64
		wantBranch bool
	}{
		{"equal true", sbpf.JEQ64_REG, 7, 7, true},
		{"equal false", sbpf.JEQ64_REG, 7, 8, false},
		{"not equal true", sbpf.JNE64_REG, 7, 8, true},
		{"not equal false", sbpf.JNE64_REG, 7, 7, false},
		{"unsigned greater", sbpf.JGT64_REG, 9, 8, true},
		{"unsigned greater false", sbpf.JGT64_REG, 8, 9, false},
		{"unsigned greater equal", sbpf.JGE64_REG, 9, 9, true},
		{"unsigned greater equal false", sbpf.JGE64_REG, 8, 9, false},
		{"unsigned less", sbpf.JLT64_REG, 8, 9, true},
		{"unsigned less false", sbpf.JLT64_REG, 9, 8, false},
		{"unsigned less equal", sbpf.JLE64_REG, 9, 9, true},
		{"unsigned less equal false", sbpf.JLE64_REG, 9, 8, false},
		{"signed greater", sbpf.JSGT64_REG, minusOne, minusTwo, true},
		{"signed greater false", sbpf.JSGT64_REG, minusTwo, minusOne, false},
		{"signed greater equal", sbpf.JSGE64_REG, minusTwo, minusTwo, true},
		{"signed greater equal false", sbpf.JSGE64_REG, minusTwo, minusOne, false},
		{"signed less", sbpf.JSLT64_REG, minusTwo, minusOne, true},
		{"signed less false", sbpf.JSLT64_REG, minusOne, minusTwo, false},
		{"signed less equal", sbpf.JSLE64_REG, minusTwo, minusTwo, true},
		{"signed less equal false", sbpf.JSLE64_REG, minusOne, minusTwo, false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			program := []sbpf.Instruction{
				sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 0),
				sbpf.JumpReg(test.op, sbpf.R1, sbpf.R2, 1),
				sbpf.Return(),
				sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 1),
				sbpf.Return(),
			}
			got := run(t, program, test.a, test.b)
			want := uint64(0)
			if test.wantBranch {
				want = 1
			}
			if got != want {
				t.Fatalf("branch result = %d, want %d", got, want)
			}
		})
	}
}

func TestLoop(t *testing.T) {
	t.Parallel()
	// Sum 1..n. Back-edge semantics are pc+1+off, exactly as sBPF.
	program := []sbpf.Instruction{
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 0),
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R2, 0),
		sbpf.JumpReg(sbpf.JGE64_REG, sbpf.R2, sbpf.R1, 3),
		sbpf.ALUImm(sbpf.ADD64_IMM, sbpf.R2, 1),
		sbpf.ALUReg(sbpf.ADD64_REG, sbpf.R0, sbpf.R2),
		sbpf.Jump(-4),
		sbpf.Return(),
	}
	if got := run(t, program, 10); got != 55 {
		t.Fatalf("sum = %d, want 55", got)
	}
}

func TestStackLoadStore(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{
		{Op: sbpf.ST_DW_REG, Dst: sbpf.R10, Src: sbpf.R1, Offset: -8},
		{Op: sbpf.LD_DW_REG, Dst: sbpf.R0, Src: sbpf.R10, Offset: -8},
		sbpf.Return(),
	}
	if got := run(t, program, 0xfeedfacecafebeef); got != 0xfeedfacecafebeef {
		t.Fatalf("stack round-trip = %#x", got)
	}
}

func TestInvalidMemoryAccess(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{
		{Op: sbpf.LD_DW_REG, Dst: sbpf.R0, Src: sbpf.R1},
		sbpf.Return(),
	}
	machine, err := New(program)
	if err != nil {
		t.Fatal(err)
	}
	_, err = machine.Run(0)
	if !errors.Is(err, ErrInvalidMemory) {
		t.Fatalf("error = %v, want invalid memory", err)
	}
}

func TestInternalCallAndCalleeSavedRegisters(t *testing.T) {
	t.Parallel()
	// pc0 initializes r6; pc1 calls pc4; callee clobbers r6 but EXIT
	// restores it; caller at pc2 moves restored r6 to r0.
	program := []sbpf.Instruction{
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R6, 42),
		sbpf.CallRelative(2),
		sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R0, sbpf.R6),
		sbpf.Return(),
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R6, 7),
		sbpf.Return(),
	}
	if got := run(t, program); got != 42 {
		t.Fatalf("callee-saved r6 = %d, want 42", got)
	}
}

func TestInternalCallUsesSeparateFixedStackFrame(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R1, 42),
		{Op: sbpf.ST_DW_REG, Dst: sbpf.R10, Src: sbpf.R1, Offset: -8},
		sbpf.CallRelative(3), // pc2 -> pc6
		{Op: sbpf.LD_DW_REG, Dst: sbpf.R0, Src: sbpf.R10, Offset: -8},
		sbpf.Return(),
		sbpf.Return(), // unreachable padding keeps the target visibly separate
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R1, 7),
		{Op: sbpf.ST_DW_REG, Dst: sbpf.R10, Src: sbpf.R1, Offset: -8},
		sbpf.Return(),
	}
	if got := run(t, program); got != 42 {
		t.Fatalf("caller frame value = %d, want 42", got)
	}
}

func TestFunctionArgumentsAndReturn(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{
		sbpf.CallRelative(1), // pc0 -> pc2
		sbpf.Return(),
		sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R0, sbpf.R1),
		sbpf.ALUReg(sbpf.ADD64_REG, sbpf.R0, sbpf.R2),
		sbpf.Return(),
	}
	if got := run(t, program, 10, 20); got != 30 {
		t.Fatalf("call result = %d, want 30", got)
	}
}

func TestSignedDivisionLoweringPrimitives(t *testing.T) {
	t.Parallel()
	// V3 has unsigned DIV64 and NEG64. The compiler lowers int64 division by
	// taking magnitudes, dividing, and restoring the result sign.
	program := []sbpf.Instruction{
		sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R0, sbpf.R1),
		sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R4, sbpf.R2),
		sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R3, sbpf.R1),
		sbpf.ALUReg(sbpf.XOR64_REG, sbpf.R3, sbpf.R2),
		sbpf.JumpImm(sbpf.JSGE64_IMM, sbpf.R0, 0, 1),
		{Op: sbpf.NEG64, Dst: sbpf.R0},
		sbpf.JumpImm(sbpf.JSGE64_IMM, sbpf.R4, 0, 1),
		{Op: sbpf.NEG64, Dst: sbpf.R4},
		sbpf.ALUReg(sbpf.DIV64_REG, sbpf.R0, sbpf.R4),
		sbpf.JumpImm(sbpf.JSGE64_IMM, sbpf.R3, 0, 1),
		{Op: sbpf.NEG64, Dst: sbpf.R0},
		sbpf.Return(),
	}
	tests := []struct{ a, b int64 }{{-21, 4}, {21, -4}, {-21, -4}, {21, 4}, {math.MinInt64, -1}}
	for _, test := range tests {
		got := int64(run(t, program, uint64(test.a), uint64(test.b)))
		want := test.a / test.b
		if got != want {
			t.Fatalf("%d / %d = %d, want %d", test.a, test.b, got, want)
		}
	}
}

func TestDivisionByZero(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{
		sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R0, sbpf.R1),
		sbpf.ALUReg(sbpf.DIV64_REG, sbpf.R0, sbpf.R2),
		sbpf.Return(),
	}
	machine, err := New(program)
	if err != nil {
		t.Fatal(err)
	}
	_, err = machine.Run(10, 0)
	if !errors.Is(err, ErrDivisionByZero) {
		t.Fatalf("error = %v, want division by zero", err)
	}
}

func TestInstructionLimit(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{sbpf.Jump(-1)}
	config := DefaultConfig()
	config.MaxInstructions = 25
	machine, err := NewWithConfig(program, config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = machine.Run()
	if !errors.Is(err, ErrInstructionLimit) {
		t.Fatalf("error = %v, want instruction limit", err)
	}
}

func TestCallDepthLimit(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{sbpf.CallRelative(-1), sbpf.Return()}
	config := DefaultConfig()
	config.MaxCallDepth = 4
	machine, err := NewWithConfig(program, config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = machine.Run()
	if !errors.Is(err, ErrCallDepthExceeded) || !errors.Is(err, ErrStackOverflow) {
		t.Fatalf("error = %v, want call-depth stack overflow", err)
	}
}

func TestUnsupportedStaticSyscall(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{{Op: sbpf.CALL_IMM, Immediate: 123}, sbpf.Return()}
	machine, err := New(program)
	if err != nil {
		t.Fatal(err)
	}
	_, err = machine.Run()
	if !errors.Is(err, ErrUnsupportedCall) {
		t.Fatalf("error = %v, want unsupported call", err)
	}
}

func TestExecutionOverrun(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 1)}
	machine, err := New(program)
	if err != nil {
		t.Fatal(err)
	}
	_, err = machine.Run()
	if !errors.Is(err, ErrExecutionOverrun) {
		t.Fatalf("error = %v, want execution overrun", err)
	}
}

func TestNewVMRetainsValidationError(t *testing.T) {
	t.Parallel()
	machine := NewVM([]sbpf.Instruction{{Op: 0xff}})
	_, err := machine.Run()
	if !errors.Is(err, sbpf.ErrInvalidOpcode) {
		t.Fatalf("error = %v, want invalid opcode", err)
	}
}

func FuzzVMArithmetic(f *testing.F) {
	f.Add(uint64(10), uint64(20))
	f.Add(^uint64(0), uint64(1))
	f.Fuzz(func(t *testing.T, a, b uint64) {
		program := []sbpf.Instruction{
			sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R0, sbpf.R1),
			sbpf.ALUReg(sbpf.ADD64_REG, sbpf.R0, sbpf.R2),
			sbpf.ALUReg(sbpf.MUL64_REG, sbpf.R0, sbpf.R2),
			sbpf.Return(),
		}
		got := run(t, program, a, b)
		want := (a + b) * b
		if got != want {
			t.Fatalf("got %d, want %d", got, want)
		}
	})
}

func FuzzVMBranches(f *testing.F) {
	f.Add(uint64(1), uint64(2))
	f.Add(^uint64(0), uint64(0))
	f.Fuzz(func(t *testing.T, a, b uint64) {
		program := []sbpf.Instruction{
			sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 0),
			sbpf.JumpReg(sbpf.JGE64_REG, sbpf.R1, sbpf.R2, 1),
			sbpf.Return(),
			sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 1),
			sbpf.Return(),
		}
		got := run(t, program, a, b)
		want := uint64(0)
		if a >= b {
			want = 1
		}
		if got != want {
			t.Fatalf("a=%d b=%d: got %d, want %d", a, b, got, want)
		}
	})
}
