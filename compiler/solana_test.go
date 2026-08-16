package compiler

import (
	"strings"
	"testing"

	"github.com/ersany/go-solana/sbpf"
	"github.com/ersany/go-solana/vm"
)

func TestGenerateSolanaEntrypointForwardsAgaveRegisters(t *testing.T) {
	source := []byte(`package program
func Program(inputAddress uint64, instructionDataAddress uint64) uint64 {
	return instructionDataAddress
}`)
	program, err := CompileSource("program.go", source)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := GenerateSolanaEntrypoint(program, "Program")
	if err != nil {
		t.Fatal(err)
	}
	if executable.Entry != "entrypoint" || executable.Functions["entrypoint"] != 0 || executable.Functions["Program"] != 2 {
		t.Fatalf("unexpected entrypoint metadata: %#v", executable)
	}
	if executable.Instructions[0] != sbpf.CallRelative(1) || executable.Instructions[1] != sbpf.Return() {
		t.Fatalf("missing generated wrapper: %#v", executable.Instructions[:2])
	}
	machine, err := vm.New(executable.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	const inputStart = uint64(4) << 32
	const instructionDataAddress = inputStart + 123
	result, err := machine.Run(inputStart, instructionDataAddress)
	if err != nil {
		t.Fatal(err)
	}
	if result != instructionDataAddress {
		t.Fatalf("got %#x", result)
	}
}

func TestGenerateSolanaEntrypointRejectsWrongSignature(t *testing.T) {
	program, err := CompileSource("bad.go", []byte("package p\nfunc Program(x uint64) uint64 { return x }"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = GenerateSolanaEntrypoint(program, "Program")
	if err == nil || !strings.Contains(err.Error(), "must accept") {
		t.Fatalf("got %v", err)
	}
}
