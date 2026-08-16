// Package vm implements a small, fail-closed reference interpreter for the
// sBPF instruction subset emitted by go-solana. It follows sBPF v3 register,
// branch, call-frame, arithmetic, and stack-address semantics; it is not an
// ELF loader or a replacement for Agave's production verifier/runtime.
package vm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/ersanyakit/go-solana/sbpf"
)

const (
	DefaultMaxInstructions = 1_000_000
	DefaultMaxCallDepth    = 64
	DefaultStackFrameSize  = 4_096
	MaxArguments           = 5
)

var (
	ErrInstructionLimit  = errors.New("sBPF instruction limit exceeded")
	ErrDivisionByZero    = errors.New("sBPF division by zero")
	ErrInvalidMemory     = errors.New("invalid sBPF memory access")
	ErrStackOverflow     = errors.New("sBPF stack overflow")
	ErrCallDepthExceeded = errors.New("sBPF call depth exceeded")
	ErrUnsupportedCall   = errors.New("unsupported sBPF call")
	ErrUnsupportedOpcode = errors.New("unsupported sBPF opcode")
	ErrExecutionOverrun  = errors.New("sBPF execution overrun")
	ErrTooManyArguments  = errors.New("too many sBPF arguments")
	ErrInvalidVMConfig   = errors.New("invalid sBPF VM configuration")
)

// Config controls resource bounds. StackFrameSize and MaxCallDepth match the
// upstream defaults; the instruction limit is deliberately explicit and
// bounded for hostile or accidentally non-terminating programs.
type Config struct {
	MaxInstructions int
	MaxCallDepth    int
	StackFrameSize  int
}

func DefaultConfig() Config {
	return Config{
		MaxInstructions: DefaultMaxInstructions,
		MaxCallDepth:    DefaultMaxCallDepth,
		StackFrameSize:  DefaultStackFrameSize,
	}
}

// VM owns a validated immutable logical program. NewVM retains constructor
// errors and reports them from Run, which keeps the convenient API shown in
// the project brief. New and NewBytes return construction errors immediately.
type VM struct {
	program []sbpf.Instruction
	byPC    map[int]int
	slots   int
	config  Config
	initErr error
}

// New validates program and returns a reusable interpreter.
func New(program []sbpf.Instruction) (*VM, error) {
	return NewWithConfig(program, DefaultConfig())
}

// NewVM is the convenience constructor used by examples. Invalid programs are
// still fail-closed: Run returns the retained validation error.
func NewVM(program []sbpf.Instruction) *VM {
	machine, err := New(program)
	if err != nil {
		return &VM{config: DefaultConfig(), initErr: err}
	}
	return machine
}

// NewWithConfig validates a logical program and resource limits.
func NewWithConfig(program []sbpf.Instruction, config Config) (*VM, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if err := sbpf.ValidateProgram(program); err != nil {
		return nil, err
	}
	copyProgram := append([]sbpf.Instruction(nil), program...)
	pcs, slots := sbpf.PhysicalPCs(copyProgram)
	byPC := make(map[int]int, len(copyProgram))
	for i, pc := range pcs {
		byPC[pc] = i
	}
	return &VM{
		program: copyProgram,
		byPC:    byPC,
		slots:   slots,
		config:  config,
	}, nil
}

// NewBytes decodes and validates raw sBPF bytecode.
func NewBytes(bytecode []byte) (*VM, error) {
	return NewBytesWithConfig(bytecode, DefaultConfig())
}

// NewBytesWithConfig decodes raw bytecode and applies explicit resource bounds.
func NewBytesWithConfig(bytecode []byte, config Config) (*VM, error) {
	program, err := sbpf.Decode(bytecode)
	if err != nil {
		return nil, err
	}
	return NewWithConfig(program, config)
}

