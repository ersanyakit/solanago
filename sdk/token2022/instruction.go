package token2022

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/system"
	classic "github.com/ersanyakit/solanago/sdk/token"
)

type AuthorityType uint8

const (
	AuthorityMintTokens AuthorityType = iota
	AuthorityFreezeAccount
	AuthorityAccountOwner
	AuthorityCloseAccount
	AuthorityTransferFeeConfig
	AuthorityWithheldWithdraw
	AuthorityCloseMint
	AuthorityInterestRate
	AuthorityPermanentDelegate
	AuthorityConfidentialTransferMint
	AuthorityTransferHookProgramID
	AuthorityConfidentialTransferFeeConfig
	AuthorityMetadataPointer
	AuthorityGroupPointer
	AuthorityGroupMemberPointer
	AuthorityScaledUIAmount
	AuthorityPause
	AuthorityPermissionedBurn
)

func InitializeMint(mint, mintAuthority sdk.Pubkey, freezeAuthority OptionalPubkey, decimals uint8) sdk.Instruction {
	return asToken2022(classic.InitializeMint(mint, mintAuthority, classicOption(freezeAuthority), decimals))
}

func InitializeMint2(mint, mintAuthority sdk.Pubkey, freezeAuthority OptionalPubkey, decimals uint8) sdk.Instruction {
	return asToken2022(classic.InitializeMint2(mint, mintAuthority, classicOption(freezeAuthority), decimals))
}

func InitializeAccount(account, mint, owner sdk.Pubkey) sdk.Instruction {
	return asToken2022(classic.InitializeAccount(account, mint, owner))
}

func InitializeAccount2(account, mint, owner sdk.Pubkey) sdk.Instruction {
	return asToken2022(classic.InitializeAccount2(account, mint, owner))
}

func InitializeAccount3(account, mint, owner sdk.Pubkey) sdk.Instruction {
	return asToken2022(classic.InitializeAccount3(account, mint, owner))
}

func InitializeMultisig(multisig sdk.Pubkey, signers []sdk.Pubkey, m uint8) (sdk.Instruction, error) {
	return convertResult(classic.InitializeMultisig(multisig, signers, m))
}

func InitializeMultisig2(multisig sdk.Pubkey, signers []sdk.Pubkey, m uint8) (sdk.Instruction, error) {
	return convertResult(classic.InitializeMultisig2(multisig, signers, m))
}

func Transfer(source, destination, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64) (sdk.Instruction, error) {
	return convertResult(classic.Transfer(source, destination, authority, signers, amount))
}

func Approve(source, delegate, owner sdk.Pubkey, signers []sdk.Pubkey, amount uint64) (sdk.Instruction, error) {
	return convertResult(classic.Approve(source, delegate, owner, signers, amount))
}

func Revoke(source, owner sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	return convertResult(classic.Revoke(source, owner, signers))
}

func SetAuthority(owned, currentAuthority sdk.Pubkey, signers []sdk.Pubkey, authorityType AuthorityType, newAuthority OptionalPubkey) (sdk.Instruction, error) {
	if authorityType > AuthorityPermissionedBurn {
		return sdk.Instruction{}, ErrInvalidInstruction
	}
	accounts, err := authorityAccounts([]sdk.AccountMeta{sdk.Writable(owned, false)}, currentAuthority, signers)
	if err != nil {
		return sdk.Instruction{}, err
	}
	data := []byte{6, byte(authorityType)}
	data = appendPubkeyOption(data, newAuthority)
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}, nil
}

func MintTo(mint, account, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64) (sdk.Instruction, error) {
	return convertResult(classic.MintTo(mint, account, authority, signers, amount))
}

func Burn(account, mint, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64) (sdk.Instruction, error) {
	return convertResult(classic.Burn(account, mint, authority, signers, amount))
}

func CloseAccount(account, destination, authority sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	return convertResult(classic.CloseAccount(account, destination, authority, signers))
}

func FreezeAccount(account, mint, authority sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	return convertResult(classic.FreezeAccount(account, mint, authority, signers))
}

func ThawAccount(account, mint, authority sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	return convertResult(classic.ThawAccount(account, mint, authority, signers))
}

func TransferChecked(source, mint, destination, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64, decimals uint8) (sdk.Instruction, error) {
	return convertResult(classic.TransferChecked(source, mint, destination, authority, signers, amount, decimals))
}

func ApproveChecked(source, mint, delegate, owner sdk.Pubkey, signers []sdk.Pubkey, amount uint64, decimals uint8) (sdk.Instruction, error) {
	return convertResult(classic.ApproveChecked(source, mint, delegate, owner, signers, amount, decimals))
}

