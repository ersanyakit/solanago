package sbpf

import "encoding/binary"

// HashSymbolName implements the Murmur3-x86-32 symbol hash used by
// solana-sbpf to identify built-in functions. The seed is zero, matching
// ebpf::hash_symbol_name and the sBPFv3 static-syscall headers.
func HashSymbolName(name string) uint32 {
	const (
		mix1 uint32 = 0xcc9e2d51
		mix2 uint32 = 0x1b873593
	)
	data := []byte(name)
	var hash uint32
	blocks := len(data) / 4
	for block := 0; block < blocks; block++ {
		value := binary.LittleEndian.Uint32(data[block*4 : block*4+4])
		value *= mix1
		value = value<<15 | value>>17
		value *= mix2
		hash ^= value
		hash = hash<<13 | hash>>19
		hash = hash*5 + 0xe6546b64
	}

	var tail uint32
	switch len(data) & 3 {
	case 3:
		tail ^= uint32(data[blocks*4+2]) << 16
		fallthrough
	case 2:
		tail ^= uint32(data[blocks*4+1]) << 8
		fallthrough
	case 1:
		tail ^= uint32(data[blocks*4])
		tail *= mix1
		tail = tail<<15 | tail>>17
		tail *= mix2
		hash ^= tail
	}

	hash ^= uint32(len(data))
	hash ^= hash >> 16
	hash *= 0x85ebca6b
	hash ^= hash >> 13
	hash *= 0xc2b2ae35
	hash ^= hash >> 16
	return hash
}

// StaticSyscall constructs an sBPFv3 external CALL_IMM. src=0 distinguishes
// the call from an internal relative call (src=1). Conversion through int32
// preserves keys with bit 31 set in the instruction's signed immediate field.
func StaticSyscall(key uint32) Instruction {
	return Instruction{Op: CALL_IMM, Immediate: int64(int32(key))}
}
