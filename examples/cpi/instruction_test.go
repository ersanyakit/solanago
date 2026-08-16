package cpi

import (
	"encoding/binary"
	"testing"

	"github.com/ersanyakit/go-solana/sdk"
	"github.com/ersanyakit/go-solana/sdk/system"
)

func TestTransferBuilder(t *testing.T) {
	program := sdk.Pubkey{1}
	source := sdk.Pubkey{2}
	destination := sdk.Pubkey{3}
	instruction := Transfer(program, source, destination, 0x0807060504030201)
	if instruction.ProgramID != program {
		t.Fatalf("program id = %s, want %s", instruction.ProgramID, program)
	}
	wantAccounts := []sdk.AccountMeta{
		sdk.Writable(source, true),
		sdk.Writable(destination, false),
		sdk.Readonly(system.ProgramID, false),
	}
	if len(instruction.Accounts) != len(wantAccounts) {
		t.Fatalf("accounts = %+v", instruction.Accounts)
	}
	for index := range wantAccounts {
		if instruction.Accounts[index] != wantAccounts[index] {
			t.Fatalf("account %d = %+v, want %+v", index, instruction.Accounts[index], wantAccounts[index])
		}
	}
	if len(instruction.Data) != TransferDataSize || binary.LittleEndian.Uint64(instruction.Data) != 0x0807060504030201 {
		t.Fatalf("data = %x", instruction.Data)
	}
}
