// Package erc1155 contains the deterministic client ABI for a custom,
// Solana-native multi-token program compiled by go-solana. It provides the
// same capabilities as Ethereum's ERC1155 (per-id balances, per-id
// supply/URI, approve-for-all) re-expressed as separate Solana accounts —
// this repository's established custom-program convention (see
// examples/gospl) — not a byte-for-byte port. See README.md for the two
// deliberate deviations from the literal ERC1155 API shape.
package erc1155

import (
	"errors"
	"fmt"

	"github.com/ersanyakit/solanago/sdk"
)

const (
	CollectionStateSize = 41
	TokenTypeStateSize  = 117
	BalanceStateSize    = 81
	ApprovalStateSize   = 98
	MaxURILength        = 64
)

// InstructionKind is the stable one-byte instruction discriminant.
type InstructionKind uint8

const (
	InstructionInitializeCollection InstructionKind = iota
	InstructionCreateTokenType
	InstructionInitializeBalance
	InstructionMintTo
	InstructionBurn
	InstructionTransfer
	InstructionInitializeApproval
	InstructionSetApproval
	InstructionTransferFrom
)

// Instruction is the fully decoded deterministic wire instruction.
type Instruction struct {
	Kind       InstructionKind
	Authority  sdk.Pubkey // InitializeCollection
	URI        string     // CreateTokenType
	Owner      sdk.Pubkey // InitializeBalance
	Amount     uint64     // MintTo, Burn, Transfer, TransferFrom
	Collection sdk.Pubkey // InitializeApproval
	Approved   bool       // InitializeApproval, SetApproval
}

// CollectionState is the decoded 41-byte custom collection state — the
// Solana-account equivalent of "the deployed ERC1155 contract instance".
type CollectionState struct {
	Initialized bool
	Authority   sdk.Pubkey
	NextID      uint64
}

// TokenTypeState is the decoded 117-byte state for one token id — the
// equivalent of one ERC1155 id's totalSupply()/uri().
type TokenTypeState struct {
	Initialized bool
	Collection  sdk.Pubkey
	ID          uint64
	Supply      uint64
	URI         string
}

// BalanceState is the decoded 81-byte state for one (id, owner) pair — the
// equivalent of one balanceOf(owner, id) entry.
type BalanceState struct {
	Initialized bool
	Collection  sdk.Pubkey
	ID          uint64
	Owner       sdk.Pubkey
	Amount      uint64
}

// ApprovalState is the decoded 98-byte state for one (owner, operator) pair
// — the equivalent of one isApprovedForAll(owner, operator) entry.
type ApprovalState struct {
	Initialized bool
	Collection  sdk.Pubkey
	Owner       sdk.Pubkey
	Operator    sdk.Pubkey
	Approved    bool
}

// ProgramError is returned directly as ProgramError::Custom(code) by the
// generated sBPF entrypoint, matching examples/gospl's error-code style.
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
	ErrCollectionMismatch
	ErrInsufficientBalance
	ErrArithmeticOverflow
	ErrSameAccount
	ErrNotApproved
)

func (e ProgramError) Error() string {
	return fmt.Sprintf("erc1155: custom program error %d", uint32(e))
}

var (
	ErrInvalidInstructionEncoding = errors.New("erc1155: invalid instruction encoding")
	ErrInvalidStateEncoding       = errors.New("erc1155: invalid state encoding")
	ErrURITooLong                 = errors.New("erc1155: uri exceeds MaxURILength")
)
