package token

import (
	"encoding/binary"
	"math"
	"unicode/utf8"

	"github.com/ersanyakit/solanago/sdk"
)

type InstructionKind uint8

const (
	InitializeMintKind InstructionKind = iota
	InitializeAccountKind
	InitializeMultisigKind
	TransferKind
	ApproveKind
	RevokeKind
	SetAuthorityKind
	MintToKind
	BurnKind
	CloseAccountKind
	FreezeAccountKind
	ThawAccountKind
	TransferCheckedKind
	ApproveCheckedKind
	MintToCheckedKind
	BurnCheckedKind
	InitializeAccount2Kind
	SyncNativeKind
	InitializeAccount3Kind
	InitializeMultisig2Kind
	InitializeMint2Kind
	GetAccountDataSizeKind
	InitializeImmutableOwnerKind
	AmountToUIAmountKind
	UIAmountToAmountKind
	WithdrawExcessLamportsKind InstructionKind = 38
	UnwrapLamportsKind         InstructionKind = 45
	BatchKind                  InstructionKind = 255
)

type AuthorityType uint8

const (
	AuthorityMintTokens AuthorityType = iota
	AuthorityFreezeAccount
	AuthorityAccountOwner
	AuthorityCloseAccount
)

// InstructionData is the decoded classic TokenInstruction value. Only fields
// associated with Kind are serialized.
type InstructionData struct {
	Kind            InstructionKind
	Decimals        uint8
	M               uint8
	Amount          uint64
	MintAuthority   sdk.Pubkey
	FreezeAuthority OptionalPubkey
	AuthorityType   AuthorityType
	NewAuthority    OptionalPubkey
	Owner           sdk.Pubkey
	OptionalAmount  OptionalU64
	UIAmount        string
	BatchData       []byte
}

func EncodeInstructionData(value InstructionData) ([]byte, error) {
	switch value.Kind {
	case InitializeMintKind, InitializeMint2Kind:
		data := make([]byte, 0, 67)
		data = append(data, byte(value.Kind), value.Decimals)
		data = append(data, value.MintAuthority[:]...)
		data = appendInstructionPubkeyOption(data, value.FreezeAuthority)
		return data, nil
	case InitializeAccountKind, RevokeKind, CloseAccountKind, FreezeAccountKind, ThawAccountKind,
		SyncNativeKind, GetAccountDataSizeKind, InitializeImmutableOwnerKind,
		WithdrawExcessLamportsKind:
		return []byte{byte(value.Kind)}, nil
	case InitializeMultisigKind, InitializeMultisig2Kind:
		return []byte{byte(value.Kind), value.M}, nil
	case TransferKind, ApproveKind, MintToKind, BurnKind, AmountToUIAmountKind:
		return amountInstructionData(value.Kind, value.Amount), nil
	case SetAuthorityKind:
		if value.AuthorityType > AuthorityCloseAccount {
			return nil, ErrInvalidInstruction
		}
		data := []byte{byte(value.Kind), byte(value.AuthorityType)}
		return appendInstructionPubkeyOption(data, value.NewAuthority), nil
	case TransferCheckedKind, ApproveCheckedKind, MintToCheckedKind, BurnCheckedKind:
		data := amountInstructionData(value.Kind, value.Amount)
		return append(data, value.Decimals), nil
	case InitializeAccount2Kind, InitializeAccount3Kind:
		data := []byte{byte(value.Kind)}
		return append(data, value.Owner[:]...), nil
	case UIAmountToAmountKind:
		if !utf8.ValidString(value.UIAmount) {
			return nil, ErrInvalidInstruction
		}
		data := []byte{byte(value.Kind)}
		return append(data, []byte(value.UIAmount)...), nil
	case UnwrapLamportsKind:
		data := []byte{byte(value.Kind)}
		if !value.OptionalAmount.Set {
			return append(data, 0), nil
		}
		data = append(data, 1)
		var amount [8]byte
		binary.LittleEndian.PutUint64(amount[:], value.OptionalAmount.Value)
		return append(data, amount[:]...), nil
	case BatchKind:
		return append([]byte{byte(value.Kind)}, value.BatchData...), nil
	default:
		return nil, ErrInvalidInstruction
	}
}

