package token

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/ersany/go-solana/sdk"
)

// These byte vectors are copied from spl-token-interface's Rust packing tests
// at f5285693a93135a144e24859c84d26ac20037a3a and independently agree with the
// official generated TypeScript codecs in clients/js.
func TestOfficialInstructionGoldenVectors(t *testing.T) {
	one := key(1)
	tests := []struct {
		value InstructionData
		want  string
	}{
		{InstructionData{Kind: InitializeMintKind, Decimals: 2, MintAuthority: one}, "0002" + repeatHex(1, 32) + "00"},
		{InstructionData{Kind: InitializeAccountKind}, "01"},
		{InstructionData{Kind: InitializeMultisigKind, M: 1}, "0201"},
		{InstructionData{Kind: TransferKind, Amount: 1}, "030100000000000000"},
		{InstructionData{Kind: ApproveKind, Amount: 1}, "040100000000000000"},
		{InstructionData{Kind: RevokeKind}, "05"},
		{InstructionData{Kind: SetAuthorityKind, AuthorityType: AuthorityFreezeAccount, NewAuthority: SomePubkey(key(4))}, "060101" + repeatHex(4, 32)},
		{InstructionData{Kind: MintToKind, Amount: 1}, "070100000000000000"},
		{InstructionData{Kind: BurnKind, Amount: 1}, "080100000000000000"},
		{InstructionData{Kind: CloseAccountKind}, "09"},
		{InstructionData{Kind: FreezeAccountKind}, "0a"},
		{InstructionData{Kind: ThawAccountKind}, "0b"},
		{InstructionData{Kind: TransferCheckedKind, Amount: 1, Decimals: 2}, "0c010000000000000002"},
		{InstructionData{Kind: ApproveCheckedKind, Amount: 1, Decimals: 2}, "0d010000000000000002"},
		{InstructionData{Kind: MintToCheckedKind, Amount: 1, Decimals: 2}, "0e010000000000000002"},
		{InstructionData{Kind: BurnCheckedKind, Amount: 1, Decimals: 2}, "0f010000000000000002"},
		{InstructionData{Kind: InitializeAccount2Kind, Owner: key(2)}, "10" + repeatHex(2, 32)},
		{InstructionData{Kind: SyncNativeKind}, "11"},
		{InstructionData{Kind: InitializeAccount3Kind, Owner: key(2)}, "12" + repeatHex(2, 32)},
		{InstructionData{Kind: InitializeMultisig2Kind, M: 1}, "1301"},
		{InstructionData{Kind: InitializeMint2Kind, Decimals: 2, MintAuthority: one}, "1402" + repeatHex(1, 32) + "00"},
		{InstructionData{Kind: GetAccountDataSizeKind}, "15"},
		{InstructionData{Kind: InitializeImmutableOwnerKind}, "16"},
		{InstructionData{Kind: AmountToUIAmountKind, Amount: 42}, "172a00000000000000"},
		{InstructionData{Kind: UIAmountToAmountKind, UIAmount: "0.42"}, "18302e3432"},
		{InstructionData{Kind: WithdrawExcessLamportsKind}, "26"},
		{InstructionData{Kind: UnwrapLamportsKind, OptionalAmount: SomeU64(42)}, "2d012a00000000000000"},
		{InstructionData{Kind: UnwrapLamportsKind}, "2d00"},
		{InstructionData{Kind: BatchKind}, "ff"},
	}
	for _, test := range tests {
		data, err := EncodeInstructionData(test.value)
		if err != nil {
			t.Fatalf("kind %d: %v", test.value.Kind, err)
		}
		if hex.EncodeToString(data) != test.want {
			t.Fatalf("kind %d data = %x, want %s", test.value.Kind, data, test.want)
		}
		got, err := DecodeInstructionData(data)
		if err != nil || !reflect.DeepEqual(got, test.value) {
			t.Fatalf("kind %d round trip = (%+v, %v), want %+v", test.value.Kind, got, err, test.value)
		}
	}
}

func TestClassicNativeMintConstant(t *testing.T) {
	if NativeMintID.String() != "So11111111111111111111111111111111111111112" || NativeMintDecimals != 9 {
		t.Fatalf("native mint = (%s, %d)", NativeMintID, NativeMintDecimals)
	}
}

