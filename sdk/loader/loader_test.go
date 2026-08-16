package loader

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/ersany/go-solana/sdk"
	"github.com/ersany/go-solana/sdk/system"
)

func TestAgaveV7GoldenInstructionData(t *testing.T) {
	var payer, buffer, authority, program sdk.Pubkey
	payer[0], buffer[0], authority[0], program[0] = 1, 2, 3, 4
	created, err := CreateBuffer(payer, buffer, authority, 99, 123)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(created[1].Data); got != "00000000" {
		t.Fatalf("initialize = %s", got)
	}
	write := Write(buffer, authority, 0x11223344, []byte{0xaa, 0xbb})
	if got := hex.EncodeToString(write.Data); got != "01000000443322110200000000000000aabb" {
		t.Fatalf("write = %s", got)
	}
	deployed, err := DeployWithMaxDataLen(payer, program, buffer, authority, 88, 0x1020304)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(deployed[1].Data); got != "020000000403020100000000" {
		t.Fatalf("deploy = %s", got)
	}
	if len(deployed[1].Accounts) != 8 || deployed[1].Accounts[7].Pubkey != authority || !deployed[1].Accounts[7].IsSigner {
		t.Fatalf("deploy accounts = %#v", deployed[1].Accounts)
	}
}

func TestUpgradeGoldenInstructionData(t *testing.T) {
	var program, buffer, authority, spill sdk.Pubkey
	program[0], buffer[0], authority[0], spill[0] = 1, 2, 3, 4
	instruction, err := Upgrade(program, buffer, authority, spill)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(instruction.Data); got != "03000000" {
		t.Fatalf("upgrade data = %s, want 03000000", got)
	}
	if instruction.ProgramID != ProgramID {
		t.Fatalf("upgrade program id = %s, want %s", instruction.ProgramID, ProgramID)
	}
	wantProgramData, err := ProgramDataAddress(program)
	if err != nil {
		t.Fatal(err)
	}
	wantAccounts := []sdk.AccountMeta{
		sdk.Writable(wantProgramData, false),
		sdk.Writable(program, false),
		sdk.Writable(buffer, false),
		sdk.Writable(spill, false),
		sdk.Readonly(system.RentSysvar, false),
		sdk.Readonly(ClockSysvar, false),
		sdk.Readonly(authority, true),
	}
	if len(instruction.Accounts) != len(wantAccounts) {
		t.Fatalf("upgrade accounts = %d, want %d", len(instruction.Accounts), len(wantAccounts))
	}
	for index, want := range wantAccounts {
		if instruction.Accounts[index] != want {
			t.Fatalf("upgrade account[%d] = %+v, want %+v", index, instruction.Accounts[index], want)
		}
	}
	// Only the authority signs; unlike DeployWithMaxDataLen, the program
	// account is writable but not itself a signer for Upgrade.
	if instruction.Accounts[1].IsSigner {
		t.Fatal("program account must not be a signer for Upgrade")
	}
}

func TestDecodeProgramDataAccount(t *testing.T) {
	var authority sdk.Pubkey
	authority[0] = 7
	programBytes := []byte{0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 0}
	data := make([]byte, ProgramDataMetadataSize+len(programBytes))
	binary.LittleEndian.PutUint32(data[0:4], 3)
	binary.LittleEndian.PutUint64(data[4:12], 42)
	data[12] = 1
	copy(data[13:ProgramDataMetadataSize], authority[:])
	copy(data[ProgramDataMetadataSize:], programBytes)

	state, err := DecodeProgramDataAccount(data)
	if err != nil {
		t.Fatal(err)
	}
	if state.Slot != 42 || !state.HasAuthority || state.Authority != authority {
		t.Fatalf("decoded state = %+v", state)
	}
	if !bytes.Equal(state.ProgramBytes, programBytes) {
		t.Fatalf("decoded program bytes = %x, want %x", state.ProgramBytes, programBytes)
	}

	if _, err := DecodeProgramDataAccount(data[:ProgramDataMetadataSize-1]); err == nil {
		t.Fatal("expected error for truncated ProgramData account")
	}
	wrongTag := append([]byte(nil), data...)
	binary.LittleEndian.PutUint32(wrongTag[0:4], 2)
	if _, err := DecodeProgramDataAccount(wrongTag); err == nil {
		t.Fatal("expected error for non-ProgramData state tag")
	}
}

func TestProgramDataAddressOfficialVector(t *testing.T) {
	program := sdk.MustParsePubkey("BPFLoaderUpgradeab1e11111111111111111111111")
	address, err := ProgramDataAddress(program)
	if err != nil {
		t.Fatal(err)
	}
	// Cross-checked with Agave 4.0.0 `solana find-program-derived-address`.
	if got, want := address.String(), "DtLycSE3ba2c5NZz7fna677DhUu5x9CFigDaMGRAq2xa"; got != want {
		t.Fatalf("programdata = %s, want %s", got, want)
	}
}
