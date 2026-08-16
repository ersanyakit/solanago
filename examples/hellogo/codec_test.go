package hellogo

import (
	"bytes"
	"math"
	"testing"

	"github.com/ersanyakit/solanago/examples/spl20"
	"github.com/ersanyakit/solanago/sdk"
)

func TestWireCodecMatchesIndependentNativeReference(t *testing.T) {
	authority := testKey(10)
	nativeAuthority := splKey(authority)

	vectors := []struct {
		name   string
		got    []byte
		native []byte
	}{
		{"initialize mint", EncodeInitializeMint(6, authority), spl20.EncodeInitializeMint(6, nativeAuthority)},
		{"initialize account", EncodeInitializeAccount(authority), spl20.EncodeInitializeAccount(nativeAuthority)},
		{"revoke", EncodeRevoke(), spl20.EncodeRevoke()},
	}
	for _, kind := range []InstructionKind{InstructionTransfer, InstructionMintTo, InstructionBurn, InstructionApprove} {
		got, err := EncodeAmountInstruction(kind, math.MaxUint64-7)
		if err != nil {
			t.Fatal(err)
		}
		native, err := spl20.EncodeAmountInstruction(spl20.InstructionKind(kind), math.MaxUint64-7)
		if err != nil {
			t.Fatal(err)
		}
		vectors = append(vectors, struct {
			name   string
			got    []byte
			native []byte
		}{"amount", got, native})
	}
	for _, vector := range vectors {
		if !bytes.Equal(vector.got, vector.native) {
			t.Fatalf("%s differs: %x != %x", vector.name, vector.got, vector.native)
		}
		if _, err := DecodeInstruction(vector.got); err != nil {
			t.Fatalf("decode %s: %v", vector.name, err)
		}
	}

	for _, optional := range []OptionalPubkey{{}, {Set: true, Key: authority}} {
		got, err := EncodeSetAuthority(AuthorityMintTokens, optional)
		if err != nil {
			t.Fatal(err)
		}
		native, err := spl20.EncodeSetAuthority(spl20.AuthorityMintTokens, spl20.OptionalPubkey{Set: optional.Set, Key: splKey(optional.Key)})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, native) {
			t.Fatalf("set authority differs: %x != %x", got, native)
		}
	}
}

func TestStateCodecCanonicalRoundTrip(t *testing.T) {
	mint := MintState{
		Initialized: true,
		Decimals:    9,
		Supply:      math.MaxUint64,
		MintAuthority: OptionalPubkey{
			Set: true,
			Key: testKey(1),
		},
	}
	mintData := make([]byte, MintStateSize)
	if err := EncodeMintState(mintData, mint); err != nil {
		t.Fatal(err)
	}
	decodedMint, err := DecodeMintState(mintData)
	if err != nil || decodedMint != mint {
		t.Fatalf("mint round trip = %+v, %v", decodedMint, err)
	}

	token := TokenAccountState{
		Initialized:     true,
		Mint:            testKey(2),
		Owner:           testKey(3),
		Amount:          99,
		Delegate:        OptionalPubkey{Set: true, Key: testKey(4)},
		DelegatedAmount: 12,
	}
	tokenData := make([]byte, TokenAccountStateSize)
	if err := EncodeTokenAccountState(tokenData, token); err != nil {
		t.Fatal(err)
	}
	decodedToken, err := DecodeTokenAccountState(tokenData)
	if err != nil || decodedToken != token {
		t.Fatalf("token round trip = %+v, %v", decodedToken, err)
	}

	malformed := append([]byte(nil), tokenData...)
	malformed[2] = 1
	if _, err := DecodeTokenAccountState(malformed); err == nil {
		t.Fatal("non-canonical reserved byte accepted")
	}
	malformed = make([]byte, TokenAccountStateSize)
	malformed[0], malformed[1] = tokenStateTag, flagInitialized
	malformed[16] = 1
	if _, err := DecodeTokenAccountState(malformed); err == nil {
		t.Fatal("allowance without delegate accepted")
	}
}

func TestInstructionBuildersUseExactPrivilegesAndOrder(t *testing.T) {
	programID, mint, source, destination, authority := testKey(1), testKey(2), testKey(3), testKey(4), testKey(5)
	instruction := Transfer(programID, source, destination, authority, 7)
	if instruction.ProgramID != programID || len(instruction.Accounts) != 3 {
		t.Fatalf("unexpected transfer: %#v", instruction)
	}
	want := []sdk.AccountMeta{
		sdk.Writable(source, false),
		sdk.Writable(destination, false),
		sdk.Readonly(authority, true),
	}
	for index := range want {
		if instruction.Accounts[index] != want[index] {
			t.Fatalf("meta %d = %#v, want %#v", index, instruction.Accounts[index], want[index])
		}
	}
	if decoded, err := DecodeInstruction(instruction.Data); err != nil || decoded.Kind != InstructionTransfer || decoded.Amount != 7 {
		t.Fatalf("transfer decode = %#v, %v", decoded, err)
	}

	initialize := InitializeMint(programID, mint, authority, 6)
	if len(initialize.Accounts) != 1 || !initialize.Accounts[0].IsWritable || !initialize.Accounts[0].IsSigner {
		t.Fatalf("initialize mint privileges = %#v", initialize.Accounts)
	}
	initializeAccount := InitializeAccount(programID, source, mint, authority)
	if len(initializeAccount.Accounts) != 2 || !initializeAccount.Accounts[0].IsWritable || !initializeAccount.Accounts[0].IsSigner || initializeAccount.Accounts[1].IsSigner {
		t.Fatalf("initialize account privileges = %#v", initializeAccount.Accounts)
	}
}

func testKey(seed byte) sdk.Pubkey {
	var key sdk.Pubkey
	for index := range key {
		key[index] = seed + byte(index)
	}
	return key
}

func splKey(key sdk.Pubkey) spl20.Pubkey { return spl20.Pubkey(key) }
