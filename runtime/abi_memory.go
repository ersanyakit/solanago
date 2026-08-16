package runtime

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/ersany/go-solana/sdk"
)

// MemoryMap builds the split sBPF mapping emitted by direct account-data mode.
func (input *SerializedInput) MemoryMap() (*MemoryMap, error) {
	if input == nil || !input.directMapping {
		return nil, ErrInvalidMemoryRegion
	}
	if input.memory != nil {
		return input.memory, nil
	}
	regions := make([]MemoryRegion, 0, len(input.mappedSources))
	for _, source := range input.mappedSources {
		mapped := source.snapshot()
		regions = append(regions, MemoryRegion{
			VMStart: mapped.VMStart, Data: mapped.Data, Writable: mapped.Writable,
			ReservedLength: mapped.ReservedLength, Growable: mapped.Growable, Name: mapped.Name,
		})
	}
	memory, err := NewMemoryMap(regions...)
	if err != nil {
		return nil, err
	}
	input.memory = memory
	if input.context != nil {
		input.context.memory = memory
		input.context.attachMappedAccounts(memory)
	}
	return memory, nil
}

// ParseMappedInputV1 independently parses the split-region serializer output
// through virtual-address translation, rather than trusting its prebuilt
// account views.
func ParseMappedInputV1(input *SerializedInput, options ParseOptions) (*Context, error) {
	memory, err := input.MemoryMap()
	if err != nil {
		return nil, err
	}
	context, err := ParseInputV1Memory(memory, options)
	if err != nil {
		return nil, err
	}
	for index := range input.mappedSources {
		source := &input.mappedSources[index]
		if source.kind == InputRegionAccountData && source.accountIndex >= 0 && source.accountIndex < len(context.accountSlots) {
			source.account = context.accountSlots[source.accountIndex]
		}
	}
	context.raw = input.Buffer
	context.mappedSources = input.mappedSources
	input.context = context
	return context, nil
}

