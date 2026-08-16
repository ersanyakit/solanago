package spl20

import "encoding/binary"

type InstructionKind uint8

const (
	InstructionInitializeMint InstructionKind = iota
	InstructionInitializeAccount
	InstructionTransfer
	InstructionMintTo
	InstructionBurn
	InstructionApprove
	InstructionRevoke
	InstructionSetAuthority
)

// Instruction is the decoded form of this custom program's deterministic
// instruction data. Account addresses and signer privileges are supplied by
// the transaction, not trusted from this payload.
type Instruction struct {
	Kind          InstructionKind
	Amount        uint64
	Decimals      uint8
	Authority     Pubkey
	AuthorityType AuthorityType
	NewAuthority  OptionalPubkey
}

func DecodeInstruction(data []byte) (Instruction, error) {
	if len(data) == 0 {
		return Instruction{}, ErrInvalidInstruction
	}
	instruction := Instruction{Kind: InstructionKind(data[0])}
	switch instruction.Kind {
	case InstructionInitializeMint:
		if len(data) != 34 {
			return Instruction{}, ErrInvalidInstruction
		}
		instruction.Decimals = data[1]
		copy(instruction.Authority[:], data[2:34])
	case InstructionInitializeAccount:
		if len(data) != 33 {
			return Instruction{}, ErrInvalidInstruction
		}
		copy(instruction.Authority[:], data[1:33])
	case InstructionTransfer, InstructionMintTo, InstructionBurn, InstructionApprove:
		if len(data) != 9 {
			return Instruction{}, ErrInvalidInstruction
		}
		instruction.Amount = binary.LittleEndian.Uint64(data[1:9])
	case InstructionRevoke:
		if len(data) != 1 {
			return Instruction{}, ErrInvalidInstruction
		}
	case InstructionSetAuthority:
		if len(data) != 35 || data[2] > 1 {
			return Instruction{}, ErrInvalidInstruction
		}
		instruction.AuthorityType = AuthorityType(data[1])
		if instruction.AuthorityType != AuthorityMintTokens && instruction.AuthorityType != AuthorityAccountOwner {
			return Instruction{}, ErrInvalidInstruction
		}
		instruction.NewAuthority.Set = data[2] == 1
		copy(instruction.NewAuthority.Key[:], data[3:35])
		if !instruction.NewAuthority.Set && !allZero(instruction.NewAuthority.Key[:]) {
			return Instruction{}, ErrInvalidInstruction
		}
	default:
		return Instruction{}, ErrInvalidInstruction
	}
	return instruction, nil
}

func EncodeInitializeMint(decimals uint8, authority Pubkey) []byte {
	data := make([]byte, 34)
	data[0] = byte(InstructionInitializeMint)
	data[1] = decimals
	copy(data[2:], authority[:])
	return data
}

func EncodeInitializeAccount(owner Pubkey) []byte {
	data := make([]byte, 33)
	data[0] = byte(InstructionInitializeAccount)
	copy(data[1:], owner[:])
	return data
}

func EncodeAmountInstruction(kind InstructionKind, amount uint64) ([]byte, error) {
	switch kind {
	case InstructionTransfer, InstructionMintTo, InstructionBurn, InstructionApprove:
	default:
		return nil, ErrInvalidInstruction
	}
	data := make([]byte, 9)
	data[0] = byte(kind)
	binary.LittleEndian.PutUint64(data[1:], amount)
	return data, nil
}

func EncodeRevoke() []byte { return []byte{byte(InstructionRevoke)} }

func EncodeSetAuthority(kind AuthorityType, authority OptionalPubkey) ([]byte, error) {
	if kind != AuthorityMintTokens && kind != AuthorityAccountOwner {
		return nil, ErrInvalidInstruction
	}
	if kind == AuthorityAccountOwner && !authority.Set {
		return nil, ErrInvalidInstruction
	}
	if !authority.Set && !allZero(authority.Key[:]) {
		return nil, ErrInvalidInstruction
	}
	data := make([]byte, 35)
	data[0] = byte(InstructionSetAuthority)
	data[1] = byte(kind)
	if authority.Set {
		data[2] = 1
		copy(data[3:], authority.Key[:])
	}
	return data, nil
}
