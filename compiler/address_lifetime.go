package compiler

import (
	"fmt"
	"math"
)

// addressOrigins describes integer values which may contain an sBPF guest
// address. local means an address owned by the function currently being
// analyzed. parameters and parameterMemory are symbolic dependencies used to
// carry provenance across internal calls without treating input-region guest
// addresses as addresses owned by the current frame.
type addressOrigins struct {
	local           bool
	parameters      uint8
	parameterMemory uint8
}

func (origins addressOrigins) empty() bool {
	return !origins.local && origins.parameters == 0 && origins.parameterMemory == 0
}

func (origins *addressOrigins) add(other addressOrigins) bool {
	before := *origins
	origins.local = origins.local || other.local
	origins.parameters |= other.parameters
	origins.parameterMemory |= other.parameterMemory
	return *origins != before
}

func (origins addressOrigins) withoutLocal() addressOrigins {
	origins.local = false
	return origins
}

type addressLocationKind uint8

const (
	addressLocationUnknown addressLocationKind = iota
	addressLocationObject
	addressLocationParameter
)

// addressLocation is deliberately stricter than addressOrigins. It proves
// that a store destination remains inside one compiler-owned memory object, or
// describes an exact parameter-relative destination for a caller summary.
// Merely deriving an integer from a local address is not such a proof: raw
// arithmetic can cross an sBPF frame boundary.
type addressLocation struct {
	kind      addressLocationKind
	object    MemoryObjectID
	parameter uint8
	offset    uint64
}

type addressValue struct {
	origins       addressOrigins
	location      addressLocation
	constant      uint64
	constantKnown bool
}

func joinAddressValue(destination *addressValue, source addressValue) bool {
	before := *destination
	destination.origins.add(source.origins)
	if !destination.constantKnown || !source.constantKnown || destination.constant != source.constant {
		destination.constantKnown = false
		destination.constant = 0
	}
	if destination.location != source.location {
		destination.location = addressLocation{}
	}
	return *destination != before
}

type addressState struct {
	values          []addressValue
	objectMemory    []addressOrigins
	parameterMemory [5]addressOrigins
}

func newAddressState(function *Function) *addressState {
	state := &addressState{
		values:       make([]addressValue, len(function.Values)),
		objectMemory: make([]addressOrigins, len(function.Memory)),
	}
	for index, id := range function.Params {
		value, _ := function.Value(id)
		if !canCarryGuestAddress(value.Type) {
			continue
		}
		bit := uint8(1 << index)
		state.values[id] = addressValue{
			origins:  addressOrigins{parameters: bit},
			location: addressLocation{kind: addressLocationParameter, parameter: uint8(index)},
		}
		state.parameterMemory[index] = addressOrigins{parameterMemory: bit}
	}
	return state
}

func canCarryGuestAddress(valueType Type) bool {
	_, pointer := valueType.PointerElement()
	return pointer || valueType.integer()
}

func carriesFullGuestAddress(valueType Type) bool {
	_, pointer := valueType.PointerElement()
	return pointer || valueType.bits() == 64
}

func (state *addressState) clone() *addressState {
	clone := &addressState{
		values:          append([]addressValue(nil), state.values...),
		objectMemory:    append([]addressOrigins(nil), state.objectMemory...),
		parameterMemory: state.parameterMemory,
	}
	return clone
}

func (state *addressState) join(source *addressState) bool {
	changed := false
	for index := range state.values {
		changed = joinAddressValue(&state.values[index], source.values[index]) || changed
	}
	for index := range state.objectMemory {
		changed = state.objectMemory[index].add(source.objectMemory[index]) || changed
	}
	for index := range state.parameterMemory {
		changed = state.parameterMemory[index].add(source.parameterMemory[index]) || changed
	}
	return changed
}

func (state *addressState) memoryAt(address addressValue) addressOrigins {
	var result addressOrigins
	if address.location.kind == addressLocationObject {
		if uint64(address.location.object) < uint64(len(state.objectMemory)) {
			result.add(state.objectMemory[address.location.object])
		}
	} else if address.origins.local {
		// A local-derived address without an object proof may alias any object in
		// this frame. This is intentionally conservative for raw arithmetic.
		for _, memory := range state.objectMemory {
			result.add(memory)
		}
	}
	for index := range state.parameterMemory {
		if address.origins.parameters&(1<<index) != 0 {
			result.add(state.parameterMemory[index])
		}
	}
	// A pointer loaded from parameter memory may be dereferenced again. Keep
	// the symbolic dependency until a caller can resolve the pointed-to memory.
	result.parameterMemory |= address.origins.parameterMemory
	return result
}

type addressStoreEffect struct {
	value       addressOrigins
	destination addressValue
	width       uint32 // zero means an unbounded or dynamically sized copy
}

