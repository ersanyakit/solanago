package erc20

import "encoding/binary"

const (
	MintStateSize      = 100
	BalanceStateSize   = 80
	AllowanceStateSize = 112

	mintStateTag      byte = 1
	balanceStateTag   byte = 2
	allowanceStateTag byte = 3

	flagInitialized byte = 1 << 0
	flagAuthority   byte = 1 << 1
)

// DecodeMintState reads the fixed-width mint layout:
//
//	byte 0        tag
//	byte 1        flags (bit 0: initialized, bit 1: mint authority set)
//	bytes 2..8    reserved, must be zero
//	bytes 8..16   total supply (uint64 LE)
//	bytes 16..48  mint authority pubkey
//	byte 48       decimals
//	bytes 49..56  reserved, must be zero
//	bytes 56..88  name (zero-padded UTF-8, max 32 bytes)
//	bytes 88..98  symbol (zero-padded UTF-8, max 10 bytes)
//	bytes 98..100 reserved, must be zero
func DecodeMintState(data []byte) (MintState, error) {
	if len(data) != MintStateSize {
		return MintState{}, ErrInvalidState
	}
	if allZero(data) {
		return MintState{}, nil
	}
	if data[0] != mintStateTag || data[1]&^(flagInitialized|flagAuthority) != 0 || !allZero(data[2:8]) || !allZero(data[49:56]) || !allZero(data[98:100]) {
		return MintState{}, ErrInvalidState
	}
	state := MintState{
		Initialized: data[1]&flagInitialized != 0,
		TotalSupply: binary.LittleEndian.Uint64(data[8:16]),
		Decimals:    data[48],
		Name:        trimZero(data[56:88]),
		Symbol:      trimZero(data[88:98]),
	}
	copy(state.MintAuthority.Key[:], data[16:48])
	state.MintAuthority.Set = data[1]&flagAuthority != 0
	if !state.Initialized || (!state.MintAuthority.Set && !allZero(state.MintAuthority.Key[:])) {
		return MintState{}, ErrInvalidState
	}
	return state, nil
}

// EncodeMintState writes the canonical fixed-width mint layout.
func EncodeMintState(data []byte, state MintState) error {
	if len(data) != MintStateSize || !state.Initialized {
		return ErrInvalidState
	}
	if !state.MintAuthority.Set && !allZero(state.MintAuthority.Key[:]) {
		return ErrInvalidState
	}
	if len(state.Name) > MaxNameLen || len(state.Symbol) > MaxSymbolLen {
		return ErrInvalidState
	}
	clear(data)
	data[0] = mintStateTag
	data[1] = flagInitialized
	binary.LittleEndian.PutUint64(data[8:16], state.TotalSupply)
	if state.MintAuthority.Set {
		data[1] |= flagAuthority
		copy(data[16:48], state.MintAuthority.Key[:])
	}
	data[48] = state.Decimals
	copy(data[56:88], state.Name)
	copy(data[88:98], state.Symbol)
	return nil
}

// DecodeBalanceState reads the fixed-width per-holder layout:
//
//	byte 0       tag
//	byte 1       flags (bit 0: initialized)
//	bytes 2..8   reserved, must be zero
//	bytes 8..16  amount (uint64 LE)
//	bytes 16..48 mint pubkey
//	bytes 48..80 owner pubkey
func DecodeBalanceState(data []byte) (BalanceState, error) {
	if len(data) != BalanceStateSize {
		return BalanceState{}, ErrInvalidState
	}
	if allZero(data) {
		return BalanceState{}, nil
	}
	if data[0] != balanceStateTag || data[1]&^flagInitialized != 0 || !allZero(data[2:8]) {
		return BalanceState{}, ErrInvalidState
	}
	state := BalanceState{
		Initialized: data[1]&flagInitialized != 0,
		Amount:      binary.LittleEndian.Uint64(data[8:16]),
	}
	copy(state.Mint[:], data[16:48])
	copy(state.Owner[:], data[48:80])
	if !state.Initialized {
		return BalanceState{}, ErrInvalidState
	}
	return state, nil
}

// EncodeBalanceState writes the canonical fixed-width per-holder layout.
func EncodeBalanceState(data []byte, state BalanceState) error {
	if len(data) != BalanceStateSize || !state.Initialized {
		return ErrInvalidState
	}
	clear(data)
	data[0] = balanceStateTag
	data[1] = flagInitialized
	binary.LittleEndian.PutUint64(data[8:16], state.Amount)
	copy(data[16:48], state.Mint[:])
	copy(data[48:80], state.Owner[:])
	return nil
}

// DecodeAllowanceState reads the fixed-width per-(owner,spender) layout:
//
//	byte 0        tag
//	byte 1        flags (bit 0: initialized)
//	bytes 2..8    reserved, must be zero
//	bytes 8..16   amount (uint64 LE)
//	bytes 16..48  mint pubkey
//	bytes 48..80  owner pubkey
//	bytes 80..112 spender pubkey
func DecodeAllowanceState(data []byte) (AllowanceState, error) {
	if len(data) != AllowanceStateSize {
		return AllowanceState{}, ErrInvalidState
	}
	if allZero(data) {
		return AllowanceState{}, nil
	}
	if data[0] != allowanceStateTag || data[1]&^flagInitialized != 0 || !allZero(data[2:8]) {
		return AllowanceState{}, ErrInvalidState
	}
	state := AllowanceState{
		Initialized: data[1]&flagInitialized != 0,
		Amount:      binary.LittleEndian.Uint64(data[8:16]),
	}
	copy(state.Mint[:], data[16:48])
	copy(state.Owner[:], data[48:80])
	copy(state.Spender[:], data[80:112])
	if !state.Initialized {
		return AllowanceState{}, ErrInvalidState
	}
	return state, nil
}

// EncodeAllowanceState writes the canonical fixed-width per-(owner,spender)
// layout.
func EncodeAllowanceState(data []byte, state AllowanceState) error {
	if len(data) != AllowanceStateSize || !state.Initialized {
		return ErrInvalidState
	}
	clear(data)
	data[0] = allowanceStateTag
	data[1] = flagInitialized
	binary.LittleEndian.PutUint64(data[8:16], state.Amount)
	copy(data[16:48], state.Mint[:])
	copy(data[48:80], state.Owner[:])
	copy(data[80:112], state.Spender[:])
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

func trimZero(data []byte) string {
	end := len(data)
	for end > 0 && data[end-1] == 0 {
		end--
	}
	return string(data[:end])
}

func copyMintState(state MintState) ([]byte, error) {
	data := make([]byte, MintStateSize)
	if err := EncodeMintState(data, state); err != nil {
		return nil, err
	}
	return data, nil
}

func copyBalanceState(state BalanceState) ([]byte, error) {
	data := make([]byte, BalanceStateSize)
	if err := EncodeBalanceState(data, state); err != nil {
		return nil, err
	}
	return data, nil
}

func copyAllowanceState(state AllowanceState) ([]byte, error) {
	data := make([]byte, AllowanceStateSize)
	if err := EncodeAllowanceState(data, state); err != nil {
		return nil, err
	}
	return data, nil
}
