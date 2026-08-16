// Package elf writes and validates the strict ELF64 container accepted by
// solana-sbpf v0.23.0 for sBPFv3 programs.
//
// The container is deliberately small, but it is a real ET_DYN/EM_BPF ELF:
// one executable PT_LOAD segment plus .text, .strtab, .symtab, and .shstrtab
// sections. It contains no writable data and needs no relocations.
package elf

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ersany/go-solana/sbpf"
)

const (
	Class64      = 2
	DataLittle   = 1
	Version      = 1
	OSABINone    = 0
	TypeDynamic  = 3
	MachineBPF   = 247
	SBPFVersion3 = 3

	ProgramLoad = 1
	FlagExecute = 1
	FlagRead    = 4

	SectionProgramBits = 1
	SectionSymbolTable = 2
	SectionStringTable = 3
	SectionFlagAlloc   = 1 << 1
	SectionFlagExec    = 1 << 2

	HeaderSize        = 64
	ProgramHeaderSize = 56
	SectionHeaderSize = 64
	SymbolSize        = 24

	MMRegionSize    uint64 = 1 << 32
	MMBytecodeStart        = MMRegionSize
)

var (
	ErrInvalidELF   = errors.New("invalid strict sBPF ELF")
	ErrInvalidEntry = errors.New("invalid sBPF ELF entrypoint")
)

// Image is the validated executable portion of an sBPF ELF file.
type Image struct {
	Version uint32
	EntryPC uint64
	Text    []byte
}

// BuildV3 wraps verified raw sBPFv3 text in the strict ELF64 layout selected
// by Agave's solana-sbpf 0.23.0 loader. entryPC is an instruction-slot index.
func BuildV3(text []byte, entryPC uint64) ([]byte, error) {
	program, err := sbpf.Decode(text)
	if err != nil {
		return nil, fmt.Errorf("ELF text: %w", err)
	}
	_, slots := sbpf.PhysicalPCs(program)
	if entryPC >= uint64(slots) || !isInstructionStart(program, int(entryPC)) {
		return nil, fmt.Errorf("%w: pc %d", ErrInvalidEntry, entryPC)
	}

	const textOffset = HeaderSize + ProgramHeaderSize
	shstrtab := []byte("\x00.text\x00.symtab\x00.strtab\x00.shstrtab\x00")
	strtab := []byte("\x00entrypoint\x00")

	strtabOffset := align(textOffset+len(text), 8)
	symtabOffset := align(strtabOffset+len(strtab), 8)
	// The first symbol is the required null symbol; the second names entrypoint.
	symtabSize := 2 * SymbolSize
	shstrtabOffset := symtabOffset + symtabSize
	sectionOffset := align(shstrtabOffset+len(shstrtab), 8)
	const sectionCount = 5
	totalSize := sectionOffset + sectionCount*SectionHeaderSize

	result := make([]byte, totalSize)
	copy(result[:16], []byte{0x7f, 'E', 'L', 'F', Class64, DataLittle, Version, OSABINone})
	put16(result, 16, TypeDynamic)
	put16(result, 18, MachineBPF)
	put32(result, 20, Version)
	put64(result, 24, MMBytecodeStart+entryPC*sbpf.InstructionSize)
	put64(result, 32, HeaderSize)
	put64(result, 40, uint64(sectionOffset))
	put32(result, 48, SBPFVersion3)
	put16(result, 52, HeaderSize)
	put16(result, 54, ProgramHeaderSize)
	put16(result, 56, 1)
	put16(result, 58, SectionHeaderSize)
	put16(result, 60, sectionCount)
	put16(result, 62, 4)

	// solana-sbpf's strict parser requires the first segment to begin exactly
	// after the program-header table and to use the sBPF bytecode virtual base.
	ph := result[HeaderSize : HeaderSize+ProgramHeaderSize]
	put32(ph, 0, ProgramLoad)
	put32(ph, 4, FlagExecute)
	put64(ph, 8, textOffset)
	put64(ph, 16, MMBytecodeStart)
	put64(ph, 24, MMBytecodeStart)
	put64(ph, 32, uint64(len(text)))
	put64(ph, 40, uint64(len(text)))
	put64(ph, 48, sbpf.InstructionSize)
	copy(result[textOffset:], text)
	copy(result[strtabOffset:], strtab)
	copy(result[shstrtabOffset:], shstrtab)

	// Symbol 0 is all zero. Symbol 1 is a global function named entrypoint.
	symbol := result[symtabOffset+SymbolSize : symtabOffset+2*SymbolSize]
	put32(symbol, 0, 1) // offset of "entrypoint" in .strtab
	symbol[4] = 0x12    // STB_GLOBAL | STT_FUNC
	put16(symbol, 6, 1) // .text
	put64(symbol, 8, MMBytecodeStart+entryPC*sbpf.InstructionSize)
	put64(symbol, 16, uint64(len(text))-entryPC*sbpf.InstructionSize)

	// Section 0 is the mandatory null section.
	writeSection(result, sectionOffset+SectionHeaderSize, sectionHeader{
		name: 1, kind: SectionProgramBits, flags: SectionFlagAlloc | SectionFlagExec,
		address: MMBytecodeStart, offset: textOffset, size: len(text), align: 8,
	})
	// solana-sbpf's label-enabled ELF parser requires non-NOBITS section
	// headers to be ordered by their physical file ranges.  The string table
	// is physically before the symbol table, so it must also precede it here.
	writeSection(result, sectionOffset+2*SectionHeaderSize, sectionHeader{
		name: 15, kind: SectionStringTable, offset: strtabOffset, size: len(strtab), align: 1,
	})
	writeSection(result, sectionOffset+3*SectionHeaderSize, sectionHeader{
		name: 7, kind: SectionSymbolTable, offset: symtabOffset, size: symtabSize,
		link: 2, info: 1, align: 8, entrySize: SymbolSize,
	})
	writeSection(result, sectionOffset+4*SectionHeaderSize, sectionHeader{
		name: 23, kind: SectionStringTable, offset: shstrtabOffset, size: len(shstrtab), align: 1,
	})

	if _, err := ParseStrictV3(result); err != nil {
		return nil, fmt.Errorf("internal ELF writer produced an invalid image: %w", err)
	}
	return result, nil
}

