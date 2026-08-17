package erc20

import (
	"bytes"
	"testing"

	"github.com/ersanyakit/solanago/compiler"
	"github.com/ersanyakit/solanago/runtime"
	"github.com/ersanyakit/solanago/sbpf"
	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/vm"
)

// differentialHarness runs the compiled guest program (testdata/{accounts,
// program}.go) in the reference VM against real serialized Agave ABIv1
// memory, and cross-checks every result and every account's resulting data
// against this same package's native Program — the same approach
// examples/gospl uses against examples/spl20 and examples/payable uses for
// its non-CPI instructions. Unlike payable, this program never moves
// lamports, so every instruction (including the allowance path) gets full
// differential coverage — nothing here needs a live CPI executor.
type differentialHarness struct {
	t         *testing.T
	programID sdk.Pubkey
	native    Program
	machine   *vm.VM
}

func newDifferentialHarness(t *testing.T) *differentialHarness {
	t.Helper()
	program, err := compiler.CompileFiles([]string{"testdata/accounts.go", "testdata/program.go"})
	if err != nil {
		t.Fatalf("compile erc20 contract: %v", err)
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
	programID := testKey(190)
	return &differentialHarness{t: t, programID: programID, native: Program{ID: Pubkey(programID)}, machine: machine}
}

type pairedAccount struct {
	guest  runtime.Account
	native *Account
}

func (h *differentialHarness) owned(key sdk.Pubkey, size int, writable bool) *pairedAccount {
	data := make([]byte, size)
	return &pairedAccount{
		guest:  runtime.Account{Key: key, Owner: h.programID, Data: data, IsWritable: writable},
		native: &Account{Key: Pubkey(key), Owner: Pubkey(h.programID), Data: append([]byte(nil), data...), IsWritable: writable},
	}
}

func (h *differentialHarness) wallet(key sdk.Pubkey, signer bool) *pairedAccount {
	return &pairedAccount{
		guest:  runtime.Account{Key: key, IsSigner: signer},
		native: &Account{Key: Pubkey(key), IsSigner: signer},
	}
}

func (h *differentialHarness) invoke(accounts []*pairedAccount, instruction []byte) uint64 {
	h.t.Helper()
	nativeAccounts := make([]*Account, len(accounts))
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
		h.t.Fatalf("run compiled erc20 program: %v", err)
	}

	nativeErr := h.native.Process(nativeAccounts, instruction)
	want := uint64(0)
	if nativeErr != nil {
		code, ok := nativeErr.(ProgramError)
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

func testKey(seed byte) sdk.Pubkey {
	var key sdk.Pubkey
	for index := range key {
		key[index] = seed + byte(index)
	}
	return key
}

func mustEncodeInitializeMint(t *testing.T, name, symbol string, decimals uint8, authority Pubkey) []byte {
	t.Helper()
	data, err := EncodeInitializeMint(name, symbol, decimals, authority)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustEncodeAmount(t *testing.T, kind InstructionKind, amount uint64) []byte {
	t.Helper()
	data, err := EncodeAmountInstruction(kind, amount)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCompiledContractMatchesNativeFullLifecycle(t *testing.T) {
	h := newDifferentialHarness(t)
	authority := h.wallet(testKey(1), true)
	ownerA := h.wallet(testKey(2), true)
	ownerB := h.wallet(testKey(3), true)
	spender := h.wallet(testKey(4), true)

	mint := h.owned(testKey(10), MintStateSize, true)
	mint.guest.IsSigner, mint.native.IsSigner = true, true
	h.invoke([]*pairedAccount{mint}, mustEncodeInitializeMint(t, "Example Token", "EXT", 6, Pubkey(authority.guest.Key)))
	mint.guest.IsSigner, mint.native.IsSigner = false, false

	holderA := h.owned(testKey(11), BalanceStateSize, true)
	holderA.guest.IsSigner, holderA.native.IsSigner = true, true
	h.invoke([]*pairedAccount{holderA, mint}, EncodeInitializeBalance(Pubkey(ownerA.guest.Key)))
	holderA.guest.IsSigner, holderA.native.IsSigner = false, false

	holderB := h.owned(testKey(12), BalanceStateSize, true)
	holderB.guest.IsSigner, holderB.native.IsSigner = true, true
	h.invoke([]*pairedAccount{holderB, mint}, EncodeInitializeBalance(Pubkey(ownerB.guest.Key)))
	holderB.guest.IsSigner, holderB.native.IsSigner = false, false

	h.invoke([]*pairedAccount{mint, holderA, authority}, mustEncodeAmount(t, InstructionMintTo, 1_000))
	h.invoke([]*pairedAccount{holderA, holderB, ownerA}, mustEncodeAmount(t, InstructionTransfer, 250))
	h.invoke([]*pairedAccount{holderB, mint, ownerB}, mustEncodeAmount(t, InstructionBurn, 50))

	mintState, err := DecodeMintState(mint.guest.Data)
	if err != nil || mintState.TotalSupply != 950 || mintState.Name != "Example Token" || mintState.Symbol != "EXT" || mintState.Decimals != 6 {
		t.Fatalf("mint state = %+v, %v", mintState, err)
	}
	a, _ := DecodeBalanceState(holderA.guest.Data)
	b, _ := DecodeBalanceState(holderB.guest.Data)
	if a.Amount != 750 || b.Amount != 200 {
		t.Fatalf("balances after base flow: A=%d B=%d, want 750/200", a.Amount, b.Amount)
	}

	allowance := h.owned(testKey(13), AllowanceStateSize, true)
	allowance.guest.IsSigner, allowance.native.IsSigner = true, true
	h.invoke([]*pairedAccount{allowance, mint, ownerA, spender}, EncodeInitializeAllowance())
	allowance.guest.IsSigner, allowance.native.IsSigner = false, false

	h.invoke([]*pairedAccount{allowance, ownerA}, mustEncodeAmount(t, InstructionApprove, 300))
	h.invoke([]*pairedAccount{holderA, holderB, allowance, spender}, mustEncodeAmount(t, InstructionTransferFrom, 120))

	allowanceState, err := DecodeAllowanceState(allowance.guest.Data)
	if err != nil || allowanceState.Amount != 180 {
		t.Fatalf("allowance state = %+v, %v, want amount=180", allowanceState, err)
	}
	a, _ = DecodeBalanceState(holderA.guest.Data)
	b, _ = DecodeBalanceState(holderB.guest.Data)
	if a.Amount != 630 || b.Amount != 320 {
		t.Fatalf("balances after transferFrom: A=%d B=%d, want 630/320", a.Amount, b.Amount)
	}

	if got := h.invoke([]*pairedAccount{holderA, holderB, allowance, spender}, mustEncodeAmount(t, InstructionTransferFrom, 181)); got != uint64(ErrInsufficientAllowance) {
		t.Fatalf("over-allowance transferFrom = %d, want %d", got, ErrInsufficientAllowance)
	}
}

func TestCompiledContractRejectsWrongAuthorityAndInsufficientFunds(t *testing.T) {
	h := newDifferentialHarness(t)
	authority := h.wallet(testKey(21), true)
	notAuthority := h.wallet(testKey(22), true)
	ownerA := h.wallet(testKey(23), true)
	ownerB := h.wallet(testKey(24), true)

	mint := h.owned(testKey(30), MintStateSize, true)
	mint.guest.IsSigner, mint.native.IsSigner = true, true
	h.invoke([]*pairedAccount{mint}, mustEncodeInitializeMint(t, "X", "X", 0, Pubkey(authority.guest.Key)))
	mint.guest.IsSigner, mint.native.IsSigner = false, false

	holderA := h.owned(testKey(31), BalanceStateSize, true)
	holderA.guest.IsSigner, holderA.native.IsSigner = true, true
	h.invoke([]*pairedAccount{holderA, mint}, EncodeInitializeBalance(Pubkey(ownerA.guest.Key)))
	holderA.guest.IsSigner, holderA.native.IsSigner = false, false

	holderB := h.owned(testKey(32), BalanceStateSize, true)
	holderB.guest.IsSigner, holderB.native.IsSigner = true, true
	h.invoke([]*pairedAccount{holderB, mint}, EncodeInitializeBalance(Pubkey(ownerB.guest.Key)))
	holderB.guest.IsSigner, holderB.native.IsSigner = false, false

	if got := h.invoke([]*pairedAccount{mint, holderA, notAuthority}, mustEncodeAmount(t, InstructionMintTo, 100)); got != uint64(ErrInvalidAuthority) {
		t.Fatalf("wrong mint authority = %d, want %d", got, ErrInvalidAuthority)
	}
	h.invoke([]*pairedAccount{mint, holderA, authority}, mustEncodeAmount(t, InstructionMintTo, 100))
	if got := h.invoke([]*pairedAccount{holderA, holderB, ownerA}, mustEncodeAmount(t, InstructionTransfer, 101)); got != uint64(ErrInsufficientFunds) {
		t.Fatalf("over-balance transfer = %d, want %d", got, ErrInsufficientFunds)
	}
}
