package runtime

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/ersany/go-solana/sbpf"
	"github.com/ersany/go-solana/sdk"
)

const (
	// MMInputStart is the real sBPF virtual address of program input.
	MMInputStart uint64 = sbpf.MMInputStart
	// MaxAccountsPerInstruction follows Agave's u8 duplicate-index boundary.
	MaxAccountsPerInstruction = 255
	// MaxInstructionDataLen is the current Agave CPI/top-level instruction cap.
	MaxInstructionDataLen = 10 * 1024
	// NonDuplicateMarker identifies a full account record.
	NonDuplicateMarker = uint8(0xff)
	// BPFAlignOfU128 is 8 for SBF even when a host's u128 alignment differs.
	BPFAlignOfU128 = 8
)

var (
	ErrInvalidABI          = errors.New("runtime: invalid Solana program ABI")
	ErrTooManyAccounts     = errors.New("runtime: too many instruction accounts")
	ErrInstructionTooLarge = errors.New("runtime: instruction data too large")
	ErrInvalidDuplicate    = errors.New("runtime: invalid duplicate account index")
)

// AccountRegion describes one serialized account slot using sBPF virtual
// addresses, never Go host pointers. Duplicate slots copy the first slot's
// addresses exactly, matching Agave's SerializedAccountMetadata.
type AccountRegion struct {
	VMAddress        uint64
	KeyAddress       uint64
	OwnerAddress     uint64
	LamportsAddress  uint64
	DataAddress      uint64
	OriginalDataLen  int
	DuplicateOf      int
	RecordByteOffset int
}

// SerializeOptions selects current feature-gated ABIv1 additions.
type SerializeOptions struct {
	// DirectAccountPointers appends SIMD-0449's aligned account record pointer
	// array after program_id. SDK deserialize ignores this appendix.
	DirectAccountPointers bool
	// AccountDataDirectMapping enables Agave's split-region mode: account
	// data is mapped independently and its original length plus 10 KiB is
	// reserved in VM address space. It implies virtual-address adjustments.
	AccountDataDirectMapping bool
}

// SerializedInput is the canonical aligned ABIv1 byte buffer and exact VM
// metadata used to build memory regions.
type SerializedInput struct {
	Buffer                      []byte
	AccountRegions              []AccountRegion
	InstructionDataOffset       int
	DirectAccountPointersOffset int
	InstructionDataAddress      uint64
	directMapping               bool
	mappedSources               []mappedRegionSource
	context                     *Context
	memory                      *MemoryMap
}

// InputMemoryRegionKind identifies the source of one split guest region.
type InputMemoryRegionKind uint8

const (
	InputRegionMetadata InputMemoryRegionKind = iota
	InputRegionAccountData
)

// InputMemoryRegion describes one real sBPF mapping. ReservedLength can exceed
// len(Data) for direct account data; the difference is Agave's realloc address
// reservation and is intentionally not a Go slice or host pointer.
type InputMemoryRegion struct {
	VMStart        uint64
	Data           []byte
	Writable       bool
	ReservedLength uint64
	Growable       bool
	Kind           InputMemoryRegionKind
	AccountIndex   int
	Name           string
}

type mappedRegionSource struct {
	VMStart        uint64
	data           []byte
	account        *AccountView
	writable       bool
	reservedLength uint64
	growable       bool
	kind           InputMemoryRegionKind
	accountIndex   int
	name           string
}

func (source mappedRegionSource) snapshot() InputMemoryRegion {
	data := source.data
	if source.account != nil {
		data = source.account.data[:source.account.dataLen]
	}
	return InputMemoryRegion{
		VMStart: source.VMStart, Data: data, Writable: source.writable,
		ReservedLength: source.reservedLength, Growable: source.growable, Kind: source.kind,
		AccountIndex: source.accountIndex, Name: source.name,
	}
}

// MemoryRegions returns live host slices paired only with explicit guest
// addresses. It is populated for AccountDataDirectMapping mode.
func (input *SerializedInput) MemoryRegions() []InputMemoryRegion {
	if input == nil {
		return nil
	}
	regions := make([]InputMemoryRegion, len(input.mappedSources))
	for index, source := range input.mappedSources {
		regions[index] = source.snapshot()
	}
	return regions
}

// MappedContext returns the verified account views created with split-region
// serialization. Contiguous callers should use ParseInputV1 on Buffer.
func (input *SerializedInput) MappedContext() (*Context, error) {
	if input == nil || !input.directMapping || input.context == nil {
		return nil, ErrInvalidABI
	}
	return input.context, nil
}