// ParseStrictV3 validates the strict header and executable segment rules used
// by solana-sbpf v0.23.0. It also runs the local sBPF verifier over .text.
func ParseStrictV3(data []byte) (Image, error) {
	if len(data) < HeaderSize+ProgramHeaderSize {
		return Image{}, invalid("file is shorter than ELF and program headers")
	}
	if string(data[:4]) != "\x7fELF" || data[4] != Class64 || data[5] != DataLittle ||
		data[6] != Version || data[7] != OSABINone || !allZero(data[8:16]) {
		return Image{}, invalid("non-canonical e_ident")
	}
	if u16(data, 16) != TypeDynamic || u16(data, 18) != MachineBPF || u32(data, 20) != Version {
		return Image{}, invalid("wrong ELF type, machine, or version")
	}
	if u32(data, 48) != SBPFVersion3 {
		return Image{}, invalid("e_flags is %d, want sBPFv3", u32(data, 48))
	}
	programHeaderOffset := u64(data, 32)
	programHeaderCount := uint64(u16(data, 56))
	if programHeaderOffset != HeaderSize || u16(data, 52) != HeaderSize ||
		u16(data, 54) != ProgramHeaderSize || programHeaderCount == 0 {
		return Image{}, invalid("non-canonical program-header table")
	}
	programHeaderTableSize := programHeaderCount * ProgramHeaderSize
	if programHeaderTableSize/ProgramHeaderSize != programHeaderCount ||
		programHeaderOffset > uint64(len(data)) ||
		programHeaderTableSize > uint64(len(data))-programHeaderOffset {
		return Image{}, invalid("program-header table is out of bounds")
	}
	programHeaderTableEnd := programHeaderOffset + programHeaderTableSize
	programHeader := func(index uint64) []byte {
		offset := programHeaderOffset + index*ProgramHeaderSize
		return data[offset : offset+ProgramHeaderSize]
	}

	// solana-sbpf v0.23.0 permits either a single executable segment or a
	// contiguous read-only segment followed by the executable segment.  LLVM
	// currently emits a third, zero-filled program header; the upstream strict
	// parser deliberately ignores headers after the required one(s).
	first := programHeader(0)
	textHeaderIndex := uint64(0)
	expectedTextOffset := programHeaderTableEnd
	if u32(first, 4) == FlagRead {
		if programHeaderCount < 2 || !validLoadSegment(first, FlagRead, 0, programHeaderTableEnd, data) {
			return Image{}, invalid("non-canonical read-only segment")
		}
		textHeaderIndex = 1
		expectedTextOffset += u64(first, 32)
	}
	textHeader := programHeader(textHeaderIndex)
	if !validLoadSegment(textHeader, FlagExecute, MMBytecodeStart, expectedTextOffset, data) {
		return Image{}, invalid("non-canonical executable segment")
	}
	textOffset := u64(textHeader, 8)
	textSize := u64(textHeader, 32)
	if textSize == 0 || textSize%sbpf.InstructionSize != 0 || textSize >= MMRegionSize ||
		textOffset > uint64(len(data)) || textSize > uint64(len(data))-textOffset {
		return Image{}, invalid("executable segment is empty, unaligned, or out of bounds")
	}
	entry := u64(data, 24)
	if entry < MMBytecodeStart || entry%sbpf.InstructionSize != 0 ||
		entry-MMBytecodeStart >= textSize {
		return Image{}, fmt.Errorf("%w: virtual address %#x", ErrInvalidEntry, entry)
	}
	text := append([]byte(nil), data[textOffset:textOffset+textSize]...)
	if err := validateStrictLabelSections(data, programHeaderOffset, programHeaderTableSize); err != nil {
		return Image{}, err
	}
	program, err := sbpf.Decode(text)
	if err != nil {
		return Image{}, invalid("text verification failed: %v", err)
	}
	entryPC := (entry - MMBytecodeStart) / sbpf.InstructionSize
	if !isInstructionStart(program, int(entryPC)) {
		return Image{}, fmt.Errorf("%w: pc %d enters an LD_DW_IMM continuation", ErrInvalidEntry, entryPC)
	}
	return Image{Version: SBPFVersion3, EntryPC: entryPC, Text: text}, nil
}

