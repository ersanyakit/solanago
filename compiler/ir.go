package compiler

import "fmt"

// Type is a compact, comparable representation of the deterministic subset's
// register and fixed-array types. Pointer values are sBPF virtual addresses;
// they are never Go/host pointers.
type Type uint32

const (
	TypeInvalid Type = iota
	TypeVoid
	TypeUint64
	TypeInt64
	TypeBool
	TypeUint32
	TypeInt32
	TypeUint8
)

const (
	typePointerFlag Type = 0x10000000
	typeArrayFlag   Type = 0x20000000
	typeKindMask    Type = 0xf0000000
	typeElementMask Type = 0xff
)

func PointerTo(element Type) Type {
	if !element.memoryScalar() {
		return TypeInvalid
	}
	return typePointerFlag | element
}

func ArrayOf(element Type, length uint32) Type {
	if !element.memoryScalar() || length == 0 || length > 0x000fffff {
		return TypeInvalid
	}
	return typeArrayFlag | Type(length<<8) | element
}

func (t Type) PointerElement() (Type, bool) {
	if t&typeKindMask != typePointerFlag {
		return TypeInvalid, false
	}
	element := t & typeElementMask
	return element, element.memoryScalar()
}

func (t Type) ArrayElement() (Type, uint32, bool) {
	if t&typeKindMask != typeArrayFlag {
		return TypeInvalid, 0, false
	}
	element := t & typeElementMask
	length := uint32((t &^ (typeKindMask | typeElementMask)) >> 8)
	return element, length, element.memoryScalar() && length != 0
}

func (t Type) String() string {
	switch t {
	case TypeVoid:
		return "void"
	case TypeUint64:
		return "uint64"
	case TypeInt64:
		return "int64"
	case TypeBool:
		return "bool"
	case TypeUint32:
		return "uint32"
	case TypeInt32:
		return "int32"
	case TypeUint8:
		return "uint8"
	default:
		if element, ok := t.PointerElement(); ok {
			return "*" + element.String()
		}
		if element, length, ok := t.ArrayElement(); ok {
			return fmt.Sprintf("[%d]%s", length, element)
		}
		return "invalid"
	}
}

func (t Type) scalar() bool {
	_, pointer := t.PointerElement()
	return t == TypeUint64 || t == TypeInt64 || t == TypeUint32 || t == TypeInt32 || t == TypeUint8 || t == TypeBool || pointer
}

func (t Type) integer() bool {
	return t == TypeUint64 || t == TypeInt64 || t == TypeUint32 || t == TypeInt32 || t == TypeUint8
}

func (t Type) signed() bool { return t == TypeInt64 || t == TypeInt32 }

func (t Type) bits() uint8 {
	if t == TypeUint8 {
		return 8
	}
	if t == TypeUint32 || t == TypeInt32 {
		return 32
	}
	if t == TypeUint64 || t == TypeInt64 {
		return 64
	}
	return 0
}

func (t Type) memoryScalar() bool {
	return t == TypeUint64 || t == TypeInt64 || t == TypeUint32 || t == TypeInt32 || t == TypeUint8 || t == TypeBool
}

func (t Type) memoryBytes() uint32 {
	switch t {
	case TypeBool, TypeUint8:
		return 1
	case TypeUint32, TypeInt32:
		return 4
	case TypeUint64, TypeInt64:
		return 8
	default:
		return 0
	}
}

// ValueID identifies a mutable virtual register. This is intentionally not
// SSA: source variables retain one ID across loop iterations and are assigned
// with OpMove. Backend register allocation may keep or spill these values.
type ValueID uint32

// BlockID identifies a basic block inside one function.
type BlockID uint32

const (
	NoValue ValueID = ^ValueID(0)
	NoBlock BlockID = ^BlockID(0)
)

type ValueKind uint8

const (
	ValueInvalid ValueKind = iota
	ValueParameter
	ValueLocal
	ValueTemporary
)

type Value struct {
	ID   ValueID
	Name string
	Type Type
	Kind ValueKind
}

// Op is a three-address, AST-independent operation. Control flow is expressed
// solely through each block's Terminator.
type Op uint8

const (
	OpInvalid Op = iota
	OpConst
	OpMove
	OpAdd
	OpSub
	OpMul
	OpDiv
	OpMod
	OpEqual
	OpNotEqual
	OpLess
	OpLessEqual
	OpGreater
	OpGreaterEqual
	OpCall
	OpSyscall
	OpAddress
	OpPointerAddress
	OpLoad
	OpStore
	OpPointerAdd
	OpBoundsCheck
	OpZeroMemory
	OpCopyMemory
)

