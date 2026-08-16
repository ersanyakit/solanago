package compiler

import (
	"fmt"
	"math"

	"github.com/ersanyakit/go-solana/sbpf"
)

// Executable is an sBPFv3 raw text program. Bytecode is intentionally not an
// ELF file and therefore is not directly deployable to a Solana validator.
type Executable struct {
	Entry        string
	Instructions []sbpf.Instruction
	Bytecode     []byte
	Functions    map[string]int // physical instruction-slot PCs
}

// Generate selects sBPFv3 instructions from validated IR. The selected entry
// function is placed at physical PC zero; other source functions follow it.
func Generate(program *Program, entry string) (*Executable, error) {
	if err := program.Validate(); err != nil {
		return nil, err
	}
	if entry == "" {
		if len(program.Functions) == 0 {
			return nil, fmt.Errorf("compiler: program has no functions")
		}
		entry = program.Functions[0].Name
	}
	entryFunction, ok := program.Function(entry)
	if !ok {
		return nil, fmt.Errorf("compiler: entry function %q does not exist", entry)
	}

	reachable := reachableFunctions(program, entryFunction)
	ordered := make([]*Function, 0, len(reachable))
	ordered = append(ordered, entryFunction)
	for _, function := range program.Functions {
		if function != entryFunction {
			if _, ok := reachable[function.Name]; !ok {
				continue
			}
			ordered = append(ordered, function)
		}
	}

	selector := &instructionSelector{
		functions: make(map[string]int, len(ordered)),
	}
	for _, function := range ordered {
		if err := selector.emitFunction(function); err != nil {
			return nil, err
		}
	}
	if err := selector.patchFixups(); err != nil {
		return nil, err
	}
	bytecode, err := sbpf.Encode(selector.program)
	if err != nil {
		return nil, fmt.Errorf("compiler: validate encoded program: %w", err)
	}
	return &Executable{
		Entry:        entry,
		Instructions: append([]sbpf.Instruction(nil), selector.program...),
		Bytecode:     bytecode,
		Functions:    selector.functions,
	}, nil
}

func reachableFunctions(program *Program, entry *Function) map[string]struct{} {
	reachable := make(map[string]struct{})
	var visit func(*Function)
	visit = func(function *Function) {
		if _, seen := reachable[function.Name]; seen {
			return
		}
		reachable[function.Name] = struct{}{}
		for _, block := range function.Blocks {
			for _, instruction := range block.Instructions {
				if instruction.Op != OpCall {
					continue
				}
				if callee, ok := program.Function(instruction.Callee); ok {
					visit(callee)
				}
			}
		}
	}
	visit(entry)
	return reachable
}

// Compile performs the complete source -> checked IR -> sBPFv3 pipeline.
func Compile(filename string, source []byte, entry string) (*Executable, error) {
	program, err := CompileSource(filename, source)
	if err != nil {
		return nil, err
	}
	return Generate(program, entry)
}

type fixupKind uint8

const (
	fixupJump fixupKind = iota
	fixupCall
)

type codeFixup struct {
	kind     fixupKind
	index    int
	sourcePC int
	function string
	block    BlockID
	owner    string
}

type instructionSelector struct {
	program    []sbpf.Instruction
	pc         int
	functions  map[string]int
	blocks     map[string]map[BlockID]int
	fixups     []codeFixup
	function   *Function
	allocation *registerAllocation
}

func (s *instructionSelector) emit(instruction sbpf.Instruction) int {
	index := len(s.program)
	s.program = append(s.program, instruction)
	s.pc += instruction.Slots()
	return index
}

