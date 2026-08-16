package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ersanyakit/solanago/compiler"
	gosoldeploy "github.com/ersanyakit/solanago/deploy"
	"github.com/ersanyakit/solanago/sdk"
)

func TestRunAddExample(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runCLI([]string{"run", "../../examples/add.go"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runCLI: %v; stderr=%s", err, stderr.String())
	}
	if got, want := stdout.String(), "result: 30\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestScalarCLIParsesAndPrintsNarrowTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind compiler.Type
		text string
		want uint64
	}{
		{compiler.TypeUint8, "0xff", math.MaxUint8},
		{compiler.TypeUint32, "4294967295", math.MaxUint32},
		{compiler.TypeInt32, "-2147483648", math.MaxUint64 - math.MaxInt32},
		{compiler.TypeInt32, "2147483647", math.MaxInt32},
	}
	for _, test := range tests {
		got, err := parseScalar(test.kind, test.text)
		if err != nil || got != test.want {
			t.Fatalf("parseScalar(%s, %q) = %d, %v; want %d", test.kind, test.text, got, err, test.want)
		}
	}
	for _, overflow := range []struct {
		kind compiler.Type
		text string
	}{
		{compiler.TypeUint8, "256"},
		{compiler.TypeUint32, "4294967296"},
		{compiler.TypeInt32, "-2147483649"},
		{compiler.TypeInt32, "2147483648"},
	} {
		if _, err := parseScalar(overflow.kind, overflow.text); err == nil {
			t.Fatalf("parseScalar(%s, %q) accepted an out-of-range value", overflow.kind, overflow.text)
		}
	}
	var output bytes.Buffer
	printResult(&output, compiler.TypeInt32, math.MaxUint64-122)
	if got, want := output.String(), "result: -123\n"; got != want {
		t.Fatalf("int32 output = %q, want %q", got, want)
	}
}

