package compiler

import (
	"strings"
	"testing"

	"github.com/ersanyakit/go-solana/sbpf"
	"github.com/ersanyakit/go-solana/vm"
)

const allSyscallDeclarations = `
func InvokeSignedC(instructionAddress uint64, accountInfosAddress uint64, accountInfosLength uint64, signerSeedsAddress uint64, signerSeedsLength uint64) uint64
func Log(messageAddress uint64, length uint64)
func Memcpy(destinationAddress uint64, sourceAddress uint64, length uint64)
func Memmove(destinationAddress uint64, sourceAddress uint64, length uint64)
func Memset(destinationAddress uint64, value uint8, length uint64)
func Memcmp(leftAddress uint64, rightAddress uint64, length uint64, resultAddress uint64)
func CreateProgramAddress(seedsAddress uint64, seedsLength uint64, programIDAddress uint64, resultAddress uint64) uint64
func TryFindProgramAddress(seedsAddress uint64, seedsLength uint64, programIDAddress uint64, resultAddress uint64, bumpSeedAddress uint64) uint64
`

func TestSolanaSyscallIntrinsicsLowerToExactSymbols(t *testing.T) {
	t.Parallel()
	program := compileTestSource(t, `package main
`+allSyscallDeclarations+`
func Program(a uint64, b uint64, c uint64, d uint64, e uint64) uint64 {
	Log(a, b)
	Memcpy(a, b, c)
	Memmove(a, b, c)
	Memset(a, uint8(c), b)
	Memcmp(a, b, c, d)
	CreateProgramAddress(a, b, c, d)
	TryFindProgramAddress(a, b, c, d, e)
	return InvokeSignedC(a, b, c, d, e)
}
`)
	function, ok := program.Function("Program")
	if !ok {
		t.Fatal("Program function missing")
	}
	wantSymbols := []string{
		"sol_log_", "sol_memcpy_", "sol_memmove_", "sol_memset_", "sol_memcmp_",
		"sol_create_program_address", "sol_try_find_program_address", "sol_invoke_signed_c",
	}
	var calls []Instruction
	for _, block := range function.Blocks {
		for _, instruction := range block.Instructions {
			if instruction.Op == OpSyscall {
				calls = append(calls, instruction)
			}
		}
	}
	if len(calls) != len(wantSymbols) {
		t.Fatalf("syscall count = %d, want %d: %+v", len(calls), len(wantSymbols), calls)
	}
	for index, symbol := range wantSymbols {
		call := calls[index]
		if call.Callee != symbol || call.SyscallID != sbpf.HashSymbolName(symbol) {
			t.Errorf("call %d = %+v, want %s/0x%08x", index, call, symbol, sbpf.HashSymbolName(symbol))
		}
		if symbol == "sol_invoke_signed_c" && call.Dest == NoValue {
			t.Error("InvokeSignedC result was discarded in IR return path")
		}
		if symbol == "sol_log_" && call.Dest != NoValue {
			t.Error("void Log syscall has a destination")
		}
	}
}

func TestSolanaSyscallBackendEmitsStaticV3Calls(t *testing.T) {
	t.Parallel()
	source := []byte(`package main
` + allSyscallDeclarations + `
func Program(a uint64, b uint64, c uint64, d uint64, e uint64) uint64 {
	Log(a, b)
	return InvokeSignedC(a, b, c, d, e)
}
`)
	executable, err := Compile("syscalls.go", source, "Program")
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{sbpf.HashSymbolName("sol_log_"), sbpf.HashSymbolName("sol_invoke_signed_c")}
	var got []uint32
	for _, instruction := range executable.Instructions {
		if instruction.Op == sbpf.CALL_IMM && instruction.Src == 0 {
			got = append(got, uint32(instruction.Immediate))
		}
	}
	if len(got) != len(want) {
		t.Fatalf("static syscall ids = %#v, want %#v; program=%+v", got, want, executable.Instructions)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("static call %d id = 0x%08x, want 0x%08x", index, got[index], want[index])
		}
	}
	if err := sbpf.ValidateProgram(executable.Instructions); err != nil {
		t.Fatalf("sBPF verifier rejected static calls: %v", err)
	}
}

