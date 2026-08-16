package compiler

import (
	"fmt"

	"github.com/ersanyakit/go-solana/sbpf"
)

// GenerateSolanaEntrypoint emits an sBPFv3 program whose physical PC zero is
// a generated Solana entrypoint wrapper. Agave enters with r1 set to
// MM_INPUT_START and r2 set to the absolute guest virtual address of the
// instruction-data bytes. Agave's serializer obtains r2 from Serializer's
// write_all(), which already includes MM_INPUT_START; it is not a relative
// byte offset. The wrapper
// forwards both registers to handler and returns its u64 ProgramError value.
//
// The deliberately explicit two-u64 handler boundary keeps virtual addresses
// as guest integers; it never turns them into Go heap pointers. Higher-level
// account views are decoded against the runtime memory-region APIs.
func GenerateSolanaEntrypoint(program *Program, handler string) (*Executable, error) {
	if handler == "" {
		handler = "Program"
	}
	function, ok := program.Function(handler)
	if !ok {
		return nil, fmt.Errorf("compiler: Solana handler %q does not exist", handler)
	}
	if len(function.Params) != 2 {
		return nil, fmt.Errorf("compiler: Solana handler %s must accept (inputAddress uint64, instructionDataAddress uint64)", handler)
	}
	for index, id := range function.Params {
		value, valid := function.Value(id)
		if !valid || value.Type != TypeUint64 {
			return nil, fmt.Errorf("compiler: Solana handler %s parameter %d must be uint64", handler, index+1)
		}
	}
	if function.Result != TypeUint64 {
		return nil, fmt.Errorf("compiler: Solana handler %s must return uint64", handler)
	}

	handlerExecutable, err := Generate(program, handler)
	if err != nil {
		return nil, err
	}
	// CALL at pc 0 targets the handler at pc 2. EXIT at pc 1 terminates the
	// root frame after the handler's EXIT returns from its internal frame.
	instructions := make([]sbpf.Instruction, 0, len(handlerExecutable.Instructions)+2)
	instructions = append(instructions, sbpf.CallRelative(1), sbpf.Return())
	instructions = append(instructions, handlerExecutable.Instructions...)
	bytecode, err := sbpf.Encode(instructions)
	if err != nil {
		return nil, fmt.Errorf("compiler: generated Solana entrypoint: %w", err)
	}
	functions := make(map[string]int, len(handlerExecutable.Functions)+1)
	functions["entrypoint"] = 0
	for name, pc := range handlerExecutable.Functions {
		functions[name] = pc + 2
	}
	return &Executable{
		Entry:        "entrypoint",
		Instructions: instructions,
		Bytecode:     bytecode,
		Functions:    functions,
	}, nil
}