type addressLifetimeSummary struct {
	returned addressOrigins
	stores   map[addressStoreEffect]struct{}
}

const (
	maxAddressLifetimeIterations = 256
	maxAddressLifetimeEffects    = 4096
)

func newAddressLifetimeSummary() addressLifetimeSummary {
	return addressLifetimeSummary{stores: make(map[addressStoreEffect]struct{})}
}

func (summary *addressLifetimeSummary) merge(source addressLifetimeSummary) bool {
	changed := summary.returned.add(source.returned)
	for effect := range source.stores {
		if _, exists := summary.stores[effect]; exists {
			continue
		}
		summary.stores[effect] = struct{}{}
		changed = true
	}
	return changed
}

// validateStackAddressLifetimes prevents AddressUint64 from turning current
// frame storage into an unscoped integer capability. The analysis is run on IR
// so hand-built programs and frontend output receive the same checks. It is
// flow-sensitive within each CFG and uses symbolic summaries across internal
// calls. Official static syscalls are synchronous sinks; memcpy/memmove are
// additionally modeled because they can retain bytes in another region.
func validateStackAddressLifetimes(program *Program) error {
	if !programUsesPointerAddress(program) {
		return nil
	}
	summaries := make(map[string]*addressLifetimeSummary, len(program.Functions))
	for _, function := range program.Functions {
		summary := newAddressLifetimeSummary()
		summaries[function.Name] = &summary
	}
	for iteration := 0; iteration < maxAddressLifetimeIterations; iteration++ {
		changed := false
		for _, function := range program.Functions {
			observed, err := analyzeStackAddressLifetime(function, summaries)
			if err != nil {
				return err
			}
			changed = summaries[function.Name].merge(observed) || changed
			if len(summaries[function.Name].stores) > maxAddressLifetimeEffects {
				return fmt.Errorf("IR function %s: stack-address lifetime effect set exceeds %d entries", function.Name, maxAddressLifetimeEffects)
			}
		}
		if !changed {
			return nil
		}
	}
	return fmt.Errorf("IR: stack-address lifetime analysis did not converge after %d iterations", maxAddressLifetimeIterations)
}

func programUsesPointerAddress(program *Program) bool {
	for _, function := range program.Functions {
		for _, block := range function.Blocks {
			for _, instruction := range block.Instructions {
				if instruction.Op == OpPointerAddress {
					return true
				}
			}
		}
	}
	return false
}

func analyzeStackAddressLifetime(function *Function, summaries map[string]*addressLifetimeSummary) (addressLifetimeSummary, error) {
	result := newAddressLifetimeSummary()
	inputs := make([]*addressState, len(function.Blocks))
	inputs[function.Entry] = newAddressState(function)
	queue := []BlockID{function.Entry}
	queued := make([]bool, len(function.Blocks))
	queued[function.Entry] = true

	for len(queue) != 0 {
		blockID := queue[0]
		queue = queue[1:]
		queued[blockID] = false
		block, _ := function.Block(blockID)
		state := inputs[blockID].clone()
		for instructionIndex, instruction := range block.Instructions {
			if err := transferAddressInstruction(function, instruction, state, summaries, &result); err != nil {
				return result, fmt.Errorf("IR function %s: block %d instruction %d: %w", function.Name, block.ID, instructionIndex, err)
			}
		}

		switch block.Terminator.Kind {
		case TermReturn:
			if block.Terminator.Result == NoValue {
				continue
			}
			returned := state.values[block.Terminator.Result].origins
			if returned.local {
				return result, fmt.Errorf("IR function %s: block %d return: current-frame guest address escapes through an integer return", function.Name, block.ID)
			}
			result.returned.add(returned)
		case TermJump:
			enqueueAddressSuccessor(block.Terminator.Target, state, inputs, queued, &queue)
		case TermBranch:
			enqueueAddressSuccessor(block.Terminator.True, state, inputs, queued, &queue)
			enqueueAddressSuccessor(block.Terminator.False, state, inputs, queued, &queue)
		}
	}
	return result, nil
}

func enqueueAddressSuccessor(block BlockID, state *addressState, inputs []*addressState, queued []bool, queue *[]BlockID) {
	changed := false
	if inputs[block] == nil {
		inputs[block] = state.clone()
		changed = true
	} else {
		changed = inputs[block].join(state)
	}
	if changed && !queued[block] {
		queued[block] = true
		*queue = append(*queue, block)
	}
}

