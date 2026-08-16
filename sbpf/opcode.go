package sbpf

// Opcode is the one-byte operation code stored at byte zero of every sBPF
// instruction slot. The values below are the eBPF-compatible encodings used
// by sBPF v3. They are intentionally limited to the compiler MVP's backend.
type Opcode uint8

const (
	// LD_DW_IMM occupies two consecutive 8-byte instruction slots. The low
	// 32 bits are in the first slot and the high 32 bits in the second.
	LD_DW_IMM Opcode = 0x18
	LD_B_REG  Opcode = 0x71
	LD_H_REG  Opcode = 0x69
	LD_W_REG  Opcode = 0x61
	LD_DW_REG Opcode = 0x79
	ST_B_REG  Opcode = 0x73
	ST_H_REG  Opcode = 0x6b
	ST_W_REG  Opcode = 0x63
	ST_DW_REG Opcode = 0x7b

	ADD32_IMM  Opcode = 0x04
	ADD32_REG  Opcode = 0x0c
	SUB32_IMM  Opcode = 0x14
	SUB32_REG  Opcode = 0x1c
	MUL32_IMM  Opcode = 0x24
	MUL32_REG  Opcode = 0x2c
	DIV32_IMM  Opcode = 0x34
	DIV32_REG  Opcode = 0x3c
	LSH32_IMM  Opcode = 0x64
	LSH32_REG  Opcode = 0x6c
	RSH32_IMM  Opcode = 0x74
	RSH32_REG  Opcode = 0x7c
	NEG32      Opcode = 0x84
	MOD32_IMM  Opcode = 0x94
	MOD32_REG  Opcode = 0x9c
	XOR32_IMM  Opcode = 0xa4
	XOR32_REG  Opcode = 0xac
	MOV32_IMM  Opcode = 0xb4
	MOV32_REG  Opcode = 0xbc
	ARSH32_IMM Opcode = 0xc4
	ARSH32_REG Opcode = 0xcc

	ADD64_IMM  Opcode = 0x07
	ADD64_REG  Opcode = 0x0f
	SUB64_IMM  Opcode = 0x17
	SUB64_REG  Opcode = 0x1f
	MUL64_IMM  Opcode = 0x27
	MUL64_REG  Opcode = 0x2f
	DIV64_IMM  Opcode = 0x37
	DIV64_REG  Opcode = 0x3f
	LSH64_IMM  Opcode = 0x67
	LSH64_REG  Opcode = 0x6f
	RSH64_IMM  Opcode = 0x77
	RSH64_REG  Opcode = 0x7f
	NEG64      Opcode = 0x87
	MOD64_IMM  Opcode = 0x97
	MOD64_REG  Opcode = 0x9f
	XOR64_IMM  Opcode = 0xa7
	XOR64_REG  Opcode = 0xaf
	MOV64_IMM  Opcode = 0xb7
	MOV64_REG  Opcode = 0xbf
	ARSH64_IMM Opcode = 0xc7
	ARSH64_REG Opcode = 0xcf

	JEQ32_IMM  Opcode = 0x16
	JEQ32_REG  Opcode = 0x1e
	JGT32_IMM  Opcode = 0x26
	JGT32_REG  Opcode = 0x2e
	JGE32_IMM  Opcode = 0x36
	JGE32_REG  Opcode = 0x3e
	JNE32_IMM  Opcode = 0x56
	JNE32_REG  Opcode = 0x5e
	JSGT32_IMM Opcode = 0x66
	JSGT32_REG Opcode = 0x6e
	JSGE32_IMM Opcode = 0x76
	JSGE32_REG Opcode = 0x7e
	JLT32_IMM  Opcode = 0xa6
	JLT32_REG  Opcode = 0xae
	JLE32_IMM  Opcode = 0xb6
	JLE32_REG  Opcode = 0xbe
	JSLT32_IMM Opcode = 0xc6
	JSLT32_REG Opcode = 0xce
	JSLE32_IMM Opcode = 0xd6
	JSLE32_REG Opcode = 0xde

	JA         Opcode = 0x05
	JEQ64_IMM  Opcode = 0x15
	JEQ64_REG  Opcode = 0x1d
	JGT64_IMM  Opcode = 0x25
	JGT64_REG  Opcode = 0x2d
	JGE64_IMM  Opcode = 0x35
	JGE64_REG  Opcode = 0x3d
	JNE64_IMM  Opcode = 0x55
	JNE64_REG  Opcode = 0x5d
	JSGT64_IMM Opcode = 0x65
	JSGT64_REG Opcode = 0x6d
	JSGE64_IMM Opcode = 0x75
	JSGE64_REG Opcode = 0x7d
	CALL_IMM   Opcode = 0x85
	EXIT       Opcode = 0x95
	JLT64_IMM  Opcode = 0xa5
	JLT64_REG  Opcode = 0xad
	JLE64_IMM  Opcode = 0xb5
	JLE64_REG  Opcode = 0xbd
	JSLT64_IMM Opcode = 0xc5
	JSLT64_REG Opcode = 0xcd
	JSLE64_IMM Opcode = 0xd5
	JSLE64_REG Opcode = 0xdd
)

