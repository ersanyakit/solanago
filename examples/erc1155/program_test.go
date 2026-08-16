package erc1155_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ersanyakit/solanago/compiler"
	"github.com/ersanyakit/solanago/examples/erc1155"
	"github.com/ersanyakit/solanago/runtime"
	"github.com/ersanyakit/solanago/sbpf"
	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/vm"
)

// harness runs the compiled multi-token program in the reference VM against
// real serialized Agave ABIv1 memory. This is the correctness oracle for
// this example: every assertion below decodes actual post-instruction
// account bytes produced by executing the compiled sBPF, not a separate
// hand-maintained model (see README.md for why no spl20-style native
// reference exists here).
type harness struct {
	t         *testing.T
	programID sdk.Pubkey
	machine   *vm.VM
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	directory := testdataDir(t)
	accountsSource, err := os.ReadFile(filepath.Join(directory, "accounts.go"))
	if err != nil {
		t.Fatal(err)
	}
	programSource, err := os.ReadFile(filepath.Join(directory, "program.go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := compiler.CompileSources(
		[]string{"accounts.go", "program.go"},
		[][]byte{accountsSource, programSource},
	)
	if err != nil {
		t.Fatalf("compile erc1155 contract: %v", err)
	}
	executable, err := compiler.GenerateSolanaEntrypoint(program, "Program")
	if err != nil {
		t.Fatalf("generate Solana entrypoint: %v", err)
	}
	config := vm.DefaultConfig()
	config.MaxInstructions = 2_000_000
	machine, err := vm.NewWithConfig(executable.Instructions, config)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, programID: testKey(190), machine: machine}
}

func (h *harness) owned(key sdk.Pubkey, size int) *runtime.Account {
	return &runtime.Account{Key: key, Owner: h.programID, Data: make([]byte, size)}
}

func (h *harness) authority(key sdk.Pubkey) *runtime.Account {
	return &runtime.Account{Key: key}
}

// setFlags mutates and returns account so every invoke call site is
// self-documenting about the exact privileges that instruction needs for
// that account, matching program.go's per-handler accounts comment.
func setFlags(account *runtime.Account, writable, signer bool) *runtime.Account {
	account.IsWritable = writable
	account.IsSigner = signer
	return account
}

func (h *harness) invoke(accounts []*runtime.Account, instruction []byte) uint64 {
	h.t.Helper()
	inputs := make([]runtime.InputAccount, len(accounts))
	for index, account := range accounts {
		inputs[index] = runtime.UniqueInputAccount(*account)
	}
	serialized, err := runtime.SerializeInputV1(h.programID, inputs, instruction)
	if err != nil {
		h.t.Fatal(err)
	}
	result, err := h.machine.RunWithMemory(
		[]vm.MemoryRegion{vm.WritableRegion(sbpf.MMInputStart, serialized.Buffer)},
		sbpf.MMInputStart,
		serialized.InstructionDataAddress,
	)
	if err != nil {
		h.t.Fatalf("run compiled erc1155 program: %v", err)
	}
	for index, region := range serialized.AccountRegions {
		offset := region.DataAddress - sbpf.MMInputStart
		accounts[index].Data = append(accounts[index].Data[:0], serialized.Buffer[offset:offset+uint64(region.OriginalDataLen)]...)
	}
	return result
}

func testKey(seed byte) sdk.Pubkey {
	var key sdk.Pubkey
	for index := range key {
		key[index] = seed + byte(index)
	}
	return key
}