func (op Op) String() string {
	switch op {
	case OpConst:
		return "const"
	case OpMove:
		return "mov"
	case OpAdd:
		return "add"
	case OpSub:
		return "sub"
	case OpMul:
		return "mul"
	case OpDiv:
		return "div"
	case OpMod:
		return "mod"
	case OpEqual:
		return "eq"
	case OpNotEqual:
		return "ne"
	case OpLess:
		return "lt"
	case OpLessEqual:
		return "le"
	case OpGreater:
		return "gt"
	case OpGreaterEqual:
		return "ge"
	case OpCall:
		return "call"
	case OpSyscall:
		return "syscall"
	case OpAddress:
		return "address"
	case OpPointerAddress:
		return "ptraddress"
	case OpLoad:
		return "load"
	case OpStore:
		return "store"
	case OpPointerAdd:
		return "ptradd"
	case OpBoundsCheck:
		return "bounds"
	case OpZeroMemory:
		return "zero"
	case OpCopyMemory:
		return "copy"
	default:
		return "invalid"
	}
}

type Instruction struct {
	Op           Op
	Dest         ValueID
	X            ValueID
	Y            ValueID
	Imm          uint64
	Callee       string
	Args         []ValueID
	SyscallID    uint32
	Object       MemoryObjectID
	SourceObject MemoryObjectID
	Memory       MemoryType
	Scale        uint32
	Pos          SourcePosition
}

type MemoryType uint8

const (
	MemoryInvalid MemoryType = iota
	MemoryBool
	MemoryUint32
	MemoryInt32
	MemoryUint64
	MemoryInt64
	MemoryUint8
	MemoryUint16
	MemoryByte
)

func memoryTypeFor(valueType Type) MemoryType {
	switch valueType {
	case TypeBool:
		return MemoryBool
	case TypeUint32:
		return MemoryUint32
	case TypeInt32:
		return MemoryInt32
	case TypeUint64:
		return MemoryUint64
	case TypeInt64:
		return MemoryInt64
	case TypeUint8:
		return MemoryByte
	default:
		return MemoryInvalid
	}
}

func (memory MemoryType) ValueType() Type {
	switch memory {
	case MemoryBool:
		return TypeBool
	case MemoryUint32:
		return TypeUint32
	case MemoryInt32:
		return TypeInt32
	case MemoryUint64:
		return TypeUint64
	case MemoryInt64:
		return TypeInt64
	case MemoryByte:
		return TypeUint8
	case MemoryUint8, MemoryUint16:
		return TypeUint32
	default:
		return TypeInvalid
	}
}

func (memory MemoryType) Bytes() uint32 {
	switch memory {
	case MemoryUint8, MemoryBool, MemoryByte:
		return 1
	case MemoryUint16:
		return 2
	default:
		return memory.ValueType().memoryBytes()
	}
}

type MemoryObjectID uint32

const NoMemoryObject MemoryObjectID = ^MemoryObjectID(0)

type MemoryObject struct {
	ID      MemoryObjectID
	Name    string
	Element Type
	Length  uint32
}

func (object MemoryObject) Size() uint32 { return object.Element.memoryBytes() * object.Length }

type TerminatorKind uint8

const (
	TermInvalid TerminatorKind = iota
	TermJump
	TermBranch
	TermReturn
)

type Terminator struct {
	Kind   TerminatorKind
	Target BlockID
	True   BlockID
	False  BlockID
	Cond   ValueID
	Result ValueID
	Pos    SourcePosition
}

type BasicBlock struct {
	ID           BlockID
	Name         string
	Instructions []Instruction
	Terminator   Terminator
}

type Function struct {
	Name   string
	Params []ValueID
	Result Type
	Values []Value
	Memory []MemoryObject
	Blocks []*BasicBlock
	Entry  BlockID
}

func (f *Function) Value(id ValueID) (Value, bool) {
	if f == nil || uint64(id) >= uint64(len(f.Values)) {
		return Value{}, false
	}
	value := f.Values[id]
	return value, value.ID == id
}

func (f *Function) Block(id BlockID) (*BasicBlock, bool) {
	if f == nil || uint64(id) >= uint64(len(f.Blocks)) {
		return nil, false
	}
	block := f.Blocks[id]
	return block, block != nil && block.ID == id
}

func (f *Function) MemoryObject(id MemoryObjectID) (MemoryObject, bool) {
	if f == nil || uint64(id) >= uint64(len(f.Memory)) {
		return MemoryObject{}, false
	}
	object := f.Memory[id]
	return object, object.ID == id
}

