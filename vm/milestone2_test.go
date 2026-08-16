package vm

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/ersanyakit/go-solana/sbpf"
)

func TestV3ALU32AndJump32Semantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		op   sbpf.Opcode
		a, b uint32
		want uint64
	}{
		{"add sign extends V3 result", sbpf.ADD32_REG, math.MaxInt32, 1, 0xffffffff80000000},
		{"sub wraps", sbpf.SUB32_REG, 0, 1, math.MaxUint64},
		{"mul sign extends", sbpf.MUL32_REG, 0x40000000, 2, 0xffffffff80000000},
		{"div zero extends", sbpf.DIV32_REG, math.MaxUint32, 1, math.MaxUint32},
		{"remainder", sbpf.MOD32_REG, math.MaxUint32, 97, uint64(math.MaxUint32 % 97)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program := []sbpf.Instruction{
				sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R0, sbpf.R1),
				sbpf.ALUReg(test.op, sbpf.R0, sbpf.R2),
				sbpf.Return(),
			}
			if got := run(t, program, uint64(test.a), uint64(test.b)); got != test.want {
				t.Fatalf("got %#x, want %#x", got, test.want)
			}
		})
	}

	signedLess := []sbpf.Instruction{
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 0),
		sbpf.JumpReg(sbpf.JSLT32_REG, sbpf.R1, sbpf.R2, 1),
		sbpf.Return(),
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 1),
		sbpf.Return(),
	}
	if got := run(t, signedLess, math.MaxUint32, 0); got != 1 {
		t.Fatalf("int32(-1) < 0 returned %d", got)
	}
}

func TestExplicitMemoryRegionsWidthsAndPermissions(t *testing.T) {
	t.Parallel()
	data := make([]byte, 16)
	data[0] = 0xaa
	binary.LittleEndian.PutUint16(data[2:], 0xbbcc)
	binary.LittleEndian.PutUint32(data[4:], 0xddeeff00)
	binary.LittleEndian.PutUint64(data[8:], 0x1122334455667788)
	base := sbpf.MMInputStart + 64
	program := []sbpf.Instruction{
		{Op: sbpf.LD_B_REG, Dst: sbpf.R0, Src: sbpf.R1},
		{Op: sbpf.LD_H_REG, Dst: sbpf.R2, Src: sbpf.R1, Offset: 2},
		sbpf.ALUReg(sbpf.ADD64_REG, sbpf.R0, sbpf.R2),
		{Op: sbpf.LD_W_REG, Dst: sbpf.R2, Src: sbpf.R1, Offset: 4},
		sbpf.ALUReg(sbpf.ADD64_REG, sbpf.R0, sbpf.R2),
		{Op: sbpf.ST_DW_REG, Dst: sbpf.R1, Src: sbpf.R0, Offset: 8},
		sbpf.Return(),
	}
	machine, err := New(program)
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(0xaa + 0xbbcc + 0xddeeff00)
	got, err := machine.RunWithMemory([]MemoryRegion{WritableRegion(base, data)}, base)
	if err != nil || got != want || binary.LittleEndian.Uint64(data[8:]) != want {
		t.Fatalf("got=%#x stored=%#x err=%v", got, binary.LittleEndian.Uint64(data[8:]), err)
	}
	_, err = machine.RunWithMemory([]MemoryRegion{ReadOnlyRegion(base, data)}, base)
	if !errors.Is(err, ErrReadOnlyMemory) {
		t.Fatalf("read-only store error = %v", err)
	}
}

func TestMemoryMappingRejectsOverlapStackAndCrossRegion(t *testing.T) {
	t.Parallel()
	program := []sbpf.Instruction{sbpf.Return()}
	machine, err := New(program)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		regions []MemoryRegion
		want    error
	}{
		{"overlap", []MemoryRegion{WritableRegion(sbpf.MMInputStart, make([]byte, 8)), WritableRegion(sbpf.MMInputStart+4, make([]byte, 8))}, ErrOverlappingMemory},
		{"stack", []MemoryRegion{WritableRegion(sbpf.MMStackStart, make([]byte, 8))}, ErrInvalidMemoryMap},
		{"cross", []MemoryRegion{WritableRegion(sbpf.MMInputStart+sbpf.MMRegionSize-4, make([]byte, 8))}, ErrInvalidMemoryMap},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, runErr := machine.RunWithMemory(test.regions); !errors.Is(runErr, test.want) {
				t.Fatalf("error = %v, want %v", runErr, test.want)
			}
		})
	}
}

func FuzzVMUint32Arithmetic(f *testing.F) {
	f.Add(uint32(0), uint32(1))
	f.Add(uint32(math.MaxUint32), uint32(97))
	f.Fuzz(func(t *testing.T, a, b uint32) {
		if b == 0 {
			b = 1
		}
		program := []sbpf.Instruction{
			sbpf.ALUReg(sbpf.MOV32_REG, sbpf.R0, sbpf.R1),
			sbpf.ALUReg(sbpf.ADD32_REG, sbpf.R0, sbpf.R2),
			sbpf.ALUReg(sbpf.MOV32_REG, sbpf.R0, sbpf.R0),
			sbpf.ALUReg(sbpf.MOD32_REG, sbpf.R0, sbpf.R2),
			sbpf.Return(),
		}
		got := run(t, program, uint64(a), uint64(b))
		want := uint64((a + b) % b)
		if got != want {
			t.Fatalf("a=%d b=%d got=%d want=%d", a, b, got, want)
		}
	})
}
