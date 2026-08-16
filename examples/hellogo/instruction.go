package hellogo

import (
	"encoding/binary"

	"github.com/ersanyakit/go-solana/sdk"
)

// DecodeInstruction verifies exact lengths and canonical optional-authority
// bytes. Account keys and privileges are always taken from the transaction.
func DecodeInstruction(data []byte) (Instruction, error) {
	if len(data) == 0 {
		return Instruction{}, ErrInvalidInstructionEncoding
	}
	instruction := Instruction{Kind: InstructionKind(data[0])}
	switch instruction.Kind {
	case InstructionInitializeMint:
		if len(data) != 34 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		instruction.Decimals = data[1]
		copy(instruction.Authority[:], data[2:34])
	case InstructionInitializeAccount:
		if len(data) != 33 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		copy(instruction.Authority[:], data[1:33])
	case InstructionTransfer, InstructionMintTo, InstructionBurn, InstructionApprove:
		if len(data) != 9 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		instruction.Amount = binary.LittleEndian.Uint64(data[1:9])
	case InstructionRevoke:
		if len(data) != 1 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
	case InstructionSetAuthority:
		if len(data) != 35 || data[1] > byte(AuthorityAccountOwner) || data[2] > 1 {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		instruction.AuthorityType = AuthorityType(data[1])
		instruction.NewAuthority.Set = data[2] == 1
		copy(instruction.NewAuthority.Key[:], data[3:35])
		if !instruction.NewAuthority.Set && !allZero(instruction.NewAuthority.Key[:]) {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
		if instruction.AuthorityType == AuthorityAccountOwner && !instruction.NewAuthority.Set {
			return Instruction{}, ErrInvalidInstructionEncoding
		}
	default:
		return Instruction{}, ErrInvalidInstructionEncoding
	}
	return instruction, nil
}

func EncodeInitializeMint(decimals uint8, authority sdk.Pubkey) []byte {
	data := make([]byte, 34)
	data[0] = byte(InstructionInitializeMint)
	data[1] = decimals
	copy(data[2:], authority[:])
	return data
}

func EncodeInitializeAccount(owner sdk.Pubkey) []byte {
	data := make([]byte, 33)
	data[0] = byte(InstructionInitializeAccount)
	copy(data[1:], owner[:])
	return data
}

func EncodeAmountInstruction(kind InstructionKind, amount uint64) ([]byte, error) {
	switch kind {
	case InstructionTransfer, InstructionMintTo, InstructionBurn, InstructionApprove:
	default:
		return nil, ErrInvalidInstructionEncoding
	}
	data := make([]byte, 9)
	data[0] = byte(kind)
	binary.LittleEndian.PutUint64(data[1:], amount)
	return data, nil
}

func EncodeRevoke() []byte { return []byte{byte(InstructionRevoke)} }

func EncodeSetAuthority(kind AuthorityType, authority OptionalPubkey) ([]byte, error) {
	if kind != AuthorityMintTokens && kind != AuthorityAccountOwner {
		return nil, ErrInvalidInstructionEncoding
	}
	if kind == AuthorityAccountOwner && !authority.Set {
		return nil, ErrInvalidInstructionEncoding
	}
	if !authority.Set && !allZero(authority.Key[:]) {
		return nil, ErrInvalidInstructionEncoding
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

func InitializeMint(programID, mint, authority sdk.Pubkey, decimals uint8) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts:  []sdk.AccountMeta{sdk.Writable(mint, true)},
		Data:      EncodeInitializeMint(decimals, authority),
	}
}

func InitializeAccount(programID, account, mint, owner sdk.Pubkey) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(account, true),
			sdk.Readonly(mint, false),
		},
		Data: EncodeInitializeAccount(owner),
	}
}

func Transfer(programID, source, destination, authority sdk.Pubkey, amount uint64) sdk.Instruction {
	return amountInstruction(programID, InstructionTransfer, amount,
		sdk.Writable(source, false), sdk.Writable(destination, false), sdk.Readonly(authority, true))
}

func MintTo(programID, mint, destination, authority sdk.Pubkey, amount uint64) sdk.Instruction {
	return amountInstruction(programID, InstructionMintTo, amount,
		sdk.Writable(mint, false), sdk.Writable(destination, false), sdk.Readonly(authority, true))
}

func Burn(programID, source, mint, authority sdk.Pubkey, amount uint64) sdk.Instruction {
	return amountInstruction(programID, InstructionBurn, amount,
		sdk.Writable(source, false), sdk.Writable(mint, false), sdk.Readonly(authority, true))
}

func Approve(programID, source, owner, delegate sdk.Pubkey, amount uint64) sdk.Instruction {
	return amountInstruction(programID, InstructionApprove, amount,
		sdk.Writable(source, false), sdk.Readonly(owner, true), sdk.Readonly(delegate, false))
}

func Revoke(programID, source, owner sdk.Pubkey) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: programID,
		Accounts:  []sdk.AccountMeta{sdk.Writable(source, false), sdk.Readonly(owner, true)},
		Data:      EncodeRevoke(),
	}
}

func SetAuthority(programID, target, currentAuthority sdk.Pubkey, kind AuthorityType, authority OptionalPubkey) (sdk.Instruction, error) {
	data, err := EncodeSetAuthority(kind, authority)
	if err != nil {
		return sdk.Instruction{}, err
	}
	return sdk.Instruction{
		ProgramID: programID,
		Accounts:  []sdk.AccountMeta{sdk.Writable(target, false), sdk.Readonly(currentAuthority, true)},
		Data:      data,
	}, nil
}

func amountInstruction(programID sdk.Pubkey, kind InstructionKind, amount uint64, accounts ...sdk.AccountMeta) sdk.Instruction {
	data, err := EncodeAmountInstruction(kind, amount)
	if err != nil {
		panic(err) // callers above pass only compile-time valid discriminants
	}
	return sdk.Instruction{ProgramID: programID, Accounts: accounts, Data: data}
}