func MintToChecked(mint, account, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64, decimals uint8) (sdk.Instruction, error) {
	return convertResult(classic.MintToChecked(mint, account, authority, signers, amount, decimals))
}

func BurnChecked(account, mint, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64, decimals uint8) (sdk.Instruction, error) {
	return convertResult(classic.BurnChecked(account, mint, authority, signers, amount, decimals))
}

func SyncNative(account sdk.Pubkey) sdk.Instruction { return asToken2022(classic.SyncNative(account)) }

func SyncNativeWithRentSysvar(account sdk.Pubkey) sdk.Instruction {
	return asToken2022(classic.SyncNativeWithRentSysvar(account))
}

func GetAccountDataSize(mint sdk.Pubkey, extensionTypes []ExtensionType) (sdk.Instruction, error) {
	data := []byte{21}
	for _, extensionType := range extensionTypes {
		if extensionType == ExtensionUninitialized || extensionType > maxExtensionType {
			return sdk.Instruction{}, ErrInvalidInstruction
		}
		data = binary.LittleEndian.AppendUint16(data, uint16(extensionType))
	}
	return instruction(data, sdk.Readonly(mint, false)), nil
}

func InitializeImmutableOwner(account sdk.Pubkey) sdk.Instruction {
	return instruction([]byte{22}, sdk.Writable(account, false))
}

func AmountToUIAmount(mint sdk.Pubkey, amount uint64) sdk.Instruction {
	return asToken2022(classic.AmountToUIAmount(mint, amount))
}

func UIAmountToAmount(mint sdk.Pubkey, uiAmount string) (sdk.Instruction, error) {
	instruction, err := classic.UIAmountToAmount(mint, uiAmount)
	return convertResult(instruction, err)
}

func InitializeMintCloseAuthority(mint sdk.Pubkey, authority OptionalPubkey) sdk.Instruction {
	data := appendPubkeyOption([]byte{25}, authority)
	return instruction(data, sdk.Writable(mint, false))
}

func Reallocate(account, payer, owner sdk.Pubkey, signers []sdk.Pubkey, extensionTypes []ExtensionType) (sdk.Instruction, error) {
	if len(signers) > MaxSigners {
		return sdk.Instruction{}, ErrInvalidInstruction
	}
	data := []byte{29}
	for _, extensionType := range extensionTypes {
		base, err := extensionType.BaseType()
		if err != nil || base != BaseAccount {
			return sdk.Instruction{}, ErrExtensionBase
		}
		data = binary.LittleEndian.AppendUint16(data, uint16(extensionType))
	}
	accounts := []sdk.AccountMeta{sdk.Writable(account, false), sdk.Writable(payer, true), sdk.Readonly(system.ProgramID, false), sdk.Readonly(owner, len(signers) == 0)}
	for _, signer := range signers {
		accounts = append(accounts, sdk.Readonly(signer, true))
	}
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}, nil
}

func CreateNativeMint(payer sdk.Pubkey) sdk.Instruction {
	return instruction([]byte{31},
		sdk.Writable(payer, true),
		sdk.Writable(NativeMintID, false),
		sdk.Readonly(system.ProgramID, false),
	)
}

func InitializeNonTransferableMint(mint sdk.Pubkey) sdk.Instruction {
	return instruction([]byte{32}, sdk.Writable(mint, false))
}

func InitializePermanentDelegate(mint, delegate sdk.Pubkey) sdk.Instruction {
	data := append([]byte{35}, delegate[:]...)
	return instruction(data, sdk.Writable(mint, false))
}

func WithdrawExcessLamports(account, destination, authority sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	return convertResult(classic.WithdrawExcessLamports(account, destination, authority, signers))
}

func UnwrapLamports(account, destination, authority sdk.Pubkey, signers []sdk.Pubkey, amount OptionalU64) (sdk.Instruction, error) {
	return convertResult(classic.UnwrapLamports(account, destination, authority, signers, classic.OptionalU64{Set: amount.Set, Value: amount.Value}))
}

func InitializeTransferFeeConfig(mint sdk.Pubkey, configAuthority, withdrawAuthority OptionalPubkey, basisPoints uint16, maximumFee uint64) sdk.Instruction {
	data := []byte{26, 0}
	data = appendPubkeyOption(data, configAuthority)
	data = appendPubkeyOption(data, withdrawAuthority)
	data = binary.LittleEndian.AppendUint16(data, basisPoints)
	data = binary.LittleEndian.AppendUint64(data, maximumFee)
	return instruction(data, sdk.Writable(mint, false))
}

