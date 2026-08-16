package svmtest

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestAgaveLocalValidator is opt-in because it launches the official external
// validator. CI's real-SVM job supplies both environment variables.
func TestAgaveLocalValidator(t *testing.T) {
	binDir := os.Getenv("GOSBF_AGAVE_BIN")
	program := os.Getenv("GOSBF_PROGRAM_SO")
	if binDir == "" || program == "" {
		t.Skip("set GOSBF_AGAVE_BIN and GOSBF_PROGRAM_SO for the real Agave gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	validator, err := StartLocalValidator(ctx, LocalValidatorConfig{
		AgaveBinDir: binDir,
		ProgramPath: program,
		LedgerDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := validator.Close(); err != nil {
			t.Errorf("close validator: %v", err)
		}
	}()
	transactionContext, transactionCancel := context.WithTimeout(ctx, 30*time.Second)
	defer transactionCancel()
	signature, err := validator.Invoke(transactionContext, []byte{0x47, 0x4f, 0x53, 0x50, 0x4c})
	if err != nil {
		t.Fatalf("real Agave transaction failed: %v (validator log %s)", err, validator.LogPath)
	}
	if signature == "" {
		t.Fatal("real Agave transaction returned no signature")
	}
	t.Logf("real Agave transaction confirmed: %s", signature)
}
