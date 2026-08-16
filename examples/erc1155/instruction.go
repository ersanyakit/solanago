package erc1155

import (
	"encoding/binary"

	"github.com/ersanyakit/solanago/sdk"
)

// DecodeInstruction verifies exact lengths for every fixed-size instruction
// and validates the URI length for the one variable-size instruction
// (CreateTokenType). Account keys and privileges are always taken from the
// transaction, never from instruction data.
func DecodeInstruction(data []byte) (Instruction, error) {
	if len(data) == 0 {
		return Instruction{}, ErrInvalidInstructionEncoding
	}
	instruction := Instruction{Kind: InstructionKind(data[0])}
	switch instruction.Kind {
	case InstructionInitializeCollection:
		if len(data) != 33 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		copy(instruction.Authority[:], data[1:33])
	case InstructionCreateTokenType:
		if len(data) < 5 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		uriLen := binary.LittleEndian.Uint32(data[1:5])
		if uriLen > MaxURILength || len(data) != int(5+uriLen) {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		instruction.URI = string(data[5:])
	case InstructionInitializeBalance:
		if len(data) != 33 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		copy(instruction.Owner[:], data[1:33])
	case InstructionMintTo, InstructionBurn, InstructionTransfer, InstructionTransferFrom:
		if len(data) != 9 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		instruction.Amount = binary.LittleEndian.Uint64(data[1:9])
	case InstructionInitializeApproval:
		if len(data) != 34 || data[33] > 1 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		copy(instruction.Collection[:], data[1:33])
		instruction.Approved = data[33] == 1
	case InstructionSetApproval:
		if len(data) != 2 || data[1] > 1 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		instruction.Approved = data[1] == 1
	default:
		return Instruction{}, ErrInvalidInstructionEncoding
	}
	return instruction, nil
}

func EncodeInitializeCollection(authority sdk.Pubkey) []byte {
	data := make([]byte, 33)
	data[0] = byte(InstructionInitializeCollection)
	copy(data[1:], authority[:])
	return data
}

func EncodeCreateTokenType(uri string) ([]byte, error) {
	if len(uri) > MaxURILength {
		return nil, ErrURITooLong
	}
	data := make([]byte, 5+len(uri))
	data[0] = byte(InstructionCreateTokenType)
	binary.LittleEndian.PutUint32(data[1:5], uint32(len(uri)))
	copy(data[5:], uri)
	return data, nil
}

func EncodeInitializeBalance(owner sdk.Pubkey) []byte {
	data := make([]byte, 33)
	data[0] = byte(InstructionInitializeBalance)
	copy(data[1:], owner[:])
	return data
}

func encodeAmountInstruction(kind InstructionKind, amount uint64) []byte {
	data := make([]byte, 9)
	data[0] = byte(kind)
	binary.LittleEndian.PutUint64(data[1:], amount)
	return data
}

func EncodeMintTo(amount uint64) []byte { return encodeAmountInstruction(InstructionMintTo, amount) }
func EncodeBurn(amount uint64) []byte   { return encodeAmountInstruction(InstructionBurn, amount) }
func EncodeTransfer(amount uint64) []byte {
	return encodeAmountInstruction(InstructionTransfer, amount)
}
func EncodeTransferFrom(amount uint64) []byte {
	return encodeAmountInstruction(InstructionTransferFrom, amount)
}

func EncodeInitializeApproval(collection sdk.Pubkey, approved bool) []byte {
	data := make([]byte, 34)
	data[0] = byte(InstructionInitializeApproval)
	copy(data[1:33], collection[:])
	if approved {
		data[33] = 1
	}
	return data
}

func EncodeSetApproval(approved bool) []byte {
	data := []byte{byte(InstructionSetApproval), 0}
	if approved {
		data[1] = 1
	}
	return data
}

// InitializeCollection creates a new collection ("contract instance").
// collection must be a fresh, program-owned, exact-CollectionStateSize
// account and must sign.
func InitializeCollection(programID, collection, authority sdk.Pubkey) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts:  []sdk.AccountMeta{sdk.Writable(collection, true)},
		Data:      EncodeInitializeCollection(authority),
	}
}

// CreateTokenType defines a new token id under collection, assigning it
// collection's current next_id and incrementing that counter. tokenType
// must be a fresh, program-owned, exact-TokenTypeStateSize account and must
// sign; authority must match collection's stored authority.
func CreateTokenType(programID, tokenType, collection, authority sdk.Pubkey, uri string) (sdk.Instruction, error) {
	data, err := EncodeCreateTokenType(uri)
	if err != nil {
		return sdk.Instruction{}, err
	}
	return sdk.Instruction{
		ProgramID: programID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(tokenType, true),
			sdk.Writable(collection, false),
			sdk.Readonly(authority, true),
		},
		Data: data,
	}, nil
}

// InitializeBalance creates a zeroed balance for (tokenType.ID, owner).
// balance must be a fresh, program-owned, exact-BalanceStateSize account
// and must sign.
func InitializeBalance(programID, balance, tokenType, owner sdk.Pubkey) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(balance, true),
			sdk.Readonly(tokenType, false),
		},
		Data: EncodeInitializeBalance(owner),
	}
}

// MintTo increases tokenType's supply and balance's amount by amount.
// authority must match collection's stored authority and sign.
func MintTo(programID, collection, tokenType, balance, authority sdk.Pubkey, amount uint64) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts: []sdk.AccountMeta{
			sdk.Readonly(collection, false),
			sdk.Writable(tokenType, false),
			sdk.Writable(balance, false),
			sdk.Readonly(authority, true),
		},
		Data: EncodeMintTo(amount),
	}
}

// Burn decreases tokenType's supply and balance's amount by amount. owner
// must match balance's stored owner and sign.
func Burn(programID, tokenType, balance, owner sdk.Pubkey, amount uint64) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(tokenType, false),
			sdk.Writable(balance, false),
			sdk.Readonly(owner, true),
		},
		Data: EncodeBurn(amount),
	}
}

// Transfer moves amount from source to destination, both owned by the
// signing owner. Batch transfers are just multiple Transfer instructions in
// one transaction — see README.md.
func Transfer(programID, source, destination, owner sdk.Pubkey, amount uint64) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(source, false),
			sdk.Writable(destination, false),
			sdk.Readonly(owner, true),
		},
		Data: EncodeTransfer(amount),
	}
}

// InitializeApproval creates a new approve-for-all record for
// (owner, operator) under collection. approval must be a fresh,
// program-owned, exact-ApprovalStateSize account and must sign; owner must
// also sign.
func InitializeApproval(programID, approval, owner, operator, collection sdk.Pubkey, approved bool) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(approval, true),
			sdk.Readonly(owner, true),
			sdk.Readonly(operator, false),
		},
		Data: EncodeInitializeApproval(collection, approved),
	}
}

// SetApproval flips an existing approval's approved flag. owner must match
// the approval's stored owner and sign.
func SetApproval(programID, approval, owner sdk.Pubkey, approved bool) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(approval, false),
			sdk.Readonly(owner, true),
		},
		Data: EncodeSetApproval(approved),
	}
}

// TransferFrom moves amount from source to destination on behalf of
// source's owner, authorized by a matching, approved Approval account.
// operator must match the approval's stored operator and sign.
func TransferFrom(programID, source, destination, approval, operator sdk.Pubkey, amount uint64) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(source, false),
			sdk.Writable(destination, false),
			sdk.Readonly(approval, false),
			sdk.Readonly(operator, true),
		},
		Data: EncodeTransferFrom(amount),
	}
}
