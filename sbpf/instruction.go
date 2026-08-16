package sbpf

import (
	"errors"
	"fmt"
	"math"
)

const (
	// InstructionSize is the size of one encoded sBPF instruction slot.
	InstructionSize = 8
	// MaxProgramInstructions is the upper bound used by the upstream sBPF
	// implementation (ebpf::PROG_MAX_INSNS).
	MaxProgramInstructions = 65_536
	// RegisterCount includes general-purpose r0-r9 and frame pointer r10.
	RegisterCount = 11

	// The upper 32 bits select an sBPF virtual-memory region. These values are
	// shared with solana-sbpf's ebpf.rs and are addresses, never host pointers.
	MMRegionSize    uint64 = 1 << 32
	MMRODataStart   uint64 = 0
	MMBytecodeStart        = MMRegionSize
	MMStackStart           = 2 * MMRegionSize
	MMHeapStart            = 3 * MMRegionSize
	MMInputStart           = 4 * MMRegionSize
)

var (
	ErrMalformedBytecode = errors.New("malformed sBPF bytecode")
	ErrInvalidOpcode     = errors.New("invalid sBPF opcode")
	ErrInvalidRegister   = errors.New("invalid sBPF register")
	ErrInvalidJump       = errors.New("invalid sBPF jump")
	ErrDivisionByZero    = errors.New("sBPF division by zero")
	ErrProgramTooLarge   = errors.New("sBPF program exceeds instruction limit")
	ErrEmptyProgram      = errors.New("empty sBPF program")
)

// Register is the four-bit register number encoded in the high or low nibble
// of byte one. Although four bits can encode r0-r15, sBPF only permits r0-r10.
type Register uint8

const (
	R0 Register = iota
	R1
	R2
	R3
	R4
	R5
	R6
	R7
	R8
	R9
	R10
)

const FramePointer = R10

func (r Register) String() string { return fmt.Sprintf("r%d", uint8(r)) }

func (r Register) valid() bool { return r < RegisterCount }

// Instruction is a decoded sBPF instruction. Immediate is sign-extended from
// the encoded i32 for ordinary instructions. For LD_DW_IMM it holds all 64
// bits (as an int64 bit pattern), and Encode emits the required second slot.
//
// Offset and CALL_IMM Immediate offsets are measured in physical 8-byte slots,
// exactly as in the VM. LD_DW_IMM therefore counts as two slots.
type Instruction struct {
	Op        Opcode
	Dst       Register
	Src       Register
	Offset    int16
	Immediate int64
}

// MemoryType is the explicit width of an sBPF memory operation. Loads are
// zero-extending; language-level signedness is handled by the compiler.
type MemoryType uint8

const (
	MemoryInvalid MemoryType = iota
	MemoryByte
	MemoryHalf
	MemoryWord
	MemoryDoubleWord
)

func (kind MemoryType) Bytes() uint64 {
	switch kind {
	case MemoryByte:
		return 1
	case MemoryHalf:
		return 2
	case MemoryWord:
		return 4
	case MemoryDoubleWord:
		return 8
	default:
		return 0
	}
}

// LoadMemory and StoreMemory select the canonical V3 register memory opcode
// for an explicit access width.
func LoadMemory(kind MemoryType, dst, base Register, offset int16) (Instruction, error) {
	op := map[MemoryType]Opcode{
		MemoryByte: LD_B_REG, MemoryHalf: LD_H_REG,
		MemoryWord: LD_W_REG, MemoryDoubleWord: LD_DW_REG,
	}[kind]
	if op == 0 {
		return Instruction{}, fmt.Errorf("%w: invalid load width %d", ErrMalformedBytecode, kind)
	}
	return Instruction{Op: op, Dst: dst, Src: base, Offset: offset}, nil
}

func StoreMemory(kind MemoryType, base, src Register, offset int16) (Instruction, error) {
	op := map[MemoryType]Opcode{
		MemoryByte: ST_B_REG, MemoryHalf: ST_H_REG,
		MemoryWord: ST_W_REG, MemoryDoubleWord: ST_DW_REG,
	}[kind]
	if op == 0 {
		return Instruction{}, fmt.Errorf("%w: invalid store width %d", ErrMalformedBytecode, kind)
	}
	return Instruction{Op: op, Dst: base, Src: src, Offset: offset}, nil
}

