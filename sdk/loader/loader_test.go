package loader

import (
	"encoding/hex"
	"testing"

	"github.com/ersany/go-solana/sdk"
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