func TransferCheckedWithFee(source, mint, destination, authority sdk.Pubkey, signers []sdk.Pubkey, amount uint64, decimals uint8, fee uint64) (sdk.Instruction, error) {
	accounts, err := authorityAccounts([]sdk.AccountMeta{sdk.Writable(source, false), sdk.Readonly(mint, false), sdk.Writable(destination, false)}, authority, signers)
	if err != nil {
		return sdk.Instruction{}, err
	}
	data := []byte{26, 1}
	data = binary.LittleEndian.AppendUint64(data, amount)
	data = append(data, decimals)
	data = binary.LittleEndian.AppendUint64(data, fee)
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}, nil
}

func WithdrawWithheldTokensFromMint(mint, destination, authority sdk.Pubkey, signers []sdk.Pubkey) (sdk.Instruction, error) {
	accounts, err := authorityAccounts([]sdk.AccountMeta{sdk.Writable(mint, false), sdk.Writable(destination, false)}, authority, signers)
	if err != nil {
		return sdk.Instruction{}, err
	}
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: []byte{26, 2}}, nil
}

func WithdrawWithheldTokensFromAccounts(mint, destination, authority sdk.Pubkey, signers, sources []sdk.Pubkey) (sdk.Instruction, error) {
	if len(sources) > math.MaxUint8 {
		return sdk.Instruction{}, ErrInvalidInstruction
	}
	accounts, err := authorityAccounts([]sdk.AccountMeta{sdk.Readonly(mint, false), sdk.Writable(destination, false)}, authority, signers)
	if err != nil {
		return sdk.Instruction{}, err
	}
	for _, source := range sources {
		accounts = append(accounts, sdk.Writable(source, false))
	}
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: []byte{26, 3, byte(len(sources))}}, nil
}

func HarvestWithheldTokensToMint(mint sdk.Pubkey, sources []sdk.Pubkey) sdk.Instruction {
	accounts := []sdk.AccountMeta{sdk.Writable(mint, false)}
	for _, source := range sources {
		accounts = append(accounts, sdk.Writable(source, false))
	}
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: []byte{26, 4}}
}

func SetTransferFee(mint, authority sdk.Pubkey, signers []sdk.Pubkey, basisPoints uint16, maximumFee uint64) (sdk.Instruction, error) {
	accounts, err := authorityAccounts([]sdk.AccountMeta{sdk.Writable(mint, false)}, authority, signers)
	if err != nil {
		return sdk.Instruction{}, err
	}
	data := []byte{26, 5}
	data = binary.LittleEndian.AppendUint16(data, basisPoints)
	data = binary.LittleEndian.AppendUint64(data, maximumFee)
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}, nil
}

func Batch(instructions []sdk.Instruction) (sdk.Instruction, error) {
	data := []byte{255}
	var accounts []sdk.AccountMeta
	for _, child := range instructions {
		if child.ProgramID != ProgramID || len(child.Accounts) > math.MaxUint8 || len(child.Data) > math.MaxUint8 || (len(child.Data) > 0 && child.Data[0] == 255) {
			return sdk.Instruction{}, ErrInvalidInstruction
		}
		data = append(data, byte(len(child.Accounts)), byte(len(child.Data)))
		data = append(data, child.Data...)
		accounts = append(accounts, child.Accounts...)
	}
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}, nil
}

func authorityAccounts(accounts []sdk.AccountMeta, authority sdk.Pubkey, signers []sdk.Pubkey) ([]sdk.AccountMeta, error) {
	if len(signers) > MaxSigners {
		return nil, ErrInvalidInstruction
	}
	accounts = append(accounts, sdk.Readonly(authority, len(signers) == 0))
	for _, signer := range signers {
		accounts = append(accounts, sdk.Readonly(signer, true))
	}
	return accounts, nil
}

func appendPubkeyOption(data []byte, value OptionalPubkey) []byte {
	if !value.Set {
		return append(data, 0)
	}
	data = append(data, 1)
	return append(data, value.Value[:]...)
}

func instruction(data []byte, accounts ...sdk.AccountMeta) sdk.Instruction {
	return sdk.Instruction{ProgramID: ProgramID, Accounts: accounts, Data: data}
}

func asToken2022(instruction sdk.Instruction) sdk.Instruction {
	instruction.ProgramID = ProgramID
	return instruction
}

func convertResult(instruction sdk.Instruction, err error) (sdk.Instruction, error) {
	if err != nil {
		switch {
		case errors.Is(err, classic.ErrInvalidSigners):
			return sdk.Instruction{}, ErrInvalidSigners
		case errors.Is(err, classic.ErrInvalidInstruction):
			return sdk.Instruction{}, ErrInvalidInstruction
		default:
			return sdk.Instruction{}, err
		}
	}
	return asToken2022(instruction), nil
}