// SerializeInputV1 serializes current loader-v2/v3 aligned ABIv1.
//
// Source pin: anza-xyz/agave program-runtime/src/serialization.rs at
// 12b5c7e4df705927b2f7f579f3aa606aa4bde1c0 and anza-xyz/solana-sdk
// program-entrypoint/src/lib.rs at 7437469d1ab5bddbf665f3a1a69aefb422c33e36.
func SerializeInputV1(programID sdk.Pubkey, accounts []InputAccount, instructionData []byte) (*SerializedInput, error) {
	return SerializeInputV1WithOptions(programID, accounts, instructionData, SerializeOptions{})
}

// SerializeInputV1WithOptions is SerializeInputV1 with explicit feature gates.
func SerializeInputV1WithOptions(programID sdk.Pubkey, accounts []InputAccount, instructionData []byte, options SerializeOptions) (*SerializedInput, error) {
	if options.AccountDataDirectMapping {
		return serializeInputV1DirectMapping(programID, accounts, instructionData, options)
	}
	if len(accounts) > MaxAccountsPerInstruction {
		return nil, fmt.Errorf("%w: got %d, maximum %d", ErrTooManyAccounts, len(accounts), MaxAccountsPerInstruction)
	}
	if len(instructionData) > MaxInstructionDataLen {
		return nil, fmt.Errorf("%w: got %d, maximum %d", ErrInstructionTooLarge, len(instructionData), MaxInstructionDataLen)
	}

	// The exact size calculation both prevents repeated growth and rejects host
	// integer overflow before allocating attacker-influenced lengths.
	size := 8
	for index, input := range accounts {
		if input.DuplicateOf != nil {
			if input.Account != nil || int(*input.DuplicateOf) >= index {
				return nil, fmt.Errorf("%w at slot %d: target %d", ErrInvalidDuplicate, index, *input.DuplicateOf)
			}
			size, _ = checkedAddInt(size, 8)
			continue
		}
		if input.Account == nil {
			return nil, fmt.Errorf("%w at slot %d: neither account nor duplicate", ErrInvalidABI, index)
		}
		if len(input.Account.Data) > MaxPermittedDataLength {
			return nil, fmt.Errorf("%w at slot %d: data length %d exceeds %d", ErrInvalidABI, index, len(input.Account.Data), MaxPermittedDataLength)
		}
		accountSize, ok := checkedAddInt(96, len(input.Account.Data))
		if !ok {
			return nil, ErrInvalidABI
		}
		accountSize, ok = checkedAddInt(accountSize, MaxPermittedDataIncrease+alignmentPadding(len(input.Account.Data)))
		if !ok {
			return nil, ErrInvalidABI
		}
		size, ok = checkedAddInt(size, accountSize)
		if !ok {
			return nil, ErrInvalidABI
		}
	}
	var ok bool
	size, ok = checkedAddInt(size, 8+len(instructionData)+32)
	if !ok {
		return nil, ErrInvalidABI
	}
	pointerPadding := 0
	if options.DirectAccountPointers {
		pointerPadding = alignmentPadding(size)
		pointerBytes, multiplyOK := checkedMulInt(len(accounts), 8)
		if !multiplyOK {
			return nil, ErrInvalidABI
		}
		size, ok = checkedAddInt(size, pointerPadding+pointerBytes)
		if !ok {
			return nil, ErrInvalidABI
		}
	}

	buffer := make([]byte, 0, size)
	buffer = appendU64(buffer, uint64(len(accounts)))
	regions := make([]AccountRegion, 0, len(accounts))
	for _, input := range accounts {
		if input.DuplicateOf != nil {
			region := regions[int(*input.DuplicateOf)]
			region.DuplicateOf = int(*input.DuplicateOf)
			region.RecordByteOffset = len(buffer)
			regions = append(regions, region)
			buffer = append(buffer, *input.DuplicateOf)
			buffer = append(buffer, make([]byte, 7)...)
			continue
		}

		account := input.Account
		recordOffset := len(buffer)
		region := AccountRegion{
			VMAddress:        MMInputStart + uint64(recordOffset),
			OriginalDataLen:  len(account.Data),
			DuplicateOf:      -1,
			RecordByteOffset: recordOffset,
		}
		buffer = append(buffer, NonDuplicateMarker, boolByte(account.IsSigner), boolByte(account.IsWritable), boolByte(account.Executable))
		buffer = append(buffer, 0, 0, 0, 0) // SDK-owned original_data_len/padding
		region.KeyAddress = MMInputStart + uint64(len(buffer))
		buffer = append(buffer, account.Key[:]...)
		region.OwnerAddress = MMInputStart + uint64(len(buffer))
		buffer = append(buffer, account.Owner[:]...)
		region.LamportsAddress = MMInputStart + uint64(len(buffer))
		buffer = appendU64(buffer, account.Lamports)
		buffer = appendU64(buffer, uint64(len(account.Data)))
		region.DataAddress = MMInputStart + uint64(len(buffer))
		buffer = append(buffer, account.Data...)
		buffer = append(buffer, make([]byte, MaxPermittedDataIncrease+alignmentPadding(len(account.Data)))...)
		buffer = appendU64(buffer, math.MaxUint64) // rent_epoch is masked by Agave
		regions = append(regions, region)
	}

	buffer = appendU64(buffer, uint64(len(instructionData)))
	instructionOffset := len(buffer)
	buffer = append(buffer, instructionData...)
	buffer = append(buffer, programID[:]...)
	pointersOffset := -1
	if options.DirectAccountPointers {
		buffer = append(buffer, make([]byte, pointerPadding)...)
		pointersOffset = len(buffer)
		for _, region := range regions {
			buffer = appendU64(buffer, region.VMAddress)
		}
	}
	if len(buffer) != size {
		return nil, fmt.Errorf("%w: internal size mismatch %d != %d", ErrInvalidABI, len(buffer), size)
	}
	return &SerializedInput{
		Buffer:                      buffer,
		AccountRegions:              regions,
		InstructionDataOffset:       instructionOffset,
		DirectAccountPointersOffset: pointersOffset,
		InstructionDataAddress:      MMInputStart + uint64(instructionOffset),
	}, nil
}

