package system

import (
	"encoding/binary"

	"github.com/ersanyakit/solanago/sdk"
)

const NonceStateSize = 80

type NonceVersion uint32

const (
	NonceVersionLegacy NonceVersion = iota
	NonceVersionCurrent
)

type NonceStatus uint32

const (
	NonceUninitialized NonceStatus = iota
	NonceInitialized
)

// NonceState is the current official fixed 80-byte generated nonce layout.
type NonceState struct {
	Version              NonceVersion
	Status               NonceStatus
	Authority            sdk.Pubkey
	Blockhash            sdk.Pubkey
	LamportsPerSignature uint64
}

func DecodeNonceState(data []byte) (NonceState, error) {
	if len(data) != NonceStateSize {
		return NonceState{}, ErrInvalidNonceState
	}
	state := NonceState{
		Version:              NonceVersion(binary.LittleEndian.Uint32(data[0:4])),
		Status:               NonceStatus(binary.LittleEndian.Uint32(data[4:8])),
		LamportsPerSignature: binary.LittleEndian.Uint64(data[72:80]),
	}
	if state.Version > NonceVersionCurrent || state.Status > NonceInitialized {
		return NonceState{}, ErrInvalidNonceState
	}
	copy(state.Authority[:], data[8:40])
	copy(state.Blockhash[:], data[40:72])
	return state, nil
}

func EncodeNonceState(state NonceState) ([]byte, error) {
	if state.Version > NonceVersionCurrent || state.Status > NonceInitialized {
		return nil, ErrInvalidNonceState
	}
	data := make([]byte, NonceStateSize)
	binary.LittleEndian.PutUint32(data[0:4], uint32(state.Version))
	binary.LittleEndian.PutUint32(data[4:8], uint32(state.Status))
	copy(data[8:40], state.Authority[:])
	copy(data[40:72], state.Blockhash[:])
	binary.LittleEndian.PutUint64(data[72:80], state.LamportsPerSignature)
	return data, nil
}
