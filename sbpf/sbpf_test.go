package sbpf

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOfficialAddEncoding(t *testing.T) {
	t.Parallel()
	program := []Instruction{
		ALUReg(MOV64_REG, R0, R1),
		ALUReg(ADD64_REG, R0, R2),
		Return(),
	}
	got, err := Encode(program)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0xbf, 0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x0f, 0x20, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x95, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encoded add mismatch\n got: % x\nwant: % x", got, want)
	}

	decoded, err := Decode(got)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, program) {
		t.Fatalf("round-trip mismatch\n got: %#v\nwant: %#v", decoded, program)
	}
}

func TestLoadImmediate64EncodingAndPhysicalPCs(t *testing.T) {
	t.Parallel()
	program := []Instruction{
		LoadImm64(R0, 0xfedcba9876543210), // pc 0 and 1
		Jump(1),                           // pc 2 -> pc 4
		ALUImm(MOV64_IMM, R0, 0),          // pc 3
		Return(),                          // pc 4
	}
	encoded, err := Encode(program)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := []byte{
		0x18, 0x00, 0x00, 0x00, 0x10, 0x32, 0x54, 0x76,
		0x00, 0x00, 0x00, 0x00, 0x98, 0xba, 0xdc, 0xfe,
	}
	if !bytes.Equal(encoded[:16], wantPrefix) {
		t.Fatalf("LD_DW_IMM encoding mismatch: got % x want % x", encoded[:16], wantPrefix)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, program) {
		t.Fatalf("round-trip mismatch\n got: %#v\nwant: %#v", decoded, program)
	}
	pcs, slots := PhysicalPCs(decoded)
	if !reflect.DeepEqual(pcs, []int{0, 2, 3, 4}) || slots != 5 {
		t.Fatalf("physical PCs = %v, slots = %d", pcs, slots)
	}
}

func TestEncodeDecodeSupportedOpcodes(t *testing.T) {
	t.Parallel()
	nonControl := []Instruction{
		LoadImm64(R0, ^uint64(0)),
		{Op: LD_DW_REG, Dst: R0, Src: R10, Offset: -8},
		{Op: ST_DW_REG, Dst: R10, Src: R1, Offset: -8},
		ALUImm(ADD64_IMM, R0, -1), ALUReg(ADD64_REG, R0, R1),
		ALUImm(SUB64_IMM, R0, -1), ALUReg(SUB64_REG, R0, R1),
		ALUImm(MUL64_IMM, R0, -1), ALUReg(MUL64_REG, R0, R1),
		ALUImm(DIV64_IMM, R0, 1), ALUReg(DIV64_REG, R0, R1),
		ALUImm(XOR64_IMM, R0, -1), ALUReg(XOR64_REG, R0, R1),
		{Op: NEG64, Dst: R0},
		ALUImm(MOV64_IMM, R0, -1), ALUReg(MOV64_REG, R0, R1),
		Return(),
	}
	for _, original := range nonControl {
		original := original
		t.Run(original.Op.String(), func(t *testing.T) {
			encoded, err := EncodeInstruction(original)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeInstruction(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if decoded != original {
				t.Fatalf("decoded %#v, want %#v", decoded, original)
			}
		})
	}

	control := []Instruction{
		Jump(0),
		JumpImm(JEQ64_IMM, R0, 1, 0), JumpReg(JEQ64_REG, R0, R1, 0),
		JumpImm(JNE64_IMM, R0, 1, 0), JumpReg(JNE64_REG, R0, R1, 0),
		JumpImm(JGT64_IMM, R0, 1, 0), JumpReg(JGT64_REG, R0, R1, 0),
		JumpImm(JGE64_IMM, R0, 1, 0), JumpReg(JGE64_REG, R0, R1, 0),
		JumpImm(JLT64_IMM, R0, 1, 0), JumpReg(JLT64_REG, R0, R1, 0),
		JumpImm(JLE64_IMM, R0, 1, 0), JumpReg(JLE64_REG, R0, R1, 0),
		JumpImm(JSGT64_IMM, R0, -1, 0), JumpReg(JSGT64_REG, R0, R1, 0),
		JumpImm(JSGE64_IMM, R0, -1, 0), JumpReg(JSGE64_REG, R0, R1, 0),
		JumpImm(JSLT64_IMM, R0, -1, 0), JumpReg(JSLT64_REG, R0, R1, 0),
		JumpImm(JSLE64_IMM, R0, -1, 0), JumpReg(JSLE64_REG, R0, R1, 0),
		CallRelative(0),
	}
	for _, original := range control {
		original := original
		t.Run(original.Op.String()+"_control", func(t *testing.T) {
			program := []Instruction{original, Return()}
			encoded, err := Encode(program)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, program) {
				t.Fatalf("decoded %#v, want %#v", decoded, program)
			}
		})
	}
}

