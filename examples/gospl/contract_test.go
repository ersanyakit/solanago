package gospl

import (
	"bytes"
	"math"
	"os"
	"testing"

	"github.com/ersanyakit/go-solana/compiler"
	"github.com/ersanyakit/go-solana/examples/spl20"
	"github.com/ersanyakit/go-solana/runtime"
	"github.com/ersanyakit/go-solana/sbpf"
	"github.com/ersanyakit/go-solana/sdk"
	"github.com/ersanyakit/go-solana/vm"
)

type pairedAccount struct {
	guest  runtime.Account
	native *spl20.Account
}

type differentialHarness struct {
	t          *testing.T
	programID  sdk.Pubkey
	native     spl20.Program
	machine    *vm.VM
	executable *compiler.Executable
}

func newDifferentialHarness(t *testing.T) *differentialHarness {
	t.Helper()
	source, err := os.ReadFile("testdata/program.go")
	if err != nil {
		t.Fatal(err)
	}
	program, err := compiler.CompileSource("program.go", source)
	if err != nil {
		t.Fatalf("compile GOSPL source: %v", err)
	}
	executable, err := compiler.GenerateSolanaEntrypoint(program, "Program")
	if err != nil {
		t.Fatalf("generate Solana entrypoint: %v", err)
	}
	config := vm.DefaultConfig()
	config.MaxInstructions = 5_000_000
	machine, err := vm.NewWithConfig(executable.Instructions, config)
	if err != nil {
		t.Fatal(err)
	}
	programID := testKey(190)
	return &differentialHarness{
		t:          t,
		programID:  programID,
		native:     spl20.Program{ID: splKey(programID)},
		machine:    machine,
		executable: executable,
	}
}

func (h *differentialHarness) owned(key sdk.Pubkey, size int, writable bool) *pairedAccount {
	data := make([]byte, size)
	return &pairedAccount{
		guest: runtime.Account{Key: key, Owner: h.programID, Data: data, IsWritable: writable},
		native: &spl20.Account{
			Key: splKey(key), Owner: splKey(h.programID), Data: append([]byte(nil), data...), IsWritable: writable,
		},
	}
}

func (h *differentialHarness) authority(key sdk.Pubkey, signer bool) *pairedAccount {
	return &pairedAccount{
		guest:  runtime.Account{Key: key, IsSigner: signer},
		native: &spl20.Account{Key: splKey(key), IsSigner: signer},
	}
}

func (h *differentialHarness) invoke(accounts []*pairedAccount, instruction []byte) uint64 {
	h.t.Helper()
	nativeAccounts := make([]*spl20.Account, len(accounts))
	inputs := make([]runtime.InputAccount, len(accounts))
	for index, account := range accounts {
		nativeAccounts[index] = account.native
		inputs[index] = runtime.UniqueInputAccount(account.guest)
	}

	serialized, err := runtime.SerializeInputV1(h.programID, inputs, instruction)
	if err != nil {
		h.t.Fatal(err)
	}
	before := make([][]byte, len(accounts))
	for index, region := range serialized.AccountRegions {
		offset := region.DataAddress - sbpf.MMInputStart
		before[index] = append([]byte(nil), serialized.Buffer[offset:offset+uint64(region.OriginalDataLen)]...)
	}
	result, err := h.machine.RunWithMemory(
		[]vm.MemoryRegion{vm.WritableRegion(sbpf.MMInputStart, serialized.Buffer)},
		sbpf.MMInputStart,
		serialized.InstructionDataAddress,
	)
	if err != nil {
		h.t.Fatalf("run compiled GOSPL: %v", err)
	}

	nativeErr := h.native.Process(nativeAccounts, instruction)
	want := uint64(0)
	if nativeErr != nil {
		code, ok := nativeErr.(spl20.ProgramError)
		if !ok {
			h.t.Fatalf("unexpected native error %T: %v", nativeErr, nativeErr)
		}
		want = uint64(code)
	}
	if result != want {
		h.t.Fatalf("compiled return code %d, native return code %d (%v)", result, want, nativeErr)
	}

	for index, region := range serialized.AccountRegions {
		offset := region.DataAddress - sbpf.MMInputStart
		actual := serialized.Buffer[offset : offset+uint64(region.OriginalDataLen)]
		if result != 0 {
			if !bytes.Equal(actual, before[index]) {
				h.t.Fatalf("failed instruction partially mutated guest account %d", index)
			}
			continue
		}
		accounts[index].guest.Data = append(accounts[index].guest.Data[:0], actual...)
	}
	for index, account := range accounts {
		if !bytes.Equal(account.guest.Data, account.native.Data) {
			h.t.Fatalf("account %d differs from native reference\nguest:  %x\nnative: %x", index, account.guest.Data, account.native.Data)
		}
	}
	return result
}