func transferAddressInstruction(function *Function, instruction Instruction, state *addressState, summaries map[string]*addressLifetimeSummary, observed *addressLifetimeSummary) error {
	value := func(id ValueID) addressValue { return state.values[id] }
	set := func(id ValueID, next addressValue) {
		if id != NoValue {
			state.values[id] = next
		}
	}

	switch instruction.Op {
	case OpConst:
		set(instruction.Dest, addressValue{constant: instruction.Imm, constantKnown: true})
	case OpMove:
		next := value(instruction.X)
		destination, _ := function.Value(instruction.Dest)
		source, _ := function.Value(instruction.X)
		if !carriesFullGuestAddress(destination.Type) || !carriesFullGuestAddress(source.Type) {
			next.location = addressLocation{}
		}
		if next.constantKnown {
			next.constant = normalizeAddressConstant(destination.Type, next.constant)
		}
		set(instruction.Dest, next)
	case OpAdd, OpSub, OpMul, OpDiv, OpMod:
		x, y := value(instruction.X), value(instruction.Y)
		next := addressValue{origins: x.origins}
		next.origins.add(y.origins)
		next.location = arithmeticAddressLocation(function, instruction.Op, x, y)
		if x.constantKnown && y.constantKnown {
			destination, _ := function.Value(instruction.Dest)
			next.constant, next.constantKnown = foldAddressConstant(destination.Type, instruction.Op, x.constant, y.constant)
		}
		set(instruction.Dest, next)
	case OpEqual, OpNotEqual, OpLess, OpLessEqual, OpGreater, OpGreaterEqual:
		set(instruction.Dest, addressValue{})
	case OpCall:
		arguments := make([]addressValue, len(instruction.Args))
		for index, id := range instruction.Args {
			arguments[index] = value(id)
		}
		calleeSummary := summaries[instruction.Callee]
		if instruction.Dest != NoValue {
			set(instruction.Dest, addressValue{origins: substituteAddressOrigins(calleeSummary.returned, arguments, state)})
		}
		for effect := range calleeSummary.stores {
			resolved := addressStoreEffect{
				value:       substituteAddressOrigins(effect.value, arguments, state),
				destination: substituteAddressValue(effect.destination, arguments, state),
				width:       effect.width,
			}
			if err := applyAddressStore(function, resolved, state, observed); err != nil {
				return fmt.Errorf("call to %s: %w", instruction.Callee, err)
			}
		}
	case OpSyscall:
		if instruction.Callee == "sol_memcpy_" || instruction.Callee == "sol_memmove_" {
			destination := value(instruction.Args[0])
			source := value(instruction.Args[1])
			width, knownWidth := addressCopyWidth(value(instruction.Args[2]))
			if !knownWidth || width != 0 {
				if err := applyAddressStore(function, addressStoreEffect{
					value: state.memoryAt(source), destination: destination, width: width,
				}, state, observed); err != nil {
					return fmt.Errorf("Solana syscall %s: %w", instruction.Callee, err)
				}
			}
		} else if instruction.Callee == "sol_memset_" {
			width, knownWidth := addressCopyWidth(value(instruction.Args[2]))
			if !knownWidth || width != 0 {
				if err := applyAddressStore(function, addressStoreEffect{
					value:       value(instruction.Args[1]).origins,
					destination: value(instruction.Args[0]),
					width:       width,
				}, state, observed); err != nil {
					return fmt.Errorf("Solana syscall %s: %w", instruction.Callee, err)
				}
			}
		}
		set(instruction.Dest, addressValue{})
	case OpAddress:
		set(instruction.Dest, addressValue{
			origins:  addressOrigins{local: true},
			location: addressLocation{kind: addressLocationObject, object: instruction.Object},
		})
	case OpPointerAddress:
		set(instruction.Dest, value(instruction.X))
	case OpLoad:
		set(instruction.Dest, addressValue{origins: state.memoryAt(value(instruction.X))})
	case OpStore:
		if err := applyAddressStore(function, addressStoreEffect{
			value: value(instruction.Y).origins, destination: value(instruction.X), width: instruction.Memory.Bytes(),
		}, state, observed); err != nil {
			return err
		}
	case OpPointerAdd:
		base, index := value(instruction.X), value(instruction.Y)
		next := addressValue{origins: base.origins}
		if index.constantKnown && index.constant <= math.MaxUint64/uint64(instruction.Scale) {
			next.location = adjustAddressLocation(function, base.location, index.constant*uint64(instruction.Scale), false)
		}
		set(instruction.Dest, next)
	case OpBoundsCheck:
		// Bounds checks do not create or retain addresses.
	case OpZeroMemory:
		state.objectMemory[instruction.Object] = addressOrigins{}
	case OpCopyMemory:
		state.objectMemory[instruction.Object] = state.objectMemory[instruction.SourceObject]
	}
	return nil
}

func addressCopyWidth(length addressValue) (uint32, bool) {
	if length.constantKnown && length.constant <= math.MaxUint32 {
		return uint32(length.constant), true
	}
	return 0, false
}

