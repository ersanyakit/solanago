package vm

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/ersanyakit/solanago/sbpf"
)

var (
	ErrReadOnlyMemory    = errors.New("write to read-only sBPF memory")
	ErrInvalidMemoryMap  = errors.New("invalid sBPF memory map")
	ErrOverlappingMemory = errors.New("overlapping sBPF memory regions")
)

// MemoryPermissions describes VM-visible permissions. The backing slice is a
// host implementation detail; programs see only VMAddress and can never
// recover a Go pointer or interact with Go heap/GC metadata.
type MemoryPermissions uint8

const (
	MemoryRead MemoryPermissions = 1 << iota
	MemoryWrite

	MemoryReadOnly  = MemoryRead
	MemoryReadWrite = MemoryRead | MemoryWrite
)

// MemoryRegion maps one byte slice at an explicit sBPF virtual address.
// Regions may reside in bytecode/rodata, heap, or input address space, but the
// stack region is owned exclusively by the VM.
type MemoryRegion struct {
	VMAddress  uint64
	Data       []byte
	Permission MemoryPermissions
	Name       string
}

func ReadOnlyRegion(address uint64, data []byte) MemoryRegion {
	return MemoryRegion{VMAddress: address, Data: data, Permission: MemoryReadOnly}
}

func WritableRegion(address uint64, data []byte) MemoryRegion {
	return MemoryRegion{VMAddress: address, Data: data, Permission: MemoryReadWrite}
}

func validateMemoryRegions(regions []MemoryRegion) ([]MemoryRegion, error) {
	validated := append([]MemoryRegion(nil), regions...)
	for index, region := range validated {
		if len(region.Data) == 0 {
			return nil, fmt.Errorf("%w: region %d is empty", ErrInvalidMemoryMap, index)
		}
		if region.Permission != MemoryReadOnly && region.Permission != MemoryReadWrite {
			return nil, fmt.Errorf("%w: region %d has permissions %#x", ErrInvalidMemoryMap, index, region.Permission)
		}
		length := uint64(len(region.Data))
		if region.VMAddress > math.MaxUint64-length {
			return nil, fmt.Errorf("%w: region %d address range overflows", ErrInvalidMemoryMap, index)
		}
		last := region.VMAddress + length - 1
		if region.VMAddress/sbpf.MMRegionSize != last/sbpf.MMRegionSize {
			return nil, fmt.Errorf("%w: region %d crosses a 4GiB sBPF region boundary", ErrInvalidMemoryMap, index)
		}
		if region.VMAddress/sbpf.MMRegionSize == sbpf.MMStackStart/sbpf.MMRegionSize {
			return nil, fmt.Errorf("%w: region %d overlaps the VM-owned stack address space", ErrInvalidMemoryMap, index)
		}
	}
	sort.Slice(validated, func(i, j int) bool { return validated[i].VMAddress < validated[j].VMAddress })
	for index := 1; index < len(validated); index++ {
		previous := validated[index-1]
		if validated[index].VMAddress < previous.VMAddress+uint64(len(previous.Data)) {
			return nil, fmt.Errorf("%w: regions at %#x and %#x", ErrOverlappingMemory,
				previous.VMAddress, validated[index].VMAddress)
		}
	}
	return validated, nil
}

func (state *runState) memoryRange(address, width uint64, write bool) ([]byte, error) {
	if width == 0 || address > math.MaxUint64-width {
		return nil, fmt.Errorf("%w: address %#x width %d overflows", ErrInvalidMemory, address, width)
	}
	if address >= sbpf.MMStackStart {
		offset := address - sbpf.MMStackStart
		if offset <= uint64(len(state.stack)) && width <= uint64(len(state.stack))-offset {
			return state.stack[offset : offset+width], nil
		}
	}
	for index := range state.regions {
		region := &state.regions[index]
		if address < region.VMAddress {
			break
		}
		offset := address - region.VMAddress
		if offset > uint64(len(region.Data)) || width > uint64(len(region.Data))-offset {
			continue
		}
		if region.Permission&MemoryRead == 0 {
			return nil, fmt.Errorf("%w: region %q at %#x", ErrInvalidMemory, region.Name, region.VMAddress)
		}
		if write && region.Permission&MemoryWrite == 0 {
			return nil, fmt.Errorf("%w: region %q address %#x", ErrReadOnlyMemory, region.Name, address)
		}
		return region.Data[offset : offset+width], nil
	}
	operation := "load"
	if write {
		operation = "store"
	}
	return nil, fmt.Errorf("%w: %s address %#x width %d is unmapped", ErrInvalidMemory, operation, address, width)
}
