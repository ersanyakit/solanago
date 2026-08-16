// Package token implements the classic SPL Token interface for the canonical
// Tokenkeg program. It must not be used for Token-2022 accounts or extensions;
// those live in the sibling sdk/token2022 package.
//
// Layout pin: solana-program/token commit
// f5285693a93135a144e24859c84d26ac20037a3a, spl-token-interface 3.0.0.
package token

import (
	"errors"

	"github.com/ersany/go-solana/sdk"
)

const (
	MintSize     = 82
	AccountSize  = 165
	MultisigSize = 355
	MinSigners   = 1
	MaxSigners   = 11
	// NativeMintDecimals is the number of lamport decimal places in native SOL.
	NativeMintDecimals = 9
)

var (
	ProgramID             = sdk.MustParsePubkey("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA")
	NativeMintID          = sdk.MustParsePubkey("So11111111111111111111111111111111111111112")
	RentSysvar            = sdk.MustParsePubkey("SysvarRent111111111111111111111111111111111")
	ErrInvalidState       = errors.New("token: invalid classic SPL Token state")
	ErrInvalidInstruction = errors.New("token: invalid classic SPL Token instruction")
	ErrInvalidSigners     = errors.New("token: invalid multisignature signer set")
	ErrIncorrectProgramID = errors.New("token: batch instruction uses a different program id")
)

type OptionalPubkey struct {
	Set   bool
	Value sdk.Pubkey
}

func SomePubkey(value sdk.Pubkey) OptionalPubkey { return OptionalPubkey{Set: true, Value: value} }

type OptionalU64 struct {
	Set   bool
	Value uint64
}

func SomeU64(value uint64) OptionalU64 { return OptionalU64{Set: true, Value: value} }
