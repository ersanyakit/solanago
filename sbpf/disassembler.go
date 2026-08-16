package sbpf

import (
	"fmt"
	"strings"
)

// Disassemble renders a validated program with physical slot PCs. The opcode
// names mirror the constants in the upstream sBPF implementation.
func Disassemble(program []Instruction) (string, error) {
	if err := ValidateProgram(program); err != nil {
		return "", err
	}
	pcs, _ := PhysicalPCs(program)
	var out strings.Builder
	for i, ins := range program {
		if i != 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "%04d: %s", pcs[i], formatInstruction(ins, pcs[i]))
	}
	return out.String(), nil
}

// DisassembleBytes validates and disassembles encoded sBPF bytecode.
func DisassembleBytes(bytecode []byte) (string, error) {
	program, err := Decode(bytecode)
	if err != nil {
		return "", err
	}
	return Disassemble(program)
}

func formatInstruction(ins Instruction, pc int) string {
	switch {
	case ins.Op == LD_DW_IMM:
		return fmt.Sprintf("%s %s, 0x%016x", ins.Op, ins.Dst, uint64(ins.Immediate))
	case ins.Op.isMemoryLoad():
		return fmt.Sprintf("%s %s, [%s%s]", ins.Op, ins.Dst, ins.Src, signedOffset(ins.Offset))
	case ins.Op.isMemoryStore():
		return fmt.Sprintf("%s [%s%s], %s", ins.Op, ins.Dst, signedOffset(ins.Offset), ins.Src)
	case ins.Op.isALUImmediate():
		return fmt.Sprintf("%s %s, %d", ins.Op, ins.Dst, ins.Immediate)
	case ins.Op.isALURegister():
		return fmt.Sprintf("%s %s, %s", ins.Op, ins.Dst, ins.Src)
	case ins.Op == NEG32 || ins.Op == NEG64:
		return fmt.Sprintf("%s %s", ins.Op, ins.Dst)
	case ins.Op == JA:
		target := pc + 1 + int(ins.Offset)
		return fmt.Sprintf("%s %s (-> %04d)", ins.Op, signedOffset(ins.Offset), target)
	case ins.Op.isJumpImmediate():
		target := pc + 1 + int(ins.Offset)
		return fmt.Sprintf("%s %s, %d, %s (-> %04d)", ins.Op, ins.Dst, ins.Immediate, signedOffset(ins.Offset), target)
	case ins.Op.isJumpRegister():
		target := pc + 1 + int(ins.Offset)
		return fmt.Sprintf("%s %s, %s, %s (-> %04d)", ins.Op, ins.Dst, ins.Src, signedOffset(ins.Offset), target)
	case ins.Op == CALL_IMM:
		if ins.Src == 1 {
			target := pc + 1 + int(ins.Immediate)
			return fmt.Sprintf("%s internal %+d (-> %04d)", ins.Op, ins.Immediate, target)
		}
		return fmt.Sprintf("%s syscall 0x%08x", ins.Op, uint32(ins.Immediate))
	case ins.Op == EXIT:
		return ins.Op.String()
	default:
		return fmt.Sprintf("UNKNOWN 0x%02x", uint8(ins.Op))
	}
}

func signedOffset(off int16) string {
	if off >= 0 {
		return fmt.Sprintf("+%d", off)
	}
	return fmt.Sprintf("%d", off)
}