// TestERC1155FullLifecycle exercises every instruction once, in the order a
// real client would use them, asserting exact decoded state after each
// step: init collection -> create token type -> init two balances -> mint
// -> transfer -> burn -> init+set approval -> transferFrom.
func TestERC1155FullLifecycle(t *testing.T) {
	h := newHarness(t)
	collectionKey, tokenTypeKey := testKey(1), testKey(2)
	balanceAKey, balanceBKey := testKey(3), testKey(4)
	approvalKey := testKey(5)
	authorityKey := testKey(6)
	ownerAKey, ownerBKey, operatorKey := testKey(7), testKey(8), testKey(9)

	collection := h.owned(collectionKey, erc1155.CollectionStateSize)
	if got := h.invoke([]*runtime.Account{setFlags(collection, true, true)},
		erc1155.EncodeInitializeCollection(authorityKey)); got != 0 {
		t.Fatalf("initialize collection = %d", got)
	}
	collectionState, err := erc1155.DecodeCollectionState(collection.Data)
	if err != nil || !collectionState.Initialized || collectionState.Authority != authorityKey || collectionState.NextID != 0 {
		t.Fatalf("collection state = %+v, %v", collectionState, err)
	}

	tokenType := h.owned(tokenTypeKey, erc1155.TokenTypeStateSize)
	authority := h.authority(authorityKey)
	createData, err := erc1155.EncodeCreateTokenType("https://example.com/erc1155/1.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.invoke([]*runtime.Account{
		setFlags(tokenType, true, true),
		setFlags(collection, true, false),
		setFlags(authority, false, true),
	}, createData); got != 0 {
		t.Fatalf("create token type = %d", got)
	}
	tokenTypeState, err := erc1155.DecodeTokenTypeState(tokenType.Data)
	if err != nil || !tokenTypeState.Initialized || tokenTypeState.Collection != collectionKey ||
		tokenTypeState.ID != 0 || tokenTypeState.Supply != 0 || tokenTypeState.URI != "https://example.com/erc1155/1.json" {
		t.Fatalf("token type state = %+v, %v", tokenTypeState, err)
	}
	collectionState, err = erc1155.DecodeCollectionState(collection.Data)
	if err != nil || collectionState.NextID != 1 {
		t.Fatalf("collection next_id after create = %+v, %v", collectionState, err)
	}

	balanceA := h.owned(balanceAKey, erc1155.BalanceStateSize)
	if got := h.invoke([]*runtime.Account{
		setFlags(balanceA, true, true),
		setFlags(tokenType, false, false),
	}, erc1155.EncodeInitializeBalance(ownerAKey)); got != 0 {
		t.Fatalf("initialize balance a = %d", got)
	}
	balanceB := h.owned(balanceBKey, erc1155.BalanceStateSize)
	if got := h.invoke([]*runtime.Account{
		setFlags(balanceB, true, true),
		setFlags(tokenType, false, false),
	}, erc1155.EncodeInitializeBalance(ownerBKey)); got != 0 {
		t.Fatalf("initialize balance b = %d", got)
	}
	balanceAState, err := erc1155.DecodeBalanceState(balanceA.Data)
	if err != nil || balanceAState.Owner != ownerAKey || balanceAState.Amount != 0 {
		t.Fatalf("balance a state after init = %+v, %v", balanceAState, err)
	}

	if got := h.invoke([]*runtime.Account{
		setFlags(collection, false, false),
		setFlags(tokenType, true, false),
		setFlags(balanceA, true, false),
		setFlags(authority, false, true),
	}, erc1155.EncodeMintTo(100)); got != 0 {
		t.Fatalf("mint to = %d", got)
	}
	tokenTypeState, err = erc1155.DecodeTokenTypeState(tokenType.Data)
	if err != nil || tokenTypeState.Supply != 100 {
		t.Fatalf("token type supply after mint = %+v, %v", tokenTypeState, err)
	}
	balanceAState, err = erc1155.DecodeBalanceState(balanceA.Data)
	if err != nil || balanceAState.Amount != 100 {
		t.Fatalf("balance a after mint = %+v, %v", balanceAState, err)
	}

	ownerA := h.authority(ownerAKey)
	if got := h.invoke([]*runtime.Account{
		setFlags(balanceA, true, false),
		setFlags(balanceB, true, false),
		setFlags(ownerA, false, true),
	}, erc1155.EncodeTransfer(40)); got != 0 {
		t.Fatalf("transfer = %d", got)
	}
	balanceAState, err = erc1155.DecodeBalanceState(balanceA.Data)
	if err != nil || balanceAState.Amount != 60 {
		t.Fatalf("balance a after transfer = %+v, %v", balanceAState, err)
	}
	balanceBState, err := erc1155.DecodeBalanceState(balanceB.Data)
	if err != nil || balanceBState.Amount != 40 || balanceBState.Owner != ownerBKey {
		t.Fatalf("balance b after transfer = %+v, %v", balanceBState, err)
	}

	if got := h.invoke([]*runtime.Account{
		setFlags(tokenType, true, false),
		setFlags(balanceA, true, false),
		setFlags(ownerA, false, true),
	}, erc1155.EncodeBurn(10)); got != 0 {
		t.Fatalf("burn = %d", got)
	}
	tokenTypeState, err = erc1155.DecodeTokenTypeState(tokenType.Data)
	if err != nil || tokenTypeState.Supply != 90 {
		t.Fatalf("token type supply after burn = %+v, %v", tokenTypeState, err)
	}
	balanceAState, err = erc1155.DecodeBalanceState(balanceA.Data)
	if err != nil || balanceAState.Amount != 50 {
		t.Fatalf("balance a after burn = %+v, %v", balanceAState, err)
	}

	approval := h.owned(approvalKey, erc1155.ApprovalStateSize)
	operator := h.authority(operatorKey)
	if got := h.invoke([]*runtime.Account{
		setFlags(approval, true, true),
		setFlags(ownerA, false, true),
		setFlags(operator, false, false),
	}, erc1155.EncodeInitializeApproval(collectionKey, true)); got != 0 {
		t.Fatalf("initialize approval = %d", got)
	}
	approvalState, err := erc1155.DecodeApprovalState(approval.Data)
	if err != nil || approvalState.Owner != ownerAKey || approvalState.Operator != operatorKey || !approvalState.Approved {
		t.Fatalf("approval state after init = %+v, %v", approvalState, err)
	}

	if got := h.invoke([]*runtime.Account{
		setFlags(balanceA, true, false),
		setFlags(balanceB, true, false),
		setFlags(approval, false, false),
		setFlags(operator, false, true),
	}, erc1155.EncodeTransferFrom(15)); got != 0 {
		t.Fatalf("transfer from = %d", got)
	}
	balanceAState, err = erc1155.DecodeBalanceState(balanceA.Data)
	if err != nil || balanceAState.Amount != 35 {
		t.Fatalf("balance a after transferFrom = %+v, %v", balanceAState, err)
	}
	balanceBState, err = erc1155.DecodeBalanceState(balanceB.Data)
	if err != nil || balanceBState.Amount != 55 {
		t.Fatalf("balance b after transferFrom = %+v, %v", balanceBState, err)
	}

	// Revoking approval (SetApproval false) must block a further transferFrom.
	if got := h.invoke([]*runtime.Account{
		setFlags(approval, true, false),
		setFlags(ownerA, false, true),
	}, erc1155.EncodeSetApproval(false)); got != 0 {
		t.Fatalf("set approval = %d", got)
	}
	if got := h.invoke([]*runtime.Account{
		setFlags(balanceA, true, false),
		setFlags(balanceB, true, false),
		setFlags(approval, false, false),
		setFlags(operator, false, true),
	}, erc1155.EncodeTransferFrom(1)); got != uint64(erc1155.ErrNotApproved) {
		t.Fatalf("transfer from after revoke = %d, want %d (ErrNotApproved)", got, erc1155.ErrNotApproved)
	}
}

