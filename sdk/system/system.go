// Package system builds and decodes instructions for Solana's native System
// Program. The layouts are pinned to the official generated System client at
// solana-program/system commit f61ddfe278125ea7624ba5df66baad5d01b9dccd.
package system

import (
	"errors"

	"github.com/ersanyakit/solanago/sdk"
)

var (
	ProgramID               = sdk.MustParsePubkey("11111111111111111111111111111111")
	RecentBlockhashesSysvar = sdk.MustParsePubkey("SysvarRecentB1ockHashes11111111111111111111")
	RentSysvar              = sdk.MustParsePubkey("SysvarRent111111111111111111111111111111111")
	ErrInvalidInstruction   = errors.New("system: invalid instruction data")
	ErrInvalidNonceState    = errors.New("system: invalid nonce state")
)

// InstructionKind is the little-endian u32 System Program discriminator.
type InstructionKind uint32

const (
	CreateAccountKind InstructionKind = iota
	AssignKind
	TransferKind
	CreateAccountWithSeedKind
	AdvanceNonceAccountKind
	WithdrawNonceAccountKind
	InitializeNonceAccountKind
	AuthorizeNonceAccountKind
	AllocateKind
	AllocateWithSeedKind
	AssignWithSeedKind
	TransferWithSeedKind
	UpgradeNonceAccountKind
	CreateAccountAllowPrefundKind
)

// DecodedInstruction is the lossless typed payload for any current System
// Program instruction. Fields not used by Kind are zero-valued.
type DecodedInstruction struct {
	Kind         InstructionKind
	Lamports     uint64
	Space        uint64
	Owner        sdk.Pubkey
	Base         sdk.Pubkey
	Seed         string
	NewAuthority sdk.Pubkey
}