func (s *instructionSelector) emitFunction(function *Function) error {
	allocation, err := allocateRegisters(function)
	if err != nil {
		return err
	}
	s.function = function
	s.allocation = allocation
	if s.blocks == nil {
		s.blocks = make(map[string]map[BlockID]int)
	}
	s.blocks[function.Name] = make(map[BlockID]int, len(function.Blocks))
	s.functions[function.Name] = s.pc

	// Materialize ABI parameters in their allocated homes before the entry
	// block. Internal branches to the entry block intentionally skip this.
	for index, id := range function.Params {
		incoming := sbpf.Register(index + 1)
		parameter, _ := function.Value(id)
		if err := s.normalizeRegister(parameter.Type, incoming); err != nil {
			return s.fail("parameter %d: %v", index+1, err)
		}
		if err := s.writeValue(id, incoming); err != nil {
			return s.fail("parameter %d: %v", index+1, err)
		}
	}

	blocks := make([]*BasicBlock, 0, len(function.Blocks))
	entry, _ := function.Block(function.Entry)
	blocks = append(blocks, entry)
	for _, block := range function.Blocks {
		if block.ID != function.Entry {
			blocks = append(blocks, block)
		}
	}
	for _, block := range blocks {
		s.blocks[function.Name][block.ID] = s.pc
		for index, instruction := range block.Instructions {
			if err := s.emitIRInstruction(instruction); err != nil {
				return s.fail("block %s instruction %d (%s): %v", block.Name, index, instruction.Op, err)
			}
		}
		if err := s.emitTerminator(block.Terminator); err != nil {
			return s.fail("block %s terminator: %v", block.Name, err)
		}
	}
	return nil
}

func (s *instructionSelector) emitIRInstruction(instruction Instruction) error {
	switch instruction.Op {
	case OpConst:
		return s.emitConstant(instruction.Dest, instruction.Imm)
	case OpMove:
		return s.moveValue(instruction.Dest, instruction.X)
	case OpAdd, OpSub, OpMul:
		return s.emitArithmetic(instruction)
	case OpDiv, OpMod:
		value, _ := s.function.Value(instruction.Dest)
		if value.Type.signed() {
			return s.emitSignedDivisionOrRemainder(instruction)
		}
		return s.emitArithmetic(instruction)
	case OpEqual, OpNotEqual, OpLess, OpLessEqual, OpGreater, OpGreaterEqual:
		return s.emitComparison(instruction)
	case OpCall:
		for index, argument := range instruction.Args {
			if err := s.readValue(argument, sbpf.Register(index+1)); err != nil {
				return fmt.Errorf("argument %d: %w", index+1, err)
			}
		}
		callIndex := s.emit(sbpf.CallRelative(0))
		s.fixups = append(s.fixups, codeFixup{
			kind: fixupCall, index: callIndex, sourcePC: s.pc - 1,
			function: instruction.Callee, owner: s.function.Name,
		})
		if instruction.Dest != NoValue {
			return s.writeValue(instruction.Dest, sbpf.R0)
		}
		return nil
	case OpSyscall:
		for index, argument := range instruction.Args {
			if err := s.readValue(argument, sbpf.Register(index+1)); err != nil {
				return fmt.Errorf("syscall argument %d: %w", index+1, err)
			}
		}
		s.emit(sbpf.StaticSyscall(instruction.SyscallID))
		if instruction.Dest != NoValue {
			return s.writeValue(instruction.Dest, sbpf.R0)
		}
		return nil
	case OpAddress:
		return s.emitAddress(instruction)
	case OpPointerAddress:
		return s.moveValue(instruction.Dest, instruction.X)
	case OpLoad:
		return s.emitLoad(instruction)
	case OpStore:
		return s.emitStore(instruction)
	case OpPointerAdd:
		return s.emitPointerAdd(instruction)
	case OpBoundsCheck:
		return s.emitBoundsCheck(instruction)
	case OpZeroMemory:
		return s.emitZeroMemory(instruction)
	case OpCopyMemory:
		return s.emitCopyMemory(instruction)
	default:
		return fmt.Errorf("unsupported IR opcode %s", instruction.Op)
	}
}

func (s *instructionSelector) emitConstant(destination ValueID, value uint64) error {
	location, ok := s.allocation.homes[destination]
	if !ok {
		return fmt.Errorf("value %d has no allocated home", destination)
	}
	target := sbpf.R0
	if !location.stack {
		target = location.reg
	}
	if value == uint64(int64(int32(value))) {
		s.emit(sbpf.ALUImm(sbpf.MOV64_IMM, target, int32(value)))
	} else {
		s.emit(sbpf.LoadImm64(target, value))
	}
	destinationValue, _ := s.function.Value(destination)
	if err := s.normalizeRegister(destinationValue.Type, target); err != nil {
		return err
	}
	if location.stack {
		s.emit(sbpf.Instruction{Op: sbpf.ST_DW_REG, Dst: sbpf.R10, Src: target, Offset: location.stackOffset})
	}
	return nil
}

