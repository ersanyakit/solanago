package spl20

import "encoding/binary"

const (
	MintStateSize         = 48
	TokenAccountStateSize = 120

	mintStateTag  byte = 1
	tokenStateTag byte = 2

	flagInitialized byte = 1 << 0
	flagAuthority   byte = 1 << 1
)

// DecodeMintState reads the custom example's fixed-width mint layout.
func DecodeMintState(data []byte) (MintState, error) {
	if len(data) != MintStateSize {
		return MintState{}, ErrInvalidState
	}
	if allZero(data) {
		return MintState{}, nil
	}
	if data[0] != mintStateTag || data[1]&^(flagInitialized|flagAuthority) != 0 || !allZero(data[3:8]) {
		return MintState{}, ErrInvalidState
	}
	state := MintState{
		Initialized: data[1]&flagInitialized != 0,
		Decimals:    data[2],
		Supply:      binary.LittleEndian.Uint64(data[8:16]),
	}
	copy(state.MintAuthority.Key[:], data[16:48])
	state.MintAuthority.Set = data[1]&flagAuthority != 0
	if !state.Initialized || (!state.MintAuthority.Set && !allZero(state.MintAuthority.Key[:])) {
		return MintState{}, ErrInvalidState
	}
	return state, nil
}

// EncodeMintState writes the custom example's canonical fixed-width layout.
func EncodeMintState(data []byte, state MintState) error {
	if len(data) != MintStateSize || !state.Initialized {
		return ErrInvalidState
	}
	if !state.MintAuthority.Set && !allZero(state.MintAuthority.Key[:]) {
		return ErrInvalidState
	}
	clear(data)
	data[0] = mintStateTag
	data[1] = flagInitialized
	data[2] = state.Decimals
	binary.LittleEndian.PutUint64(data[8:16], state.Supply)
	if state.MintAuthority.Set {
		data[1] |= flagAuthority
		copy(data[16:48], state.MintAuthority.Key[:])
	}
	return nil
}

// DecodeTokenAccountState reads the custom example's token-account layout.
func DecodeTokenAccountState(data []byte) (TokenAccountState, error) {
	if len(data) != TokenAccountStateSize {
		return TokenAccountState{}, ErrInvalidState
	}
	if allZero(data) {
		return TokenAccountState{}, nil
	}
	if data[0] != tokenStateTag || data[1]&^(flagInitialized|flagAuthority) != 0 || !allZero(data[2:8]) {
		return TokenAccountState{}, ErrInvalidState
	}
	state := TokenAccountState{
		Initialized:     data[1]&flagInitialized != 0,
		Amount:          binary.LittleEndian.Uint64(data[8:16]),
		DelegatedAmount: binary.LittleEndian.Uint64(data[16:24]),
	}
	copy(state.Mint[:], data[24:56])
	copy(state.Owner[:], data[56:88])
	copy(state.Delegate.Key[:], data[88:120])
	state.Delegate.Set = data[1]&flagAuthority != 0
	if !state.Initialized {
		return TokenAccountState{}, ErrInvalidState
	}
	if !state.Delegate.Set && (state.DelegatedAmount != 0 || !allZero(state.Delegate.Key[:])) {
		return TokenAccountState{}, ErrInvalidState
	}
	return state, nil
}

// EncodeTokenAccountState writes the canonical custom token-account layout.
func EncodeTokenAccountState(data []byte, state TokenAccountState) error {
	if len(data) != TokenAccountStateSize || !state.Initialized {
		return ErrInvalidState
	}
	if !state.Delegate.Set && (state.DelegatedAmount != 0 || !allZero(state.Delegate.Key[:])) {
		return ErrInvalidState
	}
	clear(data)
	data[0] = tokenStateTag
	data[1] = flagInitialized
	binary.LittleEndian.PutUint64(data[8:16], state.Amount)
	binary.LittleEndian.PutUint64(data[16:24], state.DelegatedAmount)
	copy(data[24:56], state.Mint[:])
	copy(data[56:88], state.Owner[:])
	if state.Delegate.Set {
		data[1] |= flagAuthority
		copy(data[88:120], state.Delegate.Key[:])
	}
	return nil
}

func allZero(data []byte) bool {
	return !containsNonZero(data)
}

func containsNonZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return true
		}
	}
	return false
}

func copyMintState(state MintState) ([]byte, error) {
	data := make([]byte, MintStateSize)
	if err := EncodeMintState(data, state); err != nil {
		return nil, err
	}
	return data, nil
}

func copyTokenState(state TokenAccountState) ([]byte, error) {
	data := make([]byte, TokenAccountStateSize)
	if err := EncodeTokenAccountState(data, state); err != nil {
		return nil, err
	}
	return data, nil
}