func TestUint8RegisterAndFixedArraySemantics(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func F(value uint32) uint8 {
	bytes := [2]uint8{uint8(value), uint8(1)}
	bytes[0] += bytes[1]
	return bytes[0]
}
`, "F")
	machine, err := vm.New(executable.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		input uint64
		want  uint64
	}{{0, 1}, {254, 255}, {255, 0}, {0x1234, 0x35}} {
		got, runErr := machine.Run(test.input)
		if runErr != nil || got != test.want {
			t.Fatalf("F(%#x) = %#x, %v; want %#x", test.input, got, runErr, test.want)
		}
	}
}

func TestSolanaSyscallDeclarationsRejectSpoofingAndWrongSignatures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name: "body spoof",
			source: `package p
func InvokeSignedC(a uint64, b uint64, c uint64, d uint64, e uint64) uint64 { return 0 }
`,
			want: "reserved and must be a bodyless declaration",
		},
		{
			name: "raw symbol spoof",
			source: `package p
func sol_invoke_signed_c(a uint64, b uint64, c uint64, d uint64, e uint64) uint64
func Program() {}
`,
			want: "functions without a body are not supported",
		},
		{
			name: "invoke wrong result",
			source: `package p
func InvokeSignedC(a uint64, b uint64, c uint64, d uint64, e uint64) uint32
func Program() {}
`,
			want: "must have signature func InvokeSignedC(uint64, uint64, uint64, uint64, uint64) uint64",
		},
		{
			name: "log wrong result",
			source: `package p
func Log(a uint64, b uint64) uint64
func Program() {}
`,
			want: "must have signature func Log(uint64, uint64)",
		},
		{
			name: "memset byte must be exact uint8",
			source: `package p
func Memset(a uint64, value uint32, length uint64)
func Program() {}
`,
			want: "must have signature func Memset(uint64, uint8, uint64)",
		},
		{
			name: "PDA pointer is guest address",
			source: `package p
func CreateProgramAddress(a *uint64, b uint64, c uint64, d uint64) uint64
func Program() {}
`,
			want: "must have signature func CreateProgramAddress(uint64, uint64, uint64, uint64) uint64",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CompileSource(test.name+".go", []byte(test.source))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestIRVerifierRejectsForgedSolanaSyscalls(t *testing.T) {
	t.Parallel()
	program := compileTestSource(t, `package main
func InvokeSignedC(a uint64, b uint64, c uint64, d uint64, e uint64) uint64
func Program(a uint64, b uint64, c uint64, d uint64, e uint64) uint64 {
	return InvokeSignedC(a, b, c, d, e)
}
`)
	function, _ := program.Function("Program")
	call := &function.Blocks[0].Instructions[0]
	if call.Op != OpSyscall {
		t.Fatalf("first instruction = %+v", *call)
	}

	originalID, originalSymbol := call.SyscallID, call.Callee
	call.SyscallID++
	if err := program.Validate(); err == nil || !strings.Contains(err.Error(), "want") {
		t.Fatalf("forged id validation error = %v", err)
	}
	call.SyscallID = originalID
	call.Callee = "sol_invoke_signed_rust"
	if err := program.Validate(); err == nil || !strings.Contains(err.Error(), "unknown Solana syscall symbol") {
		t.Fatalf("forged symbol validation error = %v", err)
	}
	call.Callee = originalSymbol
	call.Args = call.Args[:4]
	if err := program.Validate(); err == nil || !strings.Contains(err.Error(), "has 4 arguments, want 5") {
		t.Fatalf("forged signature validation error = %v", err)
	}
}

func TestAddressUint64SupportsFrameLocalGuestMemoryAccess(t *testing.T) {
	t.Parallel()
	source := []byte(`package main
func AddressUint64(pointer *uint64) uint64
func LoadUint64(address uint64) uint64
func StoreUint64(address uint64, value uint64)
func Program(index uint32) uint64 {
	words := [3]uint64{11, 22, 33}
	address := AddressUint64(&words[index])
	StoreUint64(address, uint64(index)+uint64(41))
	return LoadUint64(address)
}
`)
	program, err := CompileSource("address.go", source)
	if err != nil {
		t.Fatal(err)
	}
	function, _ := program.Function("Program")
	var sawAddress bool
	for _, block := range function.Blocks {
		for _, instruction := range block.Instructions {
			if instruction.Op == OpPointerAddress {
				sawAddress = true
				if instruction.Callee != "" || instruction.SyscallID != 0 {
					t.Fatalf("guest address unexpectedly linked as syscall: %+v", instruction)
				}
			}
		}
	}
	if !sawAddress {
		t.Fatal("OpPointerAddress not found")
	}
	executable, err := Generate(program, "Program")
	if err != nil {
		t.Fatal(err)
	}
	for _, instruction := range executable.Instructions {
		if instruction.Op == sbpf.CALL_IMM {
			t.Fatalf("AddressUint64 emitted a call: %+v", instruction)
		}
	}
	machine, err := vm.New(executable.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	for index := uint64(0); index < 3; index++ {
		got, runErr := machine.Run(index)
		if runErr != nil {
			t.Fatal(runErr)
		}
		want := index + 41
		if got != want {
			t.Fatalf("frame-local Store/Load at words[%d] = %d, want %d", index, got, want)
		}
	}
}

func TestAddressUint64RejectsFrameEscapeAndRetention(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name: "direct integer return",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func Program() uint64 { var word uint64; return AddressUint64(&word) }
`,
			want: "current-frame guest address escapes through an integer return",
		},
		{
			name: "move and arithmetic return",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func Program() uint64 {
	words := [2]uint64{1, 2}
	address := AddressUint64(&words[0])
	alias := address
	alias = alias + uint64(8)
	return alias
}
`,
			want: "current-frame guest address escapes through an integer return",
		},
		{
			name: "internal return laundering",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func Program() uint64 { var word uint64; return Identity(AddressUint64(&word)) }
func Identity(value uint64) uint64 { return value }
`,
			want: "current-frame guest address escapes through an integer return",
		},
		{
			name: "callee returns own frame",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func Leak() uint64 { var word uint64; return AddressUint64(&word) }
func Program() uint64 { return Leak() }
`,
			want: "IR function Leak",
		},
		{
			name: "direct external retention",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func StoreUint64(address uint64, value uint64)
func Program(inputAddress uint64) uint64 {
	var word uint64
	StoreUint64(inputAddress, AddressUint64(&word))
	return uint64(0)
}
`,
			want: "may be retained outside its owning stack object",
		},
		{
			name: "helper external retention",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func StoreUint64(address uint64, value uint64)
func Retain(destination uint64, value uint64) { StoreUint64(destination, value) }
func Program(inputAddress uint64) uint64 {
	var word uint64
	Retain(inputAddress, AddressUint64(&word))
	return uint64(0)
}
`,
			want: "call to Retain",
		},
		{
			name: "callee stores own frame into caller memory",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func StoreUint64(address uint64, value uint64)
func Leak(destination uint64) {
	var word uint64
	StoreUint64(destination, AddressUint64(&word))
}
func Program(inputAddress uint64) uint64 { Leak(inputAddress); return uint64(0) }
`,
			want: "IR function Leak",
		},
		{
			name: "local store load laundering",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func LoadUint64(address uint64) uint64
func StoreUint64(address uint64, value uint64)
func Program() uint64 {
	var word uint64
	base := AddressUint64(&word)
	StoreUint64(base, base)
	return LoadUint64(base)
}
`,
			want: "current-frame guest address escapes through an integer return",
		},
		{
			name: "fixed array copy laundering",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func StoreUint64(address uint64, value uint64)
func Program() uint64 {
	first := [1]uint64{0}
	second := [1]uint64{0}
	base := AddressUint64(&first[0])
	StoreUint64(base, base)
	second = first
	return second[0]
}
`,
			want: "current-frame guest address escapes through an integer return",
		},
		{
			name: "memcpy external retention",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func StoreUint64(address uint64, value uint64)
func Memcpy(destinationAddress uint64, sourceAddress uint64, length uint64)
func Program(inputAddress uint64) uint64 {
	var word uint64
	base := AddressUint64(&word)
	StoreUint64(base, base)
	Memcpy(inputAddress, base, uint64(8))
	return uint64(0)
}
`,
			want: "Solana syscall sol_memcpy_",
		},
		{
			name: "memset byte retention",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func Memset(destinationAddress uint64, value uint8, length uint64)
func Program(inputAddress uint64) uint64 {
	var word uint64
	base := AddressUint64(&word)
	Memset(inputAddress, uint8(base), uint64(1))
	return uint64(0)
}
`,
			want: "Solana syscall sol_memset_",
		},
		{
			name: "out of object retention",
			source: `package p
func AddressUint64(pointer *uint64) uint64
func StoreUint64(address uint64, value uint64)
func Program() uint64 {
	var word uint64
	base := AddressUint64(&word)
	StoreUint64(base+uint64(4096), base)
	return uint64(0)
}
`,
			want: "may be retained outside its owning stack object",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CompileSource(test.name+".go", []byte(test.source))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestAddressUint64AllowsBoundedCallerFrameConsumption(t *testing.T) {
	t.Parallel()
	source := []byte(`package p
func AddressUint64(pointer *uint64) uint64
func LoadUint64(address uint64) uint64
func StoreUint64(address uint64, value uint64)
func InvokeSignedC(instructionAddress uint64, accountInfosAddress uint64, accountInfosLength uint64, signerSeedsAddress uint64, signerSeedsLength uint64) uint64
func Identity(value uint64) uint64 { return value }
func Write(address uint64, value uint64) { StoreUint64(address, value) }
func Program(value uint64) uint64 {
	words := [4]uint64{0, 0, 0, 0}
	pointer := &words[0]
	base := AddressUint64(pointer)
	Write(base, value)
	stored := LoadUint64(base)
	Write(base+uint64(8), base+uint64(16))
	status := InvokeSignedC(Identity(base), base+uint64(8), stored-stored, uint64(0), uint64(0))
	return status
}
`)
	program, err := CompileSource("bounded.go", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Generate(program, "Program"); err != nil {
		t.Fatal(err)
	}
}

func TestAddressUint64FlowSensitiveOverwriteDoesNotEscape(t *testing.T) {
	t.Parallel()
	_, err := CompileSource("overwrite.go", []byte(`package p
func AddressUint64(pointer *uint64) uint64
func StoreUint64(address uint64, value uint64)
func Program() uint64 {
	words := [2]uint64{0, 0}
	base := AddressUint64(&words[0])
	value := base
	StoreUint64(base, value)
	value = uint64(0)
	return value
}
`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestAddressUint64IRVerifierRejectsForgedReturn(t *testing.T) {
	t.Parallel()
	program, err := CompileSource("verified.go", []byte(`package p
func AddressUint64(pointer *uint64) uint64
func StoreUint64(address uint64, value uint64)
func Program() uint64 {
	var word uint64
	base := AddressUint64(&word)
	StoreUint64(base, uint64(7))
	return uint64(7)
}
`))
	if err != nil {
		t.Fatal(err)
	}
	function, _ := program.Function("Program")
	address := NoValue
	for _, block := range function.Blocks {
		for _, instruction := range block.Instructions {
			if instruction.Op == OpPointerAddress {
				address = instruction.Dest
			}
		}
	}
	if address == NoValue {
		t.Fatal("OpPointerAddress not found")
	}
	for _, block := range function.Blocks {
		if block.Terminator.Kind == TermReturn {
			block.Terminator.Result = address
		}
	}
	if err = program.Validate(); err == nil || !strings.Contains(err.Error(), "escapes through an integer return") {
		t.Fatalf("forged IR validation error = %v", err)
	}
	if _, err = Generate(program, "Program"); err == nil || !strings.Contains(err.Error(), "escapes through an integer return") {
		t.Fatalf("forged IR generation error = %v", err)
	}
}

func TestIRVerifierRejectsForgedPointerResultWithoutAddressIntrinsic(t *testing.T) {
	t.Parallel()
	program := &Program{
		Package: "p",
		Functions: []*Function{{
			Name:   "Leak",
			Result: PointerTo(TypeUint64),
			Values: []Value{{ID: 0, Name: "address", Type: PointerTo(TypeUint64), Kind: ValueTemporary}},
			Memory: []MemoryObject{{ID: 0, Name: "word", Element: TypeUint64, Length: 1}},
			Blocks: []*BasicBlock{{
				ID:           0,
				Name:         "entry",
				Instructions: []Instruction{{Op: OpAddress, Dest: 0, Object: 0}},
				Terminator:   Terminator{Kind: TermReturn, Result: 0},
			}},
			Entry: 0,
		}},
	}
	if err := program.Validate(); err == nil || !strings.Contains(err.Error(), "pointer results are forbidden") {
		t.Fatalf("forged pointer-result IR validation error = %v", err)
	}
	if _, err := Generate(program, "Leak"); err == nil || !strings.Contains(err.Error(), "pointer results are forbidden") {
		t.Fatalf("forged pointer-result IR generation error = %v", err)
	}
}

func TestAddressUint64RejectsSpoofAndWrongSignature(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, declaration, want string
	}{
		{
			name:        "body spoof",
			declaration: "func AddressUint64(pointer *uint64) uint64 { return 0 }",
			want:        "reserved and must be a bodyless declaration",
		},
		{
			name:        "integer parameter",
			declaration: "func AddressUint64(pointer uint64) uint64",
			want:        "must have signature func(pointer *uint64) uint64",
		},
		{
			name:        "wrong pointer element",
			declaration: "func AddressUint64(pointer *uint32) uint64",
			want:        "must have signature func(pointer *uint64) uint64",
		},
		{
			name:        "wrong result",
			declaration: "func AddressUint64(pointer *uint64) int64",
			want:        "must have signature func(pointer *uint64) uint64",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := []byte("package p\n" + test.declaration + "\nfunc Program() {}\n")
			_, err := CompileSource(test.name+".go", source)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestMinimalStackBackedCPIProgramCompilesForSolanaEntrypoint(t *testing.T) {
	t.Parallel()
	program, err := CompileSource("cpi.go", []byte(`package program
func AddressUint64(pointer *uint64) uint64
func InvokeSignedC(instructionAddress uint64, accountInfosAddress uint64, accountInfosLength uint64, signerSeedsAddress uint64, signerSeedsLength uint64) uint64
func Program(inputAddress uint64, instructionDataAddress uint64) uint64 {
	// Five u64 words are the exact repr(C) SolInstruction fields. A real
	// program obtains the program-id/account pointers from the Agave input.
	instruction := [5]uint64{inputAddress, 0, 0, instructionDataAddress, 0}
	return InvokeSignedC(AddressUint64(&instruction[0]), 0, 0, 0, 0)
}
`))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := GenerateSolanaEntrypoint(program, "Program")
	if err != nil {
		t.Fatal(err)
	}
	var staticCalls, internalCalls int
	for _, instruction := range executable.Instructions {
		if instruction.Op != sbpf.CALL_IMM {
			continue
		}
		if instruction.Src == 0 {
			staticCalls++
			if uint32(instruction.Immediate) != sbpf.HashSymbolName("sol_invoke_signed_c") {
				t.Fatalf("wrong CPI syscall key: %+v", instruction)
			}
		} else if instruction.Src == 1 {
			internalCalls++
		}
	}
	if staticCalls != 1 || internalCalls != 1 {
		t.Fatalf("static/internal calls = %d/%d, want 1/1", staticCalls, internalCalls)
	}
}

func TestSyscallIntrinsicRegistryRejectsSymbolAndHashCollisions(t *testing.T) {
	t.Parallel()
	if err := validateSyscallIntrinsics(); err != nil {
		t.Fatalf("built-in registry: %v", err)
	}
	clone := make(map[string]syscallIntrinsic, len(syscallIntrinsics)+1)
	for name, intrinsic := range syscallIntrinsics {
		clone[name] = intrinsic
	}
	duplicate := clone["Log"]
	clone["FakeLog"] = duplicate
	if err := validateSyscallIntrinsicSet(clone); err == nil || !strings.Contains(err.Error(), "registered by") {
		t.Fatalf("duplicate symbol error = %v", err)
	}

	delete(clone, "FakeLog")
	collision := clone["Log"]
	collision.Symbol = "different_symbol"
	// Preserve Log's ID deliberately; validation must reject the mismatched
	// symbol hash before a colliding static-call key can enter the backend.
	clone["Collision"] = collision
	if err := validateSyscallIntrinsicSet(clone); err == nil || !strings.Contains(err.Error(), "invalid Solana syscall intrinsic") {
		t.Fatalf("forged hash error = %v", err)
	}
}

func TestIRVerifierRejectsNonCanonicalSyscallFields(t *testing.T) {
	t.Parallel()
	program := compileTestSource(t, `package main
func Log(address uint64, length uint64)
func Program(address uint64, length uint64) { Log(address, length) }
`)
	function, _ := program.Function("Program")
	call := &function.Blocks[0].Instructions[0]
	if call.Op != OpSyscall {
		t.Fatalf("first instruction = %+v", *call)
	}
	call.Imm = 1
	if err := program.Validate(); err == nil || !strings.Contains(err.Error(), "non-canonical unused fields") {
		t.Fatalf("non-canonical syscall error = %v", err)
	}
}

func TestStaticSyscallArgumentRegistersAndUint8Normalization(t *testing.T) {
	t.Parallel()
	invoke, err := Compile("invoke_args.go", []byte(`package main
func InvokeSignedC(a uint64, b uint64, c uint64, d uint64, e uint64) uint64
func Program(a uint64, b uint64, c uint64, d uint64, e uint64) uint64 {
	return InvokeSignedC(a, b, c, d, e)
}
`), "Program")
	if err != nil {
		t.Fatal(err)
	}
	inputs := []uint64{11, 22, 33, 44, 55}
	for index, want := range inputs {
		got := registerBeforeFirstStaticCall(t, invoke, sbpf.Register(index+1), inputs...)
		if got != want {
			t.Errorf("InvokeSignedC r%d = %d, want %d", index+1, got, want)
		}
	}

	memset, err := Compile("memset_args.go", []byte(`package main
func Memset(address uint64, value uint8, length uint64)
func Program(address uint64, value uint32, length uint64) uint64 {
	Memset(address, uint8(value), length)
	return 0
}
`), "Program")
	if err != nil {
		t.Fatal(err)
	}
	memsetInputs := []uint64{0x200000080, 0x1234, 99}
	wantMemset := []uint64{0x200000080, 0x34, 99}
	for index, want := range wantMemset {
		got := registerBeforeFirstStaticCall(t, memset, sbpf.Register(index+1), memsetInputs...)
		if got != want {
			t.Errorf("Memset r%d = %#x, want %#x", index+1, got, want)
		}
	}
}

func registerBeforeFirstStaticCall(t *testing.T, executable *Executable, register sbpf.Register, arguments ...uint64) uint64 {
	t.Helper()
	prefix := make([]sbpf.Instruction, 0, len(executable.Instructions)+2)
	found := false
	for _, instruction := range executable.Instructions {
		if instruction.Op == sbpf.CALL_IMM && instruction.Src == 0 {
			prefix = append(prefix, sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R0, register), sbpf.Return())
			found = true
			break
		}
		prefix = append(prefix, instruction)
	}
	if !found {
		t.Fatal("static syscall not found")
	}
	machine, err := vm.New(prefix)
	if err != nil {
		t.Fatalf("build register probe VM: %v", err)
	}
	result, err := machine.Run(arguments...)
	if err != nil {
		t.Fatalf("run register probe VM: %v", err)
	}
	return result
}
