// Package payable contains a native-Go reference implementation of a
// Solana "payable" vault contract: the SVM analogue of a Solidity contract
// with `payable` functions that receive `msg.value`.
//
// Solana has no implicit per-call value: there is no `msg.value`, and a
// program cannot simply declare a function "payable" to receive lamports as
// a side effect of being called. Moving lamports is always an explicit
// System Program operation, and the rule that makes this contract's two
// halves look different is ownership: a program may only *debit* lamports
// from accounts it owns, but any account may *credit* lamports to any other
// account. Concretely:
//
//   - Deposit (the `payable` half): the depositor's wallet is owned by the
//     System Program, not by this contract, so this contract cannot debit it
//     directly. A real deployment must invoke the System Program's Transfer
//     instruction via CPI, with the depositor as a signing source account —
//     see examples/cpi/testdata/program.go for the exact low-level
//     sol_invoke_signed_c mechanics this Go source models but does not
//     itself execute.
//   - Withdraw (the pull-payment half): the vault account is owned by this
//     contract, so this contract may debit it directly and credit the
//     recipient directly, with no CPI required.
//
// This exact source is a readable native oracle, following the same
// native-model-first pattern as examples/spl20: idiomatic Go structs,
// pointers, and slices that the guest compiler does not accept. It is
// exercised only by the native Go test suite in this package, not compiled
// to sBPF.
package payable

import "fmt"

// Pubkey is the 32-byte public-key representation used by Solana accounts.
type Pubkey [32]byte

// Account is the minimum account view needed by this reference program.
// Owner is the program that owns Data; IsSigner and IsWritable are
// privileges granted to the current instruction; Lamports is the account's
// native SOL balance, exactly as ABIv1 hands it to a real program.
type Account struct {
	Key        Pubkey
	Owner      Pubkey
	Data       []byte
	Lamports   uint64
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
	ErrVaultMismatch
	ErrDepositorMismatch
	ErrInsufficientLamports
	ErrInsufficientFunds
	ErrArithmeticOverflow
	ErrInvalidAuthority
)

func (e ProgramError) Error() string {
	switch e {
	case ErrInvalidInstruction:
		return "payable: invalid instruction"
	case ErrMissingAccount:
		return "payable: missing account"
	case ErrInvalidProgramOwner:
		return "payable: account is not owned by this program"
	case ErrAccountReadOnly:
		return "payable: writable account required"
	case ErrMissingSignature:
		return "payable: signature required"
	case ErrInvalidState:
		return "payable: malformed account state"
	case ErrAlreadyInitialized:
		return "payable: account is already initialized"
	case ErrUninitialized:
		return "payable: account is not initialized"
	case ErrSameAccount:
		return "payable: accounts must differ"
	case ErrVaultMismatch:
		return "payable: deposit account does not belong to this vault"
	case ErrDepositorMismatch:
		return "payable: depositor does not match the deposit account"
	case ErrInsufficientLamports:
		return "payable: depositor does not hold enough lamports"
	case ErrInsufficientFunds:
		return "payable: withdrawal exceeds deposited balance"
	case ErrArithmeticOverflow:
		return "payable: arithmetic overflow"
	case ErrInvalidAuthority:
		return "payable: signer does not match the vault's emergency authority"
	default:
		return fmt.Sprintf("payable: custom error %d", uint32(e))
	}
}

// VaultState is the contract-level ledger for one vault: the running total
// of every DepositState balance currently held, mirroring what a Solidity
// contract would keep in a single storage slot alongside its address
// balance. Authority is the sole account allowed to call EmergencyWithdraw,
// the SVM equivalent of an `onlyOwner` rescue function.
type VaultState struct {
	Initialized    bool
	TotalDeposited uint64
	Authority      Pubkey
}

// DepositState is one depositor's entry, the SVM equivalent of a single
// slot in Solidity's `mapping(address => uint256) public balances`. It is a
// separate account (like an SPL token account) rather than a slot inside
// VaultState because Solana account data has no dynamic per-key storage.
type DepositState struct {
	Initialized bool
	Vault       Pubkey
	Depositor   Pubkey
	Balance     uint64
}