func DecodeInstructionData(data []byte) (InstructionData, error) {
	if len(data) == 0 {
		return InstructionData{}, ErrInvalidInstruction
	}
	value := InstructionData{Kind: InstructionKind(data[0])}
	rest := data[1:]
	switch value.Kind {
	case InitializeMintKind, InitializeMint2Kind:
		if len(rest) < 34 {
			return InstructionData{}, ErrInvalidInstruction
		}
		value.Decimals = rest[0]
		copy(value.MintAuthority[:], rest[1:33])
		var err error
		value.FreezeAuthority, rest, err = consumeInstructionPubkeyOption(rest[33:])
		if err != nil {
			return InstructionData{}, err
		}
	case InitializeAccountKind, RevokeKind, CloseAccountKind, FreezeAccountKind, ThawAccountKind,
		SyncNativeKind, GetAccountDataSizeKind, InitializeImmutableOwnerKind,
		WithdrawExcessLamportsKind:
	case InitializeMultisigKind, InitializeMultisig2Kind:
		if len(rest) != 1 {
			return InstructionData{}, ErrInvalidInstruction
		}
		value.M, rest = rest[0], rest[1:]
	case TransferKind, ApproveKind, MintToKind, BurnKind, AmountToUIAmountKind:
		if len(rest) != 8 {
			return InstructionData{}, ErrInvalidInstruction
		}
		value.Amount, rest = binary.LittleEndian.Uint64(rest), rest[8:]
	case SetAuthorityKind:
		if len(rest) < 2 || AuthorityType(rest[0]) > AuthorityCloseAccount {
			return InstructionData{}, ErrInvalidInstruction
		}
		value.AuthorityType = AuthorityType(rest[0])
		var err error
		value.NewAuthority, rest, err = consumeInstructionPubkeyOption(rest[1:])
		if err != nil {
			return InstructionData{}, err
		}
	case TransferCheckedKind, ApproveCheckedKind, MintToCheckedKind, BurnCheckedKind:
		if len(rest) != 9 {
			return InstructionData{}, ErrInvalidInstruction
		}
		value.Amount = binary.LittleEndian.Uint64(rest[:8])
		value.Decimals, rest = rest[8], rest[9:]
	case InitializeAccount2Kind, InitializeAccount3Kind:
		if len(rest) != 32 {
			return InstructionData{}, ErrInvalidInstruction
		}
		copy(value.Owner[:], rest)
		rest = rest[32:]
	case UIAmountToAmountKind:
		if !utf8.Valid(rest) {
			return InstructionData{}, ErrInvalidInstruction
		}
		value.UIAmount = string(rest)
		rest = nil
	case UnwrapLamportsKind:
		if len(rest) == 1 && rest[0] == 0 {
			rest = rest[1:]
		} else if len(rest) == 9 && rest[0] == 1 {
			value.OptionalAmount = SomeU64(binary.LittleEndian.Uint64(rest[1:]))
			rest = rest[9:]
		} else {
			return InstructionData{}, ErrInvalidInstruction
		}
	case BatchKind:
		value.BatchData = append([]byte(nil), rest...)
		rest = nil
	default:
		return InstructionData{}, ErrInvalidInstruction
	}
	if len(rest) != 0 {
		return InstructionData{}, ErrInvalidInstruction
	}
	return value, nil
}

func InitializeMint(mint, mintAuthority sdk.Pubkey, freezeAuthority OptionalPubkey, decimals uint8) sdk.Instruction {
	return build(InstructionData{Kind: InitializeMintKind, Decimals: decimals, MintAuthority: mintAuthority, FreezeAuthority: freezeAuthority},
		sdk.Writable(mint, false), sdk.Readonly(RentSysvar, false))
}

func InitializeMint2(mint, mintAuthority sdk.Pubkey, freezeAuthority OptionalPubkey, decimals uint8) sdk.Instruction {
	return build(InstructionData{Kind: InitializeMint2Kind, Decimals: decimals, MintAuthority: mintAuthority, FreezeAuthority: freezeAuthority}, sdk.Writable(mint, false))
}

func InitializeAccount(account, mint, owner sdk.Pubkey) sdk.Instruction {
	return build(InstructionData{Kind: InitializeAccountKind}, sdk.Writable(account, false), sdk.Readonly(mint, false), sdk.Readonly(owner, false), sdk.Readonly(RentSysvar, false))
}

func InitializeAccount2(account, mint, owner sdk.Pubkey) sdk.Instruction {
	return build(InstructionData{Kind: InitializeAccount2Kind, Owner: owner}, sdk.Writable(account, false), sdk.Readonly(mint, false), sdk.Readonly(RentSysvar, false))
}

func InitializeAccount3(account, mint, owner sdk.Pubkey) sdk.Instruction {
	return build(InstructionData{Kind: InitializeAccount3Kind, Owner: owner}, sdk.Writable(account, false), sdk.Readonly(mint, false))
}

func InitializeMultisig(multisig sdk.Pubkey, signers []sdk.Pubkey, m uint8) (sdk.Instruction, error) {
	return initializeMultisig(InitializeMultisigKind, multisig, signers, m, true)
}