func TestBuildAndDisassembleOfficialAdd(t *testing.T) {
	output := filepath.Join(t.TempDir(), "program.sbpf")
	var stdout, stderr bytes.Buffer
	err := runCLI([]string{"build", "-o", output, "-func", "Add", "../../examples/add.go"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("build: %v; stderr=%s", err, stderr.String())
	}
	bytecode, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(bytecode), 24; got != want {
		t.Fatalf("bytecode length = %d, want %d", got, want)
	}
	stdout.Reset()
	stderr.Reset()
	if err := runCLI([]string{"disassemble", output}, &stdout, &stderr); err != nil {
		t.Fatalf("disassemble: %v; stderr=%s", err, stderr.String())
	}
	want := "0000: MOV64_REG r0, r1\n0001: ADD64_REG r0, r2\n0002: EXIT\n"
	if stdout.String() != want {
		t.Fatalf("disassembly\n got: %q\nwant: %q", stdout.String(), want)
	}
}

func TestBuildAcceptsMultipleSourceFiles(t *testing.T) {
	directory := t.TempDir()
	fileA := filepath.Join(directory, "a.go")
	fileB := filepath.Join(directory, "b.go")
	if err := os.WriteFile(fileA, []byte("package program\nfunc Add(a uint64, b uint64) uint64 { return a + b }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("package program\nfunc Main() uint64 { return Add(uint64(4), uint64(6)) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "program.sbpf")
	var stdout, stderr bytes.Buffer
	if err := runCLI([]string{"build", "-o", output, "-func", "Main", fileA, fileB}, &stdout, &stderr); err != nil {
		t.Fatalf("build: %v; stderr=%s", err, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if err := runCLI([]string{"run", "-func", "Main", "-files", fileB, fileA}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v; stderr=%s", err, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "result: 10"; got != want {
		t.Fatalf("run -files output = %q, want %q", got, want)
	}
}

func TestRunReportsUnsupportedSource(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bad.go")
	if err := os.WriteFile(file, []byte("package main\nfunc F() { go F() }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := runCLI([]string{"run", file}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "goroutines") {
		t.Fatalf("error = %v, want unsupported goroutine diagnostic", err)
	}
}

func TestBuildVerifyAndDisassembleStrictSolanaELF(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "program.go")
	if err := os.WriteFile(source, []byte("package program\nfunc Program(inputAddress uint64, instructionDataAddress uint64) uint64 { return 0 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "program.so")
	var stdout, stderr bytes.Buffer
	if err := runCLI([]string{"build", "-target", "solana", "-o", output, source}, &stdout, &stderr); err != nil {
		t.Fatalf("build: %v; stderr=%s", err, stderr.String())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 4 || string(data[:4]) != "\x7fELF" {
		t.Fatalf("not an ELF: %x", data[:min(4, len(data))])
	}
	stdout.Reset()
	stderr.Reset()
	if err := runCLI([]string{"verify", output}, &stdout, &stderr); err != nil {
		t.Fatalf("verify: %v; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "sBPFv3") {
		t.Fatalf("verify output = %q", stdout.String())
	}
	stdout.Reset()
	if err := runCLI([]string{"disassemble", output}, &stdout, &stderr); err != nil {
		t.Fatalf("disassemble: %v", err)
	}
	if !strings.Contains(stdout.String(), "CALL_IMM") || !strings.Contains(stdout.String(), "EXIT") {
		t.Fatalf("unexpected disassembly: %s", stdout.String())
	}

	programKeypair := filepath.Join(directory, "program-keypair.json")
	payerKeypair := filepath.Join(directory, "payer.json")
	if err := runCLI([]string{"keygen", "-o", programKeypair}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := runCLI([]string{"keygen", "-o", payerKeypair}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := runCLI([]string{"airdrop", "--dry-run", "--keypair", payerKeypair, "--url", "localhost"}, &stdout, &stderr); err != nil {
		t.Fatalf("airdrop dry-run: %v", err)
	}
	if !strings.Contains(stdout.String(), "validated airdrop request") {
		t.Fatalf("airdrop dry-run output = %q", stdout.String())
	}
	if err := runCLI([]string{"airdrop", "--dry-run", "--keypair", payerKeypair, "--url", "testnet"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "--allow-live") {
		t.Fatalf("airdrop live guard error = %v", err)
	}
	stdout.Reset()
	if err := runCLI([]string{"deploy", "--dry-run", "--program-id", programKeypair, "--keypair", payerKeypair, output}, &stdout, &stderr); err != nil {
		t.Fatalf("safe dry-run: %v", err)
	}
	if !strings.Contains(stdout.String(), "Go-only deploy") {
		t.Fatalf("dry-run output = %q", stdout.String())
	}
	var resumeBuffer sdk.Pubkey
	resumeBuffer[0] = 7
	stdout.Reset()
	if err := runCLI([]string{
		"deploy", "--dry-run", "--buffer", resumeBuffer.String(),
		"--program-id", programKeypair, "--keypair", payerKeypair, output,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("resume dry-run: %v", err)
	}
	if !strings.Contains(stdout.String(), "Go-only deploy resume") || !strings.Contains(stdout.String(), "buffer="+resumeBuffer.String()) {
		t.Fatalf("resume dry-run output = %q", stdout.String())
	}
	if err := runCLI([]string{
		"deploy", "--dry-run", "--buffer", "not-a-pubkey",
		"--program-id", programKeypair, "--keypair", payerKeypair, output,
	}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "invalid --buffer") {
		t.Fatalf("invalid resume buffer error = %v", err)
	}
	if err := runCLI([]string{"deploy", "--dry-run", "--program-id", programKeypair, "--keypair", payerKeypair, "--url", "https://api.mainnet-beta.solana.com", output}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "--allow-live") {
		t.Fatalf("live guard error = %v", err)
	}
}

func TestInitCreatesBuildableProject(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "hello")
	var stdout, stderr bytes.Buffer
	if err := runCLI([]string{"init", directory}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"program.go", "go.mod", "README.md"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	output := filepath.Join(directory, "program.so")
	if err := runCLI([]string{"build", "-target", "solana", "-o", output, filepath.Join(directory, "program.go")}, &stdout, &stderr); err != nil {
		t.Fatalf("build initialized project: %v", err)
	}
	if err := runCLI([]string{"init", directory}, &stdout, &stderr); err == nil {
		t.Fatal("second init unexpectedly overwrote a non-empty project")
	}
}

func TestHasPartialDeployJournal(t *testing.T) {
	if hasPartialDeployJournal(nil) || hasPartialDeployJournal(&gosoldeploy.Result{}) {
		t.Fatal("empty result reported as a partial deploy journal")
	}
	if !hasPartialDeployJournal(&gosoldeploy.Result{SubmittedSignatures: []string{"submitted"}}) {
		t.Fatal("submitted-only journal was hidden")
	}
	if !hasPartialDeployJournal(&gosoldeploy.Result{Signatures: []string{"finalized"}}) {
		t.Fatal("finalized journal was hidden")
	}
	var buffer sdk.Pubkey
	buffer[0] = 1
	if !hasPartialDeployJournal(&gosoldeploy.Result{BufferAddress: buffer}) {
		t.Fatal("buffer-only recovery journal was hidden")
	}
}
