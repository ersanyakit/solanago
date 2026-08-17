// Package erc20 contains a native-Go reference implementation of a
// Solana-native ERC-20 analogue, plus (in testdata/) a guest-compiled
// counterpart with the identical wire format — the same native-model-first
// pattern examples/spl20/examples/gospl and examples/payable use.
//
// Unlike examples/gospl (SPL-Token-shaped: one delegate per token account,
// no name/symbol/totalSupply), this package deliberately mirrors Solidity's
// ERC-20 interface field-for-field:
//
//	string public name;
//	string public symbol;
//	uint8 public decimals;
//	uint256 public totalSupply;
//	mapping(address => uint256) public balanceOf;
//	mapping(address => mapping(address => uint256)) public allowance;
//
//	function transfer(address to, uint256 amount) external returns (bool);
//	function approve(address spender, uint256 amount) external returns (bool);
//	function transferFrom(address from, address to, uint256 amount) external returns (bool);
//
// See README.md for the full field-by-field and function-by-function
// mapping, and for why `allowance` becomes its own account type here rather
// than SPL Token's single per-account delegate.
package erc20

import "fmt"

// Pubkey is the 32-byte public-key representation used by Solana accounts.
type Pubkey [32]byte

// Account is the minimum account view needed by this reference program.
type Account struct {
	Key        Pubkey
	Owner      Pubkey
	Data       []byte
	IsSigner   bool
	IsWritable bool
}

// ProgramError is a stable custom error code.
type ProgramError uint32

const (
	ErrInvalidInstruction ProgramError = iota + 1
	ErrMissingAccount
	ErrInvalidProgramOwner
	ErrAccountReadOnly
	ErrMissingSignature
	ErrInvalidState
	ErrAlreadyInitialized
	ErrUninitialized
	ErrSameAccount
	ErrMintMismatch
	ErrInsufficientFunds
	ErrInsufficientAllowance
	ErrArithmeticOverflow
	ErrInvalidAuthority
	ErrAllowanceMismatch
)

func (e ProgramError) Error() string {
	switch e {
	case ErrInvalidInstruction:
		return "erc20: invalid instruction"
	case ErrMissingAccount:
		return "erc20: missing account"
	case ErrInvalidProgramOwner:
		return "erc20: account is not owned by this program"
	case ErrAccountReadOnly:
		return "erc20: writable account required"
	case ErrMissingSignature:
		return "erc20: signature required"
	case ErrInvalidState:
		return "erc20: malformed account state"
	case ErrAlreadyInitialized:
		return "erc20: account is already initialized"
	case ErrUninitialized:
		return "erc20: account is not initialized"
	case ErrSameAccount:
		return "erc20: accounts must differ"
	case ErrMintMismatch:
		return "erc20: accounts reference different mints"
	case ErrInsufficientFunds:
		return "erc20: insufficient balance"
	case ErrInsufficientAllowance:
		return "erc20: insufficient allowance"
	case ErrArithmeticOverflow:
		return "erc20: arithmetic overflow"
	case ErrInvalidAuthority:
		return "erc20: invalid authority"
	case ErrAllowanceMismatch:
		return "erc20: allowance does not correspond to source's owner"
	default:
		return fmt.Sprintf("erc20: custom error %d", uint32(e))
	}
}

// OptionalPubkey distinguishes a deliberately removed authority from a
// valid 32-byte key (including the all-zero System Program address).
type OptionalPubkey struct {
	Set bool
	Key Pubkey
}

// MintState is the SVM analogue of the contract-level `name`/`symbol`/
// `decimals`/`totalSupply` storage slots — one per token, the equivalent of
// "the deployed ERC-20 contract instance."
type MintState struct {
	Initialized   bool
	Name          string // max 32 bytes
	Symbol        string // max 10 bytes
	Decimals      uint8
	TotalSupply   uint64
	MintAuthority OptionalPubkey
}

// BalanceState is the SVM analogue of one `balanceOf` mapping entry,
// materialized as its own account because Solana account data has no
// dynamic per-key storage.
type BalanceState struct {
	Initialized bool
	Mint        Pubkey
	Owner       Pubkey
	Amount      uint64
}

// AllowanceState is the SVM analogue of one `allowance[owner][spender]`
// mapping entry.
type AllowanceState struct {
	Initialized bool
	Mint        Pubkey
	Owner       Pubkey
	Spender     Pubkey
	Amount      uint64
}

const (
	MaxNameLen   = 32
	MaxSymbolLen = 10
)
