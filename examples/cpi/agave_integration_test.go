package cpi_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ersanyakit/go-solana/compiler"
	"github.com/ersanyakit/go-solana/deploy"
	sbpfelf "github.com/ersanyakit/go-solana/elf"
	"github.com/ersanyakit/go-solana/examples/cpi"
	"github.com/ersanyakit/go-solana/sdk"
	"github.com/ersanyakit/go-solana/sdk/system"
	"github.com/ersanyakit/go-solana/svmtest"
)

// TestAgaveSystemTransferCPI deploys a Go-compiled ELF through the Go loader,
// executes sol_invoke_signed_c in official Agave, and proves exact finalized
// balance deltas. It also asks the real runtime to reject writable and signer
// privilege escalation. The validator is an execution oracle, not a compiler.
func TestAgaveSystemTransferCPI(t *testing.T) {
	binDir := os.Getenv("GOSBF_AGAVE_BIN")
	programPath := os.Getenv("GOSBF_CPI_SO")
	if binDir == "" || programPath == "" {
		t.Skip("set GOSBF_AGAVE_BIN and GOSBF_CPI_SO for the official-Agave CPI gate")
	}
	artifact, err := os.ReadFile(programPath)
	if err != nil {
		t.Fatalf("read Go CPI ELF: %v", err)
	}
	sourcePath := contractSourcePath(t)
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read checked-in Go CPI source: %v", err)
	}
	compiled, err := compiler.CompileSource(sourcePath, sourceBytes)
	if err != nil {
		t.Fatalf("compile checked-in Go CPI source: %v", err)
	}
	executable, err := compiler.GenerateSolanaEntrypoint(compiled, "Program")
	if err != nil {
		t.Fatalf("generate checked-in Go CPI entrypoint: %v", err)
	}
	wantArtifact, err := sbpfelf.BuildV3(executable.Bytecode, 0)
	if err != nil {
		t.Fatalf("build checked-in Go CPI ELF: %v", err)
	}
	if !bytes.Equal(artifact, wantArtifact) {
		t.Fatalf("GOSBF_CPI_SO is not the byte-exact ELF rebuilt from testdata/program.go: got %d bytes, want %d", len(artifact), len(wantArtifact))
	}
	payer := newCPISigner(t)
	program := newCPISigner(t)
	source := newCPISigner(t)
	destination := newCPISigner(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	validator, err := svmtest.StartLocalValidator(ctx, svmtest.LocalValidatorConfig{
		AgaveBinDir: binDir,
		Payer:       payer,
		LedgerDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := validator.Close(); closeErr != nil {
			t.Errorf("close validator: %v", closeErr)
		}
	}()

	deployed, err := deploy.Program(ctx, deploy.Config{
		Client:   validator.Client,
		FeePayer: payer,
		Program:  program,
	}, artifact)
	if err != nil {
		t.Fatalf("deploy Go CPI program: %v (partial=%+v, validator log %s)", err, deployed, validator.LogPath)
	}
	if !deployed.Finalized || deployed.ProgramID != program.PublicKey {
		t.Fatalf("incomplete CPI deployment: %+v", deployed)
	}

	const sourceFunding = uint64(2_000_000)
	const destinationFunding = uint64(1_000_000)
	createSignature, err := validator.Client.SendInstructions(ctx, payer, []svmtest.Signer{source, destination}, []sdk.Instruction{
		system.CreateAccount(payer.PublicKey, source.PublicKey, sourceFunding, 0, system.ProgramID),
		system.CreateAccount(payer.PublicKey, destination.PublicKey, destinationFunding, 0, system.ProgramID),
	})
	if err != nil {
		t.Fatalf("create System accounts: %v (validator log %s)", err, validator.LogPath)
	}

	const amount = uint64(123_456)
	beforeSource := balance(t, ctx, validator.Client, source.PublicKey)
	beforeDestination := balance(t, ctx, validator.Client, destination.PublicKey)
	invokeSignature, err := validator.Client.SendInstructions(ctx, payer, []svmtest.Signer{source}, []sdk.Instruction{
		cpi.Transfer(program.PublicKey, source.PublicKey, destination.PublicKey, amount),
	})
	if err != nil {
		t.Fatalf("real System CPI: %v (validator log %s)", err, validator.LogPath)
	}
	afterSource := balance(t, ctx, validator.Client, source.PublicKey)
	afterDestination := balance(t, ctx, validator.Client, destination.PublicKey)
	if afterSource > beforeSource || afterDestination < beforeDestination {
		t.Fatalf("CPI moved balances in the wrong direction: source %d->%d destination %d->%d", beforeSource, afterSource, beforeDestination, afterDestination)
	}
	sourceDelta := beforeSource - afterSource
	destinationDelta := afterDestination - beforeDestination
	if sourceDelta != amount || destinationDelta != amount {
		t.Fatalf("CPI balance delta source=%d destination=%d, want -%d/+%d", sourceDelta, destinationDelta, amount, amount)
	}

	readonlyDestination := cpi.Transfer(program.PublicKey, source.PublicKey, destination.PublicKey, 1)
	readonlyDestination.Accounts[1].IsWritable = false
	_, writableErr := validator.Client.SendInstructions(ctx, payer, []svmtest.Signer{source}, []sdk.Instruction{readonlyDestination})
	if writableErr == nil {
		t.Fatal("real Agave accepted CPI writable privilege escalation")
	}
	if !isPrivilegeEscalationError(writableErr) {
		t.Fatalf("writable escalation failed for an unrelated reason: %v", writableErr)
	}

	unsignedSource := cpi.Transfer(program.PublicKey, source.PublicKey, destination.PublicKey, 1)
	unsignedSource.Accounts[0].IsSigner = false
	_, signerErr := validator.Client.SendInstructions(ctx, payer, nil, []sdk.Instruction{unsignedSource})
	if signerErr == nil {
		t.Fatal("real Agave accepted CPI signer privilege escalation")
	}
	if !isPrivilegeEscalationError(signerErr) {
		t.Fatalf("signer escalation failed for an unrelated reason: %v", signerErr)
	}
	if gotSource, gotDestination := balance(t, ctx, validator.Client, source.PublicKey), balance(t, ctx, validator.Client, destination.PublicKey); gotSource != afterSource || gotDestination != afterDestination {
		t.Fatalf("rejected CPI mutated balances: source %d->%d destination %d->%d", afterSource, gotSource, afterDestination, gotDestination)
	}

	t.Logf("official Agave CPI finalized: program=%s deploy=%s create=%s invoke=%s amount=%d",
		program.PublicKey, deployed.Signatures[len(deployed.Signatures)-1], createSignature, invokeSignature, amount)
}

func newCPISigner(t *testing.T) svmtest.Signer {
	t.Helper()
	signer, err := svmtest.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func balance(t *testing.T, ctx context.Context, client svmtest.Client, address sdk.Pubkey) uint64 {
	t.Helper()
	value, err := client.Balance(ctx, address)
	if err != nil {
		t.Fatalf("balance %s: %v", address, err)
	}
	return value
}

func isPrivilegeEscalationError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "privilege") || strings.Contains(message, "unauthorized signer or writable account")
}
