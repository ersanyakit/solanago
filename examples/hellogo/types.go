// Package hellogo contains the deterministic client ABI for the custom HelloGo
// token program compiled by go-solana. It is deliberately distinct from both
// the classic SPL Token program and Token-2022.
package hellogo

import (
	"errors"
	"fmt"

	"github.com/ersanyakit/go-solana/sdk"
)

const (
	MintStateSize         = 48
	TokenAccountStateSize = 120
)

// InstructionKind is the stable one-byte HelloGo instruction discriminant.
type InstructionKind uint8

const (
	InstructionInitializeMint InstructionKind = iota
	InstructionInitializeAccount
	InstructionTransfer
	InstructionMintTo
	InstructionBurn
	InstructionApprove
	InstructionRevoke
	InstructionSetAuthority
)

// AuthorityType selects the authority field changed by SetAuthority.
type AuthorityType uint8

const (
	AuthorityMintTokens AuthorityType = iota
	AuthorityAccountOwner
)

// OptionalPubkey distinguishes an intentionally disabled mint authority from
// every 32-byte public key, including the all-zero System Program address.
type OptionalPubkey struct {
	Set bool
	Key sdk.Pubkey
}

// Instruction is the fully decoded deterministic wire instruction.
type Instruction struct {
	Kind          InstructionKind
	Amount        uint64
	Decimals      uint8
	Authority     sdk.Pubkey
	AuthorityType AuthorityType
	NewAuthority  OptionalPubkey
}

// MintState is the decoded 48-byte custom mint state.
type MintState struct {
	Initialized   bool
	Decimals      uint8
	Supply        uint64
	MintAuthority OptionalPubkey
}

// TokenAccountState is the decoded 120-byte custom token-account state.
type TokenAccountState struct {
	Initialized     bool
	Mint            sdk.Pubkey
	Owner           sdk.Pubkey
	Amount          uint64
	Delegate        OptionalPubkey
	DelegatedAmount uint64
}

// ProgramError is returned directly as ProgramError::Custom(code) by the
// generated sBPF entrypoint. Values are kept equal to the native reference
// model in examples/spl20.
type ProgramError uint32

const (
	ErrInvalidInstruction ProgramError = iota + 1
	ErrMissingAccount
	ErrInvalidProgramOwner
	ErrAccountReadOnly
	ErrMissingSignature
	ErrInvalidAuthority
	ErrInvalidState
	ErrAlreadyInitialized
	ErrUninitialized
	ErrMintMismatch
	ErrInsufficientFunds
	ErrInsufficientAllowance
	ErrArithmeticOverflow
	ErrSameAccount
	ErrAuthorityDisabled
)

func (e ProgramError) Error() string {
	return fmt.Sprintf("hellogo: custom program error %d", uint32(e))
}

var (
	ErrInvalidInstructionEncoding = errors.New("hellogo: invalid instruction encoding")
	ErrInvalidStateEncoding       = errors.New("hellogo: invalid state encoding")
)
