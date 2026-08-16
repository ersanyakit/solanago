package erc1155

import (
	"math"
	"testing"

	"github.com/ersanyakit/solanago/sdk"
)

func TestStateCodecCanonicalRoundTrip(t *testing.T) {
	authority := testKey(1)
	collection := CollectionState{Initialized: true, Authority: authority, NextID: math.MaxUint64}
	collectionData := make([]byte, CollectionStateSize)
	if err := EncodeCollectionState(collectionData, collection); err != nil {
		t.Fatal(err)
	}
	decodedCollection, err := DecodeCollectionState(collectionData)
	if err != nil || decodedCollection != collection {
		t.Fatalf("collection round trip = %+v, %v", decodedCollection, err)
	}

	tokenType := TokenTypeState{
		Initialized: true,
		Collection:  testKey(2),
		ID:          7,
		Supply:      99,
		URI:         "https://example.com/erc1155/7.json",
	}
	tokenData := make([]byte, TokenTypeStateSize)
	if err := EncodeTokenTypeState(tokenData, tokenType); err != nil {
		t.Fatal(err)
	}
	decodedTokenType, err := DecodeTokenTypeState(tokenData)
	if err != nil || decodedTokenType != tokenType {
		t.Fatalf("token type round trip = %+v, %v", decodedTokenType, err)
	}

	balance := BalanceState{Initialized: true, Collection: testKey(3), ID: 7, Owner: testKey(4), Amount: 42}
	balanceData := make([]byte, BalanceStateSize)
	if err := EncodeBalanceState(balanceData, balance); err != nil {
		t.Fatal(err)
	}
	decodedBalance, err := DecodeBalanceState(balanceData)
	if err != nil || decodedBalance != balance {
		t.Fatalf("balance round trip = %+v, %v", decodedBalance, err)
	}

	for _, approved := range []bool{true, false} {
		approval := ApprovalState{Initialized: true, Collection: testKey(5), Owner: testKey(6), Operator: testKey(7), Approved: approved}
		approvalData := make([]byte, ApprovalStateSize)
		if err := EncodeApprovalState(approvalData, approval); err != nil {
			t.Fatal(err)
		}
		decodedApproval, err := DecodeApprovalState(approvalData)
		if err != nil || decodedApproval != approval {
			t.Fatalf("approval round trip (approved=%v) = %+v, %v", approved, decodedApproval, err)
		}
	}
}

func TestStateCodecRejectsMalformedInput(t *testing.T) {
	if _, err := DecodeCollectionState(make([]byte, CollectionStateSize-1)); err == nil {
		t.Fatal("wrong-length collection accepted")
	}
	if _, err := DecodeTokenTypeState(make([]byte, TokenTypeStateSize+1)); err == nil {
		t.Fatal("wrong-length token type accepted")
	}
	wrongTag := make([]byte, BalanceStateSize)
	wrongTag[0] = 99
	if _, err := DecodeBalanceState(wrongTag); err == nil {
		t.Fatal("wrong state tag accepted")
	}
	nonBooleanApproved := make([]byte, ApprovalStateSize)
	nonBooleanApproved[0] = approvalTag
	nonBooleanApproved[97] = 2
	if _, err := DecodeApprovalState(nonBooleanApproved); err == nil {
		t.Fatal("non-boolean approved byte accepted")
	}
	oversizedURI := TokenTypeState{Initialized: true, URI: string(make([]byte, MaxURILength+1))}
	if err := EncodeTokenTypeState(make([]byte, TokenTypeStateSize), oversizedURI); err == nil {
		t.Fatal("over-length uri accepted")
	}

	// A freshly created (system.CreateAccount-zeroed) account decodes to an
	// uninitialized zero value, not an error.
	fresh, err := DecodeCollectionState(make([]byte, CollectionStateSize))
	if err != nil || fresh.Initialized {
		t.Fatalf("fresh account decode = %+v, %v", fresh, err)
	}
}

