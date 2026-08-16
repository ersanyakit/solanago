package system

import (
	"encoding/binary"
	"math"
	"unicode/utf8"

	"github.com/ersanyakit/solanago/sdk"
)

func CreateAccount(payer, newAccount sdk.Pubkey, lamports, space uint64, owner sdk.Pubkey) sdk.Instruction {
	data := make([]byte, 4+8+8+32)
	putKind(data, CreateAccountKind)
	binary.LittleEndian.PutUint64(data[4:12], lamports)
	binary.LittleEndian.PutUint64(data[12:20], space)
	copy(data[20:], owner[:])
	return instruction(data, sdk.Writable(payer, true), sdk.Writable(newAccount, true))
}

func Assign(account, owner sdk.Pubkey) sdk.Instruction {
	data := make([]byte, 4+32)
	putKind(data, AssignKind)
	copy(data[4:], owner[:])
	return instruction(data, sdk.Writable(account, true))
}

func Transfer(source, destination sdk.Pubkey, lamports uint64) sdk.Instruction {
	data := amountData(TransferKind, lamports)
	return instruction(data, sdk.Writable(source, true), sdk.Writable(destination, false))
}

func CreateAccountWithSeed(payer, newAccount, base sdk.Pubkey, seed string, lamports, space uint64, owner sdk.Pubkey) (sdk.Instruction, error) {
	if !utf8.ValidString(seed) {
		return sdk.Instruction{}, sdk.ErrInvalidSeed
	}
	if len(seed) > sdk.MaxSeedLength {
		return sdk.Instruction{}, sdk.ErrMaxSeedLength
	}
	data := make([]byte, 4+32+8+len(seed)+8+8+32)
	putKind(data, CreateAccountWithSeedKind)
	offset := 4
	copy(data[offset:], base[:])
	offset += 32
	offset = putString(data, offset, seed)
	binary.LittleEndian.PutUint64(data[offset:], lamports)
	offset += 8
	binary.LittleEndian.PutUint64(data[offset:], space)
	offset += 8
	copy(data[offset:], owner[:])
	accounts := []sdk.AccountMeta{sdk.Writable(payer, true), sdk.Writable(newAccount, false)}
	if base != payer {
		accounts = append(accounts, sdk.Readonly(base, true))
	}
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}, nil
}

func AdvanceNonceAccount(nonceAccount, nonceAuthority sdk.Pubkey) sdk.Instruction {
	return instruction(kindData(AdvanceNonceAccountKind),
		sdk.Writable(nonceAccount, false),
		sdk.Readonly(RecentBlockhashesSysvar, false),
		sdk.Readonly(nonceAuthority, true),
	)
}

func WithdrawNonceAccount(nonceAccount, recipient, nonceAuthority sdk.Pubkey, lamports uint64) sdk.Instruction {
	return instruction(amountData(WithdrawNonceAccountKind, lamports),
		sdk.Writable(nonceAccount, false),
		sdk.Writable(recipient, false),
		sdk.Readonly(RecentBlockhashesSysvar, false),
		sdk.Readonly(RentSysvar, false),
		sdk.Readonly(nonceAuthority, true),
	)
}

func InitializeNonceAccount(nonceAccount, nonceAuthority sdk.Pubkey) sdk.Instruction {
	data := make([]byte, 4+32)
	putKind(data, InitializeNonceAccountKind)
	copy(data[4:], nonceAuthority[:])
	return instruction(data,
		sdk.Writable(nonceAccount, false),
		sdk.Readonly(RecentBlockhashesSysvar, false),
		sdk.Readonly(RentSysvar, false),
	)
}

func AuthorizeNonceAccount(nonceAccount, nonceAuthority, newAuthority sdk.Pubkey) sdk.Instruction {
	data := make([]byte, 4+32)
	putKind(data, AuthorizeNonceAccountKind)
	copy(data[4:], newAuthority[:])
	return instruction(data, sdk.Writable(nonceAccount, false), sdk.Readonly(nonceAuthority, true))
}