func (s *instructionSelector) moveValue(destination, source ValueID) error {
	destinationLocation, destinationOK := s.allocation.homes[destination]
	sourceLocation, sourceOK := s.allocation.homes[source]
	if !destinationOK || !sourceOK {
		return fmt.Errorf("move references an unallocated value")
	}
	destinationValue, _ := s.function.Value(destination)
	sourceValue, _ := s.function.Value(source)
	if destinationLocation.equal(sourceLocation) && destinationValue.Type == sourceValue.Type {
		return nil
	}
	target := sbpf.R0
	if !destinationLocation.stack {
		target = destinationLocation.reg
	}
	if err := s.readValue(source, target); err != nil {
		return err
	}
	if err := s.normalizeRegister(destinationValue.Type, target); err != nil {
		return err
	}
	if destinationLocation.stack {
		s.emit(sbpf.Instruction{Op: sbpf.ST_DW_REG, Dst: sbpf.R10, Src: target, Offset: destinationLocation.stackOffset})
	}
	return nil
}

func (s *instructionSelector) emitArithmetic(instruction Instruction) error {
	destination := s.allocation.homes[instruction.Dest]
	x := s.allocation.homes[instruction.X]
	y := s.allocation.homes[instruction.Y]
	target := sbpf.R0
	if !destination.stack {
		target = destination.reg
	}

	// Preserve Y before overwriting an aliased destination.
	yRegister := sbpf.R4
	if !y.stack {
		yRegister = y.reg
		if y.reg == target && !x.equal(destination) {
			s.emit(sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R4, y.reg))
			yRegister = sbpf.R4
		}
	} else {
		s.emit(sbpf.Instruction{Op: sbpf.LD_DW_REG, Dst: sbpf.R4, Src: sbpf.R10, Offset: y.stackOffset})
	}
	if x.stack {
		s.emit(sbpf.Instruction{Op: sbpf.LD_DW_REG, Dst: target, Src: sbpf.R10, Offset: x.stackOffset})
	} else if x.reg != target {
		s.emit(sbpf.ALUReg(sbpf.MOV64_REG, target, x.reg))
	}

	value, _ := s.function.Value(instruction.Dest)
	opcode64 := map[Op]sbpf.Opcode{
		OpAdd: sbpf.ADD64_REG, OpSub: sbpf.SUB64_REG, OpMul: sbpf.MUL64_REG,
		OpDiv: sbpf.DIV64_REG, OpMod: sbpf.MOD64_REG,
	}
	opcode32 := map[Op]sbpf.Opcode{
		OpAdd: sbpf.ADD32_REG, OpSub: sbpf.SUB32_REG, OpMul: sbpf.MUL32_REG,
		OpDiv: sbpf.DIV32_REG, OpMod: sbpf.MOD32_REG,
	}
	opcode := opcode64[instruction.Op]
	if bits := value.Type.bits(); bits != 0 && bits <= 32 {
		opcode = opcode32[instruction.Op]
	}
	s.emit(sbpf.ALUReg(opcode, target, yRegister))
	if err := s.normalizeRegister(value.Type, target); err != nil {
		return err
	}
	if destination.stack {
		s.emit(sbpf.Instruction{Op: sbpf.ST_DW_REG, Dst: sbpf.R10, Src: target, Offset: destination.stackOffset})
	}
	return nil
}

