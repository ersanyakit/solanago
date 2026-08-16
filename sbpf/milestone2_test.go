package sbpf

import (
	"errors"
	"reflect"
	"testing"
)

func TestMilestone2OpcodesMatchSolanaSBPFV3(t *testing.T) {
	t.Parallel()
	want := map[Opcode]byte{
		LD_B_REG: 0x71, LD_H_REG: 0x69, LD_W_REG: 0x61, LD_DW_REG: 0x79,
		ST_B_REG: 0x73, ST_H_REG: 0x6b, ST_W_REG: 0x63, ST_DW_REG: 0x7b,
		ADD32_REG: 0x0c, SUB32_REG: 0x1c, MUL32_REG: 0x2c,
		DIV32_REG: 0x3c, MOD32_REG: 0x9c, MOV32_REG: 0xbc,
		MOD64_REG: 0x9f, LSH64_IMM: 0x67, ARSH64_IMM: 0xc7,
		JLT32_REG: 0xae, JSLT32_REG: 0xce,
	}
	for opcode, encoded := range want {
		if byte(opcode) != encoded {
			t.Fatalf("%s = %#x, current solana-sbpf V3 encoding is %#x", opcode, byte(opcode), encoded)
		}
	}
	if MMStackStart != 0x200000000 || MMHeapStart != 0x300000000 || MMInputStart != 0x400000000 {
		t.Fatalf("unexpected sBPF region bases: stack=%#x heap=%#x input=%#x", MMStackStart, MMHeapStart, MMInputStart)
	}
}

func TestExplicitMemoryWidthsAndV3OpcodesRoundTrip(t *testing.T) {
	t.Parallel()
	program := []Instruction{
		{Op: LD_B_REG, Dst: R0, Src: R1, Offset: 1},
		{Op: LD_H_REG, Dst: R2, Src: R1, Offset: 2},
		{Op: LD_W_REG, Dst: R3, Src: R1, Offset: 4},
		{Op: LD_DW_REG, Dst: R4, Src: R1, Offset: 8},
		{Op: ST_B_REG, Dst: R1, Src: R0, Offset: 1},
		{Op: ST_H_REG, Dst: R1, Src: R2, Offset: 2},
		{Op: ST_W_REG, Dst: R1, Src: R3, Offset: 4},
		{Op: ST_DW_REG, Dst: R1, Src: R4, Offset: 8},
		ALUReg(ADD32_REG, R0, R2), ALUReg(SUB32_REG, R0, R2),
		ALUReg(MUL32_REG, R0, R2), ALUReg(DIV32_REG, R0, R2),
		ALUReg(MOD32_REG, R0, R2), ALUReg(MOV32_REG, R0, R2),
		ALUImm(LSH64_IMM, R0, 32), ALUImm(ARSH64_IMM, R0, 32),
		ALUReg(MOD64_REG, R0, R2),
		JumpReg(JSLT32_REG, R0, R2, 0),
		Return(),
	}
	encoded, err := Encode(program)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, program) {
		t.Fatalf("round trip\n got %#v\nwant %#v", decoded, program)
	}
	for _, instruction := range program[:8] {
		if instruction.MemoryType().Bytes() == 0 {
			t.Fatalf("memory opcode %s has no explicit width", instruction.Op)
		}
	}
}

func TestVerifierRejectsInvalidMilestone2Operands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ins  Instruction
		want error
	}{
		{"mod32 immediate zero", ALUImm(MOD32_IMM, R0, 0), ErrDivisionByZero},
		{"mod64 immediate zero", ALUImm(MOD64_IMM, R0, 0), ErrDivisionByZero},
		{"shift32 out of range", ALUImm(LSH32_IMM, R0, 32), ErrMalformedBytecode},
		{"shift64 negative", ALUImm(ARSH64_IMM, R0, -1), ErrMalformedBytecode},
		{"write frame pointer", ALUReg(MOV32_REG, R10, R0), ErrInvalidRegister},
		{"memory immediate", Instruction{Op: LD_W_REG, Dst: R0, Src: R1, Immediate: 1}, ErrMalformedBytecode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateInstruction(test.ins); !errors.Is(err, test.want) {
				t.Fatalf("ValidateInstruction error = %v, want %v", err, test.want)
			}
		})
	}
}

func FuzzMilestone2EncodeDecode(f *testing.F) {
	f.Add(uint32(1), uint32(2), int16(-4))
	f.Add(^uint32(0), uint32(17), int16(8))
	f.Fuzz(func(t *testing.T, a, b uint32, offset int16) {
		if b == 0 {
			b = 1
		}
		program := []Instruction{
			ALUImm(MOV32_IMM, R0, int32(a)),
			ALUImm(MOV32_IMM, R1, int32(b)),
			ALUReg(MOD32_REG, R0, R1),
			{Op: ST_W_REG, Dst: R10, Src: R0, Offset: -4},
			{Op: LD_W_REG, Dst: R0, Src: R10, Offset: -4},
			Return(),
		}
		encoded, err := Encode(program)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(encoded)
		if err != nil || !reflect.DeepEqual(decoded, program) {
			t.Fatalf("decoded=%#v err=%v", decoded, err)
		}
		_ = offset
	})
}
