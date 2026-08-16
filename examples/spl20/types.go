// Package spl20 contains a native-Go reference implementation of an
// ERC-20-like custom Solana token program. It documents the source-level API
// used as an independent differential oracle for examples/gospl.
//
// It is intentionally not wire-compatible with either the canonical SPL Token
// Program or Token-2022. This idiomatic struct/slice implementation stays
// native; the explicit-memory Go counterpart in examples/gospl is compiled and
// deployed.
package spl20

import "fmt"

// Pubkey is the 32-byte public-key representation used by Solana accounts.
type Pubkey [32]byte

// Account is the minimum account view needed by this reference program.
// Owner is the program that owns Data; IsSigner and IsWritable are privileges
// granted to the current instruction.
type Account struct {
	Key        Pubkey
	Owner      Pubkey
	Data       []byte
	IsSigner   bool
	IsWritable bool
}

// ProgramError is a stable custom error code. The compiled GOSPL entrypoint
// returns the same values as ProgramError::Custom-compatible status codes.
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
	switch e {
	case ErrInvalidInstruction:
		return "spl20: invalid instruction"
	case ErrMissingAccount:
		return "spl20: missing account"
	case ErrInvalidProgramOwner:
		return "spl20: account is not owned by this program"
	case ErrAccountReadOnly:
		return "spl20: writable account required"
	case ErrMissingSignature:
		return "spl20: authority signature required"
	case ErrInvalidAuthority:
		return "spl20: invalid authority"
	case ErrInvalidState:
		return "spl20: malformed account state"
	case ErrAlreadyInitialized:
		return "spl20: account is already initialized"
	case ErrUninitialized:
		return "spl20: account is not initialized"
	case ErrMintMismatch:
		return "spl20: token accounts use different mints"
	case ErrInsufficientFunds:
		return "spl20: insufficient funds"
	case ErrInsufficientAllowance:
		return "spl20: insufficient delegated allowance"
	case ErrArithmeticOverflow:
		return "spl20: arithmetic overflow"
	case ErrSameAccount:
		return "spl20: source and destination must differ"
	case ErrAuthorityDisabled:
		return "spl20: mint authority has been permanently disabled"
	default:
		return fmt.Sprintf("spl20: custom error %d", uint32(e))
	}
}

// OptionalPubkey distinguishes a deliberately removed authority from a valid
// 32-byte key (including the all-zero System Program address).
type OptionalPubkey struct {
	Set bool
	Key Pubkey
}

type MintState struct {
	Initialized   bool
	Decimals      uint8
	Supply        uint64
	MintAuthority OptionalPubkey
}

type TokenAccountState struct {
	Initialized     bool
	Mint            Pubkey
	Owner           Pubkey
	Amount          uint64
	Delegate        OptionalPubkey
	DelegatedAmount uint64
}

type AuthorityType uint8

const (
	AuthorityMintTokens AuthorityType = iota
	AuthorityAccountOwner
)