func applyAddressStore(function *Function, effect addressStoreEffect, state *addressState, observed *addressLifetimeSummary) error {
	if effect.value.empty() {
		return nil
	}
	if destinationIsCurrentObject(function, effect.destination, effect.width) {
		state.objectMemory[effect.destination.location.object].add(effect.value)
		return nil
	}

	// Update every storage class which the imprecise destination may name.
	if effect.destination.location.kind == addressLocationObject {
		state.objectMemory[effect.destination.location.object].add(effect.value)
	} else if effect.destination.origins.local {
		for index := range state.objectMemory {
			state.objectMemory[index].add(effect.value)
		}
	}
	for index := range state.parameterMemory {
		if effect.destination.origins.parameters&(1<<index) != 0 {
			state.parameterMemory[index].add(effect.value)
		}
	}

	if effect.value.local {
		return fmt.Errorf("current-frame guest address may be retained outside its owning stack object")
	}
	outwardValue := effect.value.withoutLocal()
	if outwardValue.empty() {
		return nil
	}
	outwardDestination := effect.destination
	outwardDestination.origins = outwardDestination.origins.withoutLocal()
	if outwardDestination.location.kind == addressLocationObject {
		outwardDestination.location = addressLocation{}
	}
	observed.stores[addressStoreEffect{
		value: outwardValue, destination: outwardDestination, width: effect.width,
	}] = struct{}{}
	return nil
}

func destinationIsCurrentObject(function *Function, destination addressValue, width uint32) bool {
	if width == 0 || destination.location.kind != addressLocationObject ||
		!destination.origins.local || destination.origins.parameters != 0 || destination.origins.parameterMemory != 0 {
		return false
	}
	object, ok := function.MemoryObject(destination.location.object)
	if !ok || destination.location.offset > uint64(object.Size()) {
		return false
	}
	return uint64(width) <= uint64(object.Size())-destination.location.offset
}

func substituteAddressOrigins(expression addressOrigins, arguments []addressValue, state *addressState) addressOrigins {
	result := addressOrigins{local: expression.local}
	for index := range arguments {
		if expression.parameters&(1<<index) != 0 {
			result.add(arguments[index].origins)
		}
		if expression.parameterMemory&(1<<index) != 0 {
			result.add(state.memoryAt(arguments[index]))
		}
	}
	return result
}

func substituteAddressValue(expression addressValue, arguments []addressValue, state *addressState) addressValue {
	result := addressValue{origins: substituteAddressOrigins(expression.origins, arguments, state)}
	if expression.location.kind != addressLocationParameter || int(expression.location.parameter) >= len(arguments) {
		return result
	}
	result.location = adjustAddressLocation(nil, arguments[expression.location.parameter].location, expression.location.offset, false)
	return result
}

func arithmeticAddressLocation(function *Function, operation Op, x, y addressValue) addressLocation {
	switch operation {
	case OpAdd:
		if x.location.kind != addressLocationUnknown && y.constantKnown && y.origins.empty() {
			return adjustAddressLocation(function, x.location, y.constant, false)
		}
		if y.location.kind != addressLocationUnknown && x.constantKnown && x.origins.empty() {
			return adjustAddressLocation(function, y.location, x.constant, false)
		}
	case OpSub:
		if x.location.kind != addressLocationUnknown && y.constantKnown && y.origins.empty() {
			return adjustAddressLocation(function, x.location, y.constant, true)
		}
	}
	return addressLocation{}
}

func adjustAddressLocation(function *Function, location addressLocation, delta uint64, subtract bool) addressLocation {
	if location.kind == addressLocationUnknown {
		return addressLocation{}
	}
	if subtract {
		if delta > location.offset {
			return addressLocation{}
		}
		location.offset -= delta
	} else {
		if delta > math.MaxUint64-location.offset {
			return addressLocation{}
		}
		location.offset += delta
	}
	if function != nil && location.kind == addressLocationObject {
		object, ok := function.MemoryObject(location.object)
		if !ok || location.offset > uint64(object.Size()) {
			return addressLocation{}
		}
	}
	return location
}

func normalizeAddressConstant(valueType Type, value uint64) uint64 {
	switch valueType.bits() {
	case 8:
		return value & math.MaxUint8
	case 32:
		return value & math.MaxUint32
	default:
		return value
	}
}

func foldAddressConstant(valueType Type, operation Op, x, y uint64) (uint64, bool) {
	var result uint64
	switch operation {
	case OpAdd:
		result = x + y
	case OpSub:
		result = x - y
	case OpMul:
		result = x * y
	case OpDiv:
		if y == 0 || valueType.signed() {
			return 0, false
		}
		result = x / y
	case OpMod:
		if y == 0 || valueType.signed() {
			return 0, false
		}
		result = x % y
	default:
		return 0, false
	}
	return normalizeAddressConstant(valueType, result), true
}
