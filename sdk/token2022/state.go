package token2022

import (
	"github.com/ersany/go-solana/sdk"
	classic "github.com/ersany/go-solana/sdk/token"
)

type Mint struct {
	MintAuthority   OptionalPubkey
	Supply          uint64
	Decimals        uint8
	Initialized     bool
	FreezeAuthority OptionalPubkey
}

type AccountState uint8

const (
	AccountUninitialized AccountState = iota
	AccountInitialized
	AccountFrozen
)

type Account struct {
	Mint            sdk.Pubkey
	Owner           sdk.Pubkey
	Amount          uint64
	Delegate        OptionalPubkey
	State           AccountState
	IsNative        OptionalU64
	DelegatedAmount uint64
	CloseAuthority  OptionalPubkey
}

type Multisig struct {
	M           uint8
	N           uint8
	Initialized bool
	Signers     [MaxSigners]sdk.Pubkey
}

func DecodeMint(data []byte) (Mint, error) {
	base, err := classic.DecodeMint(data)
	if err != nil {
		return Mint{}, ErrInvalidState
	}
	return mintFromClassic(base), nil
}

func EncodeMint(mint Mint) ([]byte, error) {
	data, err := classic.EncodeMint(classicMint(mint))
	if err != nil {
		return nil, ErrInvalidState
	}
	return data, nil
}

func DecodeAccount(data []byte) (Account, error) {
	base, err := classic.DecodeAccount(data)
	if err != nil {
		return Account{}, ErrInvalidState
	}
	return accountFromClassic(base), nil
}

func EncodeAccount(account Account) ([]byte, error) {
	data, err := classic.EncodeAccount(classicAccount(account))
	if err != nil {
		return nil, ErrInvalidState
	}
	return data, nil
}

func DecodeMultisig(data []byte) (Multisig, error) {
	base, err := classic.DecodeMultisig(data)
	if err != nil {
		return Multisig{}, ErrInvalidState
	}
	return Multisig{M: base.M, N: base.N, Initialized: base.Initialized, Signers: base.Signers}, nil
}

func EncodeMultisig(multisig Multisig) ([]byte, error) {
	data, err := classic.EncodeMultisig(classic.Multisig{M: multisig.M, N: multisig.N, Initialized: multisig.Initialized, Signers: multisig.Signers})
	if err != nil {
		return nil, ErrInvalidState
	}
	return data, nil
}

func mintFromClassic(value classic.Mint) Mint {
	return Mint{
		MintAuthority:   optionFromClassic(value.MintAuthority),
		Supply:          value.Supply,
		Decimals:        value.Decimals,
		Initialized:     value.Initialized,
		FreezeAuthority: optionFromClassic(value.FreezeAuthority),
	}
}

func classicMint(value Mint) classic.Mint {
	return classic.Mint{
		MintAuthority:   classicOption(value.MintAuthority),
		Supply:          value.Supply,
		Decimals:        value.Decimals,
		Initialized:     value.Initialized,
		FreezeAuthority: classicOption(value.FreezeAuthority),
	}
}

func accountFromClassic(value classic.Account) Account {
	return Account{
		Mint:            value.Mint,
		Owner:           value.Owner,
		Amount:          value.Amount,
		Delegate:        optionFromClassic(value.Delegate),
		State:           AccountState(value.State),
		IsNative:        OptionalU64{Set: value.IsNative.Set, Value: value.IsNative.Value},
		DelegatedAmount: value.DelegatedAmount,
		CloseAuthority:  optionFromClassic(value.CloseAuthority),
	}
}

func classicAccount(value Account) classic.Account {
	return classic.Account{
		Mint:            value.Mint,
		Owner:           value.Owner,
		Amount:          value.Amount,
		Delegate:        classicOption(value.Delegate),
		State:           classic.AccountState(value.State),
		IsNative:        classic.OptionalU64{Set: value.IsNative.Set, Value: value.IsNative.Value},
		DelegatedAmount: value.DelegatedAmount,
		CloseAuthority:  classicOption(value.CloseAuthority),
	}
}

func optionFromClassic(value classic.OptionalPubkey) OptionalPubkey {
	return OptionalPubkey{Set: value.Set, Value: value.Value}
}

func classicOption(value OptionalPubkey) classic.OptionalPubkey {
	return classic.OptionalPubkey{Set: value.Set, Value: value.Value}
}
