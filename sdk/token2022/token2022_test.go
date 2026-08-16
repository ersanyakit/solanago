package token2022

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/ersanyakit/solanago/sdk"
	classic "github.com/ersanyakit/solanago/sdk/token"
)

func TestProgramsRemainDistinct(t *testing.T) {
	if ProgramID == classic.ProgramID {
		t.Fatal("Token-2022 and classic SPL Token IDs were conflated")
	}
	mint := key(1)
	authority := key(2)
	classicInstruction := classic.InitializeMint2(mint, authority, classic.OptionalPubkey{}, 6)
	token2022Instruction := InitializeMint2(mint, authority, OptionalPubkey{}, 6)
	if !bytes.Equal(classicInstruction.Data, token2022Instruction.Data) {
		t.Fatalf("shared base data mismatch: %x != %x", classicInstruction.Data, token2022Instruction.Data)
	}
	if token2022Instruction.ProgramID != ProgramID || classicInstruction.ProgramID != classic.ProgramID {
		t.Fatal("builder selected the wrong program")
	}
}

func TestToken2022SpecificAuthorityAndNativeMint(t *testing.T) {
	instruction, err := SetAuthority(key(1), key(2), nil, AuthorityPermissionedBurn, OptionalPubkey{})
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(instruction.Data) != "061100" {
		t.Fatalf("set authority data = %x", instruction.Data)
	}
	if _, err := SetAuthority(key(1), key(2), nil, AuthorityType(18), OptionalPubkey{}); !errors.Is(err, ErrInvalidInstruction) {
		t.Fatalf("invalid authority error = %v", err)
	}

	native := CreateNativeMint(key(3))
	if NativeMintID.String() != "9pan9bMn5HatX4EJdBwg9VgCa7Uz5HL8N1m5D3NdXejP" || hex.EncodeToString(native.Data) != "1f" {
		t.Fatalf("native mint instruction = %+v", native)
	}
	if len(native.Accounts) != 3 || native.Accounts[1].Pubkey != NativeMintID || !native.Accounts[0].IsSigner || !native.Accounts[0].IsWritable {
		t.Fatalf("native mint accounts = %+v", native.Accounts)
	}
	derived, err := sdk.CreateProgramAddress([][]byte{[]byte(NativeMintSeed), {255}}, ProgramID)
	if err != nil || derived != NativeMintID || NativeMintDecimals != 9 {
		t.Fatalf("native mint PDA = (%s, %v), decimals %d", derived, err, NativeMintDecimals)
	}
	if _, err := InitializeMultisig(key(1), nil, 0); !errors.Is(err, ErrInvalidSigners) {
		t.Fatalf("Token-2022 signer error leaked a classic-token error: %v", err)
	}
}

// Official transfer-fee vector from token-2022 interface commit
// 567074d43dc87522846728cc0b598bca27df764a, transfer_fee/instruction.rs.
func TestTransferFeeInstructionGolden(t *testing.T) {
	instruction := InitializeTransferFeeConfig(key(9), SomePubkey(key(11)), OptionalPubkey{}, 111, ^uint64(0))
	want := "1a0001" + repeatHex(11, 32) + "00" + "6f00" + "ffffffffffffffff"
	if hex.EncodeToString(instruction.Data) != want {
		t.Fatalf("data = %x\nwant = %s", instruction.Data, want)
	}
	checked, err := TransferCheckedWithFee(key(1), key(2), key(3), key(4), nil, 24, 24, 23)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(checked.Data) != "1a011800000000000000181700000000000000" {
		t.Fatalf("checked data = %x", checked.Data)
	}
}

// Official state values from token-2022 interface commit
// 567074d43dc87522846728cc0b598bca27df764a, transfer_fee/mod.rs.
func TestTransferFeeStateGolden(t *testing.T) {
	state := TransferFeeConfig{
		ConfigAuthority:   SomePubkey(key(10)),
		WithdrawAuthority: SomePubkey(key(11)),
		WithheldAmount:    ^uint64(0),
		Older:             TransferFee{Epoch: 1, MaximumFee: 10, BasisPoints: 100},
		Newer:             TransferFee{Epoch: 100, MaximumFee: 5_000, BasisPoints: 1},
	}
	data, err := EncodeTransferFeeConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	want := repeatHex(10, 32) + repeatHex(11, 32) +
		"ffffffffffffffff" +
		"01000000000000000a000000000000006400" +
		"640000000000000088130000000000000100"
	if hex.EncodeToString(data) != want {
		t.Fatalf("transfer-fee config = %x\nwant = %s", data, want)
	}
	decoded, err := DecodeTransferFeeConfig(data)
	if err != nil || decoded != state {
		t.Fatalf("decoded config = (%+v, %v)", decoded, err)
	}
	if fee := decoded.FeeForEpoch(99); fee != state.Older {
		t.Fatalf("old fee = %+v", fee)
	}
	if fee := decoded.FeeForEpoch(100); fee != state.Newer {
		t.Fatalf("new fee = %+v", fee)
	}
	if fee, ok := (TransferFee{MaximumFee: 10, BasisPoints: 100}).CalculateFee(101); !ok || fee != 2 {
		t.Fatalf("ceiling fee = (%d, %t), want (2, true)", fee, ok)
	}
	if fee, ok := (TransferFee{MaximumFee: 1, BasisPoints: MaximumFeeBasisPoints}).CalculateFee(^uint64(0)); !ok || fee != 1 {
		t.Fatalf("capped fee = (%d, %t), want (1, true)", fee, ok)
	}

	amount := TransferFeeAmount{WithheldAmount: 0x0807060504030201}
	amountData := EncodeTransferFeeAmount(amount)
	if hex.EncodeToString(amountData) != "0102030405060708" {
		t.Fatalf("transfer-fee amount = %x", amountData)
	}
	decodedAmount, err := DecodeTransferFeeAmount(amountData)
	if err != nil || decodedAmount != amount {
		t.Fatalf("decoded amount = (%+v, %v)", decodedAmount, err)
	}
}

