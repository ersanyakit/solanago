package runtime

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/ersanyakit/solanago/sbpf"
	"github.com/ersanyakit/solanago/sdk"
)

// serializeInputV1DirectMapping reproduces Agave's
// virtual_address_space_adjustments=true + account_data_direct_mapping=true
// branch. Metadata remains in the aligned host buffer while account data gets
// independent VM regions and a 10 KiB address reservation.
func serializeInputV1DirectMapping(programID sdk.Pubkey, accounts []InputAccount, instructionData []byte, options SerializeOptions) (*SerializedInput, error) {
	if len(accounts) > MaxAccountsPerInstruction {
		return nil, fmt.Errorf("%w: got %d, maximum %d", ErrTooManyAccounts, len(accounts), MaxAccountsPerInstruction)
	}
	if len(instructionData) > MaxInstructionDataLen {
		return nil, fmt.Errorf("%w: got %d, maximum %d", ErrInstructionTooLarge, len(instructionData), MaxInstructionDataLen)
	}
	hostSize := 8
	virtualSize := uint64(8)
	for index, input := range accounts {
		if input.DuplicateOf != nil {
			if input.Account != nil || int(*input.DuplicateOf) >= index {
				return nil, fmt.Errorf("%w at slot %d: target %d", ErrInvalidDuplicate, index, *input.DuplicateOf)
			}
			hostSize += 8
			virtualSize += 8
			continue
		}
		if input.Account == nil {
			return nil, fmt.Errorf("%w at slot %d: neither account nor duplicate", ErrInvalidABI, index)
		}
		if len(input.Account.Data) > MaxPermittedDataLength {
			return nil, fmt.Errorf("%w at slot %d: data length %d", ErrInvalidABI, index, len(input.Account.Data))
		}
		var ok bool
		hostSize, ok = checkedAddInt(hostSize, 104) // 96-byte record + 8 host-alignment bytes
		if !ok {
			return nil, ErrInvalidABI
		}
		reserved := uint64(len(input.Account.Data) + MaxPermittedDataIncrease)
		// VM record: 88-byte prefix, reserved data span, data alignment, rent.
		addition := uint64(96 + alignmentPadding(len(input.Account.Data)))
		if reserved > math.MaxUint64-addition || virtualSize > math.MaxUint64-(reserved+addition) {
			return nil, ErrInvalidABI
		}
		virtualSize += reserved + addition
	}
	var ok bool
	hostSize, ok = checkedAddInt(hostSize, 8+len(instructionData)+32)
	if !ok {
		return nil, ErrInvalidABI
	}
	virtualTail := uint64(8 + len(instructionData) + 32)
	if virtualSize > math.MaxUint64-virtualTail {
		return nil, ErrInvalidABI
	}
	virtualSize += virtualTail
	pointerPadding := 0
	if options.DirectAccountPointers {
		pointerPadding = alignmentPadding(hostSize)
		pointerBytes, multiplyOK := checkedMulInt(len(accounts), 8)
		if !multiplyOK {
			return nil, ErrInvalidABI
		}
		hostSize, ok = checkedAddInt(hostSize, pointerPadding+pointerBytes)
		if !ok {
			return nil, ErrInvalidABI
		}
		virtualSize += uint64(pointerPadding + pointerBytes)
	}
	if virtualSize > sbpf.MMRegionSize {
		return nil, fmt.Errorf("%w: mapped input spans %d bytes, region maximum %d", ErrInvalidABI, virtualSize, sbpf.MMRegionSize)
	}

	serializer := directABISerializer{
		buffer:  make([]byte, 0, hostSize),
		vaddr:   MMInputStart,
		sources: make([]mappedRegionSource, 0, len(accounts)*2+1),
	}
	serializer.writeU64(uint64(len(accounts)))
	accountRegions := make([]AccountRegion, 0, len(accounts))
	views := make([]*AccountView, 0, len(accounts))
	for index, input := range accounts {
		if input.DuplicateOf != nil {
			serializer.writeByte(*input.DuplicateOf)
			serializer.writeZeros(7)
			region := accountRegions[int(*input.DuplicateOf)]
			region.DuplicateOf = int(*input.DuplicateOf)
			region.RecordByteOffset = len(serializer.buffer) - 8
			accountRegions = append(accountRegions, region)
			views = append(views, views[int(*input.DuplicateOf)])
			continue
		}

		account := input.Account
		view := newAccountView(*account)
		recordOffset := len(serializer.buffer)
		region := AccountRegion{
			VMAddress: serializer.currentAddress(), OriginalDataLen: len(account.Data),
			DuplicateOf: -1, RecordByteOffset: recordOffset,
		}
		serializer.writeByte(NonDuplicateMarker)
		serializer.writeByte(boolByte(account.IsSigner))
		serializer.writeByte(boolByte(account.IsWritable))
		serializer.writeByte(boolByte(account.Executable))
		serializer.writeZeros(4)
		region.KeyAddress = serializer.currentAddress()
		serializer.writeBytes(account.Key[:])
		ownerOffset := len(serializer.buffer)
		region.OwnerAddress = serializer.currentAddress()
		serializer.writeBytes(account.Owner[:])
		lamportsOffset := len(serializer.buffer)
		region.LamportsAddress = serializer.currentAddress()
		serializer.writeU64(account.Lamports)
		dataLenOffset := len(serializer.buffer)
		serializer.writeU64(uint64(len(account.Data)))

		serializer.pushMetadataRegion()
		region.DataAddress = serializer.vaddr
		reserved := uint64(len(account.Data) + MaxPermittedDataIncrease)
		serializer.sources = append(serializer.sources, mappedRegionSource{
			VMStart: serializer.vaddr, account: view,
			writable:       account.IsWritable && !account.Executable && account.Owner == programID,
			growable:       account.IsWritable && !account.Executable && account.Owner == programID,
			reservedLength: reserved, kind: InputRegionAccountData,
			accountIndex: index, name: fmt.Sprintf("account %d data", index),
		})
		serializer.vaddr += reserved
		padding := alignmentPadding(len(account.Data))
		serializer.writeZeros(BPFAlignOfU128)
		serializer.regionStart += BPFAlignOfU128 - padding
		serializer.writeU64(math.MaxUint64)

		view.raw = serializer.buffer
		view.ownerOffset, view.lamportsOffset, view.dataLenOffset = ownerOffset, lamportsOffset, dataLenOffset
		// These slices remain valid because the exact capacity was preallocated.
		view.ownerStorage = serializer.buffer[ownerOffset : ownerOffset+32]
		view.lamportsStorage = serializer.buffer[lamportsOffset : lamportsOffset+8]
		view.dataLenStorage = serializer.buffer[dataLenOffset : dataLenOffset+8]
		accountRegions = append(accountRegions, region)
		views = append(views, view)
	}

	serializer.writeU64(uint64(len(instructionData)))
	instructionOffset := len(serializer.buffer)
	instructionAddress := serializer.currentAddress()
	serializer.writeBytes(instructionData)
	serializer.writeBytes(programID[:])
	pointersOffset := -1
	if options.DirectAccountPointers {
		serializer.writeZeros(pointerPadding)
		pointersOffset = len(serializer.buffer)
		for _, region := range accountRegions {
			serializer.writeU64(region.VMAddress)
		}
	}
	serializer.pushMetadataRegion()
	if len(serializer.buffer) != hostSize {
		return nil, fmt.Errorf("%w: direct mapping host size %d != %d", ErrInvalidABI, len(serializer.buffer), hostSize)
	}

	context := &Context{
		ProgramID: programID, Accounts: append([]*AccountView(nil), views...),
		InstructionData: append([]byte(nil), instructionData...),
		programID:       programID, accountSlots: views,
		regions: accountRegions, raw: serializer.buffer,
		mappedSources: serializer.sources,
	}
	result := &SerializedInput{
		Buffer: serializer.buffer, AccountRegions: accountRegions,
		InstructionDataOffset: instructionOffset, InstructionDataAddress: instructionAddress,
		DirectAccountPointersOffset: pointersOffset,
		directMapping:               true, mappedSources: serializer.sources, context: context,
	}
	return result, nil
}