func Allocate(account sdk.Pubkey, space uint64) sdk.Instruction {
	return instruction(amountData(AllocateKind, space), sdk.Writable(account, true))
}

func AllocateWithSeed(account, base sdk.Pubkey, seed string, space uint64, owner sdk.Pubkey) (sdk.Instruction, error) {
	data, err := seededData(AllocateWithSeedKind, base, seed, &space, owner)
	if err != nil {
		return sdk.Instruction{}, err
	}
	return instruction(data, sdk.Writable(account, false), sdk.Readonly(base, true)), nil
}

func AssignWithSeed(account, base sdk.Pubkey, seed string, owner sdk.Pubkey) (sdk.Instruction, error) {
	data, err := seededData(AssignWithSeedKind, base, seed, nil, owner)
	if err != nil {
		return sdk.Instruction{}, err
	}
	return instruction(data, sdk.Writable(account, false), sdk.Readonly(base, true)), nil
}

func TransferWithSeed(source, base, destination sdk.Pubkey, fromSeed string, fromOwner sdk.Pubkey, lamports uint64) (sdk.Instruction, error) {
	if !utf8.ValidString(fromSeed) {
		return sdk.Instruction{}, sdk.ErrInvalidSeed
	}
	if len(fromSeed) > sdk.MaxSeedLength {
		return sdk.Instruction{}, sdk.ErrMaxSeedLength
	}
	data := make([]byte, 4+8+8+len(fromSeed)+32)
	putKind(data, TransferWithSeedKind)
	binary.LittleEndian.PutUint64(data[4:12], lamports)
	offset := putString(data, 12, fromSeed)
	copy(data[offset:], fromOwner[:])
	return instruction(data,
		sdk.Writable(source, false),
		sdk.Readonly(base, true),
		sdk.Writable(destination, false),
	), nil
}

func UpgradeNonceAccount(nonceAccount sdk.Pubkey) sdk.Instruction {
	return instruction(kindData(UpgradeNonceAccountKind), sdk.Writable(nonceAccount, false))
}

// CreateAccountAllowPrefund is the current discriminator-13 System Program
// instruction. A nil payer omits the optional payer account.
func CreateAccountAllowPrefund(newAccount sdk.Pubkey, payer *sdk.Pubkey, lamports, space uint64, owner sdk.Pubkey) sdk.Instruction {
	data := make([]byte, 4+8+8+32)
	putKind(data, CreateAccountAllowPrefundKind)
	binary.LittleEndian.PutUint64(data[4:12], lamports)
	binary.LittleEndian.PutUint64(data[12:20], space)
	copy(data[20:], owner[:])
	accounts := []sdk.AccountMeta{sdk.Writable(newAccount, true)}
	if payer != nil {
		accounts = append(accounts, sdk.Writable(*payer, true))
	}
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}
}

