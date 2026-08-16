package runtime

import (
	"bytes"
	"testing"
)

func FuzzParseInputV1NeverPanics(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 0, 0, 0, 0, 0, 0, 0, 0})
	valid, err := SerializeInputV1(sequentialPubkey(1), []InputAccount{UniqueInputAccount(Account{Data: []byte{1, 2, 3}})}, []byte{4})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid.Buffer)
	f.Fuzz(func(t *testing.T, data []byte) {
		copyData := append([]byte(nil), data...)
		_, _ = ParseInputV1(copyData)
	})
}

func FuzzAgaveABIV1Differential(f *testing.F) {
	f.Add([]byte{1, 2, 3}, []byte{4, 5}, true, false, uint64(9))
	f.Fuzz(func(t *testing.T, accountData, instructionData []byte, signer, writable bool, lamports uint64) {
		if len(accountData) > 128 {
			accountData = accountData[:128]
		}
		if len(instructionData) > 128 {
			instructionData = instructionData[:128]
		}
		programID := sequentialPubkey(0xa0)
		inputs := []InputAccount{
			UniqueInputAccount(Account{
				Key: sequentialPubkey(1), Owner: programID, Lamports: lamports,
				Data: accountData, IsSigner: signer, IsWritable: writable,
			}),
			DuplicateInputAccount(0),
		}
		got, err := SerializeInputV1(programID, inputs, instructionData)
		if err != nil {
			t.Fatal(err)
		}
		want := referenceAgaveABIV1(programID, inputs, instructionData, false)
		if !bytes.Equal(got.Buffer, want) {
			t.Fatalf("differential mismatch at %d", firstDifference(got.Buffer, want))
		}
		context, err := ParseInputV1(got.Buffer)
		if err != nil {
			t.Fatal(err)
		}
		if len(context.Accounts) != 2 || context.Accounts[0] != context.Accounts[1] || context.Accounts[0].Lamports() != lamports || !bytes.Equal(context.Accounts[0].Data(), accountData) {
			t.Fatal("round trip mismatch")
		}
	})
}

func FuzzAgaveABIV1DirectMappingDifferential(f *testing.F) {
	f.Add([]byte{1, 2, 3}, []byte{4, 5}, true, false, uint64(9))
	f.Add([]byte{}, []byte{}, false, true, uint64(0))
	f.Fuzz(func(t *testing.T, accountData, instructionData []byte, signer, writable bool, lamports uint64) {
		if len(accountData) > 128 {
			accountData = accountData[:128]
		}
		if len(instructionData) > 128 {
			instructionData = instructionData[:128]
		}
		programID := sequentialPubkey(0xa0)
		inputs := []InputAccount{
			UniqueInputAccount(Account{
				Key: sequentialPubkey(1), Owner: programID, Lamports: lamports,
				Data: accountData, IsSigner: signer, IsWritable: writable,
			}),
			DuplicateInputAccount(0),
		}
		got, err := SerializeInputV1WithOptions(programID, inputs, instructionData, SerializeOptions{
			AccountDataDirectMapping: true,
			DirectAccountPointers:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		wantBuffer, wantRegions, wantAccounts, wantInstructionAddress, wantPointersOffset := referenceAgaveABIV1Direct(programID, inputs, instructionData, true)
		if !bytes.Equal(got.Buffer, wantBuffer) {
			t.Fatalf("direct host-buffer differential mismatch at %d", firstDifference(got.Buffer, wantBuffer))
		}
		if !equalAccountRegions(got.AccountRegions, wantAccounts) || got.InstructionDataAddress != wantInstructionAddress || got.DirectAccountPointersOffset != wantPointersOffset {
			t.Fatal("direct address metadata differential mismatch")
		}
		assertInputRegionsEqual(t, got.MemoryRegions(), wantRegions)
		context, err := ParseMappedInputV1(got, ParseOptions{DirectAccountPointers: true, RejectNonCanonicalBools: true})
		if err != nil {
			t.Fatal(err)
		}
		first, err := context.AccountAt(0)
		if err != nil {
			t.Fatal(err)
		}
		duplicate, err := context.AccountAt(1)
		if err != nil {
			t.Fatal(err)
		}
		if first != duplicate || first.Lamports() != lamports || !bytes.Equal(first.Data(), accountData) || !bytes.Equal(context.InstructionData, instructionData) {
			t.Fatal("direct mapped round trip mismatch")
		}
	})
}

func FuzzMemoryAndCTranslationNeverPanic(f *testing.F) {
	f.Add(uint64(0x1000), uint64(1), uint8(0), uint8(1), []byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Fuzz(func(t *testing.T, address, length uint64, accessByte, alignmentByte uint8, data []byte) {
		if len(data) == 0 || len(data) > 4096 {
			return
		}
		memory, err := NewMemoryMap(MemoryRegion{VMStart: 0x1000, Data: append([]byte(nil), data...), Writable: true})
		if err != nil {
			t.Fatal(err)
		}
		access := AccessRead
		if accessByte&1 != 0 {
			access = AccessWrite
		}
		alignment := uint64(1) << (alignmentByte & 7)
		_, _ = memory.Translate(address, length, access, alignment)
		_, _ = TranslateCInstruction(memory, address)
		_, _ = TranslateRustInstruction(memory, address)
		_, _ = TranslateCAccountInfos(memory, address, length)
		_, _ = TranslateCSignerSeeds(memory, address, length)
	})
}
