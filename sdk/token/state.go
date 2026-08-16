package token

import (
	"encoding/binary"

	"github.com/ersanyakit/solanago/sdk"
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
	if len(data) != MintSize {
		return Mint{}, ErrInvalidState
	}
	mintAuthority, err := decodeOptionalPubkeyState(data[0:36])
	if err != nil {
		return Mint{}, err
	}
	initialized, err := decodeBool(data[45])
	if err != nil {
		return Mint{}, err
	}
	freezeAuthority, err := decodeOptionalPubkeyState(data[46:82])
	if err != nil {
		return Mint{}, err
	}
	return Mint{
		MintAuthority:   mintAuthority,
		Supply:          binary.LittleEndian.Uint64(data[36:44]),
		Decimals:        data[44],
		Initialized:     initialized,
		FreezeAuthority: freezeAuthority,
	}, nil
}

func EncodeMint(mint Mint) ([]byte, error) {
	data := make([]byte, MintSize)
	if err := encodeOptionalPubkeyState(data[0:36], mint.MintAuthority); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(data[36:44], mint.Supply)
	data[44] = mint.Decimals
	if mint.Initialized {
		data[45] = 1
	}
	if err := encodeOptionalPubkeyState(data[46:82], mint.FreezeAuthority); err != nil {
		return nil, err
	}
	return data, nil
}

func DecodeAccount(data []byte) (Account, error) {
	if len(data) != AccountSize {
		return Account{}, ErrInvalidState
	}
	var account Account
	copy(account.Mint[:], data[0:32])
	copy(account.Owner[:], data[32:64])
	account.Amount = binary.LittleEndian.Uint64(data[64:72])
	var err error
	account.Delegate, err = decodeOptionalPubkeyState(data[72:108])
	if err != nil {
		return Account{}, err
	}
	account.State = AccountState(data[108])
	if account.State > AccountFrozen {
		return Account{}, ErrInvalidState
	}
	account.IsNative, err = decodeOptionalU64State(data[109:121])
	if err != nil {
		return Account{}, err
	}
	account.DelegatedAmount = binary.LittleEndian.Uint64(data[121:129])
	account.CloseAuthority, err = decodeOptionalPubkeyState(data[129:165])
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

func EncodeAccount(account Account) ([]byte, error) {
	if account.State > AccountFrozen {
		return nil, ErrInvalidState
	}
	data := make([]byte, AccountSize)
	copy(data[0:32], account.Mint[:])
	copy(data[32:64], account.Owner[:])
	binary.LittleEndian.PutUint64(data[64:72], account.Amount)
	if err := encodeOptionalPubkeyState(data[72:108], account.Delegate); err != nil {
		return nil, err
	}
	data[108] = byte(account.State)
	if err := encodeOptionalU64State(data[109:121], account.IsNative); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(data[121:129], account.DelegatedAmount)
	if err := encodeOptionalPubkeyState(data[129:165], account.CloseAuthority); err != nil {
		return nil, err
	}
	return data, nil
}

func DecodeMultisig(data []byte) (Multisig, error) {
	if len(data) != MultisigSize {
		return Multisig{}, ErrInvalidState
	}
	initialized, err := decodeBool(data[2])
	if err != nil {
		return Multisig{}, err
	}
	state := Multisig{M: data[0], N: data[1], Initialized: initialized}
	for i := range state.Signers {
		copy(state.Signers[i][:], data[3+i*32:3+(i+1)*32])
	}
	return state, nil
}

func EncodeMultisig(state Multisig) ([]byte, error) {
	data := make([]byte, MultisigSize)
	data[0], data[1] = state.M, state.N
	if state.Initialized {
		data[2] = 1
	}
	for i := range state.Signers {
		copy(data[3+i*32:3+(i+1)*32], state.Signers[i][:])
	}
	return data, nil
}

func decodeBool(value byte) (bool, error) {
	switch value {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, ErrInvalidState
	}
}

func decodeOptionalPubkeyState(data []byte) (OptionalPubkey, error) {
	if len(data) != 36 {
		return OptionalPubkey{}, ErrInvalidState
	}
	tag := binary.LittleEndian.Uint32(data[:4])
	if tag > 1 {
		return OptionalPubkey{}, ErrInvalidState
	}
	value := OptionalPubkey{Set: tag == 1}
	if value.Set {
		copy(value.Value[:], data[4:])
	}
	return value, nil
}

func encodeOptionalPubkeyState(data []byte, value OptionalPubkey) error {
	if len(data) != 36 {
		return ErrInvalidState
	}
	if value.Set {
		binary.LittleEndian.PutUint32(data[:4], 1)
		copy(data[4:], value.Value[:])
	}
	return nil
}

func decodeOptionalU64State(data []byte) (OptionalU64, error) {
	if len(data) != 12 {
		return OptionalU64{}, ErrInvalidState
	}
	tag := binary.LittleEndian.Uint32(data[:4])
	if tag > 1 {
		return OptionalU64{}, ErrInvalidState
	}
	if tag == 0 {
		return OptionalU64{}, nil
	}
	return SomeU64(binary.LittleEndian.Uint64(data[4:])), nil
}

func encodeOptionalU64State(data []byte, value OptionalU64) error {
	if len(data) != 12 {
		return ErrInvalidState
	}
	if value.Set {
		binary.LittleEndian.PutUint32(data[:4], 1)
		binary.LittleEndian.PutUint64(data[4:], value.Value)
	}
	return nil
}