// ParseOptions controls verification of feature-gated ABIv1 input.
type ParseOptions struct {
	DirectAccountPointers bool
	// RejectNonCanonicalBools adds a verifier check stricter than the SDK's
	// nonzero-is-true entrypoint parser. Canonical Agave output always passes.
	RejectNonCanonicalBools bool
}

// ParseInputV1 parses canonical ABIv1 without the SIMD-0449 appendix.
func ParseInputV1(input []byte) (*Context, error) {
	return ParseInputV1WithOptions(input, ParseOptions{})
}

// ParseInputV1WithOptions safely parses current aligned input, verifies every
// range, and writes original_data_len into the SDK-owned u32 padding field just
// as solana_program_entrypoint::deserialize does.
func ParseInputV1WithOptions(input []byte, options ParseOptions) (*Context, error) {
	cursor := abiCursor{data: input}
	numAccounts64, err := cursor.u64()
	if err != nil {
		return nil, err
	}
	if numAccounts64 > MaxAccountsPerInstruction {
		return nil, fmt.Errorf("%w: got %d, maximum %d", ErrTooManyAccounts, numAccounts64, MaxAccountsPerInstruction)
	}
	numAccounts := int(numAccounts64)
	accounts := make([]*AccountView, 0, numAccounts)
	regions := make([]AccountRegion, 0, numAccounts)
	for index := 0; index < numAccounts; index++ {
		recordOffset := cursor.offset
		marker, markerErr := cursor.u8()
		if markerErr != nil {
			return nil, accountABIError(index, markerErr)
		}
		if marker != NonDuplicateMarker {
			if int(marker) >= index {
				return nil, fmt.Errorf("%w at slot %d: target %d", ErrInvalidDuplicate, index, marker)
			}
			if _, err := cursor.take(7); err != nil {
				return nil, accountABIError(index, err)
			}
			accounts = append(accounts, accounts[int(marker)])
			region := regions[int(marker)]
			region.DuplicateOf = int(marker)
			region.RecordByteOffset = recordOffset
			regions = append(regions, region)
			continue
		}

		isSigner, err := cursor.boolean(options.RejectNonCanonicalBools)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		isWritable, err := cursor.boolean(options.RejectNonCanonicalBools)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		executable, err := cursor.boolean(options.RejectNonCanonicalBools)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		originalLenOffset := cursor.offset
		if _, err := cursor.take(4); err != nil {
			return nil, accountABIError(index, err)
		}
		keyOffset := cursor.offset
		keyBytes, err := cursor.take(32)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		ownerOffset := cursor.offset
		ownerBytes, err := cursor.take(32)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		lamportsOffset := cursor.offset
		lamports, err := cursor.u64()
		if err != nil {
			return nil, accountABIError(index, err)
		}
		dataLenOffset := cursor.offset
		dataLen64, err := cursor.u64()
		if err != nil {
			return nil, accountABIError(index, err)
		}
		if dataLen64 > MaxPermittedDataLength || dataLen64 > math.MaxUint32 {
			return nil, fmt.Errorf("%w at slot %d: data length %d", ErrInvalidABI, index, dataLen64)
		}
		dataLen := int(dataLen64)
		binary.LittleEndian.PutUint32(input[originalLenOffset:originalLenOffset+4], uint32(dataLen))
		dataOffset := cursor.offset
		dataCapacity, ok := checkedAddInt(dataLen, MaxPermittedDataIncrease)
		if !ok {
			return nil, accountABIError(index, ErrInvalidABI)
		}
		data, err := cursor.take(dataCapacity)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		if _, err := cursor.take(alignmentPadding(dataLen)); err != nil {
			return nil, accountABIError(index, err)
		}
		if _, err := cursor.u64(); err != nil { // masked rent_epoch, semantically unused
			return nil, accountABIError(index, err)
		}
		var key, owner sdk.Pubkey
		copy(key[:], keyBytes)
		copy(owner[:], ownerBytes)
		account := &AccountView{
			key: key, owner: owner, lamports: lamports,
			data: data, dataLen: dataLen, originalLen: dataLen,
			isSigner: isSigner, isWritable: isWritable, executable: executable,
			raw: input, ownerOffset: ownerOffset, lamportsOffset: lamportsOffset, dataLenOffset: dataLenOffset,
			ownerStorage: input[ownerOffset : ownerOffset+32], lamportsStorage: input[lamportsOffset : lamportsOffset+8],
			dataLenStorage: input[dataLenOffset : dataLenOffset+8],
		}
		account.acceptBaseline()
		accounts = append(accounts, account)
		regions = append(regions, AccountRegion{
			VMAddress: MMInputStart + uint64(recordOffset), KeyAddress: MMInputStart + uint64(keyOffset),
			OwnerAddress: MMInputStart + uint64(ownerOffset), LamportsAddress: MMInputStart + uint64(lamportsOffset),
			DataAddress: MMInputStart + uint64(dataOffset), OriginalDataLen: dataLen,
			DuplicateOf: -1, RecordByteOffset: recordOffset,
		})
	}

	instructionLen64, err := cursor.u64()
	if err != nil {
		return nil, err
	}
	if instructionLen64 > MaxInstructionDataLen {
		return nil, fmt.Errorf("%w: got %d, maximum %d", ErrInstructionTooLarge, instructionLen64, MaxInstructionDataLen)
	}
	instructionData, err := cursor.take(int(instructionLen64))
	if err != nil {
		return nil, err
	}
	programBytes, err := cursor.take(32)
	if err != nil {
		return nil, err
	}
	var programID sdk.Pubkey
	copy(programID[:], programBytes)
	if options.DirectAccountPointers {
		if _, err := cursor.take(alignmentPadding(cursor.offset)); err != nil {
			return nil, err
		}
		for index, region := range regions {
			pointer, err := cursor.u64()
			if err != nil {
				return nil, err
			}
			if pointer != region.VMAddress {
				return nil, fmt.Errorf("%w: account pointer %d is %#x, expected %#x", ErrInvalidABI, index, pointer, region.VMAddress)
			}
		}
	}
	if cursor.offset != len(input) {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrInvalidABI, len(input)-cursor.offset)
	}
	return &Context{
		ProgramID: programID, Accounts: accounts,
		InstructionData: append([]byte(nil), instructionData...),
		programID:       programID, accountSlots: append([]*AccountView(nil), accounts...),
		regions: regions, raw: input,
	}, nil
}