func TestClassicStateGoldenLayouts(t *testing.T) {
	mint := Mint{MintAuthority: SomePubkey(key(1)), Supply: 0x0102030405060708, Decimals: 9, Initialized: true, FreezeAuthority: SomePubkey(key(2))}
	mintData, err := EncodeMint(mint)
	if err != nil {
		t.Fatal(err)
	}
	wantMint := "01000000" + repeatHex(1, 32) + "08070605040302010901" + "01000000" + repeatHex(2, 32)
	if hex.EncodeToString(mintData) != wantMint {
		t.Fatalf("mint bytes = %x\nwant = %s", mintData, wantMint)
	}
	if got, err := DecodeMint(mintData); err != nil || got != mint {
		t.Fatalf("mint round trip = (%+v, %v)", got, err)
	}

	account := Account{Mint: key(1), Owner: key(2), Amount: 7, Delegate: SomePubkey(key(3)), State: AccountFrozen, IsNative: SomeU64(99), DelegatedAmount: 4, CloseAuthority: SomePubkey(key(5))}
	accountData, err := EncodeAccount(account)
	if err != nil {
		t.Fatal(err)
	}
	wantAccount := repeatHex(1, 32) + repeatHex(2, 32) + "0700000000000000" +
		"01000000" + repeatHex(3, 32) + "02" +
		"010000006300000000000000" + "0400000000000000" +
		"01000000" + repeatHex(5, 32)
	if hex.EncodeToString(accountData) != wantAccount {
		t.Fatalf("account bytes = %x\nwant = %s", accountData, wantAccount)
	}
	if got, err := DecodeAccount(accountData); err != nil || got != account {
		t.Fatalf("account round trip = (%+v, %v)", got, err)
	}
}

func TestBuilderPrivilegesAndBatch(t *testing.T) {
	source, mint, destination, authority, signer := key(1), key(2), key(3), key(4), key(5)
	single, err := TransferChecked(source, mint, destination, authority, nil, 7, 6)
	if err != nil {
		t.Fatal(err)
	}
	wantSingle := []sdk.AccountMeta{sdk.Writable(source, false), sdk.Readonly(mint, false), sdk.Writable(destination, false), sdk.Readonly(authority, true)}
	if !reflect.DeepEqual(single.Accounts, wantSingle) {
		t.Fatalf("single accounts = %+v", single.Accounts)
	}
	multi, err := TransferChecked(source, mint, destination, authority, []sdk.Pubkey{signer}, 7, 6)
	if err != nil {
		t.Fatal(err)
	}
	if multi.Accounts[3].IsSigner || !multi.Accounts[4].IsSigner {
		t.Fatalf("multisig privileges = %+v", multi.Accounts)
	}
	batch, err := Batch([]sdk.Instruction{single, SyncNative(destination)})
	if err != nil {
		t.Fatal(err)
	}
	if batch.ProgramID != ProgramID || batch.Data[0] != 255 || len(batch.Accounts) != 5 {
		t.Fatalf("batch = %+v", batch)
	}
}

func TestRejectsMalformedStateAndInstructions(t *testing.T) {
	badMint := make([]byte, MintSize)
	badMint[0] = 2
	if _, err := DecodeMint(badMint); err == nil {
		t.Fatal("accepted invalid COption tag")
	}
	badAccount := make([]byte, AccountSize)
	badAccount[108] = 3
	if _, err := DecodeAccount(badAccount); err == nil {
		t.Fatal("accepted invalid account state")
	}
	for _, data := range [][]byte{nil, {6, 4, 0}, {12, 1}, {45, 2}, {99}} {
		if _, err := DecodeInstructionData(data); err == nil {
			t.Fatalf("accepted %x", data)
		}
	}
	if _, err := UIAmountToAmount(key(1), string([]byte{0xff})); !errors.Is(err, ErrInvalidInstruction) {
		t.Fatalf("invalid UTF-8 UI amount error = %v", err)
	}
}

func FuzzClassicStateDecoders(f *testing.F) {
	f.Add(make([]byte, MintSize))
	f.Add(make([]byte, AccountSize))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeMint(data)
		_, _ = DecodeAccount(data)
		_, _ = DecodeMultisig(data)
	})
}

func FuzzInstructionCodec(f *testing.F) {
	f.Add([]byte{3, 1, 0, 0, 0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := DecodeInstructionData(data)
		if err != nil {
			return
		}
		roundTrip, err := EncodeInstructionData(value)
		if err != nil || !bytes.Equal(roundTrip, data) {
			t.Fatalf("round trip %x => %x (%v)", data, roundTrip, err)
		}
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
