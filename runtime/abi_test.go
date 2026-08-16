package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"github.com/ersanyakit/solanago/sdk"
)

// This byte fixture is independently assembled from the field sequence in:
//
//	Agave program-runtime/src/serialization.rs::serialize_parameters_for_abiv1
//	commit 12b5c7e4df705927b2f7f579f3aa606aa4bde1c0
//
// and consumed by the SDK parser in:
//
//	solana-sdk program-entrypoint/src/lib.rs::deserialize
//	commit 7437469d1ab5bddbf665f3a1a69aefb422c33e36.
//
// The SHA locks the full 10 KiB realloc padding, not only non-zero fields.
const agaveV1FixtureSHA256 = "46e8face055ffedc85fe47e8f0f49d861a796fb7fa4a2780f2ff754e368f259e"

// This hash locks Agave's host-side metadata buffer for the split input mode.
// Account bytes are intentionally absent from that buffer and are checked as
// independent regions below. It was assembled from the same pinned Agave
// serializer source as agaveV1FixtureSHA256.
const agaveV1DirectMappingFixtureSHA256 = "4c8ab4ff0f8c5d4a64e639b9de3e174b98dafe985414ecb3cc3221de2b99fc59"

func TestAgaveABIV1ByteForByteFixture(t *testing.T) {
	programID := sequentialPubkey(0xa0)
	account := Account{
		Key: sequentialPubkey(1), Owner: sequentialPubkey(0x60),
		Lamports: 0x0102030405060708, Data: []byte{0xde, 0xad, 0xbe},
		IsSigner: true, IsWritable: true,
	}
	inputs := []InputAccount{UniqueInputAccount(account), DuplicateInputAccount(0)}
	instructionData := []byte{9, 8, 7, 6}

	got, err := SerializeInputV1(programID, inputs, instructionData)
	if err != nil {
		t.Fatal(err)
	}
	want := referenceAgaveABIV1(programID, inputs, instructionData, false)
	if !bytes.Equal(got.Buffer, want) {
		t.Fatalf("ABIv1 differs from independent pinned reference at byte %d", firstDifference(got.Buffer, want))
	}
	hash := sha256.Sum256(want)
	if hex.EncodeToString(hash[:]) != agaveV1FixtureSHA256 {
		t.Fatalf("fixture SHA changed: got %x want %s", hash, agaveV1FixtureSHA256)
	}

	if len(got.AccountRegions) != 2 || got.AccountRegions[0].VMAddress != MMInputStart+8 {
		t.Fatalf("unexpected account metadata: %#v", got.AccountRegions)
	}
	if got.AccountRegions[1].DuplicateOf != 0 || got.AccountRegions[1].VMAddress != got.AccountRegions[0].VMAddress {
		t.Fatalf("duplicate did not alias first metadata: %#v", got.AccountRegions[1])
	}
}

func TestAgaveABIV1ParseAliasingAndMutation(t *testing.T) {
	programID := sequentialPubkey(0xa0)
	owner := programID
	inputs := []InputAccount{
		UniqueInputAccount(Account{Key: sequentialPubkey(1), Owner: owner, Lamports: 20, Data: []byte{1, 2, 3}, IsSigner: true, IsWritable: true}),
		DuplicateInputAccount(0),
		UniqueInputAccount(Account{Key: sequentialPubkey(33), Owner: sequentialPubkey(90), Lamports: 5, IsWritable: true}),
	}
	serialized, err := SerializeInputV1(programID, inputs, []byte{4, 5})
	if err != nil {
		t.Fatal(err)
	}
	context, err := ParseInputV1(serialized.Buffer)
	if err != nil {
		t.Fatal(err)
	}
	if context.ProgramID != programID || !bytes.Equal(context.InstructionData, []byte{4, 5}) {
		t.Fatalf("wrong context: %#v", context)
	}
	if context.Accounts[0] != context.Accounts[1] {
		t.Fatal("duplicate AccountInfo must share one view")
	}
	region := context.AccountRegions()[0]
	originalLenOffset := region.RecordByteOffset + 4
	if binary.LittleEndian.Uint32(serialized.Buffer[originalLenOffset:originalLenOffset+4]) != 3 {
		t.Fatal("SDK original_data_len field was not populated")
	}

	data, err := context.Accounts[1].WritableData(programID)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 0x44
	if context.Accounts[0].Data()[0] != 0x44 {
		t.Fatal("duplicate data mutation did not alias")
	}
	if err := context.Accounts[0].ResizeData(programID, 8); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint64(serialized.Buffer[region.DataAddress-MMInputStart-8:region.DataAddress-MMInputStart]) != 8 {
		t.Fatal("data length was not updated in ABI buffer")
	}
	if err := context.TransferLamports(0, 2, 7); err != nil {
		t.Fatal(err)
	}
	if context.Accounts[0].Lamports() != 13 || context.Accounts[2].Lamports() != 12 {
		t.Fatal("lamport transfer mismatch")
	}
}

