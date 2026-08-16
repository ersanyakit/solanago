package elf

import (
	"bytes"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/ersanyakit/solanago/sbpf"
)

// sbpfv3ReturnOK is Agave's programs/bpf_loader/test_elfs/out/
// sbpfv3_return_ok.so fixture at commit 12b5c7e4df705927b2f7f579f3aa606aa4bde1c0.
// SHA-256: 7b7f204cc5691a93b8bbc2036b7fdb6977d237711e48c19a65688d53ce216736.
// It exercises both optional strict-parser details our compact writer does not
// need: a read-only PT_LOAD before .text and a trailing zero program header.
const sbpfv3ReturnOK = "f0VMRgIBAQAAAAAAAAAAAAMA9wABAAAAAAAAAAEAAABAAAAAAAAAACABAAAAAAAAAwAAAEAAOAADAEAABAADAAEAAAAEAAAA6AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAACAAAAAAAAAAIAAAAAAAAAAgAAAAAAAAAAQAAAAEAAADwAAAAAAAAAAAAAAABAAAAAAAAAAEAAAAQAAAAAAAAABAAAAAAAAAACAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAtwAAAAAAAACVAAAAAAAAAAAudGV4dAAuc2hzdHJ0YWIALnJvZGF0YQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABEAAAABAAAAAgAAAAAAAAAAAAAAAAAAAOgAAAAAAAAACAAAAAAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAABAAAAAQAAAAYAAAAAAAAAAAAAAAEAAADwAAAAAAAAABAAAAAAAAAAAAAAAAAAAAAIAAAAAAAAAAAAAAAAAAAABwAAAAMAAAAAAAAAAAAAAAAAAAAAAAAAAAEAAAAAAAAZAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func TestBuildV3RoundTripAndStandardParser(t *testing.T) {
	text, err := sbpf.Encode([]sbpf.Instruction{
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 0),
		sbpf.Return(),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := BuildV3(text, 0)
	if err != nil {
		t.Fatal(err)
	}
	image, err := ParseStrictV3(data)
	if err != nil {
		t.Fatal(err)
	}
	if image.Version != 3 || image.EntryPC != 0 || !bytes.Equal(image.Text, text) {
		t.Fatalf("unexpected image: version=%d entry=%d text=%x", image.Version, image.EntryPC, image.Text)
	}

	standard, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Go debug/elf independently rejected output: %v", err)
	}
	defer standard.Close()
	if standard.Class != elf.ELFCLASS64 || standard.Data != elf.ELFDATA2LSB ||
		standard.Type != elf.ET_DYN || standard.Machine != elf.EM_BPF {
		t.Fatalf("unexpected standard ELF header: %#v", standard.FileHeader)
	}
	section := standard.Section(".text")
	if section == nil {
		t.Fatal("missing .text section")
	}
	sectionData, err := section.Data()
	if err != nil || !bytes.Equal(sectionData, text) {
		t.Fatalf(".text mismatch: data=%x err=%v", sectionData, err)
	}
	symbols, err := standard.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 1 || symbols[0].Name != "entrypoint" || symbols[0].Value != MMBytecodeStart {
		t.Fatalf("unexpected symbols: %#v", symbols)
	}

	// solana-sbpf's label-enabled parser walks section headers in table order
	// and rejects a later header whose file range starts before the prior one.
	// Keep this independent check even though Go's debug/elf accepts unordered
	// section headers.
	var previousEnd uint64
	for index, section := range standard.Sections {
		if section.Type == elf.SHT_NOBITS {
			continue
		}
		if section.Offset < previousEnd {
			t.Fatalf("section %d (%s) starts at %d before previous end %d", index, section.Name, section.Offset, previousEnd)
		}
		if section.Size > ^uint64(0)-section.Offset {
			t.Fatalf("section %d (%s) range overflows", index, section.Name)
		}
		previousEnd = section.Offset + section.Size
	}
}

