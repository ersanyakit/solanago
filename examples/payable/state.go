package payable

import "encoding/binary"

const (
	VaultStateSize   = 48
	DepositStateSize = 80

	vaultStateTag   byte = 1
	depositStateTag byte = 2

	flagInitialized byte = 1 << 0
)

// DecodeVaultState reads the fixed-width vault layout:
//
//	byte 0        tag
//	byte 1        flags (bit 0: initialized)
//	bytes 2..8    reserved, must be zero
//	bytes 8..16   total deposited lamports (uint64 LE)
//	bytes 16..48  emergency-withdraw authority pubkey
func DecodeVaultState(data []byte) (VaultState, error) {
	if len(data) != VaultStateSize {
		return VaultState{}, ErrInvalidState
	}
	if allZero(data) {
		return VaultState{}, nil
	}
	if data[0] != vaultStateTag || data[1]&^flagInitialized != 0 || !allZero(data[2:8]) {
		return VaultState{}, ErrInvalidState
	}
	state := VaultState{
		Initialized:    data[1]&flagInitialized != 0,
		TotalDeposited: binary.LittleEndian.Uint64(data[8:16]),
	}
	copy(state.Authority[:], data[16:48])
	if !state.Initialized {
		return VaultState{}, ErrInvalidState
	}
	return state, nil
}

// EncodeVaultState writes the canonical fixed-width vault layout.
func EncodeVaultState(data []byte, state VaultState) error {
	if len(data) != VaultStateSize || !state.Initialized {
		return ErrInvalidState
	}
	clear(data)
	data[0] = vaultStateTag
	data[1] = flagInitialized
	binary.LittleEndian.PutUint64(data[8:16], state.TotalDeposited)
	copy(data[16:48], state.Authority[:])
	return nil
}

// DecodeDepositState reads the fixed-width per-depositor layout:
//
//	byte 0        tag
//	byte 1        flags (bit 0: initialized)
//	bytes 2..8    reserved, must be zero
//	bytes 8..16   balance lamports (uint64 LE)
//	bytes 16..48  vault pubkey
//	bytes 48..80  depositor pubkey
func DecodeDepositState(data []byte) (DepositState, error) {
	if len(data) != DepositStateSize {
		return DepositState{}, ErrInvalidState
	}
	if allZero(data) {
		return DepositState{}, nil
	}
	if data[0] != depositStateTag || data[1]&^flagInitialized != 0 || !allZero(data[2:8]) {
		return DepositState{}, ErrInvalidState
	}
	state := DepositState{
		Initialized: data[1]&flagInitialized != 0,
		Balance:     binary.LittleEndian.Uint64(data[8:16]),
	}
	copy(state.Vault[:], data[16:48])
	copy(state.Depositor[:], data[48:80])
	if !state.Initialized {
		return DepositState{}, ErrInvalidState
	}
	return state, nil
}

// EncodeDepositState writes the canonical fixed-width per-depositor layout.
func EncodeDepositState(data []byte, state DepositState) error {
	if len(data) != DepositStateSize || !state.Initialized {
		return ErrInvalidState
	}
	clear(data)
	data[0] = depositStateTag
	data[1] = flagInitialized
	binary.LittleEndian.PutUint64(data[8:16], state.Balance)
	copy(data[16:48], state.Vault[:])
	copy(data[48:80], state.Depositor[:])
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

func copyVaultState(state VaultState) ([]byte, error) {
	data := make([]byte, VaultStateSize)
	if err := EncodeVaultState(data, state); err != nil {
		return nil, err
	}
	return data, nil
}

func copyDepositState(state DepositState) ([]byte, error) {
	data := make([]byte, DepositStateSize)
	if err := EncodeDepositState(data, state); err != nil {
		return nil, err
	}
	return data, nil
}
