package runtime

import (
	"errors"
	"math"
	"testing"

	"github.com/ersanyakit/solanago/sbpf"
)

func TestMemoryMapTranslationAndPermissions(t *testing.T) {
	readOnly := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	writable := make([]byte, 16)
	memory, err := NewMemoryMap(
		MemoryRegion{VMStart: 0x2000, Data: writable, Writable: true, Name: "rw"},
		MemoryRegion{VMStart: 0x1000, Data: readOnly, Name: "ro"},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := memory.Translate(0x1002, 3, AccessRead, 1)
	if err != nil || string(got) != string([]byte{3, 4, 5}) {
		t.Fatalf("read translation: %x %v", got, err)
	}
	if _, err := memory.Translate(0x1000, 1, AccessWrite, 1); !errors.Is(err, ErrAccessViolation) {
		t.Fatalf("expected read-only violation, got %v", err)
	}
	if err := memory.WriteUint64(0x2000, 0x0102030405060708); err != nil {
		t.Fatal(err)
	}
	value, err := memory.ReadUint64(0x2000)
	if err != nil || value != 0x0102030405060708 {
		t.Fatalf("u64 round trip: %#x %v", value, err)
	}
	if _, err := memory.Translate(0x2001, 8, AccessRead, 8); !errors.Is(err, ErrUnalignedPointer) {
		t.Fatalf("expected alignment error, got %v", err)
	}
	if _, err := memory.Translate(math.MaxUint64-1, 4, AccessRead, 1); !errors.Is(err, ErrInvalidLength) {
		t.Fatalf("expected overflow, got %v", err)
	}
}

func TestMemoryMapGrowableReservation(t *testing.T) {
	backing := make([]byte, 0, 8)
	memory, err := NewMemoryMap(
		MemoryRegion{VMStart: 0x1000, Data: backing, Writable: true, ReservedLength: 8, Growable: true, Name: "account data"},
		MemoryRegion{VMStart: 0x1008, Data: []byte{9}, Name: "metadata"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Translate(0x1000, 1, AccessRead, 1); !errors.Is(err, ErrAccessViolation) {
		t.Fatalf("unmapped reservation byte accepted: %v", err)
	}
	if err := memory.ResizeRegion(0x1000, 4); err != nil {
		t.Fatal(err)
	}
	data, err := memory.Translate(0x1000, 4, AccessWrite, 1)
	if err != nil || string(data) != string(make([]byte, 4)) {
		t.Fatalf("grown region mismatch: %x, %v", data, err)
	}
	copy(data, []byte{1, 2, 3, 4})
	if err := memory.ResizeRegion(0x1000, 2); err != nil {
		t.Fatal(err)
	}
	if err := memory.ResizeRegion(0x1000, 4); err != nil {
		t.Fatal(err)
	}
	data, _ = memory.Translate(0x1000, 4, AccessRead, 1)
	if !bytesEqual(data, []byte{1, 2, 0, 0}) {
		t.Fatalf("regrowth exposed stale bytes: %x", data)
	}
	if err := memory.ResizeRegion(0x1000, 9); !errors.Is(err, ErrAccessViolation) {
		t.Fatalf("reservation overflow accepted: %v", err)
	}
	if _, err := NewMemoryMap(
		MemoryRegion{VMStart: 0x1000, Data: make([]byte, 1), ReservedLength: 8},
		MemoryRegion{VMStart: 0x1007, Data: make([]byte, 1)},
	); !errors.Is(err, ErrMemoryOverlap) {
		t.Fatalf("reserved overlap accepted: %v", err)
	}
	if _, err := NewMemoryMap(MemoryRegion{
		VMStart: sbpf.MMInputStart + sbpf.MMRegionSize - 1,
		Data:    make([]byte, 1), ReservedLength: 2,
	}); !errors.Is(err, ErrInvalidMemoryRegion) {
		t.Fatalf("cross-boundary reservation accepted: %v", err)
	}
}

func bytesEqual(one, two []byte) bool {
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

func TestMemoryMapAdjacentAndNoCrossRegion(t *testing.T) {
	first := []byte{1, 2}
	second := []byte{3, 4}
	memory, err := NewMemoryMap(
		MemoryRegion{VMStart: 0x100, Data: first, Name: "first"},
		MemoryRegion{VMStart: 0x102, Data: second, Name: "second"},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := memory.Translate(0x102, 2, AccessRead, 1)
	if err != nil || string(got) != string(second) {
		t.Fatalf("adjacent selection: %x %v", got, err)
	}
	if _, err := memory.Translate(0x101, 2, AccessRead, 1); !errors.Is(err, ErrAccessViolation) {
		t.Fatalf("cross-region access accepted: %v", err)
	}
	if _, err := NewMemoryMap(
		MemoryRegion{VMStart: 0x100, Data: make([]byte, 3)},
		MemoryRegion{VMStart: 0x102, Data: make([]byte, 1)},
	); !errors.Is(err, ErrMemoryOverlap) {
		t.Fatalf("overlap accepted: %v", err)
	}
}