func TestParseStrictV3AcceptsOfficialAgaveFixture(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(sbpfv3ReturnOK)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 644 {
		t.Fatalf("official fixture length = %d, want 644", len(data))
	}
	image, err := ParseStrictV3(data)
	if err != nil {
		t.Fatalf("official solana-sbpf v0.23.0 fixture was rejected: %v", err)
	}
	want, err := sbpf.Encode([]sbpf.Instruction{
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 0),
		sbpf.Return(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if image.Version != SBPFVersion3 || image.EntryPC != 0 || !bytes.Equal(image.Text, want) {
		t.Fatalf("unexpected official fixture image: version=%d entry=%d text=%x", image.Version, image.EntryPC, image.Text)
	}

	broken := append([]byte(nil), data...)
	// The executable header starts after the ELF header plus the read-only
	// header. Its p_offset must immediately follow the read-only bytes.
	binary.LittleEndian.PutUint64(broken[HeaderSize+ProgramHeaderSize+8:], 248)
	if _, err := ParseStrictV3(broken); !errors.Is(err, ErrInvalidELF) {
		t.Fatalf("non-contiguous official-style segments: got %v, want %v", err, ErrInvalidELF)
	}
}

func TestBuildV3RejectsContinuationEntrypoint(t *testing.T) {
	text, err := sbpf.Encode([]sbpf.Instruction{sbpf.LoadImm64(sbpf.R0, 1), sbpf.Return()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildV3(text, 1)
	if !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("got %v, want ErrInvalidEntry", err)
	}
}

func TestParseStrictV3RejectsHeaderAndTextCorruption(t *testing.T) {
	text, _ := sbpf.Encode([]sbpf.Instruction{sbpf.Return()})
	valid, err := BuildV3(text, 0)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func([]byte)
		want   error
	}{
		{"magic", func(data []byte) { data[0] = 0 }, ErrInvalidELF},
		{"version", func(data []byte) { binary.LittleEndian.PutUint32(data[48:], 2) }, ErrInvalidELF},
		{"segment offset", func(data []byte) { binary.LittleEndian.PutUint64(data[72:], 128) }, ErrInvalidELF},
		{"section table out of bounds", func(data []byte) { binary.LittleEndian.PutUint64(data[40:], uint64(len(data)+8)) }, ErrInvalidELF},
		{"missing section name table", func(data []byte) { binary.LittleEndian.PutUint16(data[62:], 0) }, ErrInvalidELF},
		{"section names not a string table", func(data []byte) {
			sectionOffset := binary.LittleEndian.Uint64(data[40:])
			binary.LittleEndian.PutUint32(data[sectionOffset+4*SectionHeaderSize+4:], SectionProgramBits)
		}, ErrInvalidELF},
		{"symtab without strtab", func(data []byte) {
			sectionOffset := binary.LittleEndian.Uint64(data[40:])
			binary.LittleEndian.PutUint32(data[sectionOffset+2*SectionHeaderSize:], 0)
		}, ErrInvalidELF},
		{"symbol names not a string table", func(data []byte) {
			sectionOffset := binary.LittleEndian.Uint64(data[40:])
			binary.LittleEndian.PutUint32(data[sectionOffset+2*SectionHeaderSize+4:], SectionProgramBits)
		}, ErrInvalidELF},
		{"symbol table out of bounds", func(data []byte) {
			sectionOffset := binary.LittleEndian.Uint64(data[40:])
			binary.LittleEndian.PutUint64(data[sectionOffset+3*SectionHeaderSize+24:], uint64(len(data)+8))
		}, ErrInvalidELF},
		{"function symbol hash collision", func(data []byte) {
			sectionOffset := binary.LittleEndian.Uint64(data[40:])
			symtabHeader := data[sectionOffset+3*SectionHeaderSize : sectionOffset+4*SectionHeaderSize]
			symtabOffset := binary.LittleEndian.Uint64(symtabHeader[24:])
			nullSymbol := data[symtabOffset : symtabOffset+SymbolSize]
			binary.LittleEndian.PutUint32(nullSymbol, 1)
			nullSymbol[4] = 2
			binary.LittleEndian.PutUint64(nullSymbol[8:], MMBytecodeStart+sbpf.InstructionSize)
		}, ErrInvalidELF},
		{"entry alignment", func(data []byte) { binary.LittleEndian.PutUint64(data[24:], MMBytecodeStart+1) }, ErrInvalidEntry},
		{"opcode", func(data []byte) { data[HeaderSize+ProgramHeaderSize] = 0xff }, ErrInvalidELF},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := append([]byte(nil), valid...)
			test.mutate(data)
			_, err := ParseStrictV3(data)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

func FuzzParseStrictV3(f *testing.F) {
	text, _ := sbpf.Encode([]sbpf.Instruction{sbpf.Return()})
	valid, _ := BuildV3(text, 0)
	f.Add(valid)
	f.Add([]byte("not elf"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseStrictV3(data)
	})
}