type directABISerializer struct {
	buffer      []byte
	sources     []mappedRegionSource
	vaddr       uint64
	regionStart int
}

func (serializer *directABISerializer) currentAddress() uint64 {
	return serializer.vaddr + uint64(len(serializer.buffer)-serializer.regionStart)
}

func (serializer *directABISerializer) writeByte(value byte) {
	serializer.buffer = append(serializer.buffer, value)
}

func (serializer *directABISerializer) writeBytes(value []byte) {
	serializer.buffer = append(serializer.buffer, value...)
}

func (serializer *directABISerializer) writeZeros(length int) {
	serializer.buffer = append(serializer.buffer, make([]byte, length)...)
}

func (serializer *directABISerializer) writeU64(value uint64) {
	start := len(serializer.buffer)
	serializer.writeZeros(8)
	binary.LittleEndian.PutUint64(serializer.buffer[start:start+8], value)
}

func (serializer *directABISerializer) pushMetadataRegion() {
	if serializer.regionStart >= len(serializer.buffer) {
		serializer.regionStart = len(serializer.buffer)
		return
	}
	data := serializer.buffer[serializer.regionStart:len(serializer.buffer)]
	serializer.sources = append(serializer.sources, mappedRegionSource{
		VMStart: serializer.vaddr, data: data, writable: true,
		reservedLength: uint64(len(data)), kind: InputRegionMetadata,
		accountIndex: -1, name: "program input metadata",
	})
	serializer.vaddr += uint64(len(data))
	serializer.regionStart = len(serializer.buffer)
}