// TestERC1155RejectsInvalidOperationsWithExactErrorCodes covers the
// negative paths: wrong authority, insufficient balance, collection/id
// mismatch, missing signer, and an unapproved operator, each asserting the
// exact ProgramError the compiled program returns.
func TestERC1155RejectsInvalidOperationsWithExactErrorCodes(t *testing.T) {
	h := newHarness(t)
	collectionKey, tokenTypeKey := testKey(21), testKey(22)
	balanceAKey, balanceBKey := testKey(23), testKey(24)
	authorityKey, wrongAuthorityKey := testKey(25), testKey(26)
	ownerAKey, ownerBKey := testKey(27), testKey(28)

	collection := h.owned(collectionKey, erc1155.CollectionStateSize)
	h.invoke([]*runtime.Account{setFlags(collection, true, true)}, erc1155.EncodeInitializeCollection(authorityKey))

	tokenType := h.owned(tokenTypeKey, erc1155.TokenTypeStateSize)
	wrongAuthority := h.authority(wrongAuthorityKey)
	createData, err := erc1155.EncodeCreateTokenType("uri")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.invoke([]*runtime.Account{
		setFlags(tokenType, true, true),
		setFlags(collection, true, false),
		setFlags(wrongAuthority, false, true),
	}, createData); got != uint64(erc1155.ErrInvalidAuthority) {
		t.Fatalf("create token type with wrong authority = %d, want %d (ErrInvalidAuthority)", got, erc1155.ErrInvalidAuthority)
	}

	authority := h.authority(authorityKey)
	if got := h.invoke([]*runtime.Account{
		setFlags(tokenType, true, true),
		setFlags(collection, true, false),
		setFlags(authority, false, false), // not signed
	}, createData); got != uint64(erc1155.ErrMissingSignature) {
		t.Fatalf("create token type without signature = %d, want %d (ErrMissingSignature)", got, erc1155.ErrMissingSignature)
	}

	if got := h.invoke([]*runtime.Account{
		setFlags(tokenType, true, true),
		setFlags(collection, true, false),
		setFlags(authority, false, true),
	}, createData); got != 0 {
		t.Fatalf("create token type = %d", got)
	}

	balanceA := h.owned(balanceAKey, erc1155.BalanceStateSize)
	h.invoke([]*runtime.Account{setFlags(balanceA, true, true), setFlags(tokenType, false, false)}, erc1155.EncodeInitializeBalance(ownerAKey))
	balanceB := h.owned(balanceBKey, erc1155.BalanceStateSize)
	h.invoke([]*runtime.Account{setFlags(balanceB, true, true), setFlags(tokenType, false, false)}, erc1155.EncodeInitializeBalance(ownerBKey))
	h.invoke([]*runtime.Account{
		setFlags(collection, false, false),
		setFlags(tokenType, true, false),
		setFlags(balanceA, true, false),
		setFlags(authority, false, true),
	}, erc1155.EncodeMintTo(10))

	ownerB := h.authority(ownerBKey)
	if got := h.invoke([]*runtime.Account{
		setFlags(balanceA, true, false),
		setFlags(balanceB, true, false),
		setFlags(ownerB, false, true), // not balanceA's owner
	}, erc1155.EncodeTransfer(1)); got != uint64(erc1155.ErrInvalidAuthority) {
		t.Fatalf("transfer with wrong owner = %d, want %d (ErrInvalidAuthority)", got, erc1155.ErrInvalidAuthority)
	}

	ownerA := h.authority(ownerAKey)
	if got := h.invoke([]*runtime.Account{
		setFlags(balanceA, true, false),
		setFlags(balanceB, true, false),
		setFlags(ownerA, false, true),
	}, erc1155.EncodeTransfer(1000)); got != uint64(erc1155.ErrInsufficientBalance) {
		t.Fatalf("transfer over balance = %d, want %d (ErrInsufficientBalance)", got, erc1155.ErrInsufficientBalance)
	}

	otherTokenType := h.owned(testKey(29), erc1155.TokenTypeStateSize)
	createData2, err := erc1155.EncodeCreateTokenType("uri2")
	if err != nil {
		t.Fatal(err)
	}
	h.invoke([]*runtime.Account{
		setFlags(otherTokenType, true, true),
		setFlags(collection, true, false),
		setFlags(authority, false, true),
	}, createData2)
	balanceOtherType := h.owned(testKey(30), erc1155.BalanceStateSize)
	h.invoke([]*runtime.Account{setFlags(balanceOtherType, true, true), setFlags(otherTokenType, false, false)}, erc1155.EncodeInitializeBalance(ownerBKey))
	if got := h.invoke([]*runtime.Account{
		setFlags(balanceA, true, false),
		setFlags(balanceOtherType, true, false),
		setFlags(ownerA, false, true),
	}, erc1155.EncodeTransfer(1)); got != uint64(erc1155.ErrCollectionMismatch) {
		t.Fatalf("transfer across token ids = %d, want %d (ErrCollectionMismatch)", got, erc1155.ErrCollectionMismatch)
	}
}
