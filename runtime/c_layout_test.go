package runtime

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestExactCAndRustLayoutSizes(t *testing.T) {
	if got := len(AppendCInstruction(nil, CInstruction{})); got != CInstructionSize {
		t.Fatalf("C instruction size %d", got)
	}
	if got := len(AppendCAccountMeta(nil, CAccountMeta{})); got != CAccountMetaSize {
		t.Fatalf("C account meta size %d", got)
	}
	if got := len(AppendCAccountInfo(nil, CAccountInfo{})); got != CAccountInfoSize {
		t.Fatalf("C account info size %d", got)
	}
	if got := len(AppendCSignerSeed(nil, 1, 2)); got != CSignerSeedSize {
		t.Fatalf("C signer seed size %d", got)
	}
	if got := len(AppendRustStableInstruction(nil, RustStableInstruction{})); got != RustInstructionSize {
		t.Fatalf("Rust stable instruction size %d", got)
	}
}

func TestTranslateCInstruction(t *testing.T) {
	const base = uint64(0x1_0000_0000)
	storage := make([]byte, 160)
	programID := sequentialPubkey(0xa0)
	accountKey := sequentialPubkey(1)
	copy(storage[40:72], programID[:])
	copy(storage[88:120], accountKey[:])
	copy(storage[120:123], []byte{7, 8, 9})
	header := AppendCInstruction(nil, CInstruction{
		ProgramIDAddress: base + 40,
		AccountsAddress:  base + 72,
		AccountsLength:   1,
		DataAddress:      base + 120,
		DataLength:       3,
	})
	copy(storage[0:40], header)
	meta := AppendCAccountMeta(nil, CAccountMeta{PubkeyAddress: base + 88, IsWritable: 1, IsSigner: 0})
	copy(storage[72:88], meta)
	memory, err := NewMemoryMap(MemoryRegion{VMStart: base, Data: storage, Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	instruction, err := TranslateCInstruction(memory, base)
	if err != nil {
		t.Fatal(err)
	}
	if instruction.ProgramID != programID || len(instruction.Accounts) != 1 || instruction.Accounts[0].Pubkey != accountKey || !instruction.Accounts[0].IsWritable || instruction.Accounts[0].IsSigner || !bytes.Equal(instruction.Data, []byte{7, 8, 9}) {
		t.Fatalf("wrong translated instruction: %#v", instruction)
	}

	storage[72+8] = 2
	if _, err := TranslateCInstruction(memory, base); !errors.Is(err, ErrInvalidABI) {
		t.Fatalf("non-canonical bool accepted: %v", err)
	}
}

func TestTranslateRustStableInstruction(t *testing.T) {
	const base = uint64(0x2_0000_0000)
	storage := make([]byte, 128)
	programID := sequentialPubkey(0x90)
	accountKey := sequentialPubkey(5)
	header := AppendRustStableInstruction(nil, RustStableInstruction{
		Accounts:  RustStableVec{Address: base + 80, Capacity: 1, Length: 1},
		Data:      RustStableVec{Address: base + 114, Capacity: 2, Length: 2},
		ProgramID: programID,
	})
	copy(storage, header)
	copy(storage[80:112], accountKey[:])
	storage[112], storage[113] = 1, 0
	copy(storage[114:116], []byte{0xaa, 0xbb})
	memory, err := NewMemoryMap(MemoryRegion{VMStart: base, Data: storage})
	if err != nil {
		t.Fatal(err)
	}
	instruction, err := TranslateRustInstruction(memory, base)
	if err != nil {
		t.Fatal(err)
	}
	if instruction.ProgramID != programID || len(instruction.Accounts) != 1 || instruction.Accounts[0].Pubkey != accountKey || !instruction.Accounts[0].IsSigner || instruction.Accounts[0].IsWritable || !bytes.Equal(instruction.Data, []byte{0xaa, 0xbb}) {
		t.Fatalf("wrong translated stable instruction: %#v", instruction)
	}
}

func TestTranslateCAccountInfosAndSignerSeeds(t *testing.T) {
	const base = uint64(0x3_0000_0000)
	storage := make([]byte, 256)
	key := sequentialPubkey(1)
	owner := sequentialPubkey(90)
	copy(storage[56:88], key[:])
	copy(storage[88:120], owner[:])
	binary.LittleEndian.PutUint64(storage[120:128], 99)
	copy(storage[128:131], []byte{1, 2, 3})
	encodedInfo := AppendCAccountInfo(nil, CAccountInfo{
		KeyAddress: base + 56, LamportsAddress: base + 120,
		DataLength: 3, DataAddress: base + 128, OwnerAddress: base + 88,
		RentEpoch: ^uint64(0), IsSigner: 1, IsWritable: 1,
	})
	copy(storage[:56], encodedInfo)

	// Nested signer layout starts at 144: outer[0] -> seed headers -> data.
	copy(storage[144:160], AppendCSignerSeed(nil, base+160, 2))
	copy(storage[160:176], AppendCSignerSeed(nil, base+176, 3))
	copy(storage[176:192], AppendCSignerSeed(nil, base+179, 1))
	copy(storage[192:196], []byte{9, 8, 7, 6})
	// Adjust descriptors to the actual seed bytes at 192 and 195.
	binary.LittleEndian.PutUint64(storage[160:168], base+192)
	binary.LittleEndian.PutUint64(storage[176:184], base+195)

	memory, err := NewMemoryMap(MemoryRegion{VMStart: base, Data: storage, Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	infos, err := TranslateCAccountInfos(memory, base, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Key != key || infos[0].Owner != owner || infos[0].Lamports != 99 || !bytes.Equal(infos[0].Data, []byte{1, 2, 3}) || !infos[0].IsSigner || !infos[0].IsWritable {
		t.Fatalf("wrong account info: %#v", infos)
	}
	seeds, err := TranslateCSignerSeeds(memory, base+144, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 1 || len(seeds[0]) != 2 || !bytes.Equal(seeds[0][0], []byte{9, 8, 7}) || !bytes.Equal(seeds[0][1], []byte{6}) {
		t.Fatalf("wrong signer seeds: %#v", seeds)
	}
}