func (s *instructionSelector) emitSignedDivisionOrRemainder(instruction Instruction) error {
	if err := s.readValue(instruction.X, sbpf.R0); err != nil {
		return err
	}
	if err := s.readValue(instruction.Y, sbpf.R4); err != nil {
		return err
	}
	value, _ := s.function.Value(instruction.Dest)
	bits32 := value.Type.bits() == 32
	negativeJump := sbpf.JSLT64_IMM
	negate := sbpf.NEG64
	divide := sbpf.DIV64_REG
	remainder := sbpf.MOD64_REG
	if bits32 {
		negativeJump = sbpf.JSLT32_IMM
		negate = sbpf.NEG32
		divide = sbpf.DIV32_REG
		remainder = sbpf.MOD32_REG
	}
	// V3 intentionally has no PQR signed division/remainder instruction. Work
	// on two's-complement magnitudes and restore Go's quotient/remainder sign.
	s.emit(sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R5, sbpf.R0))
	if instruction.Op == OpDiv {
		xor := sbpf.XOR64_REG
		if bits32 {
			xor = sbpf.XOR32_REG
		}
		s.emit(sbpf.ALUReg(xor, sbpf.R5, sbpf.R4))
	}
	// Jump over NEG when the operand is non-negative.
	nonnegativeJump := sbpf.JSGE64_IMM
	if bits32 {
		nonnegativeJump = sbpf.JSGE32_IMM
	}
	s.emit(sbpf.JumpImm(nonnegativeJump, sbpf.R0, 0, 1))
	s.emit(sbpf.Instruction{Op: negate, Dst: sbpf.R0})
	s.emit(sbpf.JumpImm(nonnegativeJump, sbpf.R4, 0, 1))
	s.emit(sbpf.Instruction{Op: negate, Dst: sbpf.R4})
	operation := divide
	if instruction.Op == OpMod {
		operation = remainder
	}
	s.emit(sbpf.ALUReg(operation, sbpf.R0, sbpf.R4))
	s.emit(sbpf.JumpImm(negativeJump, sbpf.R5, 0, 1))
	s.emit(sbpf.Jump(1))
	s.emit(sbpf.Instruction{Op: negate, Dst: sbpf.R0})
	if err := s.normalizeRegister(value.Type, sbpf.R0); err != nil {
		return err
	}
	return s.writeValue(instruction.Dest, sbpf.R0)
}

func (s *instructionSelector) emitComparison(instruction Instruction) error {
	if err := s.readValue(instruction.X, sbpf.R0); err != nil {
		return err
	}
	if err := s.readValue(instruction.Y, sbpf.R4); err != nil {
		return err
	}
	x, _ := s.function.Value(instruction.X)
	opcode, err := comparisonOpcode(instruction.Op, x.Type)
	if err != nil {
		return err
	}
	s.emit(sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R5, 0))
	s.emit(sbpf.JumpReg(opcode, sbpf.R0, sbpf.R4, 1))
	s.emit(sbpf.Jump(1))
	s.emit(sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R5, 1))
	return s.writeValue(instruction.Dest, sbpf.R5)
}

func comparisonOpcode(operation Op, valueType Type) (sbpf.Opcode, error) {
	signed := valueType.signed()
	bits32 := valueType.bits() != 0 && valueType.bits() <= 32
	switch operation {
	case OpEqual:
		if bits32 {
			return sbpf.JEQ32_REG, nil
		}
		return sbpf.JEQ64_REG, nil
	case OpNotEqual:
		if bits32 {
			return sbpf.JNE32_REG, nil
		}
		return sbpf.JNE64_REG, nil
	case OpLess:
		if bits32 && signed {
			return sbpf.JSLT32_REG, nil
		}
		if bits32 {
			return sbpf.JLT32_REG, nil
		}
		if signed {
			return sbpf.JSLT64_REG, nil
		}
		return sbpf.JLT64_REG, nil
	case OpLessEqual:
		if bits32 && signed {
			return sbpf.JSLE32_REG, nil
		}
		if bits32 {
			return sbpf.JLE32_REG, nil
		}
		if signed {
			return sbpf.JSLE64_REG, nil
		}
		return sbpf.JLE64_REG, nil
	case OpGreater:
		if bits32 && signed {
			return sbpf.JSGT32_REG, nil
		}
		if bits32 {
			return sbpf.JGT32_REG, nil
		}
		if signed {
			return sbpf.JSGT64_REG, nil
		}
		return sbpf.JGT64_REG, nil
	case OpGreaterEqual:
		if bits32 && signed {
			return sbpf.JSGE32_REG, nil
		}
		if bits32 {
			return sbpf.JGE32_REG, nil
		}
		if signed {
			return sbpf.JSGE64_REG, nil
		}
		return sbpf.JGE64_REG, nil
	default:
		return 0, fmt.Errorf("unsupported comparison opcode %s", operation)
	}
}