func TestInstructionCodecRoundTrip(t *testing.T) {
	authority, owner, collection := testKey(1), testKey(2), testKey(4)

	initCollection := EncodeInitializeCollection(authority)
	decoded, err := DecodeInstruction(initCollection)
	if err != nil || decoded.Kind != InstructionInitializeCollection || decoded.Authority != authority {
		t.Fatalf("initialize collection decode = %+v, %v", decoded, err)
	}

	uri := "https://example.com/erc1155/{id}.json"
	createTokenType, err := EncodeCreateTokenType(uri)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = DecodeInstruction(createTokenType)
	if err != nil || decoded.Kind != InstructionCreateTokenType || decoded.URI != uri {
		t.Fatalf("create token type decode = %+v, %v", decoded, err)
	}
	if _, err := EncodeCreateTokenType(string(make([]byte, MaxURILength+1))); err == nil {
		t.Fatal("over-length uri accepted by EncodeCreateTokenType")
	}

	initBalance := EncodeInitializeBalance(owner)
	decoded, err = DecodeInstruction(initBalance)
	if err != nil || decoded.Kind != InstructionInitializeBalance || decoded.Owner != owner {
		t.Fatalf("initialize balance decode = %+v, %v", decoded, err)
	}

	for _, vector := range []struct {
		kind InstructionKind
		data []byte
	}{
		{InstructionMintTo, EncodeMintTo(math.MaxUint64 - 1)},
		{InstructionBurn, EncodeBurn(math.MaxUint64 - 2)},
		{InstructionTransfer, EncodeTransfer(math.MaxUint64 - 3)},
		{InstructionTransferFrom, EncodeTransferFrom(math.MaxUint64 - 4)},
	} {
		decoded, err = DecodeInstruction(vector.data)
		if err != nil || decoded.Kind != vector.kind {
			t.Fatalf("%v decode = %+v, %v", vector.kind, decoded, err)
		}
	}

	for _, approved := range []bool{true, false} {
		initApproval := EncodeInitializeApproval(collection, approved)
		decoded, err = DecodeInstruction(initApproval)
		if err != nil || decoded.Kind != InstructionInitializeApproval || decoded.Collection != collection || decoded.Approved != approved {
			t.Fatalf("initialize approval decode (approved=%v) = %+v, %v", approved, decoded, err)
		}
		setApproval := EncodeSetApproval(approved)
		decoded, err = DecodeInstruction(setApproval)
		if err != nil || decoded.Kind != InstructionSetApproval || decoded.Approved != approved {
			t.Fatalf("set approval decode (approved=%v) = %+v, %v", approved, decoded, err)
		}
	}

	if _, err := DecodeInstruction(nil); err == nil {
		t.Fatal("empty instruction data accepted")
	}
	if _, err := DecodeInstruction([]byte{byte(InstructionMintTo), 1, 2}); err == nil {
		t.Fatal("truncated amount instruction accepted")
	}
	if _, err := DecodeInstruction([]byte{200}); err == nil {
		t.Fatal("unknown instruction tag accepted")
	}
}

func TestInstructionBuildersUseExactPrivilegesAndOrder(t *testing.T) {
	programID, collection, tokenType, balance, authority, owner := testKey(1), testKey(2), testKey(3), testKey(4), testKey(5), testKey(6)

	init := InitializeCollection(programID, collection, authority)
	if len(init.Accounts) != 1 || !init.Accounts[0].IsWritable || !init.Accounts[0].IsSigner {
		t.Fatalf("initialize collection privileges = %#v", init.Accounts)
	}

	create, err := CreateTokenType(programID, tokenType, collection, authority, "uri")
	if err != nil {
		t.Fatal(err)
	}
	wantCreate := []sdk.AccountMeta{
		sdk.Writable(tokenType, true),
		sdk.Writable(collection, false),
		sdk.Readonly(authority, true),
	}
	assertAccounts(t, "create token type", create.Accounts, wantCreate)

	mint := MintTo(programID, collection, tokenType, balance, authority, 5)
	wantMint := []sdk.AccountMeta{
		sdk.Readonly(collection, false),
		sdk.Writable(tokenType, false),
		sdk.Writable(balance, false),
		sdk.Readonly(authority, true),
	}
	assertAccounts(t, "mint to", mint.Accounts, wantMint)

	transfer := Transfer(programID, tokenType, balance, owner, 3)
	wantTransfer := []sdk.AccountMeta{
		sdk.Writable(tokenType, false),
		sdk.Writable(balance, false),
		sdk.Readonly(owner, true),
	}
	assertAccounts(t, "transfer", transfer.Accounts, wantTransfer)

	transferFrom := TransferFrom(programID, tokenType, balance, collection, authority, 3)
	wantTransferFrom := []sdk.AccountMeta{
		sdk.Writable(tokenType, false),
		sdk.Writable(balance, false),
		sdk.Readonly(collection, false),
		sdk.Readonly(authority, true),
	}
	assertAccounts(t, "transfer from", transferFrom.Accounts, wantTransferFrom)
}

func assertAccounts(t *testing.T, label string, got, want []sdk.AccountMeta) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s accounts = %#v, want %#v", label, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s account[%d] = %#v, want %#v", label, index, got[index], want[index])
		}
	}
}

func testKey(seed byte) sdk.Pubkey {
	var key sdk.Pubkey
	for index := range key {
		key[index] = seed + byte(index)
	}
	return key
}