// validateStrictLabelSections mirrors the section/symbol-table work performed
// by solana-sbpf's strict parser when enable_symbol_and_section_labels is on.
// Program loading comes from PT_LOAD, but malformed label tables must still be
// rejected locally instead of passing verify and failing only after a paid
// loader transaction.
func validateStrictLabelSections(data []byte, programHeaderOffset, programHeaderTableSize uint64) error {
	sectionOffset := u64(data, 40)
	sectionCount := uint64(u16(data, 60))
	sectionNamesIndex := uint64(u16(data, 62))
	if sectionCount == 0 || sectionNamesIndex == 0 || sectionNamesIndex >= sectionCount || sectionOffset%8 != 0 {
		return invalid("missing or non-canonical section-header table")
	}
	sectionTableSize := sectionCount * SectionHeaderSize
	if sectionTableSize/SectionHeaderSize != sectionCount || sectionOffset > uint64(len(data)) || sectionTableSize > uint64(len(data))-sectionOffset {
		return invalid("section-header table is out of bounds")
	}
	sectionEnd := sectionOffset + sectionTableSize
	programHeaderEnd := programHeaderOffset + programHeaderTableSize
	if rangesOverlap(sectionOffset, sectionEnd, 0, HeaderSize) || rangesOverlap(sectionOffset, sectionEnd, programHeaderOffset, programHeaderEnd) {
		return invalid("section-header table overlaps an ELF header")
	}
	section := func(index uint64) []byte {
		offset := sectionOffset + index*SectionHeaderSize
		return data[offset : offset+SectionHeaderSize]
	}
	sectionBytes := func(header []byte, alignment uint64) ([]byte, error) {
		offset := u64(header, 24)
		size := u64(header, 32)
		if alignment > 1 && offset%alignment != 0 {
			return nil, invalid("section data is unaligned")
		}
		if offset > uint64(len(data)) || size > uint64(len(data))-offset {
			return nil, invalid("section data is out of bounds")
		}
		return data[offset : offset+size], nil
	}
	sectionNames, err := sectionBytes(section(sectionNamesIndex), 1)
	if err != nil {
		return err
	}
	if u32(section(sectionNamesIndex), 4) != SectionStringTable {
		return invalid("section-name table is not SHT_STRTAB")
	}
	readString := func(table []byte, offset uint32, maximum int) ([]byte, error) {
		start := uint64(offset)
		if start >= uint64(len(table)) {
			return nil, invalid("section string offset is out of bounds")
		}
		limit := len(table) - int(start)
		if limit > maximum {
			limit = maximum
		}
		for index := 0; index < limit; index++ {
			if table[int(start)+index] == 0 {
				return table[int(start) : int(start)+index], nil
			}
		}
		return nil, invalid("section string is unterminated or too long")
	}

	var symbolNamesHeader, symbolTableHeader []byte
	for index := uint64(0); index < sectionCount; index++ {
		header := section(index)
		name, err := readString(sectionNames, u32(header, 0), 64)
		if err != nil {
			return err
		}
		switch string(name) {
		case ".strtab":
			symbolNamesHeader = header
		case ".symtab":
			symbolTableHeader = header
		}
	}
	if (symbolNamesHeader == nil) != (symbolTableHeader == nil) {
		return invalid(".symtab and .strtab must both be present or both be absent")
	}
	if symbolTableHeader == nil {
		return nil
	}
	if u32(symbolNamesHeader, 4) != SectionStringTable {
		return invalid("symbol-name table is not SHT_STRTAB")
	}
	symbolNames, err := sectionBytes(symbolNamesHeader, 1)
	if err != nil {
		return err
	}
	symbols, err := sectionBytes(symbolTableHeader, 8)
	if err != nil {
		return err
	}
	if len(symbols)%SymbolSize != 0 {
		return invalid("symbol table size is not canonical")
	}
	functionTargets := make(map[uint32]uint64)
	for offset := 0; offset < len(symbols); offset += SymbolSize {
		symbol := symbols[offset : offset+SymbolSize]
		if symbol[4]&2 == 0 { // STT_FUNC in the low symbol-info nibble
			continue
		}
		name, err := readString(symbolNames, u32(symbol, 0), 255)
		if err != nil {
			return err
		}
		targetPC := (u64(symbol, 8) - minUint64(u64(symbol, 8), MMBytecodeStart)) / sbpf.InstructionSize
		hash := sbpf.HashSymbolName(string(name))
		if previous, exists := functionTargets[hash]; exists && previous != targetPC {
			return invalid("function symbol hash collision for 0x%08x", hash)
		}
		functionTargets[hash] = targetPC
	}
	return nil
}

