// Package deploy implements a Go-only upgradeable-loader deployment workflow.
// It requires no Rust compiler, Cargo project, or Solana CLI subprocess.
package deploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	sbpfelf "github.com/ersanyakit/go-solana/elf"
	"github.com/ersanyakit/go-solana/sdk"
	"github.com/ersanyakit/go-solana/sdk/loader"
	"github.com/ersanyakit/go-solana/svmtest"
)

const DefaultChunkSize = 800

var (
	ErrProgramExists             = errors.New("deploy: program account already exists; automatic upgrade is disabled")
	ErrInsufficientBalance       = errors.New("deploy: payer balance is below the rent requirement")
	ErrInvalidKeypair            = errors.New("deploy: invalid Solana keypair file")
	ErrProgramNotFound           = errors.New("deploy: program account does not exist or is not executable under the upgradeable loader")
	ErrUpgradeAuthorityMismatch  = errors.New("deploy: configured upgrade authority does not match the program's on-chain authority")
	ErrProgramTooLargeForUpgrade = errors.New("deploy: new ELF exceeds the program's allocated max data length; it cannot grow on upgrade")
)

// Client is the exact RPC boundary needed by a deploy. Keeping the boundary
// explicit makes ambiguous-submit and finalized-state handling unit-testable.
type Client interface {
	GetAccountInfo(context.Context, sdk.Pubkey) (*svmtest.AccountInfo, error)
	GenesisHash(context.Context) (string, error)
	MinimumBalanceForRentExemption(context.Context, uint64) (uint64, error)
	Balance(context.Context, sdk.Pubkey) (uint64, error)
	SendInstructions(context.Context, svmtest.Signer, []svmtest.Signer, []sdk.Instruction) (string, error)
	SendInstructionsConfirmed(context.Context, svmtest.Signer, []svmtest.Signer, []sdk.Instruction) (string, error)
	WaitForFinalized(context.Context, string) error
}

// Config contains every authority and sizing choice that can affect a deploy.
type Config struct {
	Client           Client
	FeePayer         svmtest.Signer
	Program          svmtest.Signer
	UpgradeAuthority svmtest.Signer
	MaxDataLen       int
	ChunkSize        int
	Progress         func(Stage)
	// SpillAddress receives a live program's excess buffer lamports on
	// Upgrade. It is ignored by Program/ResumeProgram. Defaults to FeePayer
	// if zero.
	SpillAddress sdk.Pubkey
}

// Stage is emitted only after a transaction is finalized.
type Stage struct {
	Kind      string
	Signature string
	Offset    int
	Length    int
}

// Result is also returned on partial failure. Buffer and submitted/finalized
// signatures let an operator inspect/recover without blindly resending an
// unknown step. This is an in-memory recovery record: a process crash can still
// lose it unless the caller persists progress outside this package.
type Result struct {
	ProgramID          sdk.Pubkey
	ProgramDataAddress sdk.Pubkey
	BufferAddress      sdk.Pubkey
	GenesisHash        string
	// SubmittedSignatures includes every locally identified transaction handed
	// to RPC, including ambiguous outcomes. Signatures contains only finalized
	// or finalized-state-proven evidence.
	SubmittedSignatures []string
	Signatures          []string
	Finalized           bool
}

// LoadKeypair reads the canonical JSON array of 64 Ed25519 private-key bytes.
func LoadKeypair(path string) (svmtest.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return svmtest.Signer{}, err
	}
	var encoded []byte
	if err := json.Unmarshal(data, &encoded); err != nil || len(encoded) != ed25519.PrivateKeySize {
		return svmtest.Signer{}, ErrInvalidKeypair
	}
	private := append(ed25519.PrivateKey(nil), encoded...)
	signer, err := svmtest.SignerFromPrivateKey(private)
	if err != nil {
		return svmtest.Signer{}, ErrInvalidKeypair
	}
	return signer, nil
}

