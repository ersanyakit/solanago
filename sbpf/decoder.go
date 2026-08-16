package sbpf

import (
	"encoding/binary"
	"fmt"
)

// Decode parses raw sBPF slots and merges each LD_DW_IMM pair into one logical
// instruction. It rejects truncated, unknown, non-canonical, and unsafe code.
func Decode(bytecode []byte) ([]Instruction, error) {
	if len(bytecode) == 0 {
		return nil, ErrEmptyProgram
	}
	if len(bytecode)%InstructionSize != 0 {
		return nil, fmt.Errorf("%w: length %d is not a multiple of %d", ErrMalformedBytecode, len(bytecode), InstructionSize)
	}
	if len(bytecode)/InstructionSize > MaxProgramInstructions {
		return nil, fmt.Errorf("%w: %d slots (maximum %d)", ErrProgramTooLarge, len(bytecode)/InstructionSize, MaxProgramInstructions)
	}

	program := make([]Instruction, 0, len(bytecode)/InstructionSize)
	for pc := 0; pc < len(bytecode)/InstructionSize; pc++ {
		slot := bytecode[pc*InstructionSize : (pc+1)*InstructionSize]
		ins := decodeSlot(slot)
		if ins.Op == LD_DW_IMM {
			if pc+1 >= len(bytecode)/InstructionSize {
				return nil, fmt.Errorf("%w: LD_DW_IMM at pc %d has no continuation slot", ErrMalformedBytecode, pc)
			}
			continuation := bytecode[(pc+1)*InstructionSize : (pc+2)*InstructionSize]
			if continuation[0] != 0 {
				return nil, fmt.Errorf("%w: LD_DW_IMM at pc %d has continuation opcode 0x%02x, want 0", ErrMalformedBytecode, pc, continuation[0])
			}
			if continuation[1] != 0 || continuation[2] != 0 || continuation[3] != 0 {
				return nil, fmt.Errorf("%w: LD_DW_IMM continuation at pc %d has non-zero register/offset fields", ErrMalformedBytecode, pc+1)
			}
			low := uint64(uint32(ins.Immediate))
			high := uint64(binary.LittleEndian.Uint32(continuation[4:8])) << 32
			ins.Immediate = int64(high | low)
			pc++
		}
		program = append(program, ins)
	}
	if err := ValidateProgram(program); err != nil {
		return nil, err
	}
	return program, nil
}

// DecodeProgram is a descriptive alias for Decode.
func DecodeProgram(bytecode []byte) ([]Instruction, error) { return Decode(bytecode) }

// DecodeInstruction decodes exactly one logical instruction (8 or 16 bytes).
func DecodeInstruction(bytecode []byte) (Instruction, error) {
	program, err := Decode(bytecode)
	if err != nil {
		return Instruction{}, err
	}
	if len(program) != 1 {
		return Instruction{}, fmt.Errorf("%w: expected one instruction, decoded %d", ErrMalformedBytecode, len(program))
	}
	return program[0], nil
}

func decodeSlot(slot []byte) Instruction {
	return Instruction{
		Op:        Opcode(slot[0]),
		Dst:       Register(slot[1] & 0x0f),
		Src:       Register(slot[1] >> 4),
		Offset:    int16(binary.LittleEndian.Uint16(slot[2:4])),
		Immediate: int64(int32(binary.LittleEndian.Uint32(slot[4:8]))),
	}
}