var opcodeNames = map[Opcode]string{
	LD_DW_IMM:  "LD_DW_IMM",
	LD_B_REG:   "LD_B_REG",
	LD_H_REG:   "LD_H_REG",
	LD_W_REG:   "LD_W_REG",
	LD_DW_REG:  "LD_DW_REG",
	ST_B_REG:   "ST_B_REG",
	ST_H_REG:   "ST_H_REG",
	ST_W_REG:   "ST_W_REG",
	ST_DW_REG:  "ST_DW_REG",
	ADD32_IMM:  "ADD32_IMM",
	ADD32_REG:  "ADD32_REG",
	SUB32_IMM:  "SUB32_IMM",
	SUB32_REG:  "SUB32_REG",
	MUL32_IMM:  "MUL32_IMM",
	MUL32_REG:  "MUL32_REG",
	DIV32_IMM:  "DIV32_IMM",
	DIV32_REG:  "DIV32_REG",
	LSH32_IMM:  "LSH32_IMM",
	LSH32_REG:  "LSH32_REG",
	RSH32_IMM:  "RSH32_IMM",
	RSH32_REG:  "RSH32_REG",
	NEG32:      "NEG32",
	MOD32_IMM:  "MOD32_IMM",
	MOD32_REG:  "MOD32_REG",
	XOR32_IMM:  "XOR32_IMM",
	XOR32_REG:  "XOR32_REG",
	MOV32_IMM:  "MOV32_IMM",
	MOV32_REG:  "MOV32_REG",
	ARSH32_IMM: "ARSH32_IMM",
	ARSH32_REG: "ARSH32_REG",
	ADD64_IMM:  "ADD64_IMM",
	ADD64_REG:  "ADD64_REG",
	SUB64_IMM:  "SUB64_IMM",
	SUB64_REG:  "SUB64_REG",
	MUL64_IMM:  "MUL64_IMM",
	MUL64_REG:  "MUL64_REG",
	DIV64_IMM:  "DIV64_IMM",
	DIV64_REG:  "DIV64_REG",
	LSH64_IMM:  "LSH64_IMM",
	LSH64_REG:  "LSH64_REG",
	RSH64_IMM:  "RSH64_IMM",
	RSH64_REG:  "RSH64_REG",
	NEG64:      "NEG64",
	MOD64_IMM:  "MOD64_IMM",
	MOD64_REG:  "MOD64_REG",
	XOR64_IMM:  "XOR64_IMM",
	XOR64_REG:  "XOR64_REG",
	MOV64_IMM:  "MOV64_IMM",
	MOV64_REG:  "MOV64_REG",
	ARSH64_IMM: "ARSH64_IMM",
	ARSH64_REG: "ARSH64_REG",
	JEQ32_IMM:  "JEQ32_IMM",
	JEQ32_REG:  "JEQ32_REG",
	JGT32_IMM:  "JGT32_IMM",
	JGT32_REG:  "JGT32_REG",
	JGE32_IMM:  "JGE32_IMM",
	JGE32_REG:  "JGE32_REG",
	JNE32_IMM:  "JNE32_IMM",
	JNE32_REG:  "JNE32_REG",
	JSGT32_IMM: "JSGT32_IMM",
	JSGT32_REG: "JSGT32_REG",
	JSGE32_IMM: "JSGE32_IMM",
	JSGE32_REG: "JSGE32_REG",
	JLT32_IMM:  "JLT32_IMM",
	JLT32_REG:  "JLT32_REG",
	JLE32_IMM:  "JLE32_IMM",
	JLE32_REG:  "JLE32_REG",
	JSLT32_IMM: "JSLT32_IMM",
	JSLT32_REG: "JSLT32_REG",
	JSLE32_IMM: "JSLE32_IMM",
	JSLE32_REG: "JSLE32_REG",
	JA:         "JA",
	JEQ64_IMM:  "JEQ64_IMM",
	JEQ64_REG:  "JEQ64_REG",
	JGT64_IMM:  "JGT64_IMM",
	JGT64_REG:  "JGT64_REG",
	JGE64_IMM:  "JGE64_IMM",
	JGE64_REG:  "JGE64_REG",
	JNE64_IMM:  "JNE64_IMM",
	JNE64_REG:  "JNE64_REG",
	JSGT64_IMM: "JSGT64_IMM",
	JSGT64_REG: "JSGT64_REG",
	JSGE64_IMM: "JSGE64_IMM",
	JSGE64_REG: "JSGE64_REG",
	CALL_IMM:   "CALL_IMM",
	EXIT:       "EXIT",
	JLT64_IMM:  "JLT64_IMM",
	JLT64_REG:  "JLT64_REG",
	JLE64_IMM:  "JLE64_IMM",
	JLE64_REG:  "JLE64_REG",
	JSLT64_IMM: "JSLT64_IMM",
	JSLT64_REG: "JSLT64_REG",
	JSLE64_IMM: "JSLE64_IMM",
	JSLE64_REG: "JSLE64_REG",
}