type abiCursor struct {
	data   []byte
	offset int
}

func (c *abiCursor) take(size int) ([]byte, error) {
	if size < 0 || c.offset > len(c.data) || size > len(c.data)-c.offset {
		return nil, fmt.Errorf("%w at offset %d: need %d, have %d", ErrInvalidABI, c.offset, size, len(c.data)-c.offset)
	}
	start := c.offset
	c.offset += size
	return c.data[start:c.offset], nil
}

func (c *abiCursor) u8() (uint8, error) {
	value, err := c.take(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (c *abiCursor) boolean(strict bool) (bool, error) {
	value, err := c.u8()
	if err != nil {
		return false, err
	}
	if strict && value > 1 {
		return false, fmt.Errorf("%w: non-canonical boolean %d", ErrInvalidABI, value)
	}
	return value != 0, nil
}

func (c *abiCursor) u64() (uint64, error) {
	value, err := c.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(value), nil
}

func appendU64(buffer []byte, value uint64) []byte {
	start := len(buffer)
	buffer = append(buffer, make([]byte, 8)...)
	binary.LittleEndian.PutUint64(buffer[start:], value)
	return buffer
}

func boolByte(value bool) byte {
	if value {
		return 1
	}
	return 0
}

func alignmentPadding(offset int) int {
	return (BPFAlignOfU128 - offset%BPFAlignOfU128) % BPFAlignOfU128
}

func checkedAddInt(one, two int) (int, bool) {
	if one < 0 || two < 0 || one > math.MaxInt-two {
		return 0, false
	}
	return one + two, true
}

func checkedMulInt(one, two int) (int, bool) {
	if one < 0 || two < 0 || (one != 0 && two > math.MaxInt/one) {
		return 0, false
	}
	return one * two, true
}

func accountABIError(index int, err error) error {
	return fmt.Errorf("account slot %d: %w", index, err)
}