type Program struct {
	Package   string
	Functions []*Function
}

func (p *Program) Function(name string) (*Function, bool) {
	if p == nil {
		return nil, false
	}
	for _, function := range p.Functions {
		if function.Name == name {
			return function, true
		}
	}
	return nil, false
}

// Validate verifies the invariants required by instruction selection. It is
// safe to call on hand-built or decoded IR as well as frontend output.
func (p *Program) Validate() error {
	if p == nil {
		return fmt.Errorf("IR: nil program")
	}
	if p.Package == "" {
		return fmt.Errorf("IR: package name is empty")
	}
	functions := make(map[string]*Function, len(p.Functions))
	for _, function := range p.Functions {
		if function == nil {
			return fmt.Errorf("IR: nil function")
		}
		if function.Name == "" {
			return fmt.Errorf("IR: function name is empty")
		}
		if _, exists := functions[function.Name]; exists {
			return fmt.Errorf("IR: duplicate function %q", function.Name)
		}
		functions[function.Name] = function
	}
	for _, function := range p.Functions {
		if err := validateFunction(function, functions); err != nil {
			return err
		}
	}
	return validateStackAddressLifetimes(p)
}

func validateFunction(function *Function, functions map[string]*Function) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("IR function %s: %s", function.Name, fmt.Sprintf(format, args...))
	}
	if function.Result != TypeVoid && !function.Result.scalar() {
		return fail("invalid result type %s", function.Result)
	}
	if _, pointer := function.Result.PointerElement(); pointer {
		return fail("pointer results are forbidden because sBPF stack addresses must not escape a call frame")
	}
	if len(function.Params) > 5 {
		return fail("has %d parameters; sBPF calls support at most 5", len(function.Params))
	}
	for index, value := range function.Values {
		if value.ID != ValueID(index) {
			return fail("value table index %d contains ID %d", index, value.ID)
		}
		if !value.Type.scalar() {
			return fail("value %d has invalid type %s", value.ID, value.Type)
		}
		if value.Kind < ValueParameter || value.Kind > ValueTemporary {
			return fail("value %d has invalid kind", value.ID)
		}
	}
	for _, id := range function.Params {
		value, ok := function.Value(id)
		if !ok || value.Kind != ValueParameter {
			return fail("invalid parameter value %d", id)
		}
	}
	for index, object := range function.Memory {
		if object.ID != MemoryObjectID(index) {
			return fail("memory table index %d contains ID %d", index, object.ID)
		}
		if !object.Element.memoryScalar() || object.Length == 0 || object.Size() == 0 {
			return fail("memory object %d has invalid type or length", object.ID)
		}
		if object.Size() > stackFrameSize {
			return fail("memory object %d requires %d bytes; frame limit is %d", object.ID, object.Size(), stackFrameSize)
		}
	}
	if _, ok := function.Block(function.Entry); !ok {
		return fail("invalid entry block %d", function.Entry)
	}
	for index, block := range function.Blocks {
		if block == nil || block.ID != BlockID(index) {
			return fail("invalid block table entry %d", index)
		}
		for instructionIndex, instruction := range block.Instructions {
			if err := validateInstruction(function, functions, instruction); err != nil {
				return fail("block %d instruction %d: %v", block.ID, instructionIndex, err)
			}
		}
		if err := validateTerminator(function, block.Terminator); err != nil {
			return fail("block %d: %v", block.ID, err)
		}
	}
	return nil
}