// SaveKeypair writes a signer in the Solana CLI-compatible JSON format. It
// uses O_EXCL unless overwrite is explicitly true.
func SaveKeypair(path string, signer svmtest.Signer, overwrite bool) error {
	if err := validateSigner(signer); err != nil {
		return ErrInvalidKeypair
	}
	// encoding/json gives []byte a special base64-string representation. Use
	// integers explicitly so newly generated files match Solana's canonical
	// 64-number keypair JSON while LoadKeypair remains backward-compatible with
	// older base64-string files created by this package.
	encoded := make([]int, len(signer.Private))
	for index, value := range signer.Private {
		encoded[index] = int(value)
	}
	data, err := json.Marshal(encoded)
	if err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// Program deploys one new strict sBPFv3 ELF through the upgradeable loader.
// Existing program accounts fail closed; upgrades require a separate explicit
// workflow so a typo cannot overwrite live code.
func Program(ctx context.Context, config Config, elfBytes []byte) (*Result, error) {
	config, result, err := prepare(ctx, config, elfBytes)
	if err != nil {
		return result, err
	}
	buffer, err := svmtest.NewSigner()
	if err != nil {
		return result, err
	}
	result.BufferAddress = buffer.PublicKey
	programDataRent, err := config.Client.MinimumBalanceForRentExemption(ctx, uint64(loader.ProgramDataMetadataSize+config.MaxDataLen))
	if err != nil {
		return result, err
	}
	programRent, err := config.Client.MinimumBalanceForRentExemption(ctx, loader.ProgramMetadataSize)
	if err != nil {
		return result, err
	}
	balance, err := config.Client.Balance(ctx, config.FeePayer.PublicKey)
	if err != nil {
		return result, err
	}
	if balance < programDataRent+programRent {
		return result, fmt.Errorf("%w: have %d, need at least %d lamports before fees", ErrInsufficientBalance, balance, programDataRent+programRent)
	}

	createBuffer, err := loader.CreateBuffer(config.FeePayer.PublicKey, buffer.PublicKey, config.UpgradeAuthority.PublicKey, programDataRent, len(elfBytes))
	if err != nil {
		return result, err
	}
	signature, err := config.Client.SendInstructions(ctx, config.FeePayer, []svmtest.Signer{buffer}, createBuffer)
	if signature != "" {
		result.SubmittedSignatures = append(result.SubmittedSignatures, signature)
	}
	if err != nil {
		return result, fmt.Errorf("create buffer %s: %w", buffer.PublicKey, err)
	}
	if signature == "" {
		return result, errors.New("create buffer returned no transaction signature")
	}
	result.Signatures = append(result.Signatures, signature)
	emit(config.Progress, Stage{Kind: "create-buffer", Signature: signature})
	return writeAndDeploy(ctx, config, result, buffer.PublicKey, programRent, elfBytes)
}

// ResumeProgram completes a new-program deployment using an existing finalized
// upgradeable-loader buffer. The buffer is not trusted merely because its
// address was supplied: its owner, executable bit, state tag, authority and
// exact payload capacity are checked before any transaction is submitted.
//
// Resume always rewrites the complete ELF starting at offset zero. It never
// guesses which writes from a prior process reached the cluster. After the last
// write finalizes, the complete buffer payload is read back and compared before
// the irreversible final deploy instruction is submitted.
func ResumeProgram(ctx context.Context, config Config, buffer sdk.Pubkey, elfBytes []byte) (*Result, error) {
	config, result, err := prepare(ctx, config, elfBytes)
	if err != nil {
		return result, err
	}
	result.BufferAddress = buffer
	bufferAccount, err := config.Client.GetAccountInfo(ctx, buffer)
	if err != nil {
		return result, fmt.Errorf("inspect loader buffer %s: %w", buffer, err)
	}
	if _, err := validateBuffer(bufferAccount, config.UpgradeAuthority.PublicKey, len(elfBytes)); err != nil {
		return result, fmt.Errorf("resume buffer %s: %w", buffer, err)
	}

	programDataRent, err := config.Client.MinimumBalanceForRentExemption(ctx, uint64(loader.ProgramDataMetadataSize+config.MaxDataLen))
	if err != nil {
		return result, err
	}
	programRent, err := config.Client.MinimumBalanceForRentExemption(ctx, loader.ProgramMetadataSize)
	if err != nil {
		return result, err
	}
	requiredBalance := programRent
	if bufferAccount.Lamports < programDataRent {
		requiredBalance += programDataRent - bufferAccount.Lamports
	}
	balance, err := config.Client.Balance(ctx, config.FeePayer.PublicKey)
	if err != nil {
		return result, err
	}
	if balance < requiredBalance {
		return result, fmt.Errorf("%w: have %d, need at least %d lamports before fees", ErrInsufficientBalance, balance, requiredBalance)
	}
	return writeAndDeploy(ctx, config, result, buffer, programRent, elfBytes)
}

func prepare(ctx context.Context, config Config, elfBytes []byte) (Config, *Result, error) {
	result := &Result{ProgramID: config.Program.PublicKey}
	if _, err := sbpfelf.ParseStrictV3(elfBytes); err != nil {
		return config, result, fmt.Errorf("deploy artifact: %w", err)
	}
	if config.Client == nil {
		return config, result, errors.New("deploy: RPC client is required")
	}
	if err := validateSigner(config.FeePayer); err != nil {
		return config, result, err
	}
	if err := validateSigner(config.Program); err != nil {
		return config, result, err
	}
	if len(config.UpgradeAuthority.Private) == 0 {
		config.UpgradeAuthority = config.FeePayer
	}
	if err := validateSigner(config.UpgradeAuthority); err != nil {
		return config, result, err
	}
	if config.MaxDataLen == 0 {
		config.MaxDataLen = len(elfBytes)
	}
	if config.MaxDataLen < len(elfBytes) {
		return config, result, fmt.Errorf("deploy: max data length %d is smaller than ELF length %d", config.MaxDataLen, len(elfBytes))
	}
	if config.ChunkSize == 0 {
		config.ChunkSize = DefaultChunkSize
	}
	if config.ChunkSize < 1 || config.ChunkSize > DefaultChunkSize {
		return config, result, fmt.Errorf("deploy: chunk size must be in [1,%d]", DefaultChunkSize)
	}
	existing, err := config.Client.GetAccountInfo(ctx, config.Program.PublicKey)
	if err != nil {
		return config, result, err
	}
	if existing != nil {
		return config, result, ErrProgramExists
	}
	result.GenesisHash, err = config.Client.GenesisHash(ctx)
	if err != nil {
		return config, result, err
	}
	result.ProgramDataAddress, err = loader.ProgramDataAddress(config.Program.PublicKey)
	return config, result, err
}

func writeAndDeploy(ctx context.Context, config Config, result *Result, buffer sdk.Pubkey, programRent uint64, elfBytes []byte) (*Result, error) {
	if err := writeBufferChunks(ctx, config, result, buffer, elfBytes); err != nil {
		return result, err
	}

	finalInstructions, err := loader.DeployWithMaxDataLen(
		config.FeePayer.PublicKey,
		config.Program.PublicKey,
		buffer,
		config.UpgradeAuthority.PublicKey,
		programRent,
		config.MaxDataLen,
	)
	if err != nil {
		return result, err
	}
	signature, err := config.Client.SendInstructions(ctx, config.FeePayer, []svmtest.Signer{config.Program, config.UpgradeAuthority}, finalInstructions)
	if signature != "" {
		result.SubmittedSignatures = append(result.SubmittedSignatures, signature)
	}
	if err != nil {
		return result, fmt.Errorf("final deploy outcome must be inspected before retry: %w", err)
	}
	if signature == "" {
		return result, errors.New("final deploy returned no transaction signature")
	}
	result.Signatures = append(result.Signatures, signature)
	emit(config.Progress, Stage{Kind: "deploy", Signature: signature})
	if err := verifyFinalDeployment(ctx, config, result, elfBytes); err != nil {
		return result, err
	}
	result.Finalized = true
	return result, nil
}

// writeBufferChunks writes elfBytes into buffer in ChunkSize pieces, waits for
// the last write to finalize, then re-reads and byte-verifies the finalized
// buffer content before returning. Shared by the new-program deploy path and
// Upgrade.
func writeBufferChunks(ctx context.Context, config Config, result *Result, buffer sdk.Pubkey, elfBytes []byte) error {
	type pendingWrite struct {
		signature string
		offset    int
		length    int
	}
	pendingWrites := make([]pendingWrite, 0, (len(elfBytes)+config.ChunkSize-1)/config.ChunkSize)
	for offset := 0; offset < len(elfBytes); offset += config.ChunkSize {
		end := min(offset+config.ChunkSize, len(elfBytes))
		instruction := loader.Write(buffer, config.UpgradeAuthority.PublicKey, uint32(offset), elfBytes[offset:end])
		signature, err := config.Client.SendInstructionsConfirmed(ctx, config.FeePayer, []svmtest.Signer{config.UpgradeAuthority}, []sdk.Instruction{instruction})
		if signature != "" {
			result.SubmittedSignatures = append(result.SubmittedSignatures, signature)
		}
		if err != nil {
			return fmt.Errorf("write buffer %s at offset %d; do not resend without inspecting finalized state: %w", buffer, offset, err)
		}
		if signature == "" {
			return fmt.Errorf("write buffer %s at offset %d returned no transaction signature", buffer, offset)
		}
		pendingWrites = append(pendingWrites, pendingWrite{signature: signature, offset: offset, length: end - offset})
	}
	if len(pendingWrites) == 0 {
		return nil
	}
	if err := config.Client.WaitForFinalized(ctx, pendingWrites[len(pendingWrites)-1].signature); err != nil {
		return fmt.Errorf("buffer write finalization checkpoint failed; do not resend without inspecting finalized state: %w", err)
	}
	bufferAccount, err := config.Client.GetAccountInfo(ctx, buffer)
	if err != nil {
		return err
	}
	bufferData, err := validateBuffer(bufferAccount, config.UpgradeAuthority.PublicKey, len(elfBytes))
	if err != nil {
		return fmt.Errorf("verify finalized loader buffer %s: %w", buffer, err)
	}
	if !bytes.Equal(bufferData[loader.BufferMetadataSize:], elfBytes) {
		return errors.New("finalized loader buffer bytes do not match the strict ELF; refusing deploy")
	}
	for _, write := range pendingWrites {
		result.Signatures = append(result.Signatures, write.signature)
		emit(config.Progress, Stage{Kind: "write", Signature: write.signature, Offset: write.offset, Length: write.length})
	}
	return nil
}

// Upgrade replaces a live upgradeable-loader program's code with a new
// strict sBPFv3 ELF. Unlike Program, this requires the program to already
// exist, be executable, and have an on-chain upgrade authority matching
// config.UpgradeAuthority. The new ELF must fit within the max data length
// the program was allocated at its first deploy; that ceiling cannot grow,
// and this is checked client-side before any transaction is sent.
func Upgrade(ctx context.Context, config Config, program sdk.Pubkey, elfBytes []byte) (*Result, error) {
	config, result, err := prepareUpgrade(ctx, config, program, elfBytes)
	if err != nil {
		return result, err
	}
	buffer, err := svmtest.NewSigner()
	if err != nil {
		return result, err
	}
	result.BufferAddress = buffer.PublicKey
	bufferRent, err := config.Client.MinimumBalanceForRentExemption(ctx, uint64(loader.BufferMetadataSize+len(elfBytes)))
	if err != nil {
		return result, err
	}
	balance, err := config.Client.Balance(ctx, config.FeePayer.PublicKey)
	if err != nil {
		return result, err
	}
	if balance < bufferRent {
		return result, fmt.Errorf("%w: have %d, need at least %d lamports before fees", ErrInsufficientBalance, balance, bufferRent)
	}

	createBuffer, err := loader.CreateBuffer(config.FeePayer.PublicKey, buffer.PublicKey, config.UpgradeAuthority.PublicKey, bufferRent, len(elfBytes))
	if err != nil {
		return result, err
	}
	signature, err := config.Client.SendInstructions(ctx, config.FeePayer, []svmtest.Signer{buffer}, createBuffer)
	if signature != "" {
		result.SubmittedSignatures = append(result.SubmittedSignatures, signature)
	}
	if err != nil {
		return result, fmt.Errorf("create buffer %s: %w", buffer.PublicKey, err)
	}
	if signature == "" {
		return result, errors.New("create buffer returned no transaction signature")
	}
	result.Signatures = append(result.Signatures, signature)
	emit(config.Progress, Stage{Kind: "create-buffer", Signature: signature})

	if err := writeBufferChunks(ctx, config, result, buffer.PublicKey, elfBytes); err != nil {
		return result, err
	}

	upgrade, err := loader.Upgrade(program, buffer.PublicKey, config.UpgradeAuthority.PublicKey, config.SpillAddress)
	if err != nil {
		return result, err
	}
	signature, err = config.Client.SendInstructions(ctx, config.FeePayer, []svmtest.Signer{config.UpgradeAuthority}, []sdk.Instruction{upgrade})
	if signature != "" {
		result.SubmittedSignatures = append(result.SubmittedSignatures, signature)
	}
	if err != nil {
		return result, fmt.Errorf("upgrade outcome must be inspected before retry: %w", err)
	}
	if signature == "" {
		return result, errors.New("upgrade returned no transaction signature")
	}
	result.Signatures = append(result.Signatures, signature)
	emit(config.Progress, Stage{Kind: "upgrade", Signature: signature})

	if err := verifyUpgradeDeployment(ctx, config, result, elfBytes); err != nil {
		return result, err
	}
	result.Finalized = true
	return result, nil
}

func prepareUpgrade(ctx context.Context, config Config, program sdk.Pubkey, elfBytes []byte) (Config, *Result, error) {
	result := &Result{ProgramID: program}
	if _, err := sbpfelf.ParseStrictV3(elfBytes); err != nil {
		return config, result, fmt.Errorf("deploy artifact: %w", err)
	}
	if config.Client == nil {
		return config, result, errors.New("deploy: RPC client is required")
	}
	if err := validateSigner(config.FeePayer); err != nil {
		return config, result, err
	}
	if len(config.UpgradeAuthority.Private) == 0 {
		config.UpgradeAuthority = config.FeePayer
	}
	if err := validateSigner(config.UpgradeAuthority); err != nil {
		return config, result, err
	}
	if config.ChunkSize == 0 {
		config.ChunkSize = DefaultChunkSize
	}
	if config.ChunkSize < 1 || config.ChunkSize > DefaultChunkSize {
		return config, result, fmt.Errorf("deploy: chunk size must be in [1,%d]", DefaultChunkSize)
	}
	if config.SpillAddress == (sdk.Pubkey{}) {
		config.SpillAddress = config.FeePayer.PublicKey
	}

	programAccount, err := config.Client.GetAccountInfo(ctx, program)
	if err != nil {
		return config, result, err
	}
	if programAccount == nil || !programAccount.Executable || programAccount.Owner != loader.ProgramID.String() {
		return config, result, ErrProgramNotFound
	}
	programDataAddress, err := loader.ProgramDataAddress(program)
	if err != nil {
		return config, result, err
	}
	result.ProgramDataAddress = programDataAddress
	programDataAccount, err := config.Client.GetAccountInfo(ctx, programDataAddress)
	if err != nil {
		return config, result, err
	}
	if programDataAccount == nil || programDataAccount.Executable || programDataAccount.Owner != loader.ProgramID.String() {
		return config, result, ErrProgramNotFound
	}
	programDataBytes, err := programDataAccount.DataBytes()
	if err != nil {
		return config, result, fmt.Errorf("decode ProgramData account: %w", err)
	}
	state, err := loader.DecodeProgramDataAccount(programDataBytes)
	if err != nil {
		return config, result, err
	}
	if !state.HasAuthority || state.Authority != config.UpgradeAuthority.PublicKey {
		return config, result, ErrUpgradeAuthorityMismatch
	}
	if len(elfBytes) > len(state.ProgramBytes) {
		return config, result, fmt.Errorf("%w: elf is %d bytes, allocated capacity is %d bytes", ErrProgramTooLargeForUpgrade, len(elfBytes), len(state.ProgramBytes))
	}
	result.GenesisHash, err = config.Client.GenesisHash(ctx)
	return config, result, err
}

func verifyUpgradeDeployment(ctx context.Context, config Config, result *Result, elfBytes []byte) error {
	account, err := config.Client.GetAccountInfo(ctx, result.ProgramDataAddress)
	if err != nil {
		return err
	}
	if account == nil || account.Executable || account.Owner != loader.ProgramID.String() {
		return errors.New("upgrade transaction finalized but ProgramData account has invalid loader metadata")
	}
	data, err := account.DataBytes()
	if err != nil {
		return fmt.Errorf("decode finalized ProgramData account: %w", err)
	}
	state, err := loader.DecodeProgramDataAccount(data)
	if err != nil {
		return fmt.Errorf("finalized ProgramData account: %w", err)
	}
	if !state.HasAuthority || state.Authority != config.UpgradeAuthority.PublicKey {
		return errors.New("finalized ProgramData authority changed unexpectedly during upgrade")
	}
	if len(state.ProgramBytes) < len(elfBytes) || !bytes.Equal(state.ProgramBytes[:len(elfBytes)], elfBytes) || !allZero(state.ProgramBytes[len(elfBytes):]) {
		return errors.New("finalized ProgramData bytes do not match the new ELF and zero padding")
	}
	return nil
}

func validateBuffer(account *svmtest.AccountInfo, authority sdk.Pubkey, elfLength int) ([]byte, error) {
	if account == nil {
		return nil, errors.New("loader buffer account is missing")
	}
	if account.Owner != loader.ProgramID.String() {
		return nil, errors.New("loader buffer has the wrong owner")
	}
	if account.Executable {
		return nil, errors.New("loader buffer must not be executable")
	}
	data, err := account.DataBytes()
	if err != nil {
		return nil, fmt.Errorf("decode loader buffer: %w", err)
	}
	if len(data) < loader.BufferMetadataSize {
		return nil, fmt.Errorf("loader buffer data is truncated: have %d bytes, need at least %d", len(data), loader.BufferMetadataSize)
	}
	if binary.LittleEndian.Uint32(data[:4]) != 1 {
		return nil, errors.New("loader account is not in Buffer state")
	}
	if data[4] != 1 {
		return nil, errors.New("loader buffer has no upgrade authority")
	}
	if !bytes.Equal(data[5:loader.BufferMetadataSize], authority[:]) {
		return nil, errors.New("loader buffer upgrade authority does not match the configured signer")
	}
	capacity := len(data) - loader.BufferMetadataSize
	if capacity != elfLength {
		return nil, fmt.Errorf("loader buffer payload capacity is %d bytes, want exactly %d", capacity, elfLength)
	}
	return data, nil
}

func verifyFinalDeployment(ctx context.Context, config Config, result *Result, elfBytes []byte) error {
	account, err := config.Client.GetAccountInfo(ctx, config.Program.PublicKey)
	if err != nil {
		return err
	}
	if account == nil || !account.Executable || account.Owner != loader.ProgramID.String() {
		return errors.New("deploy transaction finalized but program account is not executable under the upgradeable loader")
	}
	programAccountData, err := account.DataBytes()
	if err != nil {
		return fmt.Errorf("decode finalized program account: %w", err)
	}
	if len(programAccountData) != loader.ProgramMetadataSize ||
		binary.LittleEndian.Uint32(programAccountData[:4]) != 2 ||
		!bytes.Equal(programAccountData[4:], result.ProgramDataAddress[:]) {
		return errors.New("finalized program account does not reference the expected ProgramData address")
	}
	programDataAccount, err := config.Client.GetAccountInfo(ctx, result.ProgramDataAddress)
	if err != nil {
		return err
	}
	if programDataAccount == nil || programDataAccount.Executable || programDataAccount.Owner != loader.ProgramID.String() {
		return errors.New("finalized ProgramData account is missing or has invalid loader metadata")
	}
	programData, err := programDataAccount.DataBytes()
	if err != nil {
		return fmt.Errorf("decode finalized ProgramData account: %w", err)
	}
	wantProgramDataLength := loader.ProgramDataMetadataSize + config.MaxDataLen
	if len(programData) != wantProgramDataLength ||
		binary.LittleEndian.Uint32(programData[:4]) != 3 ||
		programData[12] != 1 ||
		!bytes.Equal(programData[13:loader.ProgramDataMetadataSize], config.UpgradeAuthority.PublicKey[:]) {
		return errors.New("finalized ProgramData state, authority, or length is not canonical")
	}
	programBytes := programData[loader.ProgramDataMetadataSize:]
	if !bytes.Equal(programBytes[:len(elfBytes)], elfBytes) || !allZero(programBytes[len(elfBytes):]) {
		return errors.New("finalized ProgramData bytes do not match the strict ELF and zero padding")
	}
	return nil
}

func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func validateSigner(signer svmtest.Signer) error {
	if err := svmtest.ValidateSigner(signer); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidKeypair, err)
	}
	return nil
}

func emit(callback func(Stage), stage Stage) {
	if callback != nil {
		callback(stage)
	}
}