func validateConfig(config Config) error {
	if config.MaxInstructions <= 0 {
		return fmt.Errorf("%w: MaxInstructions must be positive", ErrInvalidVMConfig)
	}
	if config.MaxCallDepth < 1 {
		return fmt.Errorf("%w: MaxCallDepth must be positive", ErrInvalidVMConfig)
	}
	if config.StackFrameSize <= 0 || config.StackFrameSize > math.MaxInt/config.MaxCallDepth {
		return fmt.Errorf("%w: invalid StackFrameSize %d", ErrInvalidVMConfig, config.StackFrameSize)
	}
	return nil
}

type callFrame struct {
	returnPC     int
	framePointer uint64
	calleeSaved  [4]uint64 // r6-r9
}

type runState struct {
	registers [sbpf.RegisterCount]uint64
	stack     []byte
	regions   []MemoryRegion
	pc        int
	steps     int
	frames    []callFrame
}

// Run executes from physical PC zero. Arguments are placed in r1-r5 and the
// returned value is r0. All state is fresh on every Run call.
func (machine *VM) Run(args ...uint64) (uint64, error) {
	return machine.run(nil, args...)
}

// RunWithMemory executes with explicit sBPF virtual-memory mappings. Pointer
// arguments are ordinary uint64 VM addresses into these mappings; no host/Go
// address is ever placed in a VM register.
func (machine *VM) RunWithMemory(regions []MemoryRegion, args ...uint64) (uint64, error) {
	validated, err := validateMemoryRegions(regions)
	if err != nil {
		return 0, err
	}
	return machine.run(validated, args...)
}

func (machine *VM) run(regions []MemoryRegion, args ...uint64) (uint64, error) {
	if machine == nil {
		return 0, fmt.Errorf("%w: nil VM", ErrInvalidVMConfig)
	}
	if machine.initErr != nil {
		return 0, machine.initErr
	}
	if len(args) > MaxArguments {
		return 0, fmt.Errorf("%w: got %d, maximum %d", ErrTooManyArguments, len(args), MaxArguments)
	}
	stackSize := machine.config.StackFrameSize * machine.config.MaxCallDepth
	state := runState{
		stack:   make([]byte, stackSize),
		regions: regions,
		frames:  make([]callFrame, 0, machine.config.MaxCallDepth-1),
	}
	for i, argument := range args {
		state.registers[i+1] = argument
	}
	// For fixed frames (sBPF v0 and v3+), r10 starts one frame above the
	// stack region base and locals use negative offsets from it.
	state.registers[sbpf.FramePointer] = sbpf.MMStackStart + uint64(machine.config.StackFrameSize)
	return machine.execute(&state)
}