func (s *instructionSelector) normalizeRegister(valueType Type, register sbpf.Register) error {
	switch valueType {
	case TypeUint8:
		// sBPF has no byte-register type. Keep the low eight bits and
		// zero-extend them to the register ABI, matching Go uint8.
		s.emit(sbpf.ALUReg(sbpf.MOV32_REG, register, register))
		s.emit(sbpf.ALUImm(sbpf.LSH32_IMM, register, 24))
		s.emit(sbpf.ALUImm(sbpf.RSH32_IMM, register, 24))
	case TypeUint32:
		s.emit(sbpf.ALUReg(sbpf.MOV32_REG, register, register))
	case TypeInt32:
		s.emit(sbpf.ALUReg(sbpf.MOV32_REG, register, register))
		s.emit(sbpf.ALUImm(sbpf.LSH64_IMM, register, 32))
		s.emit(sbpf.ALUImm(sbpf.ARSH64_IMM, register, 32))
	case TypeUint64, TypeInt64, TypeBool:
		return nil
	default:
		if _, pointer := valueType.PointerElement(); pointer {
			return nil
		}
		return fmt.Errorf("cannot normalize unsupported register type %s", valueType)
	}
	return nil
}

func sbpfMemoryType(memory MemoryType) (sbpf.MemoryType, error) {
	switch memory {
	case MemoryBool, MemoryUint8, MemoryByte:
		return sbpf.MemoryByte, nil
	case MemoryUint16:
		return sbpf.MemoryHalf, nil
	case MemoryUint32, MemoryInt32:
		return sbpf.MemoryWord, nil
	case MemoryUint64, MemoryInt64:
		return sbpf.MemoryDoubleWord, nil
	default:
		return sbpf.MemoryInvalid, fmt.Errorf("invalid explicit memory type %d", memory)
	}
}

func (s *instructionSelector) emitAddress(instruction Instruction) error {
	offset, ok := s.allocation.objects[instruction.Object]
	if !ok {
		return fmt.Errorf("memory object %d has no stack allocation", instruction.Object)
	}
	location := s.allocation.homes[instruction.Dest]
	target := sbpf.R0
	if !location.stack {
		target = location.reg
	}
	s.emit(sbpf.ALUReg(sbpf.MOV64_REG, target, sbpf.R10))
	if offset != 0 {
		s.emit(sbpf.ALUImm(sbpf.ADD64_IMM, target, int32(offset)))
	}
	if location.stack {
		s.emit(sbpf.Instruction{Op: sbpf.ST_DW_REG, Dst: sbpf.R10, Src: target, Offset: location.stackOffset})
	}
	return nil
}

func (s *instructionSelector) emitLoad(instruction Instruction) error {
	if err := s.readValue(instruction.X, sbpf.R4); err != nil {
		return err
	}
	location := s.allocation.homes[instruction.Dest]
	target := sbpf.R0
	if !location.stack {
		target = location.reg
	}
	kind, err := sbpfMemoryType(instruction.Memory)
	if err != nil {
		return err
	}
	load, err := sbpf.LoadMemory(kind, target, sbpf.R4, 0)
	if err != nil {
		return err
	}
	s.emit(load)
	value, _ := s.function.Value(instruction.Dest)
	if err := s.normalizeRegister(value.Type, target); err != nil {
		return err
	}
	if value.Type == TypeBool {
		// Canonicalize an externally supplied byte to Go's 0/1 bool form.
		s.emit(sbpf.JumpImm(sbpf.JEQ64_IMM, target, 0, 1))
		s.emit(sbpf.ALUImm(sbpf.MOV64_IMM, target, 1))
	}
	if location.stack {
		s.emit(sbpf.Instruction{Op: sbpf.ST_DW_REG, Dst: sbpf.R10, Src: target, Offset: location.stackOffset})
	}
	return nil
}

func (s *instructionSelector) emitStore(instruction Instruction) error {
	if err := s.readValue(instruction.X, sbpf.R4); err != nil {
		return err
	}
	if err := s.readValue(instruction.Y, sbpf.R5); err != nil {
		return err
	}
	kind, err := sbpfMemoryType(instruction.Memory)
	if err != nil {
		return err
	}
	store, err := sbpf.StoreMemory(kind, sbpf.R4, sbpf.R5, 0)
	if err != nil {
		return err
	}
	s.emit(store)
	return nil
}

