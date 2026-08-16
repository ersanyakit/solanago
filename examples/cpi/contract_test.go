package cpi_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ersanyakit/solanago/compiler"
	sbpfelf "github.com/ersanyakit/solanago/elf"
	"github.com/ersanyakit/solanago/sbpf"
)

func TestGoCPIContractCompilesToStrictELFWithStaticSyscall(t *testing.T) {
	path := contractSourcePath(t)
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := compiler.CompileSource(path, source)
	if err != nil {
		t.Fatalf("compile Go CPI source: %v", err)
	}
	executable, err := compiler.GenerateSolanaEntrypoint(program, "Program")
	if err != nil {
		t.Fatalf("generate Go CPI entrypoint: %v", err)
	}

	wantID := sbpf.HashSymbolName("sol_invoke_signed_c")
	staticCalls := 0
	for _, instruction := range executable.Instructions {
		if instruction.Op == sbpf.CALL_IMM && instruction.Src == 0 {
			staticCalls++
			if uint32(instruction.Immediate) != wantID {
				t.Fatalf("static syscall id = 0x%08x, want sol_invoke_signed_c 0x%08x", uint32(instruction.Immediate), wantID)
			}
		}
	}
	if staticCalls != 1 {
		t.Fatalf("static syscall count = %d, want exactly one", staticCalls)
	}

	artifact, err := sbpfelf.BuildV3(executable.Bytecode, 0)
	if err != nil {
		t.Fatalf("build strict ELF: %v", err)
	}
	parsed, err := sbpfelf.ParseStrictV3(artifact)
	if err != nil {
		t.Fatalf("parse generated strict ELF: %v", err)
	}
	if len(parsed.Text) != len(executable.Bytecode) {
		t.Fatalf("ELF text length = %d, want %d", len(parsed.Text), len(executable.Bytecode))
	}
}

func contractSourcePath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Join(filepath.Dir(filename), "testdata", "program.go")
}