func TestCompiledContractMatchesNativeFullStateMachine(t *testing.T) {
	h := newDifferentialHarness(t)
	mint := h.owned(testKey(1), MintStateSize, true)
	source := h.owned(testKey(2), TokenAccountStateSize, true)
	destination := h.owned(testKey(3), TokenAccountStateSize, true)
	mintAuthority := h.authority(testKey(4), true)
	sourceOwner := h.authority(testKey(5), true)
	destinationOwner := h.authority(testKey(6), true)
	delegate := h.authority(testKey(7), true)

	mint.guest.IsSigner, mint.native.IsSigner = true, true
	h.invoke([]*pairedAccount{mint}, EncodeInitializeMint(6, mintAuthority.guest.Key))
	mint.guest.IsSigner, mint.native.IsSigner = false, false
	source.guest.IsSigner, source.native.IsSigner = true, true
	h.invoke([]*pairedAccount{source, mint}, EncodeInitializeAccount(sourceOwner.guest.Key))
	source.guest.IsSigner, source.native.IsSigner = false, false
	destination.guest.IsSigner, destination.native.IsSigner = true, true
	h.invoke([]*pairedAccount{destination, mint}, EncodeInitializeAccount(destinationOwner.guest.Key))
	destination.guest.IsSigner, destination.native.IsSigner = false, false
	h.invoke([]*pairedAccount{mint, source, mintAuthority}, mustAmount(t, InstructionMintTo, 1_000))
	h.invoke([]*pairedAccount{source, destination, sourceOwner}, mustAmount(t, InstructionTransfer, 250))
	h.invoke([]*pairedAccount{destination, mint, destinationOwner}, mustAmount(t, InstructionBurn, 50))

	mintState, err := DecodeMintState(mint.guest.Data)
	if err != nil || mintState.Supply != 950 || mintState.Decimals != 6 {
		t.Fatalf("mint after base flow = %+v, %v", mintState, err)
	}
	sourceState, _ := DecodeTokenAccountState(source.guest.Data)
	destinationState, _ := DecodeTokenAccountState(destination.guest.Data)
	if sourceState.Amount != 750 || destinationState.Amount != 200 {
		t.Fatalf("base balances source=%d destination=%d", sourceState.Amount, destinationState.Amount)
	}

	h.invoke([]*pairedAccount{source, sourceOwner, delegate}, mustAmount(t, InstructionApprove, 120))
	h.invoke([]*pairedAccount{source, destination, delegate}, mustAmount(t, InstructionTransfer, 70))
	h.invoke([]*pairedAccount{source, mint, delegate}, mustAmount(t, InstructionBurn, 20))
	sourceState, _ = DecodeTokenAccountState(source.guest.Data)
	if !sourceState.Delegate.Set || sourceState.Delegate.Key != delegate.guest.Key || sourceState.DelegatedAmount != 30 {
		t.Fatalf("delegated state = %+v", sourceState)
	}
	if got := h.invoke([]*pairedAccount{source, destination, delegate}, mustAmount(t, InstructionTransfer, 31)); got != uint64(ErrInsufficientAllowance) {
		t.Fatalf("allowance failure = %d", got)
	}
	h.invoke([]*pairedAccount{source, sourceOwner}, EncodeRevoke())

	newOwner := h.authority(testKey(8), true)
	setOwner, err := EncodeSetAuthority(AuthorityAccountOwner, OptionalPubkey{Set: true, Key: newOwner.guest.Key})
	if err != nil {
		t.Fatal(err)
	}
	h.invoke([]*pairedAccount{source, sourceOwner}, setOwner)
	if got := h.invoke([]*pairedAccount{source, destination, sourceOwner}, mustAmount(t, InstructionTransfer, 1)); got != uint64(ErrInvalidAuthority) {
		t.Fatalf("old owner failure = %d", got)
	}
	h.invoke([]*pairedAccount{source, destination, newOwner}, mustAmount(t, InstructionTransfer, 1))

	newMintAuthority := h.authority(testKey(9), true)
	setMint, err := EncodeSetAuthority(AuthorityMintTokens, OptionalPubkey{Set: true, Key: newMintAuthority.guest.Key})
	if err != nil {
		t.Fatal(err)
	}
	h.invoke([]*pairedAccount{mint, mintAuthority}, setMint)
	if got := h.invoke([]*pairedAccount{mint, source, mintAuthority}, mustAmount(t, InstructionMintTo, 1)); got != uint64(ErrInvalidAuthority) {
		t.Fatalf("old mint authority failure = %d", got)
	}
	h.invoke([]*pairedAccount{mint, source, newMintAuthority}, mustAmount(t, InstructionMintTo, 10))
	disableMint, err := EncodeSetAuthority(AuthorityMintTokens, OptionalPubkey{})
	if err != nil {
		t.Fatal(err)
	}
	h.invoke([]*pairedAccount{mint, newMintAuthority}, disableMint)
	if got := h.invoke([]*pairedAccount{mint, source, newMintAuthority}, mustAmount(t, InstructionMintTo, 1)); got != uint64(ErrAuthorityDisabled) {
		t.Fatalf("disabled mint failure = %d", got)
	}
}