func (machine *VM) execute(state *runState) (uint64, error) {
	for {
		if state.steps >= machine.config.MaxInstructions {
			return 0, fmt.Errorf("%w: limit %d at pc %d", ErrInstructionLimit, machine.config.MaxInstructions, state.pc)
		}
		logicalIndex, ok := machine.byPC[state.pc]
		if !ok {
			return 0, fmt.Errorf("%w: pc %d is outside code or inside LD_DW_IMM", ErrExecutionOverrun, state.pc)
		}
		ins := machine.program[logicalIndex]
		state.steps++
		nextPC := state.pc + ins.Slots()

		dst := &state.registers[ins.Dst]
		src := state.registers[ins.Src]
		switch ins.Op {
		case sbpf.LD_DW_IMM:
			*dst = uint64(ins.Immediate)
		case sbpf.LD_B_REG, sbpf.LD_H_REG, sbpf.LD_W_REG, sbpf.LD_DW_REG:
			address := addSignedOffset(src, ins.Offset)
			value, err := state.loadMemory(address, ins.MemoryType())
			if err != nil {
				return 0, pcError(state.pc, err)
			}
			*dst = value
		case sbpf.ST_B_REG, sbpf.ST_H_REG, sbpf.ST_W_REG, sbpf.ST_DW_REG:
			address := addSignedOffset(*dst, ins.Offset)
			if err := state.storeMemory(address, src, ins.MemoryType()); err != nil {
				return 0, pcError(state.pc, err)
			}
		case sbpf.MOV64_IMM:
			*dst = uint64(ins.Immediate)
		case sbpf.MOV64_REG:
			*dst = src
		case sbpf.MOV32_IMM:
			*dst = uint64(uint32(ins.Immediate))
		case sbpf.MOV32_REG:
			*dst = uint64(uint32(src))
		case sbpf.ADD32_IMM:
			*dst = signExtend32(uint32(*dst) + uint32(ins.Immediate))
		case sbpf.ADD32_REG:
			*dst = signExtend32(uint32(*dst) + uint32(src))
		case sbpf.SUB32_IMM:
			*dst = signExtend32(uint32(*dst) - uint32(ins.Immediate))
		case sbpf.SUB32_REG:
			*dst = signExtend32(uint32(*dst) - uint32(src))
		case sbpf.MUL32_IMM:
			*dst = signExtend32(uint32(*dst) * uint32(ins.Immediate))
		case sbpf.MUL32_REG:
			*dst = signExtend32(uint32(*dst) * uint32(src))
		case sbpf.DIV32_IMM:
			divisor := uint32(ins.Immediate)
			if divisor == 0 {
				return 0, pcError(state.pc, ErrDivisionByZero)
			}
			*dst = uint64(uint32(*dst) / divisor)
		case sbpf.DIV32_REG:
			divisor := uint32(src)
			if divisor == 0 {
				return 0, pcError(state.pc, ErrDivisionByZero)
			}
			*dst = uint64(uint32(*dst) / divisor)
		case sbpf.MOD32_IMM:
			divisor := uint32(ins.Immediate)
			if divisor == 0 {
				return 0, pcError(state.pc, ErrDivisionByZero)
			}
			*dst = uint64(uint32(*dst) % divisor)
		case sbpf.MOD32_REG:
			divisor := uint32(src)
			if divisor == 0 {
				return 0, pcError(state.pc, ErrDivisionByZero)
			}
			*dst = uint64(uint32(*dst) % divisor)
		case sbpf.LSH32_IMM:
			*dst = uint64(uint32(*dst) << uint32(ins.Immediate))
		case sbpf.LSH32_REG:
			*dst = uint64(uint32(*dst) << (uint32(src) & 31))
		case sbpf.RSH32_IMM:
			*dst = uint64(uint32(*dst) >> uint32(ins.Immediate))
		case sbpf.RSH32_REG:
			*dst = uint64(uint32(*dst) >> (uint32(src) & 31))
		case sbpf.ARSH32_IMM:
			*dst = uint64(uint32(int32(*dst) >> uint32(ins.Immediate)))
		case sbpf.ARSH32_REG:
			*dst = uint64(uint32(int32(*dst) >> (uint32(src) & 31)))
		case sbpf.NEG32:
			*dst = uint64(uint32(-int32(*dst)))
		case sbpf.XOR32_IMM:
			*dst = uint64(uint32(*dst) ^ uint32(ins.Immediate))
		case sbpf.XOR32_REG:
			*dst = uint64(uint32(*dst) ^ uint32(src))
		case sbpf.ADD64_IMM:
			*dst += uint64(ins.Immediate)
		case sbpf.ADD64_REG:
			*dst += src
		case sbpf.SUB64_IMM:
			*dst -= uint64(ins.Immediate)
		case sbpf.SUB64_REG:
			*dst -= src
		case sbpf.MUL64_IMM:
			*dst *= uint64(ins.Immediate)
		case sbpf.MUL64_REG:
			*dst *= src
		case sbpf.DIV64_IMM:
			// Immediate zero is rejected during verification, but keep the
			// runtime guard so this interpreter remains fail-closed.
			divisor := uint64(ins.Immediate)
			if divisor == 0 {
				return 0, pcError(state.pc, ErrDivisionByZero)
			}
			*dst /= divisor
		case sbpf.DIV64_REG:
			if src == 0 {
				return 0, pcError(state.pc, ErrDivisionByZero)
			}
			*dst /= src
		case sbpf.MOD64_IMM:
			divisor := uint64(ins.Immediate)
			if divisor == 0 {
				return 0, pcError(state.pc, ErrDivisionByZero)
			}
			*dst %= divisor
		case sbpf.MOD64_REG:
			if src == 0 {
				return 0, pcError(state.pc, ErrDivisionByZero)
			}
			*dst %= src
		case sbpf.LSH64_IMM:
			*dst <<= uint32(ins.Immediate)
		case sbpf.LSH64_REG:
			*dst <<= uint32(src) & 63
		case sbpf.RSH64_IMM:
			*dst >>= uint32(ins.Immediate)
		case sbpf.RSH64_REG:
			*dst >>= uint32(src) & 63
		case sbpf.ARSH64_IMM:
			*dst = uint64(int64(*dst) >> uint32(ins.Immediate))
		case sbpf.ARSH64_REG:
			*dst = uint64(int64(*dst) >> (uint32(src) & 63))
		case sbpf.NEG64:
			*dst = 0 - *dst
		case sbpf.XOR64_IMM:
			*dst ^= uint64(ins.Immediate)
		case sbpf.XOR64_REG:
			*dst ^= src

		case sbpf.JA:
			nextPC = state.pc + 1 + int(ins.Offset)
		case sbpf.JEQ32_IMM:
			if uint32(*dst) == uint32(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JEQ32_REG:
			if uint32(*dst) == uint32(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JNE32_IMM:
			if uint32(*dst) != uint32(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JNE32_REG:
			if uint32(*dst) != uint32(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JGT32_IMM:
			if uint32(*dst) > uint32(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JGT32_REG:
			if uint32(*dst) > uint32(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JGE32_IMM:
			if uint32(*dst) >= uint32(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JGE32_REG:
			if uint32(*dst) >= uint32(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JLT32_IMM:
			if uint32(*dst) < uint32(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JLT32_REG:
			if uint32(*dst) < uint32(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JLE32_IMM:
			if uint32(*dst) <= uint32(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JLE32_REG:
			if uint32(*dst) <= uint32(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSGT32_IMM:
			if int32(*dst) > int32(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSGT32_REG:
			if int32(*dst) > int32(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSGE32_IMM:
			if int32(*dst) >= int32(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSGE32_REG:
			if int32(*dst) >= int32(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSLT32_IMM:
			if int32(*dst) < int32(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSLT32_REG:
			if int32(*dst) < int32(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSLE32_IMM:
			if int32(*dst) <= int32(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSLE32_REG:
			if int32(*dst) <= int32(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JEQ64_IMM:
			if *dst == uint64(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JEQ64_REG:
			if *dst == src {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JNE64_IMM:
			if *dst != uint64(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JNE64_REG:
			if *dst != src {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JGT64_IMM:
			if *dst > uint64(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JGT64_REG:
			if *dst > src {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JGE64_IMM:
			if *dst >= uint64(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JGE64_REG:
			if *dst >= src {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JLT64_IMM:
			if *dst < uint64(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JLT64_REG:
			if *dst < src {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JLE64_IMM:
			if *dst <= uint64(ins.Immediate) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JLE64_REG:
			if *dst <= src {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSGT64_IMM:
			if int64(*dst) > ins.Immediate {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSGT64_REG:
			if int64(*dst) > int64(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSGE64_IMM:
			if int64(*dst) >= ins.Immediate {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSGE64_REG:
			if int64(*dst) >= int64(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSLT64_IMM:
			if int64(*dst) < ins.Immediate {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSLT64_REG:
			if int64(*dst) < int64(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSLE64_IMM:
			if int64(*dst) <= ins.Immediate {
				nextPC = state.pc + 1 + int(ins.Offset)
			}
		case sbpf.JSLE64_REG:
			if int64(*dst) <= int64(src) {
				nextPC = state.pc + 1 + int(ins.Offset)
			}

		case sbpf.CALL_IMM:
			if ins.Src != 1 {
				return 0, fmt.Errorf("%w at pc %d: static syscall key 0x%08x has no registered host function",
					ErrUnsupportedCall, state.pc, uint32(ins.Immediate))
			}
			if len(state.frames)+1 >= machine.config.MaxCallDepth {
				return 0, fmt.Errorf("%w: %w at pc %d (maximum %d)", ErrStackOverflow, ErrCallDepthExceeded, state.pc, machine.config.MaxCallDepth)
			}
			frame := callFrame{
				returnPC:     state.pc + 1,
				framePointer: state.registers[sbpf.FramePointer],
			}
			copy(frame.calleeSaved[:], state.registers[sbpf.R6:sbpf.R10])
			state.frames = append(state.frames, frame)
			state.registers[sbpf.FramePointer] += uint64(machine.config.StackFrameSize)
			nextPC = state.pc + 1 + int(ins.Immediate)

		case sbpf.EXIT:
			if len(state.frames) == 0 {
				return state.registers[sbpf.R0], nil
			}
			last := len(state.frames) - 1
			frame := state.frames[last]
			state.frames = state.frames[:last]
			state.registers[sbpf.FramePointer] = frame.framePointer
			copy(state.registers[sbpf.R6:sbpf.R10], frame.calleeSaved[:])
			nextPC = frame.returnPC

		default:
			return 0, fmt.Errorf("%w 0x%02x at pc %d", ErrUnsupportedOpcode, uint8(ins.Op), state.pc)
		}

		if nextPC < 0 || nextPC >= machine.slots {
			return 0, fmt.Errorf("%w: next pc %d after pc %d", ErrExecutionOverrun, nextPC, state.pc)
		}
		if _, ok := machine.byPC[nextPC]; !ok {
			return 0, fmt.Errorf("%w: next pc %d is inside LD_DW_IMM", ErrExecutionOverrun, nextPC)
		}
		state.pc = nextPC
	}
}

func pcError(pc int, err error) error { return fmt.Errorf("pc %d: %w", pc, err) }

func addSignedOffset(base uint64, offset int16) uint64 {
	return uint64(int64(base) + int64(offset))
}

func signExtend32(value uint32) uint64 { return uint64(int64(int32(value))) }

func (state *runState) loadMemory(address uint64, kind sbpf.MemoryType) (uint64, error) {
	width := kind.Bytes()
	memory, err := state.memoryRange(address, width, false)
	if err != nil {
		return 0, err
	}
	switch kind {
	case sbpf.MemoryByte:
		return uint64(memory[0]), nil
	case sbpf.MemoryHalf:
		return uint64(binary.LittleEndian.Uint16(memory)), nil
	case sbpf.MemoryWord:
		return uint64(binary.LittleEndian.Uint32(memory)), nil
	case sbpf.MemoryDoubleWord:
		return binary.LittleEndian.Uint64(memory), nil
	default:
		return 0, fmt.Errorf("%w: unsupported memory width %d", ErrInvalidMemory, kind)
	}
}

func (state *runState) storeMemory(address, value uint64, kind sbpf.MemoryType) error {
	width := kind.Bytes()
	memory, err := state.memoryRange(address, width, true)
	if err != nil {
		return err
	}
	switch kind {
	case sbpf.MemoryByte:
		memory[0] = byte(value)
	case sbpf.MemoryHalf:
		binary.LittleEndian.PutUint16(memory, uint16(value))
	case sbpf.MemoryWord:
		binary.LittleEndian.PutUint32(memory, uint32(value))
	case sbpf.MemoryDoubleWord:
		binary.LittleEndian.PutUint64(memory, value)
	default:
		return fmt.Errorf("%w: unsupported memory width %d", ErrInvalidMemory, kind)
	}
	return nil
}
