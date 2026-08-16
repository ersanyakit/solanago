package system

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/ersanyakit/go-solana/sdk"
)

// Golden bytes and account order come from the official Codama-generated
// Rust and TypeScript clients at f61ddfe278125ea7624ba5df66baad5d01b9dccd.
func TestSystemInstructionGoldenLayouts(t *testing.T) {
	a := filled(1)
	b := filled(2)
	c := filled(3)

	create := CreateAccount(a, b, 5, 9, c)
	wantCreate := "0000000005000000000000000900000000000000" + bytesHex(3, 32)
	assertHex(t, create.Data, wantCreate)
	assertAccounts(t, create.Accounts, []sdk.AccountMeta{sdk.Writable(a, true), sdk.Writable(b, true)})

	transfer := Transfer(a, b, 0x0102030405060708)
	assertHex(t, transfer.Data, "020000000807060504030201")

	seeded, err := CreateAccountWithSeed(a, b, c, "seed", 5, 9, a)
	if err != nil {
		t.Fatal(err)
	}
	wantSeeded := "03000000" + bytesHex(3, 32) + "04000000000000007365656405000000000000000900000000000000" + bytesHex(1, 32)
	assertHex(t, seeded.Data, wantSeeded)
	assertAccounts(t, seeded.Accounts, []sdk.AccountMeta{sdk.Writable(a, true), sdk.Writable(b, false), sdk.Readonly(c, true)})

	transferSeeded, err := TransferWithSeed(a, b, c, "seed", a, 7)
	if err != nil {
		t.Fatal(err)
	}
	assertHex(t, transferSeeded.Data, "0b0000000700000000000000040000000000000073656564"+bytesHex(1, 32))

	for want, instruction := range map[InstructionKind]sdk.Instruction{
		AssignKind:                    Assign(a, b),
		AdvanceNonceAccountKind:       AdvanceNonceAccount(a, b),
		WithdrawNonceAccountKind:      WithdrawNonceAccount(a, b, c, 4),
		InitializeNonceAccountKind:    InitializeNonceAccount(a, b),
		AuthorizeNonceAccountKind:     AuthorizeNonceAccount(a, b, c),
		AllocateKind:                  Allocate(a, 8),
		UpgradeNonceAccountKind:       UpgradeNonceAccount(a),
		CreateAccountAllowPrefundKind: CreateAccountAllowPrefund(a, &b, 1, 2, c),
	} {
		if got := InstructionKind(uint32(instruction.Data[0]) | uint32(instruction.Data[1])<<8 | uint32(instruction.Data[2])<<16 | uint32(instruction.Data[3])<<24); got != want {
			t.Fatalf("tag = %d, want %d", got, want)
		}
		if instruction.ProgramID != ProgramID {
			t.Fatal("wrong program id")
		}
		if _, err := DecodeInstructionData(instruction.Data); err != nil {
			t.Fatalf("decode tag %d: %v", want, err)
		}
	}
}

func TestSystemSeededRoundTrip(t *testing.T) {
	a, b, c := filled(1), filled(2), filled(3)
	tests := []sdk.Instruction{}
	allocate, _ := AllocateWithSeed(a, b, "🚀", 99, c)
	assign, _ := AssignWithSeed(a, b, "seed", c)
	tests = append(tests, allocate, assign)
	for _, instruction := range tests {
		decoded, err := DecodeInstructionData(instruction.Data)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Base != b || decoded.Owner != c {
			t.Fatalf("decoded = %+v", decoded)
		}
	}
}

func TestSystemRejectsNonStringSeedBytes(t *testing.T) {
	a := filled(1)
	if _, err := CreateAccountWithSeed(a, a, a, string([]byte{0xff}), 0, 0, a); !errors.Is(err, sdk.ErrInvalidSeed) {
		t.Fatalf("builder error = %v", err)
	}
	data := CreateAccount(a, a, 0, 0, a).Data
	data[0] = byte(CreateAccountWithSeedKind)
	if _, err := DecodeInstructionData(data); err == nil {
		t.Fatal("accepted malformed seeded instruction")
	}
}

func TestNonceStateGoldenRoundTrip(t *testing.T) {
	state := NonceState{
		Version:              NonceVersionCurrent,
		Status:               NonceInitialized,
		Authority:            filled(7),
		Blockhash:            filled(8),
		LamportsPerSignature: 0x0102030405060708,
	}
	data, err := EncodeNonceState(state)
	if err != nil {
		t.Fatal(err)
	}
	want := "0100000001000000" + bytesHex(7, 32) + bytesHex(8, 32) + "0807060504030201"
	assertHex(t, data, want)
	got, err := DecodeNonceState(data)
	if err != nil || got != state {
		t.Fatalf("round trip = (%+v, %v)", got, err)
	}
}

func FuzzDecodeInstructionData(f *testing.F) {
	f.Add(Transfer(filled(1), filled(2), 3).Data)
	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := DecodeInstructionData(data)
		if err != nil {
			return
		}
		if decoded.Kind > CreateAccountAllowPrefundKind {
			t.Fatalf("accepted unknown kind %d", decoded.Kind)
		}
	})
}

func filled(value byte) sdk.Pubkey {
	var key sdk.Pubkey
	for i := range key {
		key[i] = value
	}
	return key
}

func bytesHex(value byte, count int) string {
	return hex.EncodeToString(bytes.Repeat([]byte{value}, count))
}

func assertHex(t *testing.T, got []byte, want string) {
	t.Helper()
	if hex.EncodeToString(got) != want {
		t.Fatalf("data = %x\nwant = %s", got, want)
	}
}

func assertAccounts(t *testing.T, got, want []sdk.AccountMeta) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("accounts = %+v\nwant = %+v", got, want)
	}
}
