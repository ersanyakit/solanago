package payable

import (
	"bytes"
	"encoding/binary"
	"errors"
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
// and lamports against this same package's native Program — the same
// approach examples/gospl uses against examples/spl20, here collapsed into
// one package since payable's native model already doubles as the host
// wire-format package.
//
// It only covers InitializeVault, InitializeDepositAccount, Withdraw, and
// EmergencyWithdraw. Deposit's guest account list has a fourth account (the
// System Program) the native model doesn't carry, since only the guest
// program actually performs the CPI — see depositGuestHarness below and
// README.md's "Native model versus compiled guest" section.
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
		t.Fatalf("compile payable contract: %v", err)
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

func (h *differentialHarness) owned(key sdk.Pubkey, size int, lamports uint64, writable bool) *pairedAccount {
	data := make([]byte, size)
	return &pairedAccount{
		guest: runtime.Account{Key: key, Owner: h.programID, Data: data, Lamports: lamports, IsWritable: writable},
		native: &Account{
			Key: Pubkey(key), Owner: Pubkey(h.programID), Data: append([]byte(nil), data...), Lamports: lamports, IsWritable: writable,
		},
	}
}

func (h *differentialHarness) wallet(key sdk.Pubkey, lamports uint64, signer, writable bool) *pairedAccount {
	return &pairedAccount{
		guest:  runtime.Account{Key: key, Lamports: lamports, IsSigner: signer, IsWritable: writable},
		native: &Account{Key: Pubkey(key), Lamports: lamports, IsSigner: signer, IsWritable: writable},
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
	beforeData := make([][]byte, len(accounts))
	beforeLamports := make([]uint64, len(accounts))
	for index, region := range serialized.AccountRegions {
		beforeData[index] = append([]byte(nil), sliceAt(serialized.Buffer, region.DataAddress, region.OriginalDataLen)...)
		beforeLamports[index] = lamportsAt(serialized.Buffer, region.LamportsAddress)
	}
	result, err := h.machine.RunWithMemory(
		[]vm.MemoryRegion{vm.WritableRegion(sbpf.MMInputStart, serialized.Buffer)},
		sbpf.MMInputStart,
		serialized.InstructionDataAddress,
	)
	if err != nil {
		h.t.Fatalf("run compiled payable program: %v", err)
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
		actualData := sliceAt(serialized.Buffer, region.DataAddress, region.OriginalDataLen)
		actualLamports := lamportsAt(serialized.Buffer, region.LamportsAddress)
		if result != 0 {
			if !bytes.Equal(actualData, beforeData[index]) {
				h.t.Fatalf("failed instruction partially mutated guest account %d data", index)
			}
			if actualLamports != beforeLamports[index] {
				h.t.Fatalf("failed instruction partially mutated guest account %d lamports", index)
			}
			continue
		}
		accounts[index].guest.Data = append(accounts[index].guest.Data[:0], actualData...)
		accounts[index].guest.Lamports = actualLamports
	}
	for index, account := range accounts {
		if !bytes.Equal(account.guest.Data, account.native.Data) {
			h.t.Fatalf("account %d data differs from native reference\nguest:  %x\nnative: %x", index, account.guest.Data, account.native.Data)
		}
		if account.guest.Lamports != account.native.Lamports {
			h.t.Fatalf("account %d lamports differ from native reference: guest=%d native=%d", index, account.guest.Lamports, account.native.Lamports)
		}
	}
	return result
}

func sliceAt(buffer []byte, address uint64, length int) []byte {
	offset := address - sbpf.MMInputStart
	return buffer[offset : offset+uint64(length)]
}

func lamportsAt(buffer []byte, address uint64) uint64 {
	offset := address - sbpf.MMInputStart
	return binary.LittleEndian.Uint64(buffer[offset : offset+8])
}

func testKey(seed byte) sdk.Pubkey {
	var key sdk.Pubkey
	for index := range key {
		key[index] = seed + byte(index)
	}
	return key
}

func TestCompiledContractMatchesNativeInitializeWithdrawEmergencyWithdraw(t *testing.T) {
	h := newDifferentialHarness(t)
	authority := h.wallet(testKey(1), 0, true, false)
	vault := h.owned(testKey(2), VaultStateSize, 0, true)
	deposit := h.owned(testKey(3), DepositStateSize, 0, true)
	depositor := h.wallet(testKey(4), 0, false, false)
	recipient := h.wallet(testKey(5), 0, false, true)

	vault.guest.IsSigner, vault.native.IsSigner = true, true
	if got := h.invoke([]*pairedAccount{vault}, EncodeInitializeVault(Pubkey(authority.guest.Key))); got != 0 {
		t.Fatalf("initialize vault = %d", got)
	}
	vault.guest.IsSigner, vault.native.IsSigner = false, false

	deposit.guest.IsSigner, deposit.native.IsSigner = true, true
	if got := h.invoke([]*pairedAccount{deposit, vault}, EncodeInitializeDepositAccount(Pubkey(depositor.guest.Key))); got != 0 {
		t.Fatalf("initialize deposit account = %d", got)
	}
	deposit.guest.IsSigner, deposit.native.IsSigner = false, false

	// Seed both sides' vault lamports/state directly (bypassing Deposit,
	// which needs a live CPI executor — see TestCompiledDepositCPI*) so
	// Withdraw/EmergencyWithdraw have something to move.
	seedVault(t, vault, deposit, 10_000, 6_000)

	depositor.guest.IsSigner, depositor.native.IsSigner = true, true
	if got := h.invoke([]*pairedAccount{vault, deposit, depositor, recipient}, EncodeWithdraw(4_000)); got != 0 {
		t.Fatalf("withdraw = %d", got)
	}
	depositor.guest.IsSigner, depositor.native.IsSigner = false, false
	if recipient.guest.Lamports != 4_000 || recipient.native.Lamports != 4_000 {
		t.Fatalf("recipient lamports after withdraw: guest=%d native=%d, want 4000", recipient.guest.Lamports, recipient.native.Lamports)
	}

	authority.guest.IsSigner, authority.native.IsSigner = true, true
	if got := h.invoke([]*pairedAccount{vault, authority, recipient}, EncodeEmergencyWithdraw(2_000)); got != 0 {
		t.Fatalf("emergency withdraw = %d", got)
	}
	if recipient.guest.Lamports != 6_000 || recipient.native.Lamports != 6_000 {
		t.Fatalf("recipient lamports after emergency withdraw: guest=%d native=%d, want 6000", recipient.guest.Lamports, recipient.native.Lamports)
	}

	nonOwner := h.wallet(testKey(6), 0, true, false)
	if got := h.invoke([]*pairedAccount{vault, nonOwner, recipient}, EncodeEmergencyWithdraw(1)); got != uint64(ErrInvalidAuthority) {
		t.Fatalf("emergency withdraw by non-authority = %d, want %d", got, ErrInvalidAuthority)
	}
}

// seedVault writes vault.Lamports/TotalDeposited and deposit.Lamports/Balance
// directly on both the guest and native sides, mirroring what a successful
// Deposit would have left behind, without needing a live CPI executor.
func seedVault(t *testing.T, vault, deposit *pairedAccount, vaultLamports, depositBalance uint64) {
	t.Helper()
	vaultState, err := DecodeVaultState(vault.native.Data)
	if err != nil {
		t.Fatal(err)
	}
	vaultState.TotalDeposited = depositBalance
	if err := EncodeVaultState(vault.native.Data, vaultState); err != nil {
		t.Fatal(err)
	}
	copy(vault.guest.Data, vault.native.Data)
	vault.guest.Lamports, vault.native.Lamports = vaultLamports, vaultLamports

	depositState, err := DecodeDepositState(deposit.native.Data)
	if err != nil {
		t.Fatal(err)
	}
	depositState.Balance = depositBalance
	if err := EncodeDepositState(deposit.native.Data, depositState); err != nil {
		t.Fatal(err)
	}
	copy(deposit.guest.Data, deposit.native.Data)
}

// --- Deposit: guest-only, since its account list (four accounts, including
// the System Program) doesn't match the native model's (three) ---

func newDepositFixture(t *testing.T, h *differentialHarness) (vault, deposit, depositor, systemProgram runtime.Account, vaultKey, depositorKey sdk.Pubkey) {
	t.Helper()
	vaultKey = testKey(11)
	depositKey := testKey(12)
	depositorKey = testKey(13)

	vault = runtime.Account{Key: vaultKey, Owner: h.programID, Data: make([]byte, VaultStateSize), IsWritable: true, IsSigner: true}
	if got := h.invokeGuestOnly([]runtime.Account{vault}, EncodeInitializeVault(Pubkey(testKey(99)))); got != 0 {
		t.Fatalf("seed initialize vault = %d", got)
	}
	vault.IsSigner = false

	deposit = runtime.Account{Key: depositKey, Owner: h.programID, Data: make([]byte, DepositStateSize), IsWritable: true, IsSigner: true}
	if got := h.invokeGuestOnly([]runtime.Account{deposit, vault}, EncodeInitializeDepositAccount(Pubkey(depositorKey))); got != 0 {
		t.Fatalf("seed initialize deposit account = %d", got)
	}
	deposit.IsSigner = false

	depositor = runtime.Account{Key: depositorKey, Lamports: 50_000, IsWritable: true, IsSigner: true}
	systemProgram = runtime.Account{Key: sdk.Pubkey{}, Executable: true}
	return vault, deposit, depositor, systemProgram, vaultKey, depositorKey
}

// invokeGuestOnly runs one instruction against the compiled guest program
// only (no native comparison, no lamport read-back) and refreshes each
// account's Data in place, mirroring erc1155's non-differential harness.
func (h *differentialHarness) invokeGuestOnly(accounts []runtime.Account, instruction []byte) uint64 {
	h.t.Helper()
	result, _ := h.runGuestOnly(accounts, instruction)
	return result
}

func (h *differentialHarness) runGuestOnly(accounts []runtime.Account, instruction []byte) (uint64, error) {
	h.t.Helper()
	inputs := make([]runtime.InputAccount, len(accounts))
	for index, account := range accounts {
		inputs[index] = runtime.UniqueInputAccount(account)
	}
	serialized, err := runtime.SerializeInputV1(h.programID, inputs, instruction)
	if err != nil {
		h.t.Fatal(err)
	}
	result, runErr := h.machine.RunWithMemory(
		[]vm.MemoryRegion{vm.WritableRegion(sbpf.MMInputStart, serialized.Buffer)},
		sbpf.MMInputStart,
		serialized.InstructionDataAddress,
	)
	if runErr != nil {
		return 0, runErr
	}
	for index, region := range serialized.AccountRegions {
		accounts[index].Data = append(accounts[index].Data[:0], sliceAt(serialized.Buffer, region.DataAddress, region.OriginalDataLen)...)
	}
	return result, nil
}

// TestCompiledDepositReachesSystemProgramCPI proves every check before the
// CPI (ownership, signer, vault/depositor linkage, sufficient lamports) is
// satisfied for well-formed accounts by running Deposit all the way to
// sol_invoke_signed_c, where the reference VM — which has no CPI executor,
// deliberately, see runtime/cpi.go — reports ErrUnsupportedCall. That is
// the strongest local proof available for the CPI leg without a live
// validator; examples/cpi's own opt-in TestAgaveSystemTransferCPI is the
// only thing that exercises the syscall itself, and this repository has no
// such live-validator gate for payable (see README.md).
func TestCompiledDepositReachesSystemProgramCPI(t *testing.T) {
	h := newDifferentialHarness(t)
	vault, deposit, depositor, systemProgram, _, _ := newDepositFixture(t, h)

	_, err := h.runGuestOnly([]runtime.Account{vault, deposit, depositor, systemProgram}, EncodeDeposit(1_000))
	if !errors.Is(err, vm.ErrUnsupportedCall) {
		t.Fatalf("run error = %v, want %v (reference VM has no CPI executor)", err, vm.ErrUnsupportedCall)
	}
}

func TestCompiledDepositRejectsUnsignedDepositor(t *testing.T) {
	h := newDifferentialHarness(t)
	vault, deposit, depositor, systemProgram, _, _ := newDepositFixture(t, h)
	depositor.IsSigner = false

	if got := h.invokeGuestOnly([]runtime.Account{vault, deposit, depositor, systemProgram}, EncodeDeposit(1_000)); got != uint64(ErrMissingSignature) {
		t.Fatalf("unsigned depositor = %d, want %d", got, ErrMissingSignature)
	}
}

func TestCompiledDepositRejectsDepositorMismatch(t *testing.T) {
	h := newDifferentialHarness(t)
	vault, deposit, _, systemProgram, _, _ := newDepositFixture(t, h)
	attacker := runtime.Account{Key: testKey(50), Lamports: 50_000, IsWritable: true, IsSigner: true}

	if got := h.invokeGuestOnly([]runtime.Account{vault, deposit, attacker, systemProgram}, EncodeDeposit(1_000)); got != uint64(ErrDepositorMismatch) {
		t.Fatalf("depositor mismatch = %d, want %d", got, ErrDepositorMismatch)
	}
}

func TestCompiledDepositRejectsInsufficientLamports(t *testing.T) {
	h := newDifferentialHarness(t)
	vault, deposit, depositor, systemProgram, _, _ := newDepositFixture(t, h)

	if got := h.invokeGuestOnly([]runtime.Account{vault, deposit, depositor, systemProgram}, EncodeDeposit(100_000)); got != uint64(ErrInsufficientLamports) {
		t.Fatalf("insufficient lamports = %d, want %d", got, ErrInsufficientLamports)
	}
}

func TestCompiledDepositRejectsWrongSystemProgram(t *testing.T) {
	h := newDifferentialHarness(t)
	vault, deposit, depositor, _, _, _ := newDepositFixture(t, h)
	notSystemProgram := runtime.Account{Key: testKey(77), Executable: true}

	if got := h.invokeGuestOnly([]runtime.Account{vault, deposit, depositor, notSystemProgram}, EncodeDeposit(1_000)); got != uint64(ErrInvalidState) {
		t.Fatalf("wrong system program = %d, want %d", got, ErrInvalidState)
	}
}