func validateInstruction(function *Function, functions map[string]*Function, instruction Instruction) error {
	value := func(id ValueID) (Value, error) {
		result, ok := function.Value(id)
		if !ok {
			return Value{}, fmt.Errorf("invalid value %d", id)
		}
		return result, nil
	}
	dest, err := value(instruction.Dest)
	if (instruction.Op == OpCall || instruction.Op == OpSyscall || instruction.Op == OpStore || instruction.Op == OpBoundsCheck || instruction.Op == OpZeroMemory || instruction.Op == OpCopyMemory) && instruction.Dest == NoValue {
		err = nil
	}
	if err != nil {
		return err
	}
	switch instruction.Op {
	case OpConst:
		if dest.Type == TypeBool && instruction.Imm > 1 {
			return fmt.Errorf("bool constant must be encoded as 0 or 1")
		}
		if _, pointer := dest.Type.PointerElement(); pointer && instruction.Imm != 0 {
			return fmt.Errorf("non-zero pointer constants are forbidden; use an sBPF memory address parameter or address operation")
		}
		return nil
	case OpMove:
		source, sourceErr := value(instruction.X)
		if sourceErr != nil {
			return sourceErr
		}
		_, sourcePointer := source.Type.PointerElement()
		_, destPointer := dest.Type.PointerElement()
		if source.Type == TypeBool || dest.Type == TypeBool || sourcePointer || destPointer {
			if source.Type != dest.Type {
				return fmt.Errorf("cannot move %s into %s", source.Type, dest.Type)
			}
		}
		return nil
	case OpAdd, OpSub, OpMul, OpDiv, OpMod:
		x, xErr := value(instruction.X)
		if xErr != nil {
			return xErr
		}
		y, yErr := value(instruction.Y)
		if yErr != nil {
			return yErr
		}
		if !dest.Type.integer() || x.Type != dest.Type || y.Type != dest.Type {
			return fmt.Errorf("%s operands must have one integer type", instruction.Op)
		}
		return nil
	case OpEqual, OpNotEqual, OpLess, OpLessEqual, OpGreater, OpGreaterEqual:
		x, xErr := value(instruction.X)
		if xErr != nil {
			return xErr
		}
		y, yErr := value(instruction.Y)
		if yErr != nil {
			return yErr
		}
		if dest.Type != TypeBool || x.Type != y.Type {
			return fmt.Errorf("%s must compare like-typed values into bool", instruction.Op)
		}
		if instruction.Op != OpEqual && instruction.Op != OpNotEqual && !x.Type.integer() {
			return fmt.Errorf("%s requires integer operands", instruction.Op)
		}
		return nil
	case OpCall:
		callee, ok := functions[instruction.Callee]
		if !ok {
			return fmt.Errorf("unknown callee %q", instruction.Callee)
		}
		if len(instruction.Args) != len(callee.Params) {
			return fmt.Errorf("call to %s has %d arguments, want %d", callee.Name, len(instruction.Args), len(callee.Params))
		}
		for index, argumentID := range instruction.Args {
			argument, argumentErr := value(argumentID)
			if argumentErr != nil {
				return argumentErr
			}
			parameter, _ := callee.Value(callee.Params[index])
			if argument.Type != parameter.Type {
				return fmt.Errorf("call to %s argument %d has type %s, want %s", callee.Name, index+1, argument.Type, parameter.Type)
			}
		}
		if callee.Result == TypeVoid {
			if instruction.Dest != NoValue {
				return fmt.Errorf("void call to %s has a destination", callee.Name)
			}
		} else if instruction.Dest == NoValue || dest.Type != callee.Result {
			return fmt.Errorf("call to %s has invalid result destination", callee.Name)
		}
		return nil
	case OpSyscall:
		intrinsic, ok := lookupSyscallBySymbol(instruction.Callee)
		if !ok {
			return fmt.Errorf("unknown Solana syscall symbol %q", instruction.Callee)
		}
		if instruction.X != NoValue || instruction.Y != NoValue || instruction.Imm != 0 ||
			instruction.Object != NoMemoryObject || instruction.SourceObject != NoMemoryObject ||
			instruction.Memory != MemoryInvalid || instruction.Scale != 0 {
			return fmt.Errorf("Solana syscall %s has non-canonical unused fields", intrinsic.Symbol)
		}
		if instruction.SyscallID != intrinsic.ID {
			return fmt.Errorf("Solana syscall %s has id 0x%08x, want 0x%08x", intrinsic.Symbol, instruction.SyscallID, intrinsic.ID)
		}
		if len(instruction.Args) != int(intrinsic.ParamCount) {
			return fmt.Errorf("Solana syscall %s has %d arguments, want %d", intrinsic.Symbol, len(instruction.Args), intrinsic.ParamCount)
		}
		for index, argumentID := range instruction.Args {
			argument, argumentErr := value(argumentID)
			if argumentErr != nil {
				return argumentErr
			}
			if argument.Type != intrinsic.Parameters[index] {
				return fmt.Errorf("Solana syscall %s argument %d has type %s, want %s", intrinsic.Symbol, index+1, argument.Type, intrinsic.Parameters[index])
			}
		}
		if intrinsic.Result == TypeVoid {
			if instruction.Dest != NoValue {
				return fmt.Errorf("void Solana syscall %s has a destination", intrinsic.Symbol)
			}
		} else if instruction.Dest == NoValue || dest.Type != intrinsic.Result {
			return fmt.Errorf("Solana syscall %s has invalid result destination", intrinsic.Symbol)
		}
		return nil
	case OpAddress:
		object, ok := function.MemoryObject(instruction.Object)
		element, pointer := dest.Type.PointerElement()
		if !ok || !pointer || element != object.Element {
			return fmt.Errorf("address has incompatible destination or memory object")
		}
		return nil
	case OpPointerAddress:
		pointer, pointerErr := value(instruction.X)
		if pointerErr != nil {
			return pointerErr
		}
		element, ok := pointer.Type.PointerElement()
		if !ok || element != TypeUint64 || dest.Type != TypeUint64 {
			return fmt.Errorf("pointer address requires a *uint64 guest pointer and uint64 destination")
		}
		return nil
	case OpLoad:
		pointer, pointerErr := value(instruction.X)
		if pointerErr != nil {
			return pointerErr
		}
		element, pointerOK := pointer.Type.PointerElement()
		explicitAddress := pointer.Type == TypeUint64
		if (!pointerOK && !explicitAddress) || (pointerOK && element != dest.Type) || instruction.Memory.ValueType() != dest.Type {
			return fmt.Errorf("load pointer, destination, and memory type disagree")
		}
		return nil
	case OpStore:
		if instruction.Dest != NoValue {
			return fmt.Errorf("store must not have a destination")
		}
		pointer, pointerErr := value(instruction.X)
		if pointerErr != nil {
			return pointerErr
		}
		stored, storedErr := value(instruction.Y)
		if storedErr != nil {
			return storedErr
		}
		element, pointerOK := pointer.Type.PointerElement()
		explicitAddress := pointer.Type == TypeUint64
		if (!pointerOK && !explicitAddress) || (pointerOK && element != stored.Type) || instruction.Memory.ValueType() != stored.Type {
			return fmt.Errorf("store pointer, value, and memory type disagree")
		}
		return nil
	case OpPointerAdd:
		base, baseErr := value(instruction.X)
		if baseErr != nil {
			return baseErr
		}
		index, indexErr := value(instruction.Y)
		if indexErr != nil {
			return indexErr
		}
		element, pointer := base.Type.PointerElement()
		if !pointer || dest.Type != base.Type || !index.Type.integer() || instruction.Scale != element.memoryBytes() {
			return fmt.Errorf("invalid pointer addition")
		}
		return nil
	case OpBoundsCheck:
		if instruction.Dest != NoValue {
			return fmt.Errorf("bounds check must not have a destination")
		}
		index, indexErr := value(instruction.X)
		if indexErr != nil {
			return indexErr
		}
		if !index.Type.integer() || instruction.Imm == 0 {
			return fmt.Errorf("invalid array bounds check")
		}
		return nil
	case OpZeroMemory:
		if instruction.Dest != NoValue {
			return fmt.Errorf("zero-memory must not have a destination")
		}
		if _, ok := function.MemoryObject(instruction.Object); !ok {
			return fmt.Errorf("zero-memory references invalid object %d", instruction.Object)
		}
		return nil
	case OpCopyMemory:
		if instruction.Dest != NoValue {
			return fmt.Errorf("copy-memory must not have a destination")
		}
		destination, destinationOK := function.MemoryObject(instruction.Object)
		source, sourceOK := function.MemoryObject(instruction.SourceObject)
		if !destinationOK || !sourceOK || destination.Element != source.Element || destination.Length != source.Length {
			return fmt.Errorf("copy-memory objects are missing or incompatible")
		}
		return nil
	default:
		return fmt.Errorf("invalid opcode %d", instruction.Op)
	}
}

func validateTerminator(function *Function, terminator Terminator) error {
	value := func(id ValueID) (Value, bool) { return function.Value(id) }
	block := func(id BlockID) bool { _, ok := function.Block(id); return ok }
	switch terminator.Kind {
	case TermJump:
		if !block(terminator.Target) {
			return fmt.Errorf("jump targets invalid block %d", terminator.Target)
		}
	case TermBranch:
		condition, ok := value(terminator.Cond)
		if !ok || condition.Type != TypeBool {
			return fmt.Errorf("branch condition %d is not bool", terminator.Cond)
		}
		if !block(terminator.True) || !block(terminator.False) {
			return fmt.Errorf("branch has an invalid target")
		}
	case TermReturn:
		if function.Result == TypeVoid {
			if terminator.Result != NoValue {
				return fmt.Errorf("void function returns a value")
			}
		} else {
			result, ok := value(terminator.Result)
			if !ok || result.Type != function.Result {
				return fmt.Errorf("return value has wrong type")
			}
		}
	default:
		return fmt.Errorf("missing terminator")
	}
	return nil
}
