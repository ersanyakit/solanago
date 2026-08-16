package token2022

import (
	"encoding/binary"
	"math"
)

const extensionAccountHeaderSize = AccountSize + 1

type BaseAccountType uint8

const (
	BaseUninitialized BaseAccountType = iota
	BaseMint
	BaseAccount
)

type ExtensionType uint16

const (
	ExtensionUninitialized ExtensionType = iota
	ExtensionTransferFeeConfig
	ExtensionTransferFeeAmount
	ExtensionMintCloseAuthority
	ExtensionConfidentialTransferMint
	ExtensionConfidentialTransferAccount
	ExtensionDefaultAccountState
	ExtensionImmutableOwner
	ExtensionMemoTransfer
	ExtensionNonTransferable
	ExtensionInterestBearingConfig
	ExtensionCPIGuard
	ExtensionPermanentDelegate
	ExtensionNonTransferableAccount
	ExtensionTransferHook
	ExtensionTransferHookAccount
	ExtensionConfidentialTransferFeeConfig
	ExtensionConfidentialTransferFeeAmount
	ExtensionMetadataPointer
	ExtensionTokenMetadata
	ExtensionGroupPointer
	ExtensionTokenGroup
	ExtensionGroupMemberPointer
	ExtensionTokenGroupMember
	ExtensionConfidentialMintBurn
	ExtensionScaledUIAmount
	ExtensionPausable
	ExtensionPausableAccount
	ExtensionPermissionedBurn
	maxExtensionType = ExtensionPermissionedBurn
)

type Extension struct {
	Type ExtensionType
	Data []byte
}

type MintWithExtensions struct {
	Base       Mint
	Extensions []Extension
}

type AccountWithExtensions struct {
	Base       Account
	Extensions []Extension
}

func (t ExtensionType) BaseType() (BaseAccountType, error) {
	switch t {
	case ExtensionTransferFeeConfig, ExtensionMintCloseAuthority,
		ExtensionConfidentialTransferMint, ExtensionDefaultAccountState,
		ExtensionNonTransferable, ExtensionInterestBearingConfig,
		ExtensionPermanentDelegate, ExtensionTransferHook,
		ExtensionConfidentialTransferFeeConfig, ExtensionMetadataPointer,
		ExtensionTokenMetadata, ExtensionGroupPointer, ExtensionTokenGroup,
		ExtensionGroupMemberPointer, ExtensionTokenGroupMember,
		ExtensionConfidentialMintBurn, ExtensionScaledUIAmount,
		ExtensionPausable, ExtensionPermissionedBurn:
		return BaseMint, nil
	case ExtensionTransferFeeAmount, ExtensionConfidentialTransferAccount,
		ExtensionImmutableOwner, ExtensionMemoTransfer,
		ExtensionCPIGuard, ExtensionNonTransferableAccount,
		ExtensionTransferHookAccount, ExtensionConfidentialTransferFeeAmount,
		ExtensionPausableAccount:
		return BaseAccount, nil
	default:
		return BaseUninitialized, ErrInvalidExtension
	}
}

// FixedValueLength returns the official packed value length for the common
// fixed-width extensions whose complete representation is covered here.
func (t ExtensionType) FixedValueLength() (int, bool) {
	switch t {
	case ExtensionTransferFeeConfig:
		return 108, true
	case ExtensionTransferFeeAmount:
		return 8, true
	case ExtensionConfidentialTransferMint:
		return 65, true
	case ExtensionConfidentialTransferAccount:
		return 295, true
	case ExtensionMintCloseAuthority, ExtensionPermanentDelegate, ExtensionPermissionedBurn:
		return 32, true
	case ExtensionDefaultAccountState, ExtensionMemoTransfer, ExtensionCPIGuard, ExtensionTransferHookAccount:
		return 1, true
	case ExtensionImmutableOwner, ExtensionNonTransferable, ExtensionNonTransferableAccount, ExtensionPausableAccount:
		return 0, true
	case ExtensionInterestBearingConfig:
		return 52, true
	case ExtensionTransferHook, ExtensionMetadataPointer, ExtensionGroupPointer, ExtensionGroupMemberPointer:
		return 64, true
	case ExtensionConfidentialTransferFeeConfig:
		return 129, true
	case ExtensionConfidentialTransferFeeAmount:
		return 64, true
	case ExtensionTokenGroup:
		return 80, true
	case ExtensionTokenGroupMember:
		return 72, true
	case ExtensionConfidentialMintBurn:
		return 196, true
	case ExtensionScaledUIAmount:
		return 56, true
	case ExtensionPausable:
		return 33, true
	default:
		return 0, false
	}
}

