package context_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ersanyakit/solanago/compiler"
	sbpfelf "github.com/ersanyakit/solanago/elf"
)

// TestContextExampleCompilesAcrossTwoFilesWithAccountIntrinsics exercises all
// three features together: program.go and accounts.go are compiled as one
// package via CompileFiles (multi-file support), program.go calls AccountAt
// (defined only in accounts.go) with no cross-file redeclaration, and
// accounts.go itself is built entirely from the account-field intrinsics
// instead of hand-computed record+N offsets.
func TestContextExampleCompilesAcrossTwoFilesWithAccountIntrinsics(t *testing.T) {
	directory := testdataDir(t)
	filenames := []string{
		filepath.Join(directory, "accounts.go"),
		filepath.Join(directory, "program.go"),
	}
	program, err := compiler.CompileFiles(filenames)
	if err != nil {
		t.Fatalf("compile context example across files: %v", err)
	}
	if _, ok := program.Function("AccountAt"); !ok {
		t.Fatal("AccountAt from accounts.go is missing from the compiled package")
	}
	if _, ok := program.Function("Program"); !ok {
		t.Fatal("Program from program.go is missing from the compiled package")
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