func InitializeMultisig2(multisig sdk.Pubkey, signers []sdk.Pubkey, m uint8) (sdk.Instruction, error) {
	return initializeMultisig(InitializeMultisig2Kind, multisig, signers, m, false)
}

func Transfer(source, destination, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(source, false), sdk.Writable(destination, false)}, authority, signers)
	return buildResult(InstructionData{Kind: TransferKind, Amount: amount}, accounts, err)
}

func Approve(source, delegate, owner sdk.Pubkey, signers []sdk.Pubkey, amount uint64) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(source, false), sdk.Readonly(delegate, false)}, owner, signers)
	return buildResult(InstructionData{Kind: ApproveKind, Amount: amount}, accounts, err)
}

func Revoke(source, owner sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(source, false)}, owner, signers)
	return buildResult(InstructionData{Kind: RevokeKind}, accounts, err)
}

func SetAuthority(owned, currentAuthority sdk.Pubkey, signers []sdk.Pubkey, authorityType AuthorityType, newAuthority OptionalPubkey) (sdk.Instruction, error) {
	if authorityType > AuthorityCloseAccount {
		return sdk.Instruction{}, ErrInvalidInstruction
	}
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(owned, false)}, currentAuthority, signers)
	return buildResult(InstructionData{Kind: SetAuthorityKind, AuthorityType: authorityType, NewAuthority: newAuthority}, accounts, err)
}

func MintTo(mint, account, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(mint, false), sdk.Writable(account, false)}, authority, signers)
	return buildResult(InstructionData{Kind: MintToKind, Amount: amount}, accounts, err)
}

func Burn(account, mint, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(account, false), sdk.Writable(mint, false)}, authority, signers)
	return buildResult(InstructionData{Kind: BurnKind, Amount: amount}, accounts, err)
}

func CloseAccount(account, destination, authority sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(account, false), sdk.Writable(destination, false)}, authority, signers)
	return buildResult(InstructionData{Kind: CloseAccountKind}, accounts, err)
}

func FreezeAccount(account, mint, authority sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(account, false), sdk.Readonly(mint, false)}, authority, signers)
	return buildResult(InstructionData{Kind: FreezeAccountKind}, accounts, err)
}

func ThawAccount(account, mint, authority sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(account, false), sdk.Readonly(mint, false)}, authority, signers)
	return buildResult(InstructionData{Kind: ThawAccountKind}, accounts, err)
}

func TransferChecked(source, mint, destination, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64, decimals uint8) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(source, false), sdk.Readonly(mint, false), sdk.Writable(destination, false)}, authority, signers)
	return buildResult(InstructionData{Kind: TransferCheckedKind, Amount: amount, Decimals: decimals}, accounts, err)
}

func ApproveChecked(source, mint, delegate, owner sdk.Pubkey, signers []sdk.Pubkey, amount uint64, decimals uint8) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(source, false), sdk.Readonly(mint, false), sdk.Readonly(delegate, false)}, owner, signers)
	return buildResult(InstructionData{Kind: ApproveCheckedKind, Amount: amount, Decimals: decimals}, accounts, err)
}

func MintToChecked(mint, account, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64, decimals uint8) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(mint, false), sdk.Writable(account, false)}, authority, signers)
	return buildResult(InstructionData{Kind: MintToCheckedKind, Amount: amount, Decimals: decimals}, accounts, err)
}

func BurnChecked(account, mint, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64, decimals uint8) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(account, false), sdk.Writable(mint, false)}, authority, signers)
	return buildResult(InstructionData{Kind: BurnCheckedKind, Amount: amount, Decimals: decimals}, accounts, err)
}

func SyncNative(account sdk.Pubkey) sdk.Instruction {
	return build(InstructionData{Kind: SyncNativeKind}, sdk.Writable(account, false))
}

func SyncNativeWithRentSysvar(account sdk.Pubkey) sdk.Instruction {
	return build(InstructionData{Kind: SyncNativeKind}, sdk.Writable(account, false), sdk.Readonly(RentSysvar, false))
}

func GetAccountDataSize(mint sdk.Pubkey) sdk.Instruction {
	return build(InstructionData{Kind: GetAccountDataSizeKind}, sdk.Readonly(mint, false))
}

func InitializeImmutableOwner(account sdk.Pubkey) sdk.Instruction {
	return build(InstructionData{Kind: InitializeImmutableOwnerKind}, sdk.Writable(account, false))
}

func AmountToUIAmount(mint sdk.Pubkey, amount uint64) sdk.Instruction {
	return build(InstructionData{Kind: AmountToUIAmountKind, Amount: amount}, sdk.Readonly(mint, false))
}