func DecodeInstructionData(data []byte) (DecodedInstruction, error) {
	if len(data) < 4 {
		return DecodedInstruction{}, ErrInvalidInstruction
	}
	decoded := DecodedInstruction{Kind: InstructionKind(binary.LittleEndian.Uint32(data[:4]))}
	rest := data[4:]
	var err error
	switch decoded.Kind {
	case CreateAccountKind, CreateAccountAllowPrefundKind:
		if len(rest) != 48 {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
		decoded.Lamports = binary.LittleEndian.Uint64(rest[:8])
		decoded.Space = binary.LittleEndian.Uint64(rest[8:16])
		copy(decoded.Owner[:], rest[16:48])
	case AssignKind:
		if len(rest) != 32 {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
		copy(decoded.Owner[:], rest)
	case TransferKind, WithdrawNonceAccountKind:
		if len(rest) != 8 {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
		decoded.Lamports = binary.LittleEndian.Uint64(rest)
	case CreateAccountWithSeedKind:
		decoded.Base, decoded.Seed, rest, err = readBaseSeed(rest)
		if err != nil || len(rest) != 48 {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
		decoded.Lamports = binary.LittleEndian.Uint64(rest[:8])
		decoded.Space = binary.LittleEndian.Uint64(rest[8:16])
		copy(decoded.Owner[:], rest[16:])
	case AdvanceNonceAccountKind, UpgradeNonceAccountKind:
		if len(rest) != 0 {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
	case InitializeNonceAccountKind, AuthorizeNonceAccountKind:
		if len(rest) != 32 {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
		copy(decoded.NewAuthority[:], rest)
	case AllocateKind:
		if len(rest) != 8 {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
		decoded.Space = binary.LittleEndian.Uint64(rest)
	case AllocateWithSeedKind, AssignWithSeedKind:
		decoded.Base, decoded.Seed, rest, err = readBaseSeed(rest)
		if err != nil {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
		if decoded.Kind == AllocateWithSeedKind {
			if len(rest) != 40 {
				return DecodedInstruction{}, ErrInvalidInstruction
			}
			decoded.Space = binary.LittleEndian.Uint64(rest[:8])
			rest = rest[8:]
		} else if len(rest) != 32 {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
		copy(decoded.Owner[:], rest)
	case TransferWithSeedKind:
		if len(rest) < 8 {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
		decoded.Lamports = binary.LittleEndian.Uint64(rest[:8])
		decoded.Seed, rest, err = readString(rest[8:])
		if err != nil || len(rest) != 32 {
			return DecodedInstruction{}, ErrInvalidInstruction
		}
		copy(decoded.Owner[:], rest)
	default:
		return DecodedInstruction{}, ErrInvalidInstruction
	}
	return decoded, nil
}

func instruction(data []byte, accounts ...sdk.AccountMeta) sdk.Instruction {
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}
}

func kindData(kind InstructionKind) []byte {
	data := make([]byte, 4)
	putKind(data, kind)
	return data
}

func amountData(kind InstructionKind, amount uint64) []byte {
	data := make([]byte, 12)
	putKind(data, kind)
	binary.LittleEndian.PutUint64(data[4:], amount)
	return data
}

func putKind(data []byte, kind InstructionKind) { binary.LittleEndian.PutUint32(data, uint32(kind)) }

func putString(data []byte, offset int, value string) int {
	binary.LittleEndian.PutUint64(data[offset:offset+8], uint64(len(value)))
	copy(data[offset+8:], value)
	return offset + 8 + len(value)
}

func seededData(kind InstructionKind, base sdk.Pubkey, seed string, space *uint64, owner sdk.Pubkey) ([]byte, error) {
	if !utf8.ValidString(seed) {
		return nil, sdk.ErrInvalidSeed
	}
	if len(seed) > sdk.MaxSeedLength {
		return nil, sdk.ErrMaxSeedLength
	}
	extra := 0
	if space != nil {
		extra = 8
	}
	data := make([]byte, 4+32+8+len(seed)+extra+32)
	putKind(data, kind)
	copy(data[4:36], base[:])
	offset := putString(data, 36, seed)
	if space != nil {
		binary.LittleEndian.PutUint64(data[offset:], *space)
		offset += 8
	}
	copy(data[offset:], owner[:])
	return data, nil
}

func readBaseSeed(data []byte) (sdk.Pubkey, string, []byte, error) {
	if len(data) < 32 {
		return sdk.Pubkey{}, "", nil, ErrInvalidInstruction
	}
	var base sdk.Pubkey
	copy(base[:], data[:32])
	seed, rest, err := readString(data[32:])
	return base, seed, rest, err
}

func readString(data []byte) (string, []byte, error) {
	if len(data) < 8 {
		return "", nil, ErrInvalidInstruction
	}
	length := binary.LittleEndian.Uint64(data[:8])
	if length > sdk.MaxSeedLength || length > math.MaxInt || uint64(len(data)-8) < length {
		return "", nil, ErrInvalidInstruction
	}
	end := 8 + int(length)
	if !utf8.Valid(data[8:end]) {
		return "", nil, ErrInvalidInstruction
	}
	return string(data[8:end]), data[end:], nil
}
