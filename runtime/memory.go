package runtime

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/ersanyakit/solanago/sbpf"
)

var (
	ErrInvalidMemoryRegion = errors.New("runtime: invalid memory region")
	ErrMemoryOverlap       = errors.New("runtime: overlapping memory regions")
	ErrAccessViolation     = errors.New("runtime: sBPF memory access violation")
	ErrUnalignedPointer    = errors.New("runtime: unaligned sBPF pointer")
	ErrInvalidLength       = errors.New("runtime: invalid memory length")
)

// AccessType is the permission requested by a virtual-address translation.
type AccessType uint8

const (
	AccessRead AccessType = iota
	AccessWrite
)

// MemoryRegion binds an explicit sBPF virtual range to bytes owned by the
// embedding runtime. VMStart is the only address visible to programs; Go host
// pointers are never part of this API. ReservedLength defaults to len(Data)
// and may be larger for Agave's account realloc address-space reservation.
// Growable permits ResizeRegion to expose additional bytes from Data's backing
// capacity, but never beyond ReservedLength.
type MemoryRegion struct {
	VMStart        uint64
	Data           []byte
	Writable       bool
	ReservedLength uint64
	Growable       bool
	Name           string
}

func (r MemoryRegion) end() (uint64, bool) {
	if uint64(len(r.Data)) > math.MaxUint64-r.VMStart {
		return 0, false
	}
	return r.VMStart + uint64(len(r.Data)), true
}

func (r MemoryRegion) reservationLength() uint64 {
	if r.ReservedLength == 0 {
		return uint64(len(r.Data))
	}
	return r.ReservedLength
}

func (r MemoryRegion) reservedEnd() (uint64, bool) {
	length := r.reservationLength()
	if length > math.MaxUint64-r.VMStart {
		return 0, false
	}
	return r.VMStart + length, true
}

// MemoryMap has an immutable virtual reservation table. Region bytes remain
// live and Growable mappings may change their mapped length within that fixed
// reservation.
type MemoryMap struct {
	regions []MemoryRegion
}

// NewMemoryMap validates, copies, and sorts a region table.
func NewMemoryMap(regions ...MemoryRegion) (*MemoryMap, error) {
	copyRegions := append([]MemoryRegion(nil), regions...)
	for index, region := range copyRegions {
		reservedLength := region.reservationLength()
		if reservedLength == 0 {
			return nil, fmt.Errorf("%w at index %d: empty reservation", ErrInvalidMemoryRegion, index)
		}
		if reservedLength < uint64(len(region.Data)) {
			return nil, fmt.Errorf("%w at index %d: reservation %d is smaller than mapped length %d", ErrInvalidMemoryRegion, index, reservedLength, len(region.Data))
		}
		if region.Growable && (!region.Writable || reservedLength > uint64(cap(region.Data))) {
			return nil, fmt.Errorf("%w at index %d: invalid growable backing", ErrInvalidMemoryRegion, index)
		}
		reservedEnd, ok := region.reservedEnd()
		if !ok {
			return nil, fmt.Errorf("%w at index %d: address overflow", ErrInvalidMemoryRegion, index)
		}
		if region.VMStart/sbpf.MMRegionSize != (reservedEnd-1)/sbpf.MMRegionSize {
			return nil, fmt.Errorf("%w at index %d: crosses a 4GiB sBPF region boundary", ErrInvalidMemoryRegion, index)
		}
		copyRegions[index].ReservedLength = reservedLength
	}
	sort.Slice(copyRegions, func(i, j int) bool { return copyRegions[i].VMStart < copyRegions[j].VMStart })
	for index := 1; index < len(copyRegions); index++ {
		previousEnd, _ := copyRegions[index-1].reservedEnd()
		if copyRegions[index].VMStart < previousEnd {
			return nil, fmt.Errorf("%w: %q and %q", ErrMemoryOverlap, copyRegions[index-1].Name, copyRegions[index].Name)
		}
	}
	return &MemoryMap{regions: copyRegions}, nil
}

