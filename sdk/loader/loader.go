// Package loader builds the upgradeable-loader v3 instructions used by the
// current Agave deployment path. The wire format is pinned to
// solana-loader-v3-interface 7.0.0 selected by Agave 12b5c7e.
package loader

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/ersany/go-solana/sdk"
	"github.com/ersany/go-solana/sdk/system"
)

const (
	BufferMetadataSize      = 37
	ProgramMetadataSize     = 36
	ProgramDataMetadataSize = 45
)

var (
	ProgramID   = sdk.MustParsePubkey("BPFLoaderUpgradeab1e11111111111111111111111")
	ClockSysvar = sdk.MustParsePubkey("SysvarC1ock11111111111111111111111111111111")
	ErrTooLarge = errors.New("loader: program or write offset exceeds wire limits")
)

// ProgramDataAddress returns the official loader PDA for programID.
func ProgramDataAddress(programID sdk.Pubkey) (sdk.Pubkey, error) {
	address, _, err := sdk.FindProgramAddress([][]byte{programID[:]}, ProgramID)
	return address, err
}

// CreateBuffer returns the atomic System CreateAccount + InitializeBuffer
// pair required to prevent a third party from taking buffer authority.
func CreateBuffer(payer, buffer, authority sdk.Pubkey, lamports uint64, programLen int) ([]sdk.Instruction, error) {
	if programLen < 0 || uint64(programLen) > math.MaxUint64-BufferMetadataSize {
		return nil, ErrTooLarge
	}
	create := system.CreateAccount(payer, buffer, lamports, uint64(BufferMetadataSize+programLen), ProgramID)
	initialize := sdk.Instruction{
		ProgramID: ProgramID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(buffer, false),
			sdk.Readonly(authority, false),
		},
		Data: []byte{0, 0, 0, 0},
	}
	return []sdk.Instruction{create, initialize}, nil
}

// Write writes one byte chunk at offset in an initialized loader buffer.
func Write(buffer, authority sdk.Pubkey, offset uint32, bytes []byte) sdk.Instruction {
	data := make([]byte, 4+4+8+len(bytes))
	binary.LittleEndian.PutUint32(data[0:4], 1)
	binary.LittleEndian.PutUint32(data[4:8], offset)
	binary.LittleEndian.PutUint64(data[8:16], uint64(len(bytes)))
	copy(data[16:], bytes)
	return sdk.Instruction{
		ProgramID: ProgramID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(buffer, false),
			sdk.Readonly(authority, true),
		},
		Data: data,
	}
}

// Upgrade returns the loader Upgrade instruction, replacing a live program's
// code with the contents of an already-written, finalized buffer. Unlike
// DeployWithMaxDataLen, the program account does not sign: only the upgrade
// authority does. Excess buffer lamports are swept to spill.
func Upgrade(program, buffer, authority, spill sdk.Pubkey) (sdk.Instruction, error) {
	programData, err := ProgramDataAddress(program)
	if err != nil {
		return sdk.Instruction{}, err
	}
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, 3)
	return sdk.Instruction{
		ProgramID: ProgramID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(programData, false),
			sdk.Writable(program, false),
			sdk.Writable(buffer, false),
			sdk.Writable(spill, false),
			sdk.Readonly(system.RentSysvar, false),
			sdk.Readonly(ClockSysvar, false),
			sdk.Readonly(authority, true),
		},
		Data: data,
	}, nil
}

// ProgramDataState is the decoded shape of a finalized ProgramData account.
type ProgramDataState struct {
	Slot         uint64
	HasAuthority bool
	Authority    sdk.Pubkey
	// ProgramBytes is the trailing program-code region. Its length equals the
	// MaxDataLen the account was allocated with at first deploy; it does not
	// shrink to the size of whichever ELF is currently stored there.
	ProgramBytes []byte
}

// DecodeProgramDataAccount decodes a finalized ProgramData account's raw
// bytes. It returns an error if the state tag is not the ProgramData variant
// (3) or the account is shorter than the fixed metadata prefix.
func DecodeProgramDataAccount(data []byte) (ProgramDataState, error) {
	if len(data) < ProgramDataMetadataSize {
		return ProgramDataState{}, fmt.Errorf("loader: ProgramData account is truncated: have %d bytes, need at least %d", len(data), ProgramDataMetadataSize)
	}
	if binary.LittleEndian.Uint32(data[:4]) != 3 {
		return ProgramDataState{}, errors.New("loader: account is not in ProgramData state")
	}
	state := ProgramDataState{
		Slot:         binary.LittleEndian.Uint64(data[4:12]),
		HasAuthority: data[12] == 1,
	}
	copy(state.Authority[:], data[13:ProgramDataMetadataSize])
	state.ProgramBytes = data[ProgramDataMetadataSize:]
	return state, nil
}

// DeployWithMaxDataLen returns the atomic Program account creation and loader
// deployment pair. The ProgramData PDA is created by the loader itself.
func DeployWithMaxDataLen(payer, program, buffer, authority sdk.Pubkey, programLamports uint64, maxDataLen int) ([]sdk.Instruction, error) {
	if maxDataLen < 0 || uint64(maxDataLen) > math.MaxUint64-ProgramDataMetadataSize {
		return nil, ErrTooLarge
	}
	programData, err := ProgramDataAddress(program)
	if err != nil {
		return nil, err
	}
	create := system.CreateAccount(payer, program, programLamports, ProgramMetadataSize, ProgramID)
	data := make([]byte, 12)
	binary.LittleEndian.PutUint32(data[0:4], 2)
	binary.LittleEndian.PutUint64(data[4:12], uint64(maxDataLen))
	deploy := sdk.Instruction{
		ProgramID: ProgramID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(payer, true),
			sdk.Writable(programData, false),
			sdk.Writable(program, false),
			sdk.Writable(buffer, false),
			sdk.Readonly(system.RentSysvar, false),
			sdk.Readonly(ClockSysvar, false),
			sdk.Readonly(system.ProgramID, false),
			sdk.Readonly(authority, true),
		},
		Data: data,
	}
	return []sdk.Instruction{create, deploy}, nil
}
