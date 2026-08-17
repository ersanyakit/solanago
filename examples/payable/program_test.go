package payable

import (
	"errors"
	"testing"
)

type vaultFixture struct {
	program   Program
	vault     *Account
	deposit   *Account
	depositor *Account
	recipient *Account
	authority *Account
}

func testPubkey(seed byte) Pubkey {
	var key Pubkey
	key[0] = seed
	key[31] = seed
	return key
}

func newVaultFixture(t *testing.T) vaultFixture {
	t.Helper()
	programID := testPubkey(1)
	fixture := vaultFixture{
		program: Program{ID: programID},
		vault: &Account{
			Key:        testPubkey(2),
			Owner:      programID,
			Data:       make([]byte, VaultStateSize),
			IsSigner:   true,
			IsWritable: true,
		},
		deposit: &Account{
			Key:        testPubkey(3),
			Owner:      programID,
			Data:       make([]byte, DepositStateSize),
			IsSigner:   true,
			IsWritable: true,
		},
		depositor: &Account{
			Key:        testPubkey(4),
			Lamports:   10_000,
			IsSigner:   true,
			IsWritable: true,
		},
		recipient: &Account{
			Key:        testPubkey(5),
			IsWritable: true,
		},
		authority: &Account{
			Key:      testPubkey(8),
			IsSigner: true,
		},
	}
	mustProcess(t, fixture.program, []*Account{fixture.vault}, EncodeInitializeVault(fixture.authority.Key))
	// InitializeDepositAccount does not require the depositor's own
	// signature, mirroring spl20's InitializeAccount(token, mint, owner).
	fixture.deposit.IsSigner = true
	mustProcess(t, fixture.program, []*Account{fixture.deposit, fixture.vault},
		EncodeInitializeDepositAccount(fixture.depositor.Key))
	fixture.deposit.IsSigner = false
	return fixture
}

func TestDepositMovesLamportsAndUpdatesLedger(t *testing.T) {
	fixture := newVaultFixture(t)

	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(4_000))

	if fixture.depositor.Lamports != 6_000 {
		t.Fatalf("depositor lamports = %d, want 6000", fixture.depositor.Lamports)
	}
	if fixture.vault.Lamports != 4_000 {
		t.Fatalf("vault lamports = %d, want 4000", fixture.vault.Lamports)
	}
	balance, err := BalanceOf(fixture.deposit.Data)
	if err != nil || balance != 4_000 {
		t.Fatalf("BalanceOf = (%d, %v), want (4000, nil)", balance, err)
	}
	vaultState, err := DecodeVaultState(fixture.vault.Data)
	if err != nil || vaultState.TotalDeposited != 4_000 {
		t.Fatalf("vault state = (%+v, %v), want TotalDeposited=4000", vaultState, err)
	}
}

func TestDepositRequiresDepositorSignature(t *testing.T) {
	fixture := newVaultFixture(t)
	fixture.depositor.IsSigner = false

	err := fixture.program.Process([]*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(1_000))
	assertProgramError(t, err, ErrMissingSignature)
	if fixture.depositor.Lamports != 10_000 || fixture.vault.Lamports != 0 {
		t.Fatalf("lamports changed on rejected deposit: depositor=%d vault=%d", fixture.depositor.Lamports, fixture.vault.Lamports)
	}
}

func TestDepositRejectsInsufficientLamports(t *testing.T) {
	fixture := newVaultFixture(t)

	err := fixture.program.Process([]*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(50_000))
	assertProgramError(t, err, ErrInsufficientLamports)
}

func TestDepositRejectsWrongDepositAccount(t *testing.T) {
	fixture := newVaultFixture(t)
	other := &Account{
		Key:        testPubkey(9),
		Lamports:   10_000,
		IsSigner:   true,
		IsWritable: true,
	}

	err := fixture.program.Process([]*Account{fixture.vault, fixture.deposit, other}, EncodeDeposit(1_000))
	assertProgramError(t, err, ErrDepositorMismatch)
}

func TestWithdrawPaysRecipientAndDebitsLedger(t *testing.T) {
	fixture := newVaultFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(5_000))

	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.deposit, fixture.depositor, fixture.recipient}, EncodeWithdraw(3_000))

	if fixture.recipient.Lamports != 3_000 {
		t.Fatalf("recipient lamports = %d, want 3000", fixture.recipient.Lamports)
	}
	if fixture.vault.Lamports != 2_000 {
		t.Fatalf("vault lamports = %d, want 2000", fixture.vault.Lamports)
	}
	balance, err := BalanceOf(fixture.deposit.Data)
	if err != nil || balance != 2_000 {
		t.Fatalf("BalanceOf = (%d, %v), want (2000, nil)", balance, err)
	}
}

