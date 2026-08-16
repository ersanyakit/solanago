package svmtest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sbpfelf "github.com/ersanyakit/solanago/elf"
	"github.com/ersanyakit/solanago/sdk"
)

// LocalValidatorConfig selects an official Agave binary distribution and,
// optionally, a strict program ELF to preload at genesis. Leaving ProgramPath
// empty starts an otherwise identical validator for testing the real loader
// deployment path.
type LocalValidatorConfig struct {
	AgaveBinDir    string
	ProgramPath    string
	ProgramID      sdk.Pubkey
	Payer          Signer
	LedgerDir      string
	RPCPort        int
	GossipPort     int
	StartupTimeout time.Duration
}

// LocalValidator owns one isolated solana-test-validator child process.
type LocalValidator struct {
	Client    Client
	Payer     Signer
	ProgramID sdk.Pubkey
	LedgerDir string
	LogPath   string

	command *exec.Cmd
	done    chan error
	logFile *os.File
	mu      sync.Mutex
	closed  bool
}

// StartLocalValidator validates any configured ELF locally, starts the
// official Agave validator, optionally preloads the program, and waits for a
// healthy RPC endpoint.
func StartLocalValidator(ctx context.Context, config LocalValidatorConfig) (*LocalValidator, error) {
	if config.AgaveBinDir == "" {
		return nil, errors.New("svmtest: AgaveBinDir is required")
	}
	validatorBinary := filepath.Join(config.AgaveBinDir, "solana-test-validator")
	if info, err := os.Stat(validatorBinary); err != nil || info.IsDir() {
		return nil, fmt.Errorf("svmtest: missing validator binary %s", validatorBinary)
	}
	if config.ProgramPath != "" {
		programBytes, err := os.ReadFile(config.ProgramPath)
		if err != nil {
			return nil, fmt.Errorf("read program ELF: %w", err)
		}
		if _, err := sbpfelf.ParseStrictV3(programBytes); err != nil {
			return nil, fmt.Errorf("program ELF failed local strict validation: %w", err)
		}
	}
	var err error
	if len(config.Payer.Private) == 0 {
		config.Payer, err = NewSigner()
		if err != nil {
			return nil, err
		}
	}
	if config.ProgramID == (sdk.Pubkey{}) {
		programSigner, signerErr := NewSigner()
		if signerErr != nil {
			return nil, signerErr
		}
		config.ProgramID = programSigner.PublicKey
	}
	if config.LedgerDir == "" {
		config.LedgerDir, err = os.MkdirTemp("", "gosbf-agave-ledger-")
		if err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(config.LedgerDir, 0o700); err != nil {
		return nil, err
	}
	if config.RPCPort == 0 {
		config.RPCPort, err = availablePort()
		if err != nil {
			return nil, err
		}
	}
	if config.GossipPort == 0 {
		config.GossipPort, err = availableUDPPort()
		if err != nil {
			return nil, err
		}
	}
	faucetPort, err := availablePort()
	if err != nil {
		return nil, err
	}
	for faucetPort == config.RPCPort {
		faucetPort, err = availablePort()
		if err != nil {
			return nil, err
		}
	}
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = 60 * time.Second
	}
	logPath := filepath.Join(config.LedgerDir, "gosbf-validator.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	arguments := []string{
		"--ledger", config.LedgerDir,
		"--reset",
		"--quiet",
		"--rpc-port", strconv.Itoa(config.RPCPort),
		"--faucet-port", strconv.Itoa(faucetPort),
		"--gossip-port", strconv.Itoa(config.GossipPort),
		"--mint", config.Payer.PublicKey.String(),
	}
	if config.ProgramPath != "" {
		arguments = append(arguments, "--bpf-program", config.ProgramID.String(), config.ProgramPath)
	}
	command := exec.Command(validatorBinary, arguments...)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start solana-test-validator: %w", err)
	}
	harness := &LocalValidator{
		Client:    Client{URL: fmt.Sprintf("http://127.0.0.1:%d", config.RPCPort)},
		Payer:     config.Payer,
		ProgramID: config.ProgramID,
		LedgerDir: config.LedgerDir,
		LogPath:   logPath,
		command:   command,
		done:      make(chan error, 1),
		logFile:   logFile,
	}
	go func() { harness.done <- command.Wait() }()

	startupContext, cancel := context.WithTimeout(ctx, config.StartupTimeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		requestContext, requestCancel := context.WithTimeout(startupContext, time.Second)
		healthErr := harness.Client.Health(requestContext)
		requestCancel()
		if healthErr == nil {
			return harness, nil
		}
		select {
		case processErr := <-harness.done:
			_ = logFile.Close()
			return nil, fmt.Errorf("solana-test-validator exited before RPC became healthy: %v (log %s)", processErr, logPath)
		case <-startupContext.Done():
			_ = harness.Close()
			return nil, fmt.Errorf("validator startup timed out: %w (log %s)", startupContext.Err(), logPath)
		case <-ticker.C:
		}
	}
}

// Invoke submits one instruction to the preloaded program and requires a
// finalized successful transaction. Agave's genesis upgradeable programs can
// briefly remain in ProgramCacheEntryType::DelayVisibility after RPC health.
// Only the exact preflight-only UnsupportedProgramId response is retried. The
// client always returns the locally derived transaction signature, including
// on an RPC error, so that signature alone is not submission evidence. An
// acknowledged transaction or any ambiguous/non-preflight error is never
// resent.
func (v *LocalValidator) Invoke(ctx context.Context, data []byte) (string, error) {
	instruction := sdk.Instruction{ProgramID: v.ProgramID, Data: append([]byte(nil), data...)}
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		signature, err := v.Client.SendInstructions(ctx, v.Payer, nil, []sdk.Instruction{instruction})
		if err == nil || !isGenesisProgramVisibilityDelay(err) {
			return signature, err
		}
		lastErr = err
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", fmt.Errorf("preloaded program visibility remained unknown: %w", ctx.Err())
		case <-timer.C:
		}
	}
	return "", fmt.Errorf("preloaded program did not leave Agave's genesis visibility delay: %w", lastErr)
}

func isGenesisProgramVisibilityDelay(err error) bool {
	var rpcError *RPCError
	return errors.As(err, &rpcError) && rpcError.Code == -32002 && strings.Contains(rpcError.Message, "Unsupported program id")
}

// Close stops the child validator. It never deletes the ledger or log, so a
// failed integration run remains inspectable.
func (v *LocalValidator) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	v.mu.Unlock()
	if v.command == nil || v.command.Process == nil {
		return nil
	}
	_ = v.command.Process.Signal(os.Interrupt)
	var processErr error
	select {
	case processErr = <-v.done:
	case <-time.After(10 * time.Second):
		_ = v.command.Process.Kill()
		processErr = <-v.done
	}
	if v.logFile != nil {
		_ = v.logFile.Close()
	}
	if processErr != nil && !stringsContainsSignal(processErr.Error()) {
		return processErr
	}
	return nil
}

func availablePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func availableUDPPort() (int, error) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.LocalAddr().(*net.UDPAddr).Port, nil
}

func stringsContainsSignal(message string) bool {
	return message == "signal: interrupt" || message == "signal: killed"
}
