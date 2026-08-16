package erc1155_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ersanyakit/solanago/compiler"
	sbpfelf "github.com/ersanyakit/solanago/elf"
)

// TestERC1155ContractCompilesAcrossTwoFilesToStrictELF compiles
// accounts.go and program.go together (multi-file support) and confirms
// the result is a valid, re-parseable strict sBPFv3 ELF — the same
// build+verify smoke test every other example in this repo has.
func TestERC1155ContractCompilesAcrossTwoFilesToStrictELF(t *testing.T) {
	directory := testdataDir(t)
	filenames := []string{
		filepath.Join(directory, "accounts.go"),
		filepath.Join(directory, "program.go"),
	}
	program, err := compiler.CompileFiles(filenames)
	if err != nil {
		t.Fatalf("compile erc1155 contract across files: %v", err)
	}
	for _, name := range []string{"AccountAt", "RequireOwned", "Program", "ProcessTransferFrom"} {
		if _, ok := program.Function(name); !ok {
			t.Fatalf("%s missing from the compiled package", name)
		}
	}

	executable, err := compiler.GenerateSolanaEntrypoint(program, "Program")
	if err != nil {
		t.Fatalf("generate Solana entrypoint: %v", err)
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

func testdataDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	directory := filepath.Join(filepath.Dir(filename), "testdata")
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("testdata directory: %v", err)
	}
	return directory
}
