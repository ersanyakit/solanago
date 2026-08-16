// Package loader builds the upgradeable-loader v3 instructions used by the
// current Agave deployment path. The wire format is pinned to
// solana-loader-v3-interface 7.0.0 selected by Agave 12b5c7e.
package loader

import (
	"encoding/binary"
	"errors"
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
