package hellogo

import "encoding/binary"

const (
	mintStateTag  = byte(1)
	tokenStateTag = byte(2)

	flagInitialized = byte(1)
	flagAuthority   = byte(2)
)

// DecodeMintState verifies and decodes the canonical 48-byte layout.
func DecodeMintState(data []byte) (MintState, error) {
	if len(data) != MintStateSize {
		return MintState{}, ErrInvalidStateEncoding
	}
	if allZero(data) {
		return MintState{}, nil
	}
	if data[0] != mintStateTag || (data[1] != flagInitialized && data[1] != flagInitialized|flagAuthority) || !allZero(data[3:8]) {
		return MintState{}, ErrInvalidStateEncoding
	}
	state := MintState{
		Initialized: data[1]&flagInitialized != 0,
		Decimals:    data[2],
		Supply:      binary.LittleEndian.Uint64(data[8:16]),
	}
	state.MintAuthority.Set = data[1]&flagAuthority != 0
	copy(state.MintAuthority.Key[:], data[16:48])
	if !state.Initialized || (!state.MintAuthority.Set && !allZero(state.MintAuthority.Key[:])) {
		return MintState{}, ErrInvalidStateEncoding
	}
	return state, nil
}

// EncodeMintState writes a canonical initialized mint into exactly 48 bytes.
func EncodeMintState(data []byte, state MintState) error {
	if len(data) != MintStateSize || !state.Initialized {
		return ErrInvalidStateEncoding
	}
	if !state.MintAuthority.Set && !allZero(state.MintAuthority.Key[:]) {
		return ErrInvalidStateEncoding
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

// DecodeTokenAccountState verifies and decodes the canonical 120-byte layout.
func DecodeTokenAccountState(data []byte) (TokenAccountState, error) {
	if len(data) != TokenAccountStateSize {
		return TokenAccountState{}, ErrInvalidStateEncoding
	}
	if allZero(data) {
		return TokenAccountState{}, nil
	}
	if data[0] != tokenStateTag || (data[1] != flagInitialized && data[1] != flagInitialized|flagAuthority) || !allZero(data[2:8]) {
		return TokenAccountState{}, ErrInvalidStateEncoding
	}
	state := TokenAccountState{
		Initialized:     true,
		Amount:          binary.LittleEndian.Uint64(data[8:16]),
		DelegatedAmount: binary.LittleEndian.Uint64(data[16:24]),
	}
	copy(state.Mint[:], data[24:56])
	copy(state.Owner[:], data[56:88])
	state.Delegate.Set = data[1]&flagAuthority != 0
	copy(state.Delegate.Key[:], data[88:120])
	if !state.Delegate.Set && (state.DelegatedAmount != 0 || !allZero(state.Delegate.Key[:])) {
		return TokenAccountState{}, ErrInvalidStateEncoding
	}
	return state, nil
}

// EncodeTokenAccountState writes a canonical initialized token account into
// exactly 120 bytes.
func EncodeTokenAccountState(data []byte, state TokenAccountState) error {
	if len(data) != TokenAccountStateSize || !state.Initialized {
		return ErrInvalidStateEncoding
	}
	if !state.Delegate.Set && (state.DelegatedAmount != 0 || !allZero(state.Delegate.Key[:])) {
		return ErrInvalidStateEncoding
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
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}
