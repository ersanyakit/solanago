// Package token2022 implements the canonical TokenzQd base interface plus the
// selected extension builders and TLV codecs covered by this repository. It is
// not a claim that every Token-2022 extension instruction has a typed builder.
// Its public state and builder types are distinct from sdk/token even where the
// on-chain base bytes intentionally match.
//
// Layout pin: solana-program/token-2022 commit
// 567074d43dc87522846728cc0b598bca27df764a, spl-token-2022-interface 3.1.1.
package token2022

import (
	"errors"

	"github.com/ersanyakit/go-solana/sdk"
)

const (
	MintSize           = 82
	AccountSize        = 165
	MultisigSize       = 355
	MaxSigners         = 11
	NativeMintDecimals = 9
	NativeMintSeed     = "native-mint"
)

var (
	ProgramID             = sdk.MustParsePubkey("TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb")
	NativeMintID          = sdk.MustParsePubkey("9pan9bMn5HatX4EJdBwg9VgCa7Uz5HL8N1m5D3NdXejP")
	RentSysvar            = sdk.MustParsePubkey("SysvarRent111111111111111111111111111111111")
	ErrInvalidState       = errors.New("token2022: invalid state")
	ErrInvalidExtension   = errors.New("token2022: invalid extension data")
	ErrExtensionBase      = errors.New("token2022: extension does not belong to this base account type")
	ErrDuplicateExtension = errors.New("token2022: duplicate extension")
	ErrVariableLength     = errors.New("token2022: extension has no fixed value length")
	ErrInvalidInstruction = errors.New("token2022: invalid instruction")
	ErrInvalidSigners     = errors.New("token2022: invalid multisignature signer set")
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
