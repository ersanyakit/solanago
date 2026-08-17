package erc20

import (
	"errors"
	"testing"
)

func testPubkey(seed byte) Pubkey {
	var key Pubkey
	key[0] = seed
	key[31] = seed
	return key
}

type fixture struct {
	program   Program
	mint      *Account
	holderA   *Account
	holderB   *Account
	authority *Account
	ownerA    *Account
	ownerB    *Account
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	programID := testPubkey(1)
	program := Program{ID: programID}
	authority := &Account{Key: testPubkey(2), IsSigner: true}
	ownerA := &Account{Key: testPubkey(3), IsSigner: true}
	ownerB := &Account{Key: testPubkey(4), IsSigner: true}

	mint := &Account{Key: testPubkey(5), Owner: programID, Data: make([]byte, MintStateSize), IsSigner: true, IsWritable: true}
	mintData, err := EncodeInitializeMint("Example Token", "EXT", 6, authority.Key)
	if err != nil {
		t.Fatal(err)
	}
	mustProcessN(t, program, []*Account{mint}, mintData)
	mint.IsSigner = false

	holderA := &Account{Key: testPubkey(6), Owner: programID, Data: make([]byte, BalanceStateSize), IsSigner: true, IsWritable: true}
	mustProcessN(t, program, []*Account{holderA, mint}, EncodeInitializeBalance(ownerA.Key))
	holderA.IsSigner = false

	holderB := &Account{Key: testPubkey(7), Owner: programID, Data: make([]byte, BalanceStateSize), IsSigner: true, IsWritable: true}
	mustProcessN(t, program, []*Account{holderB, mint}, EncodeInitializeBalance(ownerB.Key))
	holderB.IsSigner = false

	return fixture{program: program, mint: mint, holderA: holderA, holderB: holderB, authority: authority, ownerA: ownerA, ownerB: ownerB}
}

func TestMintTransferBurnHappyPath(t *testing.T) {
	f := newFixture(t)

	mustProcessN(t, f.program, []*Account{f.mint, f.holderA, f.authority}, mustAmount(t, InstructionMintTo, 1_000))
	mustProcessN(t, f.program, []*Account{f.holderA, f.holderB, f.ownerA}, mustAmount(t, InstructionTransfer, 250))
	mustProcessN(t, f.program, []*Account{f.holderB, f.mint, f.ownerB}, mustAmount(t, InstructionBurn, 50))

	mint, err := DecodeMintState(f.mint.Data)
	if err != nil || mint.TotalSupply != 950 || mint.Name != "Example Token" || mint.Symbol != "EXT" || mint.Decimals != 6 {
		t.Fatalf("mint state = %+v, %v", mint, err)
	}
	a, _ := DecodeBalanceState(f.holderA.Data)
	b, _ := DecodeBalanceState(f.holderB.Data)
	if a.Amount != 750 {
		t.Fatalf("holderA amount = %d, want 750", a.Amount)
	}
	if b.Amount != 200 {
		t.Fatalf("holderB amount = %d, want 200", b.Amount)
	}
}

func TestApproveAndTransferFromDecrementsAllowance(t *testing.T) {
	f := newFixture(t)
	mustProcessN(t, f.program, []*Account{f.mint, f.holderA, f.authority}, mustAmount(t, InstructionMintTo, 1_000))

	spender := &Account{Key: testPubkey(8), IsSigner: true}
	allowance := &Account{Key: testPubkey(9), Owner: f.program.ID, Data: make([]byte, AllowanceStateSize), IsSigner: true, IsWritable: true}
	mustProcessN(t, f.program, []*Account{allowance, f.mint, f.ownerA, spender}, EncodeInitializeAllowance())
	allowance.IsSigner = false

	mustProcessN(t, f.program, []*Account{allowance, f.ownerA}, mustAmount(t, InstructionApprove, 300))
	mustProcessN(t, f.program, []*Account{f.holderA, f.holderB, allowance, spender}, mustAmount(t, InstructionTransferFrom, 120))

	state, err := DecodeAllowanceState(allowance.Data)
	if err != nil || state.Amount != 180 {
		t.Fatalf("allowance = %+v, %v, want amount=180", state, err)
	}
	a, _ := DecodeBalanceState(f.holderA.Data)
	b, _ := DecodeBalanceState(f.holderB.Data)
	if a.Amount != 880 || b.Amount != 120 {
		t.Fatalf("balances after transferFrom: holderA=%d holderB=%d, want 880/120", a.Amount, b.Amount)
	}

	err2 := f.program.Process([]*Account{f.holderA, f.holderB, allowance, spender}, mustAmount(t, InstructionTransferFrom, 181))
	assertProgramError(t, err2, ErrInsufficientAllowance)
}

func TestTransferFromRejectsWrongSpender(t *testing.T) {
	f := newFixture(t)
	mustProcessN(t, f.program, []*Account{f.mint, f.holderA, f.authority}, mustAmount(t, InstructionMintTo, 1_000))
	spender := &Account{Key: testPubkey(8), IsSigner: true}
	attacker := &Account{Key: testPubkey(10), IsSigner: true}
	allowance := &Account{Key: testPubkey(9), Owner: f.program.ID, Data: make([]byte, AllowanceStateSize), IsSigner: true, IsWritable: true}
	mustProcessN(t, f.program, []*Account{allowance, f.mint, f.ownerA, spender}, EncodeInitializeAllowance())
	allowance.IsSigner = false
	mustProcessN(t, f.program, []*Account{allowance, f.ownerA}, mustAmount(t, InstructionApprove, 300))

	err := f.program.Process([]*Account{f.holderA, f.holderB, allowance, attacker}, mustAmount(t, InstructionTransferFrom, 10))
	assertProgramError(t, err, ErrInvalidAuthority)
}

func TestTransferRejectsInsufficientFunds(t *testing.T) {
	f := newFixture(t)
	mustProcessN(t, f.program, []*Account{f.mint, f.holderA, f.authority}, mustAmount(t, InstructionMintTo, 100))
	err := f.program.Process([]*Account{f.holderA, f.holderB, f.ownerA}, mustAmount(t, InstructionTransfer, 101))
	assertProgramError(t, err, ErrInsufficientFunds)
}

func TestMintToRejectsWrongAuthority(t *testing.T) {
	f := newFixture(t)
	notAuthority := &Account{Key: testPubkey(11), IsSigner: true}
	err := f.program.Process([]*Account{f.mint, f.holderA, notAuthority}, mustAmount(t, InstructionMintTo, 1))
	assertProgramError(t, err, ErrInvalidAuthority)
}

func TestInitializeMintRejectsDoubleInitialization(t *testing.T) {
	f := newFixture(t)
	f.mint.IsSigner = true
	data, err := EncodeInitializeMint("x", "y", 0, f.authority.Key)
	if err != nil {
		t.Fatal(err)
	}
	processErr := f.program.Process([]*Account{f.mint}, data)
	assertProgramError(t, processErr, ErrAlreadyInitialized)
}

func mustProcessN(t *testing.T, program Program, accounts []*Account, data []byte) {
	t.Helper()
	if err := program.Process(accounts, data); err != nil {
		t.Fatalf("Process(%v) = %v, want nil", accounts, err)
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