func (s *instructionSelector) emitPointerAdd(instruction Instruction) error {
	if err := s.readValue(instruction.X, sbpf.R0); err != nil {
		return err
	}
	if err := s.readValue(instruction.Y, sbpf.R4); err != nil {
		return err
	}
	if instruction.Scale != 1 {
		s.emit(sbpf.ALUImm(sbpf.MUL64_IMM, sbpf.R4, int32(instruction.Scale)))
	}
	s.emit(sbpf.ALUReg(sbpf.ADD64_REG, sbpf.R0, sbpf.R4))
	return s.writeValue(instruction.Dest, sbpf.R0)
}

func (s *instructionSelector) emitBoundsCheck(instruction Instruction) error {
	if err := s.readValue(instruction.X, sbpf.R0); err != nil {
		return err
	}
	index, _ := s.function.Value(instruction.X)
	if instruction.Imm <= math.MaxInt32 {
		s.emit(sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R4, int32(instruction.Imm)))
	} else {
		s.emit(sbpf.LoadImm64(sbpf.R4, instruction.Imm))
	}
	unsignedLess := sbpf.JLT64_REG
	signedLessZero := sbpf.JSLT64_IMM
	if bits := index.Type.bits(); bits != 0 && bits <= 32 {
		unsignedLess = sbpf.JLT32_REG
		signedLessZero = sbpf.JSLT32_IMM
	}
	if index.Type.signed() {
		// Negative -> skip the valid-range jump and land in the trap.
		s.emit(sbpf.JumpImm(signedLessZero, sbpf.R0, 0, 1))
	}
	// Valid -> skip the two-instruction divide-by-zero trap.
	s.emit(sbpf.JumpReg(unsignedLess, sbpf.R0, sbpf.R4, 2))
	s.emit(sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R5, 0))
	s.emit(sbpf.ALUReg(sbpf.DIV64_REG, sbpf.R5, sbpf.R5))
	return nil
}

func (s *instructionSelector) emitZeroMemory(instruction Instruction) error {
	object, ok := s.function.MemoryObject(instruction.Object)
	if !ok {
		return fmt.Errorf("zero references invalid memory object %d", instruction.Object)
	}
	base, ok := s.allocation.objects[instruction.Object]
	if !ok {
		return fmt.Errorf("memory object %d has no stack allocation", instruction.Object)
	}
	kind, err := sbpfMemoryType(memoryTypeFor(object.Element))
	if err != nil {
		return err
	}
	s.emit(sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 0))
	width := int(kind.Bytes())
	for offset := 0; offset < int(object.Size()); offset += width {
		store, storeErr := sbpf.StoreMemory(kind, sbpf.R10, sbpf.R0, base+int16(offset))
		if storeErr != nil {
			return storeErr
		}
		s.emit(store)
	}
	return nil
}

func (s *instructionSelector) emitCopyMemory(instruction Instruction) error {
	destination, destinationOK := s.function.MemoryObject(instruction.Object)
	source, sourceOK := s.function.MemoryObject(instruction.SourceObject)
	if !destinationOK || !sourceOK || destination.Element != source.Element || destination.Length != source.Length {
		return fmt.Errorf("copy references incompatible memory objects")
	}
	destinationBase, destinationAllocated := s.allocation.objects[instruction.Object]
	sourceBase, sourceAllocated := s.allocation.objects[instruction.SourceObject]
	if !destinationAllocated || !sourceAllocated {
		return fmt.Errorf("copy memory object has no stack allocation")
	}
	kind, err := sbpfMemoryType(memoryTypeFor(destination.Element))
	if err != nil {
		return err
	}
	width := int(kind.Bytes())
	for offset := 0; offset < int(destination.Size()); offset += width {
		load, loadErr := sbpf.LoadMemory(kind, sbpf.R0, sbpf.R10, sourceBase+int16(offset))
		if loadErr != nil {
			return loadErr
		}
		store, storeErr := sbpf.StoreMemory(kind, sbpf.R10, sbpf.R0, destinationBase+int16(offset))
		if storeErr != nil {
			return storeErr
		}
		s.emit(load)
		s.emit(store)
	}
	return nil
}