func TestCompiledContractRejectsPrivilegesOverflowAndMalformedInputAtomically(t *testing.T) {
	h := newDifferentialHarness(t)
	if got := h.invoke(nil, []byte{byte(InstructionInitializeMint)}); got != uint64(ErrInvalidInstruction) {
		t.Fatalf("malformed initialize with missing accounts = %d", got)
	}
	malformedAuthority := make([]byte, 35)
	malformedAuthority[0] = byte(InstructionSetAuthority)
	malformedAuthority[2] = 2
	if got := h.invoke(nil, malformedAuthority); got != uint64(ErrInvalidInstruction) {
		t.Fatalf("malformed authority with missing accounts = %d", got)
	}
	mint := h.owned(testKey(21), MintStateSize, true)
	token := h.owned(testKey(22), TokenAccountStateSize, true)
	destination := h.owned(testKey(25), TokenAccountStateSize, true)
	authority := h.authority(testKey(23), true)
	owner := h.authority(testKey(24), true)
	if got := h.invoke([]*pairedAccount{mint}, EncodeInitializeMint(6, authority.guest.Key)); got != uint64(ErrMissingSignature) {
		t.Fatalf("unsigned mint initialization = %d", got)
	}
	mint.guest.IsSigner, mint.native.IsSigner = true, true
	h.invoke([]*pairedAccount{mint}, EncodeInitializeMint(6, authority.guest.Key))
	mint.guest.IsSigner, mint.native.IsSigner = false, false
	if got := h.invoke([]*pairedAccount{token, mint}, EncodeInitializeAccount(owner.guest.Key)); got != uint64(ErrMissingSignature) {
		t.Fatalf("unsigned token initialization = %d", got)
	}
	token.guest.IsSigner, token.native.IsSigner = true, true
	h.invoke([]*pairedAccount{token, mint}, EncodeInitializeAccount(owner.guest.Key))
	token.guest.IsSigner, token.native.IsSigner = false, false
	destination.guest.IsSigner, destination.native.IsSigner = true, true
	h.invoke([]*pairedAccount{destination, mint}, EncodeInitializeAccount(owner.guest.Key))
	destination.guest.IsSigner, destination.native.IsSigner = false, false

	authority.guest.IsSigner = false
	authority.native.IsSigner = false
	if got := h.invoke([]*pairedAccount{mint, token, authority}, mustAmount(t, InstructionMintTo, 1)); got != uint64(ErrMissingSignature) {
		t.Fatalf("missing signature = %d", got)
	}
	authority.guest.IsSigner = true
	authority.native.IsSigner = true

	token.guest.IsWritable = false
	token.native.IsWritable = false
	if got := h.invoke([]*pairedAccount{token, destination, owner}, mustAmount(t, InstructionTransfer, 0)); got != uint64(ErrAccountReadOnly) {
		t.Fatalf("read-only source = %d", got)
	}
	token.guest.IsWritable = true
	token.native.IsWritable = true
	wrongOwner := testKey(99)
	destination.guest.Owner = wrongOwner
	destination.native.Owner = splKey(wrongOwner)
	if got := h.invoke([]*pairedAccount{token, destination, owner}, mustAmount(t, InstructionTransfer, 0)); got != uint64(ErrInvalidProgramOwner) {
		t.Fatalf("wrong program owner = %d", got)
	}
	destination.guest.Owner = h.programID
	destination.native.Owner = splKey(h.programID)

	mintState, _ := DecodeMintState(mint.guest.Data)
	mintState.Supply = math.MaxUint64
	if err := EncodeMintState(mint.guest.Data, mintState); err != nil {
		t.Fatal(err)
	}
	nativeMint := spl20.MintState{
		Initialized:   true,
		Decimals:      mintState.Decimals,
		Supply:        mintState.Supply,
		MintAuthority: spl20.OptionalPubkey{Set: true, Key: splKey(mintState.MintAuthority.Key)},
	}
	if err := spl20.EncodeMintState(mint.native.Data, nativeMint); err != nil {
		t.Fatal(err)
	}
	if got := h.invoke([]*pairedAccount{mint, token, authority}, mustAmount(t, InstructionMintTo, 1)); got != uint64(ErrArithmeticOverflow) {
		t.Fatalf("supply overflow = %d", got)
	}

	if got := h.invoke([]*pairedAccount{token, token, owner}, mustAmount(t, InstructionTransfer, 1)); got != uint64(ErrSameAccount) {
		t.Fatalf("same account precedence = %d", got)
	}

	if got := h.invoke([]*pairedAccount{mint}, []byte{99}); got != uint64(ErrInvalidInstruction) {
		t.Fatalf("unknown instruction = %d", got)
	}
}