func TestAgaveABIV1DirectAccountPointers(t *testing.T) {
	programID := sequentialPubkey(0xa0)
	inputs := []InputAccount{
		UniqueInputAccount(Account{Key: sequentialPubkey(1), Owner: programID, Data: []byte{1}}),
		DuplicateInputAccount(0),
	}
	got, err := SerializeInputV1WithOptions(programID, inputs, nil, SerializeOptions{DirectAccountPointers: true})
	if err != nil {
		t.Fatal(err)
	}
	want := referenceAgaveABIV1(programID, inputs, nil, true)
	if !bytes.Equal(got.Buffer, want) {
		t.Fatalf("direct pointer fixture mismatch at %d", firstDifference(got.Buffer, want))
	}
	if got.DirectAccountPointersOffset < 0 || got.DirectAccountPointersOffset%8 != 0 {
		t.Fatalf("unaligned pointer array offset %d", got.DirectAccountPointersOffset)
	}
	if _, err := ParseInputV1WithOptions(got.Buffer, ParseOptions{DirectAccountPointers: true}); err != nil {
		t.Fatal(err)
	}
}

func TestAgaveABIV1DirectMappingByteForByteAndRegions(t *testing.T) {
	programID := sequentialPubkey(0xa0)
	inputs := []InputAccount{
		UniqueInputAccount(Account{
			Key: sequentialPubkey(1), Owner: programID,
			Lamports: 0x0102030405060708, Data: []byte{0xde, 0xad, 0xbe},
			IsSigner: true, IsWritable: true,
		}),
		DuplicateInputAccount(0),
		UniqueInputAccount(Account{
			Key: sequentialPubkey(0x30), Owner: sequentialPubkey(0x60),
			Lamports: 7, Data: nil,
		}),
	}
	instructionData := []byte{9, 8, 7, 6}
	got, err := SerializeInputV1WithOptions(programID, inputs, instructionData, SerializeOptions{
		AccountDataDirectMapping: true,
		DirectAccountPointers:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantBuffer, wantRegions, wantAccounts, wantInstructionAddress, wantPointersOffset := referenceAgaveABIV1Direct(programID, inputs, instructionData, true)
	if !bytes.Equal(got.Buffer, wantBuffer) {
		t.Fatalf("direct-mapping host buffer differs from independent pinned reference at byte %d", firstDifference(got.Buffer, wantBuffer))
	}
	hash := sha256.Sum256(wantBuffer)
	if hex.EncodeToString(hash[:]) != agaveV1DirectMappingFixtureSHA256 {
		t.Fatalf("direct-mapping fixture SHA changed: got %x want %s", hash, agaveV1DirectMappingFixtureSHA256)
	}
	if got.InstructionDataAddress != wantInstructionAddress || got.DirectAccountPointersOffset != wantPointersOffset {
		t.Fatalf("tail metadata mismatch: instruction=%#x/%#x pointers=%d/%d", got.InstructionDataAddress, wantInstructionAddress, got.DirectAccountPointersOffset, wantPointersOffset)
	}
	if !equalAccountRegions(got.AccountRegions, wantAccounts) {
		t.Fatalf("account regions mismatch:\n got %#v\nwant %#v", got.AccountRegions, wantAccounts)
	}
	assertInputRegionsEqual(t, got.MemoryRegions(), wantRegions)

	// The account-data reservation is an address-space gap, not a host slice.
	memory, err := got.MemoryMap()
	if err != nil {
		t.Fatal(err)
	}
	dataAddress := got.AccountRegions[0].DataAddress
	data, err := memory.Translate(dataAddress, 3, AccessWrite, 1)
	if err != nil || !bytes.Equal(data, []byte{0xde, 0xad, 0xbe}) {
		t.Fatalf("mapped account data mismatch: %x, %v", data, err)
	}
	if _, err := memory.Translate(dataAddress+3, 1, AccessRead, 1); !errors.Is(err, ErrAccessViolation) {
		t.Fatalf("realloc reservation was exposed as host memory: %v", err)
	}
	paddingAddress := dataAddress + uint64(3+MaxPermittedDataIncrease)
	if padding, err := memory.Translate(paddingAddress, 5, AccessRead, 1); err != nil || !bytes.Equal(padding, make([]byte, 5)) {
		t.Fatalf("post-reservation alignment mapping mismatch: %x, %v", padding, err)
	}
	if rent, err := memory.ReadUint64(paddingAddress + 5); err != nil || rent != math.MaxUint64 {
		t.Fatalf("masked rent epoch mismatch: %#x, %v", rent, err)
	}
	// The zero-length external account has a reserved VM range but no mapped
	// host byte, exactly like Agave's zero-length MemoryRegion.
	emptyAddress := got.AccountRegions[2].DataAddress
	if _, err := memory.Translate(emptyAddress, 1, AccessRead, 1); !errors.Is(err, ErrAccessViolation) {
		t.Fatalf("zero-length account unexpectedly mapped a byte: %v", err)
	}

	parsed, err := ParseMappedInputV1(got, ParseOptions{DirectAccountPointers: true, RejectNonCanonicalBools: true})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ProgramAddress() != programID || parsed.AccountCount() != 3 || !bytes.Equal(parsed.InstructionData, instructionData) {
		t.Fatalf("mapped parser context mismatch: %#v", parsed)
	}
	first, _ := parsed.AccountAt(0)
	duplicate, _ := parsed.AccountAt(1)
	if first != duplicate || !bytes.Equal(first.Data(), []byte{0xde, 0xad, 0xbe}) {
		t.Fatal("mapped duplicate did not alias the original account")
	}

	originalPointer := binary.LittleEndian.Uint64(got.Buffer[got.DirectAccountPointersOffset:])
	binary.LittleEndian.PutUint64(got.Buffer[got.DirectAccountPointersOffset:], originalPointer+8)
	if _, err := ParseMappedInputV1(got, ParseOptions{DirectAccountPointers: true}); !errors.Is(err, ErrInvalidABI) {
		t.Fatalf("corrupt direct pointer accepted: %v", err)
	}
	binary.LittleEndian.PutUint64(got.Buffer[got.DirectAccountPointersOffset:], originalPointer)
	if err := first.ResizeData(programID, 6); err != nil {
		t.Fatalf("mapped parser could not grow account inside reservation: %v", err)
	}
	if mapped, err := memory.Translate(dataAddress, 6, AccessWrite, 1); err != nil || !bytes.Equal(mapped, []byte{0xde, 0xad, 0xbe, 0, 0, 0}) {
		t.Fatalf("mapped parser realloc mismatch: %x, %v", mapped, err)
	}
	if regions := got.MemoryRegions(); len(regions) < 2 || len(regions[1].Data) != 6 {
		t.Fatalf("serialized mapping did not track parsed realloc: %#v", regions)
	}
}

func TestAgaveABIV1DirectMappedContextReallocatesWithinReservation(t *testing.T) {
	programID := sequentialPubkey(1)
	serialized, err := SerializeInputV1WithOptions(programID, []InputAccount{
		UniqueInputAccount(Account{Key: sequentialPubkey(2), Owner: programID, Data: []byte{1, 2, 3}, IsWritable: true}),
	}, nil, SerializeOptions{AccountDataDirectMapping: true})
	if err != nil {
		t.Fatal(err)
	}
	context, err := serialized.MappedContext()
	if err != nil {
		t.Fatal(err)
	}
	account, err := context.AccountAt(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := account.ResizeData(programID, 8); err != nil {
		t.Fatal(err)
	}
	writable, err := account.WritableData(programID)
	if err != nil {
		t.Fatal(err)
	}
	copy(writable[3:], []byte{4, 5, 6, 7, 8})
	regions := serialized.MemoryRegions()
	if len(regions) != 3 || regions[1].Kind != InputRegionAccountData || len(regions[1].Data) != 8 || regions[1].ReservedLength != 3+MaxPermittedDataIncrease {
		t.Fatalf("live account mapping did not resize: %#v", regions)
	}
	memory, err := context.MemoryMap()
	if err != nil {
		t.Fatal(err)
	}
	data, err := memory.Translate(serialized.AccountRegions[0].DataAddress, 8, AccessWrite, 1)
	if err != nil || !bytes.Equal(data, []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("resized direct mapping mismatch: %x, %v", data, err)
	}
}

func TestAccountViewSynchronizesDirectVMMutations(t *testing.T) {
	programID := sequentialPubkey(1)
	serialized, err := SerializeInputV1(programID, []InputAccount{
		UniqueInputAccount(Account{Key: sequentialPubkey(2), Owner: programID, Lamports: 3, Data: []byte{1}, IsWritable: true}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := ParseInputV1(serialized.Buffer)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := context.MemoryMap()
	if err != nil {
		t.Fatal(err)
	}
	region := context.AccountRegions()[0]
	if err := memory.WriteUint64(region.LamportsAddress, 44); err != nil {
		t.Fatal(err)
	}
	if context.Accounts[0].Lamports() != 44 {
		t.Fatal("lamports getter did not observe VM memory")
	}
	if err := memory.WriteUint64(region.DataAddress-8, uint64(MaxPermittedDataIncrease+2)); err != nil {
		t.Fatal(err)
	}
	if err := context.SyncAccounts(); !isProgramErrorKind(err, ProgramErrorInvalidRealloc) {
		t.Fatalf("invalid VM realloc was accepted: %v", err)
	}
}

func TestAgaveABIV1VerifierRejectsMalformed(t *testing.T) {
	programID := sequentialPubkey(1)
	valid, err := SerializeInputV1(programID, []InputAccount{UniqueInputAccount(Account{})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{0, 1, 7, 8, 9, 95, len(valid.Buffer) - 1} {
		input := append([]byte(nil), valid.Buffer[:cut]...)
		if _, err := ParseInputV1(input); err == nil {
			t.Fatalf("truncation at %d accepted", cut)
		}
	}

	invalidDuplicate := make([]byte, 8+8+8+32)
	binary.LittleEndian.PutUint64(invalidDuplicate, 1)
	invalidDuplicate[8] = 0
	if _, err := ParseInputV1(invalidDuplicate); !errors.Is(err, ErrInvalidDuplicate) {
		t.Fatalf("expected invalid duplicate, got %v", err)
	}

	nonCanonical := append([]byte(nil), valid.Buffer...)
	nonCanonical[9] = 2
	if _, err := ParseInputV1WithOptions(nonCanonical, ParseOptions{RejectNonCanonicalBools: true}); !errors.Is(err, ErrInvalidABI) {
		t.Fatalf("expected strict boolean error, got %v", err)
	}
	// SDK compatibility mode treats any nonzero input boolean as true.
	nonCanonical = append([]byte(nil), valid.Buffer...)
	nonCanonical[9] = 2
	context, err := ParseInputV1(nonCanonical)
	if err != nil || !context.Accounts[0].IsSigner() {
		t.Fatalf("SDK-compatible bool mode mismatch: %v", err)
	}
}

func TestAccountValidationAndProgramErrors(t *testing.T) {
	programID := sequentialPubkey(1)
	context := NewContext(programID, []Account{
		{Key: sequentialPubkey(2), Owner: programID, Data: []byte{1}, IsWritable: true},
		{Key: sequentialPubkey(3), Owner: programID},
	}, nil)
	if _, err := context.RequireAccount(2, AccountRequirement{}); !isProgramErrorKind(err, ProgramErrorNotEnoughAccountKeys) {
		t.Fatalf("expected not enough keys, got %v", err)
	}
	if _, err := context.RequireAccount(0, AccountRequirement{Signer: true}); !isProgramErrorKind(err, ProgramErrorMissingRequiredSignature) {
		t.Fatalf("expected signature error, got %v", err)
	}
	if _, err := context.Accounts[1].WritableData(programID); !isProgramErrorKind(err, ProgramErrorInvalidArgument) {
		t.Fatalf("expected read-only error, got %v", err)
	}
	if err := context.AssignOwner(0, sequentialPubkey(9)); !isProgramErrorKind(err, ProgramErrorInvalidAccountData) {
		t.Fatalf("expected nonzero-data owner error, got %v", err)
	}
	if err := context.Accounts[0].ResizeData(programID, MaxPermittedDataIncrease+2); !isProgramErrorKind(err, ProgramErrorInvalidRealloc) {
		t.Fatalf("expected realloc error, got %v", err)
	}

	for kind := ProgramErrorInvalidArgument; kind <= ProgramErrorIncorrectAuthority; kind++ {
		errorValue := BuiltinProgramError(kind)
		decoded, ok := ProgramErrorFromReturnCode(errorValue.ReturnCode())
		if !ok || decoded != errorValue {
			t.Fatalf("program error round trip failed for %d: %#v", kind, decoded)
		}
	}
	for _, code := range []uint32{0, 1, math.MaxUint32} {
		errorValue := CustomProgramError(code)
		decoded, ok := ProgramErrorFromReturnCode(errorValue.ReturnCode())
		if !ok || decoded != errorValue {
			t.Fatalf("custom error round trip failed for %d: %#v", code, decoded)
		}
	}
}

func TestValidateDirectProgramAccountChanges(t *testing.T) {
	programID := sequentialPubkey(1)
	owned := Account{Key: sequentialPubkey(2), Owner: programID, Lamports: 10, Data: []byte{1}, IsWritable: true}
	external := Account{Key: sequentialPubkey(3), Owner: sequentialPubkey(80), Lamports: 5, Data: []byte{2}, IsWritable: true}
	context := NewContext(programID, []Account{owned, external}, nil)
	if err := context.TransferLamports(0, 1, 3); err != nil {
		t.Fatal(err)
	}
	if err := context.ValidateProgramChanges(); err != nil {
		t.Fatalf("authorized transfer rejected: %v", err)
	}

	context = NewContext(programID, []Account{owned, external}, nil)
	context.Accounts[1].setLamports(4)
	if err := context.ValidateProgramChanges(); !errors.Is(err, ErrExternalLamportSpend) {
		t.Fatalf("external debit accepted: %v", err)
	}

	context = NewContext(programID, []Account{{Key: owned.Key, Owner: programID, Data: []byte{1}}}, nil)
	context.Accounts[0].data[0] = 9
	if err := context.ValidateProgramChanges(); !errors.Is(err, ErrReadonlyAccountModified) {
		t.Fatalf("read-only mutation accepted: %v", err)
	}

	context = NewContext(programID, []Account{owned}, nil)
	context.Accounts[0].setLamports(11)
	if err := context.ValidateProgramChanges(); !errors.Is(err, ErrUnbalancedLamports) {
		t.Fatalf("lamport creation accepted: %v", err)
	}
}

func referenceAgaveABIV1(programID sdk.Pubkey, accounts []InputAccount, instructionData []byte, directPointers bool) []byte {
	buffer := make([]byte, 8)
	binary.LittleEndian.PutUint64(buffer, uint64(len(accounts)))
	addresses := make([]uint64, len(accounts))
	for index, input := range accounts {
		if input.DuplicateOf != nil {
			buffer = append(buffer, *input.DuplicateOf, 0, 0, 0, 0, 0, 0, 0)
			addresses[index] = addresses[*input.DuplicateOf]
			continue
		}
		addresses[index] = MMInputStart + uint64(len(buffer))
		account := input.Account
		buffer = append(buffer, 0xff, boolByte(account.IsSigner), boolByte(account.IsWritable), boolByte(account.Executable), 0, 0, 0, 0)
		buffer = append(buffer, account.Key[:]...)
		buffer = append(buffer, account.Owner[:]...)
		buffer = appendReferenceU64(buffer, account.Lamports)
		buffer = appendReferenceU64(buffer, uint64(len(account.Data)))
		buffer = append(buffer, account.Data...)
		buffer = append(buffer, make([]byte, MaxPermittedDataIncrease)...)
		for len(buffer)%8 != 0 {
			buffer = append(buffer, 0)
		}
		buffer = appendReferenceU64(buffer, math.MaxUint64)
	}
	buffer = appendReferenceU64(buffer, uint64(len(instructionData)))
	buffer = append(buffer, instructionData...)
	buffer = append(buffer, programID[:]...)
	if directPointers {
		for len(buffer)%8 != 0 {
			buffer = append(buffer, 0)
		}
		for _, address := range addresses {
			buffer = appendReferenceU64(buffer, address)
		}
	}
	return buffer
}

// referenceAgaveABIV1Direct independently follows Serializer::write_account's
// split-region algorithm from Agave 12b5c7e. In particular, it retains eight
// host padding bytes but maps only the bytes needed to align the following
// rent_epoch after the virtual 10 KiB realloc reservation.
func referenceAgaveABIV1Direct(programID sdk.Pubkey, accounts []InputAccount, instructionData []byte, directPointers bool) ([]byte, []InputMemoryRegion, []AccountRegion, uint64, int) {
	buffer := make([]byte, 8)
	binary.LittleEndian.PutUint64(buffer, uint64(len(accounts)))
	vaddr := MMInputStart
	regionStart := 0
	regions := make([]InputMemoryRegion, 0, len(accounts)*2+1)
	accountRegions := make([]AccountRegion, 0, len(accounts))
	pushMetadata := func() {
		if regionStart >= len(buffer) {
			regionStart = len(buffer)
			return
		}
		data := append([]byte(nil), buffer[regionStart:]...)
		regions = append(regions, InputMemoryRegion{
			VMStart: vaddr, Data: data, Writable: true,
			ReservedLength: uint64(len(data)), Kind: InputRegionMetadata,
			AccountIndex: -1, Name: "program input metadata",
		})
		vaddr += uint64(len(data))
		regionStart = len(buffer)
	}
	currentAddress := func() uint64 {
		return vaddr + uint64(len(buffer)-regionStart)
	}
	for index, input := range accounts {
		if input.DuplicateOf != nil {
			recordOffset := len(buffer)
			buffer = append(buffer, *input.DuplicateOf, 0, 0, 0, 0, 0, 0, 0)
			region := accountRegions[int(*input.DuplicateOf)]
			region.DuplicateOf = int(*input.DuplicateOf)
			region.RecordByteOffset = recordOffset
			accountRegions = append(accountRegions, region)
			continue
		}
		account := input.Account
		recordOffset := len(buffer)
		region := AccountRegion{VMAddress: currentAddress(), OriginalDataLen: len(account.Data), DuplicateOf: -1, RecordByteOffset: recordOffset}
		buffer = append(buffer, 0xff, boolByte(account.IsSigner), boolByte(account.IsWritable), boolByte(account.Executable), 0, 0, 0, 0)
		region.KeyAddress = currentAddress()
		buffer = append(buffer, account.Key[:]...)
		region.OwnerAddress = currentAddress()
		buffer = append(buffer, account.Owner[:]...)
		region.LamportsAddress = currentAddress()
		buffer = appendReferenceU64(buffer, account.Lamports)
		buffer = appendReferenceU64(buffer, uint64(len(account.Data)))
		pushMetadata()
		region.DataAddress = vaddr
		reserved := uint64(len(account.Data) + MaxPermittedDataIncrease)
		regions = append(regions, InputMemoryRegion{
			VMStart: vaddr, Data: append([]byte(nil), account.Data...),
			Writable:       account.IsWritable && !account.Executable && account.Owner == programID,
			Growable:       account.IsWritable && !account.Executable && account.Owner == programID,
			ReservedLength: reserved, Kind: InputRegionAccountData,
			AccountIndex: index, Name: "account data",
		})
		vaddr += reserved
		padding := alignmentPadding(len(account.Data))
		buffer = append(buffer, make([]byte, BPFAlignOfU128)...)
		regionStart += BPFAlignOfU128 - padding
		buffer = appendReferenceU64(buffer, math.MaxUint64)
		accountRegions = append(accountRegions, region)
	}
	buffer = appendReferenceU64(buffer, uint64(len(instructionData)))
	instructionAddress := currentAddress()
	buffer = append(buffer, instructionData...)
	buffer = append(buffer, programID[:]...)
	pointersOffset := -1
	if directPointers {
		buffer = append(buffer, make([]byte, alignmentPadding(len(buffer)))...)
		pointersOffset = len(buffer)
		for _, region := range accountRegions {
			buffer = appendReferenceU64(buffer, region.VMAddress)
		}
	}
	pushMetadata()
	return buffer, regions, accountRegions, instructionAddress, pointersOffset
}

func assertInputRegionsEqual(t *testing.T, got, want []InputMemoryRegion) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("input region count mismatch: got %d want %d", len(got), len(want))
	}
	for index := range got {
		if got[index].VMStart != want[index].VMStart || got[index].Writable != want[index].Writable || got[index].ReservedLength != want[index].ReservedLength || got[index].Growable != want[index].Growable || got[index].Kind != want[index].Kind || got[index].AccountIndex != want[index].AccountIndex || !bytes.Equal(got[index].Data, want[index].Data) {
			t.Fatalf("input region %d mismatch:\n got %#v data=%x\nwant %#v data=%x", index, got[index], got[index].Data, want[index], want[index].Data)
		}
	}
}

func equalAccountRegions(one, two []AccountRegion) bool {
	if len(one) != len(two) {
		return false
	}
	for index := range one {
		if one[index] != two[index] {
			return false
		}
	}
	return true
}

func appendReferenceU64(buffer []byte, value uint64) []byte {
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], value)
	return append(buffer, encoded[:]...)
}

func sequentialPubkey(start byte) sdk.Pubkey {
	var key sdk.Pubkey
	for index := range key {
		key[index] = start + byte(index)
	}
	return key
}

func firstDifference(one, two []byte) int {
	limit := len(one)
	if len(two) < limit {
		limit = len(two)
	}
	for index := 0; index < limit; index++ {
		if one[index] != two[index] {
			return index
		}
	}
	return limit
}

func isProgramErrorKind(err error, kind ProgramErrorKind) bool {
	typed, ok := err.(ProgramError)
	return ok && !typed.Custom && typed.Kind == kind
}
