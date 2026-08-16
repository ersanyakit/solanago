package svmtest_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ersany/go-solana/deploy"
	"github.com/ersany/go-solana/sdk"
	"github.com/ersany/go-solana/svmtest"
)

// TestAgaveGoOnlyDeployAndInvoke proves the production-facing path that this
// repository owns: a strict ELF is deployed through JSON-RPC transactions
// assembled and signed in Go, then invoked on an official Agave validator.
// The external validator binary is an acceptance oracle, not a build tool.
func TestAgaveGoOnlyDeployAndInvoke(t *testing.T) {
	binDir := os.Getenv("GOSBF_AGAVE_BIN")
	programPath := os.Getenv("GOSBF_PROGRAM_SO")
	if binDir == "" || programPath == "" {
		t.Skip("set GOSBF_AGAVE_BIN and GOSBF_PROGRAM_SO for the real Go-only deploy gate")
	}
	artifact, err := os.ReadFile(programPath)
	if err != nil {
		t.Fatal(err)
	}
	payer, err := svmtest.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	program, err := svmtest.NewSigner()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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

	result, err := deploy.Program(ctx, deploy.Config{
		Client:   validator.Client,
		FeePayer: payer,
		Program:  program,
	}, artifact)
	if err != nil {
		t.Fatalf("Go-only deployment failed: %v (partial=%+v, validator log %s)", err, result, validator.LogPath)
	}
	if !result.Finalized || result.ProgramID != program.PublicKey || len(result.Signatures) < 3 {
		t.Fatalf("incomplete deployment result: %+v", result)
	}

	signature, err := validator.Client.SendInstructions(ctx, payer, nil, []sdk.Instruction{{
		ProgramID: program.PublicKey,
		Data:      []byte("GOSPL"),
	}})
	if err != nil {
		t.Fatalf("deployed Go program invocation failed: %v (validator log %s)", err, validator.LogPath)
	}
	if signature == "" {
		t.Fatal("deployed Go program invocation returned no signature")
	}
	t.Logf("Go-only deploy finalized: program=%s deploy=%s invoke=%s", program.PublicKey, result.Signatures[len(result.Signatures)-1], signature)
}
