package sbpf

import (
	"encoding/binary"
	"fmt"
)

// Encode serializes a logical instruction sequence to the official little-
// endian, 8-byte sBPF layout. LD_DW_IMM expands to two slots.
func Encode(program []Instruction) ([]byte, error) {
	if err := ValidateProgram(program); err != nil {
		return nil, err
	}
	_, slots := PhysicalPCs(program)
	encoded := make([]byte, 0, slots*InstructionSize)
	for i, ins := range program {
		bytes, err := EncodeInstruction(ins)
		if err != nil {
			return nil, fmt.Errorf("instruction %d: %w", i, err)
		}
		encoded = append(encoded, bytes...)
	}
	return encoded, nil
}

// EncodeProgram is a descriptive alias for Encode.
func EncodeProgram(program []Instruction) ([]byte, error) { return Encode(program) }

// EncodeInstruction encodes one logical instruction. The returned slice has
// length 16 for LD_DW_IMM and length 8 for every other supported instruction.
func EncodeInstruction(ins Instruction) ([]byte, error) {
	if err := ValidateInstruction(ins); err != nil {
		return nil, err
	}
	length := InstructionSize
	if ins.Op == LD_DW_IMM {
		length *= 2
	}
	encoded := make([]byte, length)
	encodeSlot(encoded[:InstructionSize], ins.Op, ins.Dst, ins.Src, ins.Offset, int32(ins.Immediate))
	if ins.Op == LD_DW_IMM {
		// The continuation slot's opcode/register/offset fields are all zero;
		// only its immediate field carries the high 32 bits.
		binary.LittleEndian.PutUint32(encoded[InstructionSize+4:], uint32(uint64(ins.Immediate)>>32))
	}
	return encoded, nil
}

func encodeSlot(dst []byte, op Opcode, destination, source Register, offset int16, immediate int32) {
	dst[0] = byte(op)
	dst[1] = byte(source<<4) | byte(destination)
	binary.LittleEndian.PutUint16(dst[2:4], uint16(offset))
	binary.LittleEndian.PutUint32(dst[4:8], uint32(immediate))
}
