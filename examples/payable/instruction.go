package payable

import "encoding/binary"

type InstructionKind uint8

const (
	InstructionInitializeVault InstructionKind = iota
	InstructionInitializeDepositAccount
	InstructionDeposit
	InstructionWithdraw
	InstructionEmergencyWithdraw
)

// Instruction is the decoded form of this custom program's deterministic
// instruction data. Account addresses and signer privileges are supplied by
// the transaction, not trusted from this payload.
type Instruction struct {
	Kind      InstructionKind
	Depositor Pubkey
	Authority Pubkey
	Amount    uint64
}

func DecodeInstruction(data []byte) (Instruction, error) {
	if len(data) == 0 {
		return Instruction{}, ErrInvalidInstruction
	}
	instruction := Instruction{Kind: InstructionKind(data[0])}
	switch instruction.Kind {
	case InstructionInitializeVault:
		if len(data) != 33 {
			return Instruction{}, ErrInvalidInstruction
		}
		copy(instruction.Authority[:], data[1:33])
	case InstructionInitializeDepositAccount:
		if len(data) != 33 {
			return Instruction{}, ErrInvalidInstruction
		}
		copy(instruction.Depositor[:], data[1:33])
	case InstructionDeposit, InstructionWithdraw, InstructionEmergencyWithdraw:
		if len(data) != 9 {
			return Instruction{}, ErrInvalidInstruction
		}
		instruction.Amount = binary.LittleEndian.Uint64(data[1:9])
	default:
		return Instruction{}, ErrInvalidInstruction
	}
	return instruction, nil
}

func EncodeInitializeVault(authority Pubkey) []byte {
	data := make([]byte, 33)
	data[0] = byte(InstructionInitializeVault)
	copy(data[1:], authority[:])
	return data
}

func EncodeInitializeDepositAccount(depositor Pubkey) []byte {
	data := make([]byte, 33)
	data[0] = byte(InstructionInitializeDepositAccount)
	copy(data[1:], depositor[:])
	return data
}

func EncodeDeposit(amount uint64) []byte {
	return encodeAmount(InstructionDeposit, amount)
}

func EncodeWithdraw(amount uint64) []byte {
	return encodeAmount(InstructionWithdraw, amount)
}

func EncodeEmergencyWithdraw(amount uint64) []byte {
	return encodeAmount(InstructionEmergencyWithdraw, amount)
}

func encodeAmount(kind InstructionKind, amount uint64) []byte {
	data := make([]byte, 9)
	data[0] = byte(kind)
	binary.LittleEndian.PutUint64(data[1:], amount)
	return data
}