func (op Opcode) String() string {
	if name, ok := opcodeNames[op]; ok {
		return name
	}
	return "UNKNOWN"
}

func (op Opcode) valid() bool {
	_, ok := opcodeNames[op]
	return ok
}

func (op Opcode) isALUImmediate() bool {
	switch op {
	case ADD32_IMM, SUB32_IMM, MUL32_IMM, DIV32_IMM, LSH32_IMM,
		RSH32_IMM, MOD32_IMM, XOR32_IMM, MOV32_IMM, ARSH32_IMM,
		ADD64_IMM, SUB64_IMM, MUL64_IMM, DIV64_IMM, LSH64_IMM,
		RSH64_IMM, MOD64_IMM, XOR64_IMM, MOV64_IMM, ARSH64_IMM:
		return true
	default:
		return false
	}
}

func (op Opcode) isALURegister() bool {
	switch op {
	case ADD32_REG, SUB32_REG, MUL32_REG, DIV32_REG, LSH32_REG,
		RSH32_REG, MOD32_REG, XOR32_REG, MOV32_REG, ARSH32_REG,
		ADD64_REG, SUB64_REG, MUL64_REG, DIV64_REG, LSH64_REG,
		RSH64_REG, MOD64_REG, XOR64_REG, MOV64_REG, ARSH64_REG:
		return true
	default:
		return false
	}
}

func (op Opcode) isJumpImmediate() bool {
	switch op {
	case JEQ32_IMM, JGT32_IMM, JGE32_IMM, JNE32_IMM,
		JSGT32_IMM, JSGE32_IMM, JLT32_IMM, JLE32_IMM,
		JSLT32_IMM, JSLE32_IMM,
		JEQ64_IMM, JGT64_IMM, JGE64_IMM, JNE64_IMM,
		JSGT64_IMM, JSGE64_IMM, JLT64_IMM, JLE64_IMM,
		JSLT64_IMM, JSLE64_IMM:
		return true
	default:
		return false
	}
}

func (op Opcode) isJumpRegister() bool {
	switch op {
	case JEQ32_REG, JGT32_REG, JGE32_REG, JNE32_REG,
		JSGT32_REG, JSGE32_REG, JLT32_REG, JLE32_REG,
		JSLT32_REG, JSLE32_REG,
		JEQ64_REG, JGT64_REG, JGE64_REG, JNE64_REG,
		JSGT64_REG, JSGE64_REG, JLT64_REG, JLE64_REG,
		JSLT64_REG, JSLE64_REG:
		return true
	default:
		return false
	}
}

func (op Opcode) isMemoryLoad() bool {
	return op == LD_B_REG || op == LD_H_REG || op == LD_W_REG || op == LD_DW_REG
}

func (op Opcode) isMemoryStore() bool {
	return op == ST_B_REG || op == ST_H_REG || op == ST_W_REG || op == ST_DW_REG
}

func (op Opcode) is32BitALU() bool {
	switch op {
	case ADD32_IMM, ADD32_REG, SUB32_IMM, SUB32_REG, MUL32_IMM, MUL32_REG,
		DIV32_IMM, DIV32_REG, LSH32_IMM, LSH32_REG, RSH32_IMM, RSH32_REG,
		NEG32, MOD32_IMM, MOD32_REG, XOR32_IMM, XOR32_REG, MOV32_IMM,
		MOV32_REG, ARSH32_IMM, ARSH32_REG:
		return true
	default:
		return false
	}
}

func (op Opcode) isJump() bool {
	return op == JA || op.isJumpImmediate() || op.isJumpRegister()
}