func TestDecoderRejectsMalformedPrograms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code []byte
		want error
	}{
		{"empty", nil, ErrEmptyProgram},
		{"truncated", []byte{byte(EXIT)}, ErrMalformedBytecode},
		{"unknown opcode", []byte{0xff, 0, 0, 0, 0, 0, 0, 0}, ErrInvalidOpcode},
		{"invalid destination", []byte{byte(MOV64_REG), 0x1f, 0, 0, 0, 0, 0, 0}, ErrInvalidRegister},
		{"invalid source", []byte{byte(MOV64_REG), 0xf0, 0, 0, 0, 0, 0, 0}, ErrInvalidRegister},
		{"lddw missing continuation", []byte{byte(LD_DW_IMM), 0, 0, 0, 0, 0, 0, 0}, ErrMalformedBytecode},
		{"lddw bad continuation", append(
			[]byte{byte(LD_DW_IMM), 0, 0, 0, 0, 0, 0, 0},
			[]byte{byte(EXIT), 0, 0, 0, 0, 0, 0, 0}...), ErrMalformedBytecode},
		{"immediate division zero", []byte{byte(DIV64_IMM), 0, 0, 0, 0, 0, 0, 0}, ErrDivisionByZero},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode(test.code)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}

func TestRejectsJumpToLDDWContinuation(t *testing.T) {
	t.Parallel()
	// pc0: JA +1 -> pc2. pc1/pc2 are an LD_DW_IMM pair, so pc2 is not
	// an instruction boundary. pc3 is EXIT.
	bytecode := []byte{
		byte(JA), 0, 1, 0, 0, 0, 0, 0,
		byte(LD_DW_IMM), 0, 0, 0, 1, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0,
		byte(EXIT), 0, 0, 0, 0, 0, 0, 0,
	}
	_, err := Decode(bytecode)
	if !errors.Is(err, ErrInvalidJump) {
		t.Fatalf("error = %v, want invalid jump", err)
	}
}

func TestDisassemble(t *testing.T) {
	t.Parallel()
	program := []Instruction{
		ALUReg(MOV64_REG, R0, R1),
		ALUReg(ADD64_REG, R0, R2),
		Return(),
	}
	got, err := Disassemble(program)
	if err != nil {
		t.Fatal(err)
	}
	want := "0000: MOV64_REG r0, r1\n0001: ADD64_REG r0, r2\n0002: EXIT"
	if got != want {
		t.Fatalf("disassembly mismatch\n got: %q\nwant: %q", got, want)
	}

	withTargets := []Instruction{JumpImm(JEQ64_IMM, R1, 0, 1), Return(), Return()}
	got, err = Disassemble(withTargets)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "-> 0002") {
		t.Fatalf("disassembly lacks physical target: %s", got)
	}
}

func FuzzEncodeDecode(f *testing.F) {
	f.Add(uint64(0), int64(0), uint64(0))
	f.Add(^uint64(0), int64(-1), uint64(0x123456789abcdef0))
	f.Fuzz(func(t *testing.T, constant uint64, immediate int64, xor uint64) {
		immediate = int64(int32(immediate))
		program := []Instruction{
			LoadImm64(R0, constant),
			ALUImm(ADD64_IMM, R0, int32(immediate)),
			LoadImm64(R1, xor),
			ALUReg(XOR64_REG, R0, R1),
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
			t.Fatalf("round-trip mismatch: got %#v want %#v", decoded, program)
		}
	})
}

func FuzzDecodeNeverPanics(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{byte(EXIT), 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{byte(LD_DW_IMM), 0, 0, 0, 1, 0, 0, 0})
	f.Fuzz(func(t *testing.T, bytecode []byte) {
		_, _ = Decode(bytecode)
	})
}