// MemoryType returns the access width for a load/store opcode.
func (ins Instruction) MemoryType() MemoryType {
	switch ins.Op {
	case LD_B_REG, ST_B_REG:
		return MemoryByte
	case LD_H_REG, ST_H_REG:
		return MemoryHalf
	case LD_W_REG, ST_W_REG:
		return MemoryWord
	case LD_DW_REG, ST_DW_REG:
		return MemoryDoubleWord
	default:
		return MemoryInvalid
	}
}

// Slots returns the number of physical 8-byte slots occupied by ins.
func (ins Instruction) Slots() int {
	if ins.Op == LD_DW_IMM {
		return 2
	}
	return 1
}

// LoadImm64 constructs the two-slot instruction needed for an arbitrary
// uint64 constant.
func LoadImm64(dst Register, value uint64) Instruction {
	return Instruction{Op: LD_DW_IMM, Dst: dst, Immediate: int64(value)}
}

// ALUReg constructs a register-source arithmetic instruction.
func ALUReg(op Opcode, dst, src Register) Instruction {
	return Instruction{Op: op, Dst: dst, Src: src}
}

// ALUImm constructs an immediate-source arithmetic instruction.
func ALUImm(op Opcode, dst Register, imm int32) Instruction {
	return Instruction{Op: op, Dst: dst, Immediate: int64(imm)}
}

// JumpReg constructs a conditional register comparison. off is relative to
// the physical slot immediately following the jump.
func JumpReg(op Opcode, dst, src Register, off int16) Instruction {
	return Instruction{Op: op, Dst: dst, Src: src, Offset: off}
}

// JumpImm constructs a conditional immediate comparison. off is relative to
// the physical slot immediately following the jump.
func JumpImm(op Opcode, dst Register, imm int32, off int16) Instruction {
	return Instruction{Op: op, Dst: dst, Offset: off, Immediate: int64(imm)}
}

// Jump constructs an unconditional relative branch.
func Jump(off int16) Instruction { return Instruction{Op: JA, Offset: off} }

// CallRelative constructs an sBPF v3+ internal CALL_IMM. The source-register
// nibble value 1 distinguishes an internal relative call from a static syscall.
func CallRelative(off int32) Instruction {
	return Instruction{Op: CALL_IMM, Src: 1, Immediate: int64(off)}
}

// Return constructs EXIT. At call depth zero it terminates with r0; in a
// called function it returns to its caller.
func Return() Instruction { return Instruction{Op: EXIT} }

// PhysicalPCs returns the physical slot PC of each logical instruction and the
// total slot count. It is useful to patch branch and call relocations in the
// presence of two-slot LD_DW_IMM instructions.
func PhysicalPCs(program []Instruction) ([]int, int) {
	pcs := make([]int, len(program))
	pc := 0
	for i, ins := range program {
		pcs[i] = pc
		pc += ins.Slots()
	}
	return pcs, pc
}

// RelativeOffset computes targetPC-(sourcePC+1), the encoding shared by sBPF
// branches and sBPF v3+ internal calls.
func RelativeOffset(sourcePC, targetPC int) (int32, error) {
	off := int64(targetPC) - int64(sourcePC) - 1
	if off < math.MinInt32 || off > math.MaxInt32 {
		return 0, fmt.Errorf("%w: relative offset %d does not fit i32", ErrInvalidJump, off)
	}
	return int32(off), nil
}