func TestCompiledContractResolvesAgaveDuplicateAccountSlots(t *testing.T) {
	h := newDifferentialHarness(t)
	owner := testKey(61)
	mint := testKey(62)
	state := TokenAccountState{Initialized: true, Mint: mint, Owner: owner, Amount: 7}
	data := make([]byte, TokenAccountStateSize)
	if err := EncodeTokenAccountState(data, state); err != nil {
		t.Fatal(err)
	}
	inputs := []runtime.InputAccount{
		runtime.UniqueInputAccount(runtime.Account{Key: testKey(63), Owner: h.programID, Data: data, IsWritable: true}),
		runtime.DuplicateInputAccount(0),
		runtime.UniqueInputAccount(runtime.Account{Key: owner, IsSigner: true}),
	}
	serialized, err := runtime.SerializeInputV1(h.programID, inputs, mustAmount(t, InstructionTransfer, 1))
	if err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), serialized.Buffer...)
	result, err := h.machine.RunWithMemory(
		[]vm.MemoryRegion{vm.WritableRegion(sbpf.MMInputStart, serialized.Buffer)},
		sbpf.MMInputStart,
		serialized.InstructionDataAddress,
	)
	if err != nil || result != uint64(ErrSameAccount) {
		t.Fatalf("duplicate source/destination = %d, %v", result, err)
	}
	if !bytes.Equal(serialized.Buffer, before) {
		t.Fatal("duplicate-account rejection mutated ABI input")
	}
}

func TestCompiledContractRunsAgainstDirectMappedAgaveABI(t *testing.T) {
	h := newDifferentialHarness(t)
	authority := testKey(41)
	mintKey := testKey(42)
	serialized, err := runtime.SerializeInputV1WithOptions(
		h.programID,
		[]runtime.InputAccount{runtime.UniqueInputAccount(runtime.Account{
			Key: mintKey, Owner: h.programID, Data: make([]byte, MintStateSize), IsSigner: true, IsWritable: true,
		})},
		EncodeInitializeMint(9, authority),
		runtime.SerializeOptions{AccountDataDirectMapping: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	regions := make([]vm.MemoryRegion, 0, len(serialized.MemoryRegions()))
	for _, region := range serialized.MemoryRegions() {
		if len(region.Data) == 0 {
			continue
		}
		if region.Writable {
			regions = append(regions, vm.WritableRegion(region.VMStart, region.Data))
		} else {
			regions = append(regions, vm.ReadOnlyRegion(region.VMStart, region.Data))
		}
	}
	result, err := h.machine.RunWithMemory(regions, sbpf.MMInputStart, serialized.InstructionDataAddress)
	if err != nil || result != 0 {
		t.Fatalf("direct-mapped run = %d, %v", result, err)
	}
	context, err := serialized.MappedContext()
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeMintState(context.Accounts[0].Data())
	if err != nil || !state.Initialized || state.Decimals != 9 || state.MintAuthority.Key != authority {
		t.Fatalf("direct-mapped state = %+v, %v", state, err)
	}
}

func TestContractUsesAbsoluteR2AndGeneratedWrapper(t *testing.T) {
	h := newDifferentialHarness(t)
	if h.executable.Entry != "entrypoint" || h.executable.Functions["entrypoint"] != 0 {
		t.Fatalf("entrypoint metadata = %#v", h.executable.Functions)
	}
	mint := h.owned(testKey(51), MintStateSize, true)
	authority := h.authority(testKey(52), true)
	mint.guest.IsSigner, mint.native.IsSigner = true, true
	if got := h.invoke([]*pairedAccount{mint}, EncodeInitializeMint(2, authority.guest.Key)); got != 0 {
		t.Fatalf("absolute r2 invocation = %d", got)
	}
}

func mustAmount(t *testing.T, kind InstructionKind, amount uint64) []byte {
	t.Helper()
	data, err := EncodeAmountInstruction(kind, amount)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