func TestTransferFeeMaybeNullAuthority(t *testing.T) {
	data, err := EncodeTransferFeeConfig(TransferFeeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data[:64], make([]byte, 64)) {
		t.Fatalf("None authorities = %x", data[:64])
	}
	_, err = EncodeTransferFeeConfig(TransferFeeConfig{ConfigAuthority: SomePubkey(sdk.Pubkey{})})
	if !errors.Is(err, ErrInvalidExtension) {
		t.Fatalf("Some(zero) error = %v", err)
	}
}

func TestExtensionAwareGoldenLayouts(t *testing.T) {
	mintLen, err := CalculateMintLen([]ExtensionType{ExtensionTransferFeeConfig})
	if err != nil || mintLen != 278 {
		t.Fatalf("transfer-fee mint length = (%d, %v), want 278", mintLen, err)
	}
	accountLen, err := CalculateAccountLen([]ExtensionType{ExtensionImmutableOwner})
	if err != nil || accountLen != 170 {
		t.Fatalf("immutable-owner account length = (%d, %v), want 170", accountLen, err)
	}

	mint := Mint{MintAuthority: SomePubkey(key(1)), Initialized: true, Decimals: 6}
	mintData, err := EncodeMintWithExtensions(MintWithExtensions{
		Base:       mint,
		Extensions: []Extension{{Type: ExtensionTransferFeeConfig, Data: make([]byte, 108)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(mintData) != 278 || mintData[165] != byte(BaseMint) || hex.EncodeToString(mintData[166:170]) != "01006c00" {
		t.Fatalf("mint extension header = %x (len %d)", mintData[160:174], len(mintData))
	}
	decodedMint, err := DecodeMintWithExtensions(mintData)
	if err != nil || decodedMint.Base != mint || len(decodedMint.Extensions) != 1 || decodedMint.Extensions[0].Type != ExtensionTransferFeeConfig {
		t.Fatalf("decoded mint = (%+v, %v)", decodedMint, err)
	}

	account := Account{State: AccountInitialized}
	accountData, err := EncodeAccountWithExtensions(AccountWithExtensions{
		Base:       account,
		Extensions: []Extension{{Type: ExtensionImmutableOwner}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(accountData) != 170 || accountData[165] != byte(BaseAccount) || hex.EncodeToString(accountData[166:]) != "07000000" {
		t.Fatalf("account extension bytes = %x", accountData)
	}
	decodedAccount, err := DecodeAccountWithExtensions(accountData)
	if err != nil || decodedAccount.Base != account || len(decodedAccount.Extensions) != 1 {
		t.Fatalf("decoded account = (%+v, %v)", decodedAccount, err)
	}
}

// Values match the fixed-size codecs in the independently generated official
// TypeScript client at the pinned Token-2022 commit. TokenMetadata is the sole
// variable-width production extension.
func TestAllOfficialExtensionValueLengths(t *testing.T) {
	want := map[ExtensionType]int{
		ExtensionTransferFeeConfig:             108,
		ExtensionTransferFeeAmount:             8,
		ExtensionMintCloseAuthority:            32,
		ExtensionConfidentialTransferMint:      65,
		ExtensionConfidentialTransferAccount:   295,
		ExtensionDefaultAccountState:           1,
		ExtensionImmutableOwner:                0,
		ExtensionMemoTransfer:                  1,
		ExtensionNonTransferable:               0,
		ExtensionInterestBearingConfig:         52,
		ExtensionCPIGuard:                      1,
		ExtensionPermanentDelegate:             32,
		ExtensionNonTransferableAccount:        0,
		ExtensionTransferHook:                  64,
		ExtensionTransferHookAccount:           1,
		ExtensionConfidentialTransferFeeConfig: 129,
		ExtensionConfidentialTransferFeeAmount: 64,
		ExtensionMetadataPointer:               64,
		ExtensionGroupPointer:                  64,
		ExtensionTokenGroup:                    80,
		ExtensionGroupMemberPointer:            64,
		ExtensionTokenGroupMember:              72,
		ExtensionConfidentialMintBurn:          196,
		ExtensionScaledUIAmount:                56,
		ExtensionPausable:                      33,
		ExtensionPausableAccount:               0,
		ExtensionPermissionedBurn:              32,
	}
	for extensionType := ExtensionTransferFeeConfig; extensionType <= ExtensionPermissionedBurn; extensionType++ {
		length, fixed := extensionType.FixedValueLength()
		if extensionType == ExtensionTokenMetadata {
			if fixed {
				t.Fatalf("TokenMetadata reported fixed length %d", length)
			}
			continue
		}
		expected, ok := want[extensionType]
		if !ok || !fixed || length != expected {
			t.Fatalf("extension %d length = (%d, %t), want (%d, true)", extensionType, length, fixed, expected)
		}
		base, err := extensionType.BaseType()
		if err != nil {
			t.Fatal(err)
		}
		var accountLength int
		if base == BaseMint {
			accountLength, err = CalculateMintLen([]ExtensionType{extensionType})
		} else {
			accountLength, err = CalculateAccountLen([]ExtensionType{extensionType})
		}
		expectedAccountLength := extensionAccountHeaderSize + 4 + expected
		if expectedAccountLength == MultisigSize {
			expectedAccountLength += 2
		}
		if err != nil || accountLength != expectedAccountLength {
			t.Fatalf("extension %d account length = (%d, %v), want %d", extensionType, accountLength, err, expectedAccountLength)
		}
	}
}

func TestExtensionBaseAndRequiredAccountRules(t *testing.T) {
	required, err := RequiredAccountExtensions([]ExtensionType{
		ExtensionTransferFeeConfig,
		ExtensionNonTransferable,
		ExtensionTransferHook,
		ExtensionPausable,
		ExtensionTransferFeeConfig,
	})
	want := []ExtensionType{ExtensionTransferFeeAmount, ExtensionNonTransferableAccount, ExtensionImmutableOwner, ExtensionTransferHookAccount, ExtensionPausableAccount}
	if err != nil || !reflect.DeepEqual(required, want) {
		t.Fatalf("required = (%v, %v), want %v", required, err, want)
	}
	_, err = EncodeMintWithExtensions(MintWithExtensions{Extensions: []Extension{{Type: ExtensionImmutableOwner}}})
	if !errors.Is(err, ErrExtensionBase) {
		t.Fatalf("wrong-base error = %v", err)
	}
}

func TestRejectsAccountMintCosplayAndMalformedTLV(t *testing.T) {
	classicAccount, err := classic.EncodeAccount(classic.Account{State: classic.AccountInitialized})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeMintWithExtensions(classicAccount); err == nil {
		t.Fatal("accepted a 165-byte token account as a Token-2022 mint")
	}

	valid, err := EncodeAccountWithExtensions(AccountWithExtensions{Base: Account{State: AccountInitialized}, Extensions: []Extension{{Type: ExtensionImmutableOwner}}})
	if err != nil {
		t.Fatal(err)
	}
	valid[168] = 1 // immutable owner must have a zero-length value
	if _, err := DecodeAccountWithExtensions(valid); err == nil {
		t.Fatal("accepted malformed fixed-width TLV")
	}
}

func TestExtensionInstructionAccounts(t *testing.T) {
	account, payer, owner, signer := key(1), key(2), key(3), key(4)
	instruction, err := Reallocate(account, payer, owner, []sdk.Pubkey{signer}, []ExtensionType{ExtensionImmutableOwner})
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(instruction.Data) != "1d0700" || instruction.ProgramID != ProgramID {
		t.Fatalf("reallocate = %+v", instruction)
	}
	if !instruction.Accounts[1].IsWritable || !instruction.Accounts[1].IsSigner || instruction.Accounts[3].IsSigner || !instruction.Accounts[4].IsSigner {
		t.Fatalf("privileges = %+v", instruction.Accounts)
	}
}

func FuzzExtensionDecoders(f *testing.F) {
	seed, _ := EncodeAccountWithExtensions(AccountWithExtensions{Base: Account{State: AccountInitialized}, Extensions: []Extension{{Type: ExtensionImmutableOwner}}})
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeMintWithExtensions(data)
		_, _ = DecodeAccountWithExtensions(data)
	})
}

func key(value byte) sdk.Pubkey {
	var key sdk.Pubkey
	for i := range key {
		key[i] = value
	}
	return key
}

func repeatHex(value byte, count int) string {
	return hex.EncodeToString(bytes.Repeat([]byte{value}, count))
}