// ValidateInstruction checks the canonical operand shape accepted by this
// MVP's sBPF v3 backend. Program-relative branch targets are checked by
// ValidateProgram.
func ValidateInstruction(ins Instruction) error {
	if !ins.Op.valid() {
		return fmt.Errorf("%w: 0x%02x", ErrInvalidOpcode, uint8(ins.Op))
	}
	if !ins.Dst.valid() {
		return fmt.Errorf("%w: destination %s", ErrInvalidRegister, ins.Dst)
	}
	if !ins.Src.valid() {
		return fmt.Errorf("%w: source %s", ErrInvalidRegister, ins.Src)
	}

	// The upstream verifier allows r10 only as a memory base (or for the
	// version-specific stack bump). This v3 subset never writes r10.
	if ins.Dst == FramePointer && !ins.Op.isMemoryStore() {
		return fmt.Errorf("%w: cannot write frame pointer r10", ErrInvalidRegister)
	}

	if ins.Op != LD_DW_IMM && (ins.Immediate < math.MinInt32 || ins.Immediate > math.MaxInt32) {
		return fmt.Errorf("%w: immediate %d does not fit encoded i32", ErrMalformedBytecode, ins.Immediate)
	}

	switch {
	case ins.Op == LD_DW_IMM:
		if ins.Src != 0 || ins.Offset != 0 {
			return nonCanonical(ins, "LD_DW_IMM requires src=0 and off=0")
		}
	case ins.Op.isMemoryLoad():
		if ins.Immediate != 0 {
			return nonCanonical(ins, "memory load requires imm=0")
		}
	case ins.Op.isMemoryStore():
		if ins.Immediate != 0 {
			return nonCanonical(ins, "memory store requires imm=0")
		}
	case ins.Op.isALUImmediate():
		if ins.Src != 0 || ins.Offset != 0 {
			return nonCanonical(ins, "immediate ALU instruction requires src=0 and off=0")
		}
		if (ins.Op == DIV32_IMM || ins.Op == MOD32_IMM || ins.Op == DIV64_IMM || ins.Op == MOD64_IMM) && ins.Immediate == 0 {
			return fmt.Errorf("%w: immediate divisor", ErrDivisionByZero)
		}
		if (ins.Op == LSH32_IMM || ins.Op == RSH32_IMM || ins.Op == ARSH32_IMM) && (ins.Immediate < 0 || ins.Immediate >= 32) {
			return nonCanonical(ins, "32-bit shift immediate must be in [0,32)")
		}
		if (ins.Op == LSH64_IMM || ins.Op == RSH64_IMM || ins.Op == ARSH64_IMM) && (ins.Immediate < 0 || ins.Immediate >= 64) {
			return nonCanonical(ins, "64-bit shift immediate must be in [0,64)")
		}
	case ins.Op.isALURegister():
		if ins.Offset != 0 || ins.Immediate != 0 {
			return nonCanonical(ins, "register ALU instruction requires off=0 and imm=0")
		}
	case ins.Op == NEG32 || ins.Op == NEG64:
		if ins.Src != 0 || ins.Offset != 0 || ins.Immediate != 0 {
			return nonCanonical(ins, "NEG64 requires src=0, off=0, and imm=0")
		}
	case ins.Op == JA:
		if ins.Dst != 0 || ins.Src != 0 || ins.Immediate != 0 {
			return nonCanonical(ins, "JA requires dst=0, src=0, and imm=0")
		}
	case ins.Op.isJumpImmediate():
		if ins.Src != 0 {
			return nonCanonical(ins, "immediate jump requires src=0")
		}
	case ins.Op.isJumpRegister():
		if ins.Immediate != 0 {
			return nonCanonical(ins, "register jump requires imm=0")
		}
	case ins.Op == CALL_IMM:
		if ins.Dst != 0 || ins.Offset != 0 || (ins.Src != 0 && ins.Src != 1) {
			return nonCanonical(ins, "V3 CALL_IMM requires dst=0, off=0, and src=0 or src=1")
		}
	case ins.Op == EXIT:
		if ins.Dst != 0 || ins.Src != 0 || ins.Offset != 0 || ins.Immediate != 0 {
			return nonCanonical(ins, "EXIT has no operands")
		}
	}
	return nil
}

func nonCanonical(ins Instruction, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrMalformedBytecode, ins.Op, reason)
}

// ValidateProgram performs structural validation and verifies that every
// branch and internal call lands on the first slot of an instruction.
func ValidateProgram(program []Instruction) error {
	if len(program) == 0 {
		return ErrEmptyProgram
	}
	pcs, slotCount := PhysicalPCs(program)
	if slotCount > MaxProgramInstructions {
		return fmt.Errorf("%w: %d slots (maximum %d)", ErrProgramTooLarge, slotCount, MaxProgramInstructions)
	}
	starts := make(map[int]struct{}, len(program))
	for _, pc := range pcs {
		starts[pc] = struct{}{}
	}
	for i, ins := range program {
		if err := ValidateInstruction(ins); err != nil {
			return fmt.Errorf("instruction %d (pc %d): %w", i, pcs[i], err)
		}
		if !ins.Op.isJump() && !(ins.Op == CALL_IMM && ins.Src == 1) {
			continue
		}
		var relative int64
		if ins.Op.isJump() {
			relative = int64(ins.Offset)
		} else {
			relative = ins.Immediate
		}
		target := int64(pcs[i]) + 1 + relative
		if target < 0 || target >= int64(slotCount) {
			return fmt.Errorf("instruction %d (pc %d): %w: target pc %d is outside [0,%d)",
				i, pcs[i], ErrInvalidJump, target, slotCount)
		}
		if _, ok := starts[int(target)]; !ok {
			return fmt.Errorf("instruction %d (pc %d): %w: target pc %d is the second slot of LD_DW_IMM",
				i, pcs[i], ErrInvalidJump, target)
		}
	}
	return nil
}