func (s *instructionSelector) emitTerminator(terminator Terminator) error {
	switch terminator.Kind {
	case TermJump:
		s.emitBlockJump(sbpf.Jump(0), terminator.Target)
	case TermBranch:
		condition := s.allocation.homes[terminator.Cond]
		conditionRegister := condition.reg
		if condition.stack {
			conditionRegister = sbpf.R0
			s.emit(sbpf.Instruction{Op: sbpf.LD_DW_REG, Dst: sbpf.R0, Src: sbpf.R10, Offset: condition.stackOffset})
		}
		s.emitBlockJump(sbpf.JumpImm(sbpf.JNE64_IMM, conditionRegister, 0, 0), terminator.True)
		s.emitBlockJump(sbpf.Jump(0), terminator.False)
	case TermReturn:
		if terminator.Result == NoValue {
			s.emit(sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 0))
		} else if err := s.readValue(terminator.Result, sbpf.R0); err != nil {
			return err
		}
		s.emit(sbpf.Return())
	default:
		return fmt.Errorf("unsupported terminator %d", terminator.Kind)
	}
	return nil
}

func (s *instructionSelector) emitBlockJump(instruction sbpf.Instruction, target BlockID) {
	sourcePC := s.pc
	index := s.emit(instruction)
	s.fixups = append(s.fixups, codeFixup{
		kind: fixupJump, index: index, sourcePC: sourcePC,
		block: target, owner: s.function.Name,
	})
}

func (s *instructionSelector) readValue(id ValueID, target sbpf.Register) error {
	location, ok := s.allocation.homes[id]
	if !ok {
		return fmt.Errorf("value %d has no allocated home", id)
	}
	if location.stack {
		s.emit(sbpf.Instruction{Op: sbpf.LD_DW_REG, Dst: target, Src: sbpf.R10, Offset: location.stackOffset})
	} else if location.reg != target {
		s.emit(sbpf.ALUReg(sbpf.MOV64_REG, target, location.reg))
	}
	return nil
}

func (s *instructionSelector) writeValue(id ValueID, source sbpf.Register) error {
	location, ok := s.allocation.homes[id]
	if !ok {
		return fmt.Errorf("value %d has no allocated home", id)
	}
	if location.stack {
		s.emit(sbpf.Instruction{Op: sbpf.ST_DW_REG, Dst: sbpf.R10, Src: source, Offset: location.stackOffset})
	} else if location.reg != source {
		s.emit(sbpf.ALUReg(sbpf.MOV64_REG, location.reg, source))
	}
	return nil
}

func (s *instructionSelector) patchFixups() error {
	for _, fixup := range s.fixups {
		instruction := &s.program[fixup.index]
		switch fixup.kind {
		case fixupJump:
			targets := s.blocks[fixup.owner]
			targetPC, ok := targets[fixup.block]
			if !ok {
				return fmt.Errorf("compiler: function %s references unknown block %d", fixup.owner, fixup.block)
			}
			offset, err := sbpf.RelativeOffset(fixup.sourcePC, targetPC)
			if err != nil || offset < math.MinInt16 || offset > math.MaxInt16 {
				return fmt.Errorf("compiler: function %s jump from pc %d to %d does not fit sBPF i16 offset",
					fixup.owner, fixup.sourcePC, targetPC)
			}
			instruction.Offset = int16(offset)
		case fixupCall:
			targetPC, ok := s.functions[fixup.function]
			if !ok {
				return fmt.Errorf("compiler: function %s calls unknown function %s", fixup.owner, fixup.function)
			}
			offset, err := sbpf.RelativeOffset(fixup.sourcePC, targetPC)
			if err != nil {
				return fmt.Errorf("compiler: call from %s to %s: %w", fixup.owner, fixup.function, err)
			}
			instruction.Immediate = int64(offset)
		}
	}
	return nil
}

func (s *instructionSelector) fail(format string, args ...any) error {
	return fmt.Errorf("compiler: function %s: %s", s.function.Name, fmt.Sprintf(format, args...))
}