// ParseInputV1Memory is the aligned SDK parser over a real sBPF MemoryMap. It
// advances across the reserved, intentionally unmapped realloc gaps without
// manufacturing Go slices for them.
func ParseInputV1Memory(memory *MemoryMap, options ParseOptions) (*Context, error) {
	cursor := vmABICursor{memory: memory, address: MMInputStart}
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
		recordAddress := cursor.address
		marker, err := cursor.u8()
		if err != nil {
			return nil, accountABIError(index, err)
		}
		if marker != NonDuplicateMarker {
			if int(marker) >= index {
				return nil, fmt.Errorf("%w at slot %d: target %d", ErrInvalidDuplicate, index, marker)
			}
			if _, err := cursor.take(7, AccessRead, 1); err != nil {
				return nil, accountABIError(index, err)
			}
			accounts = append(accounts, accounts[int(marker)])
			region := regions[int(marker)]
			region.DuplicateOf = int(marker)
			region.RecordByteOffset = -1
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
		originalLenAddress := cursor.address
		if _, err := cursor.take(4, AccessRead, 1); err != nil {
			return nil, accountABIError(index, err)
		}
		keyAddress := cursor.address
		keyBytes, err := cursor.take(32, AccessRead, 1)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		ownerAddress := cursor.address
		ownerBytes, err := cursor.take(32, AccessRead, 1)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		lamportsAddress := cursor.address
		lamportsBytes, err := cursor.take(8, AccessRead, 8)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		dataLenBytes, err := cursor.take(8, AccessRead, 8)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		dataLen64 := binary.LittleEndian.Uint64(dataLenBytes)
		if dataLen64 > MaxPermittedDataLength || dataLen64 > math.MaxUint32 {
			return nil, fmt.Errorf("%w at slot %d: data length %d", ErrInvalidABI, index, dataLen64)
		}
		originalLenBytes, err := memory.Translate(originalLenAddress, 4, AccessWrite, 1)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		binary.LittleEndian.PutUint32(originalLenBytes, uint32(dataLen64))
		dataAddress := cursor.address
		data, err := cursor.take(dataLen64, AccessRead, 1)
		if err != nil {
			return nil, accountABIError(index, err)
		}
		if err := cursor.skip(MaxPermittedDataIncrease + uint64(alignmentPadding(int(dataLen64)))); err != nil {
			return nil, accountABIError(index, err)
		}
		if _, err := cursor.u64(); err != nil {
			return nil, accountABIError(index, err)
		}
		var key, owner sdk.Pubkey
		copy(key[:], keyBytes)
		copy(owner[:], ownerBytes)
		account := &AccountView{
			key: key, owner: owner, lamports: binary.LittleEndian.Uint64(lamportsBytes),
			data: data, dataLen: int(dataLen64), originalLen: int(dataLen64),
			isSigner: isSigner, isWritable: isWritable, executable: executable,
			ownerOffset: -1, lamportsOffset: -1, dataLenOffset: -1,
			ownerStorage: ownerBytes, lamportsStorage: lamportsBytes, dataLenStorage: dataLenBytes,
		}
		if storage, growable := memory.growableStorage(dataAddress, dataLen64); growable {
			account.data = storage
			account.memory = memory
			account.dataAddress = dataAddress
		}
		account.acceptBaseline()
		accounts = append(accounts, account)
		regions = append(regions, AccountRegion{
			VMAddress: recordAddress, KeyAddress: keyAddress, OwnerAddress: ownerAddress,
			LamportsAddress: lamportsAddress, DataAddress: dataAddress,
			OriginalDataLen: int(dataLen64), DuplicateOf: -1, RecordByteOffset: -1,
		})
	}

	instructionLen, err := cursor.u64()
	if err != nil {
		return nil, err
	}
	if instructionLen > MaxInstructionDataLen {
		return nil, fmt.Errorf("%w: got %d, maximum %d", ErrInstructionTooLarge, instructionLen, MaxInstructionDataLen)
	}
	instructionData, err := cursor.take(instructionLen, AccessRead, 1)
	if err != nil {
		return nil, err
	}
	programBytes, err := cursor.take(32, AccessRead, 1)
	if err != nil {
		return nil, err
	}
	var programID sdk.Pubkey
	copy(programID[:], programBytes)
	if options.DirectAccountPointers {
		padding := (BPFAlignOfU128 - int(cursor.address%BPFAlignOfU128)) % BPFAlignOfU128
		if _, err := cursor.take(uint64(padding), AccessRead, 1); err != nil {
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
	return &Context{
		ProgramID: programID, Accounts: append([]*AccountView(nil), accounts...),
		InstructionData: append([]byte(nil), instructionData...),
		programID:       programID, accountSlots: accounts, regions: regions, memory: memory,
	}, nil
}

type vmABICursor struct {
	memory  *MemoryMap
	address uint64
}

func (cursor *vmABICursor) take(length uint64, access AccessType, alignment uint64) ([]byte, error) {
	data, err := translateBytes(cursor.memory, cursor.address, length, access)
	if err != nil {
		return nil, err
	}
	if alignment > 1 && cursor.address%alignment != 0 {
		return nil, ErrUnalignedPointer
	}
	if err := cursor.skip(length); err != nil {
		return nil, err
	}
	return data, nil
}

func (cursor *vmABICursor) skip(length uint64) error {
	if length > math.MaxUint64-cursor.address {
		return ErrInvalidLength
	}
	cursor.address += length
	if cursor.address < MMInputStart || cursor.address-MMInputStart > uint64(1)<<32 {
		return ErrAccessViolation
	}
	return nil
}

func (cursor *vmABICursor) u8() (uint8, error) {
	data, err := cursor.take(1, AccessRead, 1)
	if err != nil {
		return 0, err
	}
	return data[0], nil
}

func (cursor *vmABICursor) boolean(strict bool) (bool, error) {
	value, err := cursor.u8()
	if err != nil {
		return false, err
	}
	if strict && value > 1 {
		return false, fmt.Errorf("%w: non-canonical boolean %d", ErrInvalidABI, value)
	}
	return value != 0, nil
}

func (cursor *vmABICursor) u64() (uint64, error) {
	data, err := cursor.take(8, AccessRead, 8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(data), nil
}