func UIAmountToAmount(mint sdk.Pubkey, uiAmount string) (sdk.Instruction, error) {
	if !utf8.ValidString(uiAmount) {
		return sdk.Instruction{}, ErrInvalidInstruction
	}
	return build(InstructionData{Kind: UIAmountToAmountKind, UIAmount: uiAmount}, sdk.Readonly(mint, false)), nil
}

func WithdrawExcessLamports(account, destination, authority sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(account, false), sdk.Writable(destination, false)}, authority, signers)
	return buildResult(InstructionData{Kind: WithdrawExcessLamportsKind}, accounts, err)
}

func UnwrapLamports(account, destination, authority sdk.Pubkey, signers []sdk.Pubkey, amount OptionalU64) (sdk.Instruction, error) {
	accounts, err := withAuthority([]sdk.AccountMeta{sdk.Writable(account, false), sdk.Writable(destination, false)}, authority, signers)
	return buildResult(InstructionData{Kind: UnwrapLamportsKind, OptionalAmount: amount}, accounts, err)
}

func Batch(instructions []sdk.Instruction) (sdk.Instruction, error) {
	data := []byte{byte(BatchKind)}
	var accounts []sdk.AccountMeta
	for _, child := range instructions {
		if child.ProgramID != ProgramID {
			return sdk.Instruction{}, ErrIncorrectProgramID
		}
		if len(child.Accounts) > math.MaxUint8 || len(child.Data) > math.MaxUint8 || (len(child.Data) > 0 && child.Data[0] == byte(BatchKind)) {
			return sdk.Instruction{}, ErrInvalidInstruction
		}
		data = append(data, byte(len(child.Accounts)), byte(len(child.Data)))
		data = append(data, child.Data...)
		accounts = append(accounts, child.Accounts...)
	}
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}, nil
}

func initializeMultisig(kind InstructionKind, multisig sdk.Pubkey, signers []sdk.Pubkey, m uint8, includeRent bool) (sdk.Instruction, error) {
	if len(signers) < MinSigners || len(signers) > MaxSigners || int(m) < MinSigners || int(m) > len(signers) {
		return sdk.Instruction{}, ErrInvalidSigners
	}
	accounts := []sdk.AccountMeta{sdk.Writable(multisig, false)}
	if includeRent {
		accounts = append(accounts, sdk.Readonly(RentSysvar, false))
	}
	for _, signer := range signers {
		accounts = append(accounts, sdk.Readonly(signer, false))
	}
	return build(InstructionData{Kind: kind, M: m}, accounts...), nil
}

func withAuthority(accounts []sdk.AccountMeta, authority sdk.Pubkey, signers []sdk.Pubkey) ([]sdk.AccountMeta, error) {
	if len(signers) > MaxSigners {
		return nil, ErrInvalidSigners
	}
	accounts = append(accounts, sdk.Readonly(authority, len(signers) == 0))
	for _, signer := range signers {
		accounts = append(accounts, sdk.Readonly(signer, true))
	}
	return accounts, nil
}

func build(value InstructionData, accounts ...sdk.AccountMeta) sdk.Instruction {
	data, err := EncodeInstructionData(value)
	if err != nil {
		panic(err)
	}
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}
}

func buildResult(value InstructionData, accounts []sdk.AccountMeta, err error) (sdk.Instruction, error) {
	if err != nil {
		return sdk.Instruction{}, err
	}
	data, err := EncodeInstructionData(value)
	if err != nil {
		return sdk.Instruction{}, err
	}
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}, nil
}

func amountInstructionData(kind InstructionKind, amount uint64) []byte {
	data := make([]byte, 9)
	data[0] = byte(kind)
	binary.LittleEndian.PutUint64(data[1:], amount)
	return data
}

func appendInstructionPubkeyOption(data []byte, value OptionalPubkey) []byte {
	if !value.Set {
		return append(data, 0)
	}
	data = append(data, 1)
	return append(data, value.Value[:]...)
}

func consumeInstructionPubkeyOption(data []byte) (OptionalPubkey, []byte, error) {
	if len(data) < 1 {
		return OptionalPubkey{}, nil, ErrInvalidInstruction
	}
	switch data[0] {
	case 0:
		return OptionalPubkey{}, data[1:], nil
	case 1:
		if len(data) < 33 {
			return OptionalPubkey{}, nil, ErrInvalidInstruction
		}
		var key sdk.Pubkey
		copy(key[:], data[1:33])
		return SomePubkey(key), data[33:], nil
	default:
		return OptionalPubkey{}, nil, ErrInvalidInstruction
	}
}
