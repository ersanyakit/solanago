package compiler

import (
	"fmt"

	"github.com/ersanyakit/go-solana/sbpf"
)

const (
	// sBPF v3 uses fixed 4096-byte frames. r10 points one byte past the
	// current frame and spill slots are addressed with negative offsets.
	stackFrameSize = 4096
	spillSlotSize  = 8
)

// valueLocation is the physical home assigned to one IR virtual register.
// Register homes are preferred; values beyond the four call-preserved
// registers are spilled into the current fixed stack frame.
type valueLocation struct {
	reg         sbpf.Register
	stackOffset int16
	stack       bool
}

func registerLocation(reg sbpf.Register) valueLocation { return valueLocation{reg: reg} }

func stackLocation(offset int16) valueLocation {
	return valueLocation{stackOffset: offset, stack: true}
}

func (l valueLocation) equal(other valueLocation) bool {
	if l.stack != other.stack {
		return false
	}
	if l.stack {
		return l.stackOffset == other.stackOffset
	}
	return l.reg == other.reg
}

type registerAllocation struct {
	homes   map[ValueID]valueLocation
	objects map[MemoryObjectID]int16
}

func allocateRegisters(function *Function) (*registerAllocation, error) {
	allocation := &registerAllocation{
		homes:   make(map[ValueID]valueLocation, len(function.Values)),
		objects: make(map[MemoryObjectID]int16, len(function.Memory)),
	}
	hasCall := functionHasCall(function)
	fastReturns := directReturnValues(function)

	// r0 is reserved for a value produced by the final instruction immediately
	// returned from a block. r4/r5 are fixed instruction-selection scratch
	// registers. r6-r9 are preserved by the upstream sBPF call-frame logic.
	preserved := []sbpf.Register{sbpf.R6, sbpf.R7, sbpf.R8, sbpf.R9}
	nextPreserved := 0
	usedStack := 0
	for _, object := range function.Memory {
		alignment := int(object.Element.memoryBytes())
		if alignment > spillSlotSize {
			alignment = spillSlotSize
		}
		usedStack = alignStack(usedStack, alignment)
		usedStack += int(object.Size())
		if usedStack > stackFrameSize {
			return nil, fmt.Errorf("function %s requires %d bytes for addressable locals; sBPF v3 frame limit is %d",
				function.Name, usedStack, stackFrameSize)
		}
		allocation.objects[object.ID] = int16(-usedStack)
	}
	usedStack = alignStack(usedStack, spillSlotSize)
	assignPersistent := func(id ValueID) error {
		if _, fast := fastReturns[id]; fast {
			allocation.homes[id] = registerLocation(sbpf.R0)
			return nil
		}
		if nextPreserved < len(preserved) {
			allocation.homes[id] = registerLocation(preserved[nextPreserved])
			nextPreserved++
			return nil
		}
		usedStack += spillSlotSize
		if usedStack > stackFrameSize {
			return fmt.Errorf("function %s requires %d spill bytes; sBPF v3 frame limit is %d",
				function.Name, usedStack, stackFrameSize)
		}
		allocation.homes[id] = stackLocation(int16(-usedStack))
		return nil
	}

	parameterIndex := make(map[ValueID]int, len(function.Params))
	for index, id := range function.Params {
		parameterIndex[id] = index
	}
	for _, value := range function.Values {
		if index, parameter := parameterIndex[value.ID]; parameter {
			// Without calls, the first three parameters can stay in their ABI
			// registers. r4/r5 remain reserved so instruction selection always
			// has two safe scratch registers. A function containing any call
			// relocates every parameter because r1-r5 are caller-clobbered.
			if !hasCall && index < 3 {
				allocation.homes[value.ID] = registerLocation(sbpf.Register(index + 1))
				continue
			}
		}
		if err := assignPersistent(value.ID); err != nil {
			return nil, err
		}
	}
	return allocation, nil
}

func alignStack(value, alignment int) int {
	if alignment <= 1 {
		return value
	}
	return (value + alignment - 1) &^ (alignment - 1)
}

func functionHasCall(function *Function) bool {
	for _, block := range function.Blocks {
		for _, instruction := range block.Instructions {
			if instruction.Op == OpCall || instruction.Op == OpSyscall {
				return true
			}
		}
	}
	return false
}

// directReturnValues identifies a deliberately narrow r0 coalescing case.
// It is enough to make `return a + b` select the canonical three-instruction
// sequence while keeping general CFG allocation conservative.
func directReturnValues(function *Function) map[ValueID]struct{} {
	uses := make(map[ValueID]int)
	definitions := make(map[ValueID]int)
	for _, block := range function.Blocks {
		for _, instruction := range block.Instructions {
			if instruction.Dest != NoValue {
				definitions[instruction.Dest]++
			}
			switch instruction.Op {
			case OpMove, OpPointerAddress:
				uses[instruction.X]++
			case OpAdd, OpSub, OpMul, OpDiv, OpMod, OpEqual, OpNotEqual,
				OpLess, OpLessEqual, OpGreater, OpGreaterEqual:
				uses[instruction.X]++
				uses[instruction.Y]++
			case OpCall, OpSyscall:
				for _, argument := range instruction.Args {
					uses[argument]++
				}
			case OpLoad:
				uses[instruction.X]++
			case OpStore, OpPointerAdd:
				uses[instruction.X]++
				uses[instruction.Y]++
			case OpBoundsCheck:
				uses[instruction.X]++
			}
		}
		switch block.Terminator.Kind {
		case TermBranch:
			uses[block.Terminator.Cond]++
		case TermReturn:
			if block.Terminator.Result != NoValue {
				uses[block.Terminator.Result]++
			}
		}
	}

	result := make(map[ValueID]struct{})
	for _, block := range function.Blocks {
		if block.Terminator.Kind != TermReturn || block.Terminator.Result == NoValue || len(block.Instructions) == 0 {
			continue
		}
		last := block.Instructions[len(block.Instructions)-1]
		id := block.Terminator.Result
		value, ok := function.Value(id)
		if ok && value.Kind == ValueTemporary && last.Dest == id && uses[id] == 1 && definitions[id] == 1 {
			result[id] = struct{}{}
		}
	}
	return result
}