func TestWithdrawRejectsInsufficientBalance(t *testing.T) {
	fixture := newVaultFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(1_000))

	err := fixture.program.Process([]*Account{fixture.vault, fixture.deposit, fixture.depositor, fixture.recipient}, EncodeWithdraw(1_001))
	assertProgramError(t, err, ErrInsufficientFunds)
	if fixture.recipient.Lamports != 0 || fixture.vault.Lamports != 1_000 {
		t.Fatalf("lamports changed on rejected withdraw: recipient=%d vault=%d", fixture.recipient.Lamports, fixture.vault.Lamports)
	}
}

func TestWithdrawRequiresDepositorSignature(t *testing.T) {
	fixture := newVaultFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(1_000))
	fixture.depositor.IsSigner = false

	err := fixture.program.Process([]*Account{fixture.vault, fixture.deposit, fixture.depositor, fixture.recipient}, EncodeWithdraw(500))
	assertProgramError(t, err, ErrMissingSignature)
}

func TestWithdrawRejectsAttackerNotMatchingDepositAccount(t *testing.T) {
	fixture := newVaultFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(1_000))
	attacker := &Account{Key: testPubkey(6), IsSigner: true}

	err := fixture.program.Process([]*Account{fixture.vault, fixture.deposit, attacker, fixture.recipient}, EncodeWithdraw(500))
	assertProgramError(t, err, ErrDepositorMismatch)
}

func TestEmergencyWithdrawPaysAuthorityChosenRecipient(t *testing.T) {
	fixture := newVaultFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(5_000))

	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.authority, fixture.recipient}, EncodeEmergencyWithdraw(5_000))

	if fixture.vault.Lamports != 0 {
		t.Fatalf("vault lamports = %d, want 0", fixture.vault.Lamports)
	}
	if fixture.recipient.Lamports != 5_000 {
		t.Fatalf("recipient lamports = %d, want 5000", fixture.recipient.Lamports)
	}
	// The per-depositor ledger is untouched: EmergencyWithdraw bypasses it
	// entirely, so vault can now hold fewer lamports than depositors are
	// recorded as owning.
	balance, err := BalanceOf(fixture.deposit.Data)
	if err != nil || balance != 5_000 {
		t.Fatalf("BalanceOf = (%d, %v), want (5000, nil)", balance, err)
	}
}

func TestEmergencyWithdrawRejectsNonAuthoritySigner(t *testing.T) {
	fixture := newVaultFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(5_000))
	attacker := &Account{Key: testPubkey(6), IsSigner: true}

	err := fixture.program.Process([]*Account{fixture.vault, attacker, fixture.recipient}, EncodeEmergencyWithdraw(1_000))
	assertProgramError(t, err, ErrInvalidAuthority)
	if fixture.vault.Lamports != 5_000 {
		t.Fatalf("vault lamports changed on rejected emergency withdraw: %d", fixture.vault.Lamports)
	}
}

func TestEmergencyWithdrawRequiresAuthoritySignature(t *testing.T) {
	fixture := newVaultFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(5_000))
	fixture.authority.IsSigner = false

	err := fixture.program.Process([]*Account{fixture.vault, fixture.authority, fixture.recipient}, EncodeEmergencyWithdraw(1_000))
	assertProgramError(t, err, ErrMissingSignature)
}

func TestEmergencyWithdrawRejectsAmountAboveVaultBalance(t *testing.T) {
	fixture := newVaultFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.vault, fixture.deposit, fixture.depositor}, EncodeDeposit(1_000))

	err := fixture.program.Process([]*Account{fixture.vault, fixture.authority, fixture.recipient}, EncodeEmergencyWithdraw(1_001))
	assertProgramError(t, err, ErrInsufficientFunds)
}

func TestInitializeVaultRejectsDoubleInitialization(t *testing.T) {
	fixture := newVaultFixture(t)
	err := fixture.program.Process([]*Account{fixture.vault}, EncodeInitializeVault(fixture.authority.Key))
	assertProgramError(t, err, ErrAlreadyInitialized)
}

func TestDepositRejectsUninitializedVault(t *testing.T) {
	programID := testPubkey(1)
	vault := &Account{Key: testPubkey(2), Owner: programID, Data: make([]byte, VaultStateSize), IsWritable: true}
	deposit := &Account{Key: testPubkey(3), Owner: programID, Data: make([]byte, DepositStateSize), IsWritable: true}
	depositor := &Account{Key: testPubkey(4), Lamports: 1_000, IsSigner: true, IsWritable: true}

	program := Program{ID: programID}
	err := program.Process([]*Account{vault, deposit, depositor}, EncodeDeposit(100))
	assertProgramError(t, err, ErrUninitialized)
}

func mustProcess(t *testing.T, program Program, accounts []*Account, data []byte) {
	t.Helper()
	if err := program.Process(accounts, data); err != nil {
		t.Fatalf("Process(%v) = %v, want nil", accounts, err)
	}
}

func assertProgramError(t *testing.T, err error, want ProgramError) {
	t.Helper()
	var got ProgramError
	if !errors.As(err, &got) {
		t.Fatalf("err = %v, want ProgramError", err)
	}
	if got != want {
		t.Fatalf("err = %v, want %v", got, want)
	}
}
