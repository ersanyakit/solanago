package erc20

import "encoding/binary"

type InstructionKind uint8

const (
	InstructionInitializeMint InstructionKind = iota
	InstructionInitializeBalance
	InstructionMintTo
	InstructionBurn
	InstructionTransfer
	InstructionInitializeAllowance
	InstructionApprove
	InstructionTransferFrom
)

// Instruction is the decoded form of this custom program's deterministic
// instruction data. Account addresses and signer privileges are supplied by
// the transaction, not trusted from this payload.
type Instruction struct {
	Kind      InstructionKind
	Name      string
	Symbol    string
	Decimals  uint8
	Authority Pubkey
	Owner     Pubkey
	Amount    uint64
}

func DecodeInstruction(data []byte) (Instruction, error) {
	if len(data) == 0 {
		return Instruction{}, ErrInvalidInstruction
	}
	instruction := Instruction{Kind: InstructionKind(data[0])}
	switch instruction.Kind {
	case InstructionInitializeMint:
		if len(data) != 76 {
			return Instruction{}, ErrInvalidInstruction
		}
		instruction.Name = trimZero(data[1:33])
		instruction.Symbol = trimZero(data[33:43])
		instruction.Decimals = data[43]
		copy(instruction.Authority[:], data[44:76])
	case InstructionInitializeBalance:
		if len(data) != 33 {
			return Instruction{}, ErrInvalidInstruction
		}
		copy(instruction.Owner[:], data[1:33])
	case InstructionInitializeAllowance:
		if len(data) != 1 {
			return Instruction{}, ErrInvalidInstruction
		}
	case InstructionMintTo, InstructionBurn, InstructionTransfer, InstructionApprove, InstructionTransferFrom:
		if len(data) != 9 {
			return Instruction{}, ErrInvalidInstruction
		}
		instruction.Amount = binary.LittleEndian.Uint64(data[1:9])
	default:
		return Instruction{}, ErrInvalidInstruction
	}
	return instruction, nil
}

func EncodeInitializeMint(name, symbol string, decimals uint8, authority Pubkey) ([]byte, error) {
	if len(name) > MaxNameLen || len(symbol) > MaxSymbolLen {
		return nil, ErrInvalidInstruction
	}
	data := make([]byte, 76)
	data[0] = byte(InstructionInitializeMint)
	copy(data[1:33], name)
	copy(data[33:43], symbol)
	data[43] = decimals
	copy(data[44:76], authority[:])
	return data, nil
}

func EncodeInitializeBalance(owner Pubkey) []byte {
	data := make([]byte, 33)
	data[0] = byte(InstructionInitializeBalance)
	copy(data[1:], owner[:])
	return data
}

func EncodeInitializeAllowance() []byte {
	return []byte{byte(InstructionInitializeAllowance)}
}

func EncodeAmountInstruction(kind InstructionKind, amount uint64) ([]byte, error) {
	switch kind {
	case InstructionMintTo, InstructionBurn, InstructionTransfer, InstructionApprove, InstructionTransferFrom:
	default:
		return nil, ErrInvalidInstruction
	}
	data := make([]byte, 9)
	data[0] = byte(kind)
	binary.LittleEndian.PutUint64(data[1:], amount)
	return data, nil
}