func CalculateMintLen(types []ExtensionType) (int, error) {
	return calculateLen(BaseMint, MintSize, types)
}

func CalculateAccountLen(types []ExtensionType) (int, error) {
	return calculateLen(BaseAccount, AccountSize, types)
}

func RequiredAccountExtensions(mintTypes []ExtensionType) ([]ExtensionType, error) {
	var result []ExtensionType
	seen := map[ExtensionType]bool{}
	appendType := func(value ExtensionType) {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	for _, extensionType := range mintTypes {
		base, err := extensionType.BaseType()
		if err != nil || base != BaseMint {
			return nil, ErrExtensionBase
		}
		switch extensionType {
		case ExtensionTransferFeeConfig:
			appendType(ExtensionTransferFeeAmount)
		case ExtensionNonTransferable:
			appendType(ExtensionNonTransferableAccount)
			appendType(ExtensionImmutableOwner)
		case ExtensionTransferHook:
			appendType(ExtensionTransferHookAccount)
		case ExtensionPausable:
			appendType(ExtensionPausableAccount)
		}
	}
	return result, nil
}

func DecodeMintWithExtensions(data []byte) (MintWithExtensions, error) {
	if len(data) < MintSize || len(data) == MultisigSize {
		return MintWithExtensions{}, ErrInvalidState
	}
	base, err := DecodeMint(data[:MintSize])
	if err != nil {
		return MintWithExtensions{}, err
	}
	extensions, err := decodeExtensions(data[MintSize:], BaseMint)
	if err != nil {
		return MintWithExtensions{}, err
	}
	return MintWithExtensions{Base: base, Extensions: extensions}, nil
}

func EncodeMintWithExtensions(state MintWithExtensions) ([]byte, error) {
	base, err := EncodeMint(state.Base)
	if err != nil {
		return nil, err
	}
	return encodeExtensions(base, BaseMint, state.Extensions)
}

func DecodeAccountWithExtensions(data []byte) (AccountWithExtensions, error) {
	if len(data) < AccountSize || len(data) == MultisigSize {
		return AccountWithExtensions{}, ErrInvalidState
	}
	base, err := DecodeAccount(data[:AccountSize])
	if err != nil {
		return AccountWithExtensions{}, err
	}
	extensions, err := decodeExtensions(data[AccountSize:], BaseAccount)
	if err != nil {
		return AccountWithExtensions{}, err
	}
	return AccountWithExtensions{Base: base, Extensions: extensions}, nil
}

func EncodeAccountWithExtensions(state AccountWithExtensions) ([]byte, error) {
	base, err := EncodeAccount(state.Base)
	if err != nil {
		return nil, err
	}
	return encodeExtensions(base, BaseAccount, state.Extensions)
}

func calculateLen(baseType BaseAccountType, baseSize int, types []ExtensionType) (int, error) {
	if len(types) == 0 {
		return baseSize, nil
	}
	seen := map[ExtensionType]bool{}
	total := extensionAccountHeaderSize
	for _, extensionType := range types {
		if seen[extensionType] {
			continue // Official account-length accumulation de-duplicates types.
		}
		seen[extensionType] = true
		gotBase, err := extensionType.BaseType()
		if err != nil || gotBase != baseType {
			return 0, ErrExtensionBase
		}
		length, ok := extensionType.FixedValueLength()
		if !ok {
			return 0, ErrVariableLength
		}
		if total > math.MaxInt-4-length {
			return 0, ErrInvalidExtension
		}
		total += 4 + length
	}
	if total == MultisigSize {
		total += 2
	}
	return total, nil
}

func decodeExtensions(rest []byte, baseType BaseAccountType) ([]Extension, error) {
	if len(rest) == 0 {
		return nil, nil
	}
	padding := AccountSize
	if baseType == BaseMint {
		padding -= MintSize
	} else if baseType == BaseAccount {
		padding -= AccountSize
	} else {
		return nil, ErrExtensionBase
	}
	if len(rest) < padding+1 {
		return nil, ErrInvalidExtension
	}
	for _, b := range rest[:padding] {
		if b != 0 {
			return nil, ErrInvalidExtension
		}
	}
	if BaseAccountType(rest[padding]) != baseType {
		return nil, ErrExtensionBase
	}
	tlv := rest[padding+1:]
	seen := map[ExtensionType]bool{}
	var extensions []Extension
	for offset := 0; offset < len(tlv); {
		if len(tlv)-offset < 2 {
			break // The runtime permits one trailing realloc byte.
		}
		extensionType := ExtensionType(binary.LittleEndian.Uint16(tlv[offset : offset+2]))
		if extensionType == ExtensionUninitialized {
			break
		}
		if extensionType > maxExtensionType || len(tlv)-offset < 4 {
			return nil, ErrInvalidExtension
		}
		gotBase, err := extensionType.BaseType()
		if err != nil || gotBase != baseType {
			return nil, ErrExtensionBase
		}
		if seen[extensionType] {
			return nil, ErrDuplicateExtension
		}
		seen[extensionType] = true
		length := int(binary.LittleEndian.Uint16(tlv[offset+2 : offset+4]))
		if length > len(tlv)-offset-4 {
			return nil, ErrInvalidExtension
		}
		if fixed, ok := extensionType.FixedValueLength(); ok && fixed != length {
			return nil, ErrInvalidExtension
		}
		value := append([]byte(nil), tlv[offset+4:offset+4+length]...)
		extensions = append(extensions, Extension{Type: extensionType, Data: value})
		offset += 4 + length
	}
	return extensions, nil
}

func encodeExtensions(base []byte, baseType BaseAccountType, extensions []Extension) ([]byte, error) {
	if len(extensions) == 0 {
		return base, nil
	}
	seen := map[ExtensionType]bool{}
	total := extensionAccountHeaderSize
	for _, extension := range extensions {
		gotBase, err := extension.Type.BaseType()
		if err != nil || gotBase != baseType {
			return nil, ErrExtensionBase
		}
		if seen[extension.Type] {
			return nil, ErrDuplicateExtension
		}
		seen[extension.Type] = true
		if len(extension.Data) > math.MaxUint16 {
			return nil, ErrInvalidExtension
		}
		if fixed, ok := extension.Type.FixedValueLength(); ok && fixed != len(extension.Data) {
			return nil, ErrInvalidExtension
		}
		total += 4 + len(extension.Data)
	}
	if total == MultisigSize {
		total += 2
	}
	data := make([]byte, total)
	copy(data, base)
	data[AccountSize] = byte(baseType)
	offset := extensionAccountHeaderSize
	for _, extension := range extensions {
		binary.LittleEndian.PutUint16(data[offset:offset+2], uint16(extension.Type))
		binary.LittleEndian.PutUint16(data[offset+2:offset+4], uint16(len(extension.Data)))
		copy(data[offset+4:], extension.Data)
		offset += 4 + len(extension.Data)
	}
	return data, nil
}