// ResizeRegion changes the mapped length of a growable region whose start is
// exactly address. Shrunk and newly exposed bytes are cleared deterministically.
// The virtual reservation and every other region remain unchanged.
func (m *MemoryMap) ResizeRegion(address, newLength uint64) error {
	if m == nil {
		return ErrAccessViolation
	}
	index := sort.Search(len(m.regions), func(index int) bool { return m.regions[index].VMStart >= address })
	if index >= len(m.regions) || m.regions[index].VMStart != address {
		return fmt.Errorf("%w: no region starts at %#x", ErrAccessViolation, address)
	}
	region := &m.regions[index]
	if !region.Growable || !region.Writable || newLength > region.ReservedLength || newLength > uint64(cap(region.Data)) || newLength > uint64(math.MaxInt) {
		return fmt.Errorf("%w: region %q cannot resize to %d", ErrAccessViolation, region.Name, newLength)
	}
	oldLength := len(region.Data)
	if int(newLength) > oldLength {
		region.Data = region.Data[:int(newLength)]
		clear(region.Data[oldLength:])
	} else if int(newLength) < oldLength {
		clear(region.Data[int(newLength):oldLength])
		region.Data = region.Data[:int(newLength)]
	}
	return nil
}

func (m *MemoryMap) growableStorage(address, currentLength uint64) ([]byte, bool) {
	if m == nil {
		return nil, false
	}
	index := sort.Search(len(m.regions), func(index int) bool { return m.regions[index].VMStart >= address })
	if index >= len(m.regions) || m.regions[index].VMStart != address {
		return nil, false
	}
	region := &m.regions[index]
	if !region.Growable || uint64(len(region.Data)) != currentLength || region.ReservedLength > uint64(cap(region.Data)) || region.ReservedLength > uint64(math.MaxInt) {
		return nil, false
	}
	return region.Data[:int(region.ReservedLength)], true
}

// Regions returns a copy of the virtual region descriptors. The Data fields
// still refer to the mapped storage by design.
func (m *MemoryMap) Regions() []MemoryRegion {
	if m == nil {
		return nil
	}
	return append([]MemoryRegion(nil), m.regions...)
}

// Translate maps one contiguous virtual range. A range may not cross region
// boundaries even when two regions are adjacent. alignment must be zero, one,
// or a power of two.
func (m *MemoryMap) Translate(address, length uint64, access AccessType, alignment uint64) ([]byte, error) {
	if m == nil {
		return nil, ErrAccessViolation
	}
	if access != AccessRead && access != AccessWrite {
		return nil, ErrAccessViolation
	}
	if alignment > 1 && (alignment&(alignment-1) != 0 || address%alignment != 0) {
		return nil, fmt.Errorf("%w: address %#x alignment %d", ErrUnalignedPointer, address, alignment)
	}
	if length > math.MaxUint64-address {
		return nil, fmt.Errorf("%w: address %#x length %d overflows", ErrInvalidLength, address, length)
	}
	end := address + length
	// Select the last region whose start is <= address. This handles adjacent
	// regions correctly when address is both the previous end and next start.
	next := sort.Search(len(m.regions), func(i int) bool { return m.regions[i].VMStart > address })
	if next == 0 {
		return nil, fmt.Errorf("%w: address %#x length %d", ErrAccessViolation, address, length)
	}
	region := m.regions[next-1]
	regionEnd, _ := region.end()
	// For zero-length slices, Agave still translates the pointer. The region's
	// one-past-the-end address is valid only for that zero-length case.
	if address < region.VMStart || end > regionEnd || (length > 0 && address == regionEnd) {
		return nil, fmt.Errorf("%w: address %#x length %d", ErrAccessViolation, address, length)
	}
	if access == AccessWrite && !region.Writable {
		return nil, fmt.Errorf("%w: region %q is read-only", ErrAccessViolation, region.Name)
	}
	offset := address - region.VMStart
	return region.Data[int(offset):int(offset+length)], nil
}

// ReadUint64 translates and reads one aligned little-endian u64.
func (m *MemoryMap) ReadUint64(address uint64) (uint64, error) {
	data, err := m.Translate(address, 8, AccessRead, 8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(data), nil
}

// WriteUint64 translates and writes one aligned little-endian u64.
func (m *MemoryMap) WriteUint64(address, value uint64) error {
	data, err := m.Translate(address, 8, AccessWrite, 8)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint64(data, value)
	return nil
}