func minUint64(first, second uint64) uint64 {
	if first < second {
		return first
	}
	return second
}

func rangesOverlap(firstStart, firstEnd, secondStart, secondEnd uint64) bool {
	return firstStart < secondEnd && secondStart < firstEnd
}

// validLoadSegment mirrors the fields checked by solana-sbpf v0.23.0's
// strict parser. p_align is intentionally not inspected upstream.
func validLoadSegment(header []byte, flags uint32, virtualAddress, offset uint64, data []byte) bool {
	fileSize := u64(header, 32)
	memorySize := u64(header, 40)
	return u32(header, 0) == ProgramLoad &&
		u32(header, 4) == flags &&
		u64(header, 8) == offset &&
		offset < uint64(len(data)) &&
		offset%sbpf.InstructionSize == 0 &&
		u64(header, 16) == virtualAddress &&
		u64(header, 24) == virtualAddress &&
		fileSize == memorySize &&
		fileSize <= uint64(len(data))-offset &&
		fileSize%sbpf.InstructionSize == 0 &&
		memorySize < MMRegionSize
}

type sectionHeader struct {
	name, kind       uint32
	flags, address   uint64
	offset, size     int
	link, info       uint32
	align, entrySize uint64
}

func writeSection(data []byte, offset int, section sectionHeader) {
	dst := data[offset : offset+SectionHeaderSize]
	put32(dst, 0, section.name)
	put32(dst, 4, section.kind)
	put64(dst, 8, section.flags)
	put64(dst, 16, section.address)
	put64(dst, 24, uint64(section.offset))
	put64(dst, 32, uint64(section.size))
	put32(dst, 40, section.link)
	put32(dst, 44, section.info)
	put64(dst, 48, section.align)
	put64(dst, 56, section.entrySize)
}

func isInstructionStart(program []sbpf.Instruction, pc int) bool {
	pcs, _ := sbpf.PhysicalPCs(program)
	for _, candidate := range pcs {
		if candidate == pc {
			return true
		}
	}
	return false
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidELF, fmt.Sprintf(format, args...))
}

func align(value, alignment int) int { return (value + alignment - 1) &^ (alignment - 1) }
func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func put16(data []byte, offset int, value uint16) {
	binary.LittleEndian.PutUint16(data[offset:], value)
}
func put32(data []byte, offset int, value uint32) {
	binary.LittleEndian.PutUint32(data[offset:], value)
}
func put64(data []byte, offset int, value uint64) {
	binary.LittleEndian.PutUint64(data[offset:], value)
}
func u16(data []byte, offset int) uint16 { return binary.LittleEndian.Uint16(data[offset:]) }
func u32(data []byte, offset int) uint32 { return binary.LittleEndian.Uint32(data[offset:]) }
func u64(data []byte, offset int) uint64 { return binary.LittleEndian.Uint64(data[offset:]) }
