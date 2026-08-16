package spl20

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

type tokenFixture struct {
	program       Program
	mint          *Account
	source        *Account
	destination   *Account
	mintAuthority *Account
	sourceOwner   *Account
	destOwner     *Account
}

func TestProgramHappyPathInitializeMintTransferAndBurn(t *testing.T) {
	fixture := newTokenFixture(t)

	mintTo := mustAmountInstruction(t, InstructionMintTo, 1_000)
	mustProcess(t, fixture.program, []*Account{fixture.mint, fixture.source, fixture.mintAuthority}, mintTo)

	transfer := mustAmountInstruction(t, InstructionTransfer, 250)
	mustProcess(t, fixture.program, []*Account{fixture.source, fixture.destination, fixture.sourceOwner}, transfer)

	burn := mustAmountInstruction(t, InstructionBurn, 50)
	mustProcess(t, fixture.program, []*Account{fixture.destination, fixture.mint, fixture.destOwner}, burn)

	mint := mustMintState(t, fixture.mint)
	source := mustTokenState(t, fixture.source)
	destination := mustTokenState(t, fixture.destination)
	if mint.Decimals != 6 || mint.Supply != 950 {
		t.Fatalf("mint state = %+v, want decimals=6 supply=950", mint)
	}
	if source.Amount != 750 {
		t.Fatalf("source amount = %d, want 750", source.Amount)
	}
	if destination.Amount != 200 {
		t.Fatalf("destination amount = %d, want 200", destination.Amount)
	}
}

func TestProgramDelegateApproveSpendBurnAndRevoke(t *testing.T) {
	fixture := newTokenFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.mint, fixture.source, fixture.mintAuthority}, mustAmountInstruction(t, InstructionMintTo, 500))

	delegate := &Account{Key: testPubkey(7), IsSigner: true}
	mustProcess(t, fixture.program, []*Account{fixture.source, fixture.sourceOwner, delegate}, mustAmountInstruction(t, InstructionApprove, 120))

	mustProcess(t, fixture.program, []*Account{fixture.source, fixture.destination, delegate}, mustAmountInstruction(t, InstructionTransfer, 70))
	mustProcess(t, fixture.program, []*Account{fixture.source, fixture.mint, delegate}, mustAmountInstruction(t, InstructionBurn, 20))

	source := mustTokenState(t, fixture.source)
	if !source.Delegate.Set || source.Delegate.Key != delegate.Key || source.DelegatedAmount != 30 {
		t.Fatalf("delegate state = %+v, want delegate=%v allowance=30", source, delegate.Key)
	}
	if source.Amount != 410 || mustTokenState(t, fixture.destination).Amount != 70 || mustMintState(t, fixture.mint).Supply != 480 {
		t.Fatalf("unexpected balances after delegated spend: source=%d destination=%d supply=%d",
			source.Amount, mustTokenState(t, fixture.destination).Amount, mustMintState(t, fixture.mint).Supply)
	}

	before := snapshotData(fixture.source, fixture.destination, fixture.mint)
	err := fixture.program.Process(
		[]*Account{fixture.source, fixture.destination, delegate},
		mustAmountInstruction(t, InstructionTransfer, 31),
	)
	assertProgramError(t, err, ErrInsufficientAllowance)
	assertDataUnchanged(t, before, fixture.source, fixture.destination, fixture.mint)

	mustProcess(t, fixture.program, []*Account{fixture.source, fixture.sourceOwner}, EncodeRevoke())
	source = mustTokenState(t, fixture.source)
	if source.Delegate.Set || source.DelegatedAmount != 0 {
		t.Fatalf("revoke left delegate state behind: %+v", source)
	}

	before = snapshotData(fixture.source, fixture.destination)
	err = fixture.program.Process(
		[]*Account{fixture.source, fixture.destination, delegate},
		mustAmountInstruction(t, InstructionTransfer, 1),
	)
	assertProgramError(t, err, ErrInvalidAuthority)
	assertDataUnchanged(t, before, fixture.source, fixture.destination)
}

func TestProgramSetMintAuthorityAndDisableMinting(t *testing.T) {
	fixture := newTokenFixture(t)
	newAuthority := &Account{Key: testPubkey(8), IsSigner: true}
	setNewAuthority := mustSetAuthorityInstruction(t, AuthorityMintTokens, OptionalPubkey{Set: true, Key: newAuthority.Key})
	mustProcess(t, fixture.program, []*Account{fixture.mint, fixture.mintAuthority}, setNewAuthority)

	before := snapshotData(fixture.mint, fixture.source)
	err := fixture.program.Process(
		[]*Account{fixture.mint, fixture.source, fixture.mintAuthority},
		mustAmountInstruction(t, InstructionMintTo, 1),
	)
	assertProgramError(t, err, ErrInvalidAuthority)
	assertDataUnchanged(t, before, fixture.mint, fixture.source)

	mustProcess(t, fixture.program, []*Account{fixture.mint, fixture.source, newAuthority}, mustAmountInstruction(t, InstructionMintTo, 10))
	if got := mustMintState(t, fixture.mint).Supply; got != 10 {
		t.Fatalf("supply = %d, want 10", got)
	}

	disable := mustSetAuthorityInstruction(t, AuthorityMintTokens, OptionalPubkey{})
	mustProcess(t, fixture.program, []*Account{fixture.mint, newAuthority}, disable)
	if authority := mustMintState(t, fixture.mint).MintAuthority; authority.Set {
		t.Fatalf("mint authority remains enabled: %+v", authority)
	}

	before = snapshotData(fixture.mint, fixture.source)
	err = fixture.program.Process(
		[]*Account{fixture.mint, fixture.source, newAuthority},
		mustAmountInstruction(t, InstructionMintTo, 1),
	)
	assertProgramError(t, err, ErrAuthorityDisabled)
	assertDataUnchanged(t, before, fixture.mint, fixture.source)

	err = fixture.program.Process([]*Account{fixture.mint, newAuthority}, disable)
	assertProgramError(t, err, ErrAuthorityDisabled)
}

func TestProgramSetAccountOwnerClearsDelegate(t *testing.T) {
	fixture := newTokenFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.mint, fixture.source, fixture.mintAuthority}, mustAmountInstruction(t, InstructionMintTo, 10))

	delegate := &Account{Key: testPubkey(7), IsSigner: true}
	mustProcess(t, fixture.program, []*Account{fixture.source, fixture.sourceOwner, delegate}, mustAmountInstruction(t, InstructionApprove, 8))

	newOwner := &Account{Key: testPubkey(9), IsSigner: true}
	setOwner := mustSetAuthorityInstruction(t, AuthorityAccountOwner, OptionalPubkey{Set: true, Key: newOwner.Key})
	mustProcess(t, fixture.program, []*Account{fixture.source, fixture.sourceOwner}, setOwner)

	state := mustTokenState(t, fixture.source)
	if state.Owner != newOwner.Key || state.Delegate.Set || state.DelegatedAmount != 0 {
		t.Fatalf("owner transition state = %+v", state)
	}

	before := snapshotData(fixture.source, fixture.destination)
	err := fixture.program.Process(
		[]*Account{fixture.source, fixture.destination, fixture.sourceOwner},
		mustAmountInstruction(t, InstructionTransfer, 1),
	)
	assertProgramError(t, err, ErrInvalidAuthority)
	assertDataUnchanged(t, before, fixture.source, fixture.destination)

	mustProcess(t, fixture.program, []*Account{fixture.source, fixture.destination, newOwner}, mustAmountInstruction(t, InstructionTransfer, 1))
	if got := mustTokenState(t, fixture.destination).Amount; got != 1 {
		t.Fatalf("destination amount = %d, want 1", got)
	}
}

func TestProgramRejectsInvalidPrivilegesAndPreservesState(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, fixture *tokenFixture) error
		want ProgramError
	}{
		{
			name: "missing signature",
			want: ErrMissingSignature,
			run: func(t *testing.T, fixture *tokenFixture) error {
				fixture.mintAuthority.IsSigner = false
				return fixture.program.Process([]*Account{fixture.mint, fixture.source, fixture.mintAuthority}, mustAmountInstruction(t, InstructionMintTo, 1))
			},
		},
		{
			name: "wrong authority",
			want: ErrInvalidAuthority,
			run: func(t *testing.T, fixture *tokenFixture) error {
				wrong := &Account{Key: testPubkey(99), IsSigner: true}
				return fixture.program.Process([]*Account{fixture.mint, fixture.source, wrong}, mustAmountInstruction(t, InstructionMintTo, 1))
			},
		},
		{
			name: "read only",
			want: ErrAccountReadOnly,
			run: func(t *testing.T, fixture *tokenFixture) error {
				fixture.source.IsWritable = false
				return fixture.program.Process([]*Account{fixture.source, fixture.destination, fixture.sourceOwner}, mustAmountInstruction(t, InstructionTransfer, 1))
			},
		},
		{
			name: "invalid program owner",
			want: ErrInvalidProgramOwner,
			run: func(t *testing.T, fixture *tokenFixture) error {
				fixture.destination.Owner = testPubkey(100)
				return fixture.program.Process([]*Account{fixture.source, fixture.destination, fixture.sourceOwner}, mustAmountInstruction(t, InstructionTransfer, 1))
			},
		},
		{
			name: "missing account",
			want: ErrMissingAccount,
			run: func(t *testing.T, fixture *tokenFixture) error {
				return fixture.program.Process([]*Account{fixture.source, fixture.destination}, mustAmountInstruction(t, InstructionTransfer, 1))
			},
		},
		{
			name: "extra account",
			want: ErrInvalidInstruction,
			run: func(t *testing.T, fixture *tokenFixture) error {
				return fixture.program.Process(
					[]*Account{fixture.source, fixture.destination, fixture.sourceOwner, fixture.mint},
					mustAmountInstruction(t, InstructionTransfer, 1),
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTokenFixture(t)
			mustProcess(t, fixture.program, []*Account{fixture.mint, fixture.source, fixture.mintAuthority}, mustAmountInstruction(t, InstructionMintTo, 5))
			before := snapshotData(fixture.mint, fixture.source, fixture.destination)
			err := test.run(t, fixture)
			assertProgramError(t, err, test.want)
			assertDataUnchanged(t, before, fixture.mint, fixture.source, fixture.destination)
		})
	}
}

func TestProgramRejectsInvalidBalancesAndPreservesState(t *testing.T) {
	t.Run("insufficient funds", func(t *testing.T) {
		fixture := newTokenFixture(t)
		mustProcess(t, fixture.program, []*Account{fixture.mint, fixture.source, fixture.mintAuthority}, mustAmountInstruction(t, InstructionMintTo, 5))
		before := snapshotData(fixture.mint, fixture.source, fixture.destination)
		err := fixture.program.Process([]*Account{fixture.source, fixture.destination, fixture.sourceOwner}, mustAmountInstruction(t, InstructionTransfer, 6))
		assertProgramError(t, err, ErrInsufficientFunds)
		assertDataUnchanged(t, before, fixture.mint, fixture.source, fixture.destination)
	})

	t.Run("destination overflow", func(t *testing.T) {
		fixture := newTokenFixture(t)
		mustProcess(t, fixture.program, []*Account{fixture.mint, fixture.source, fixture.mintAuthority}, mustAmountInstruction(t, InstructionMintTo, 5))
		destination := mustTokenState(t, fixture.destination)
		destination.Amount = math.MaxUint64
		mustEncodeTokenState(t, fixture.destination, destination)
		before := snapshotData(fixture.mint, fixture.source, fixture.destination)
		err := fixture.program.Process([]*Account{fixture.source, fixture.destination, fixture.sourceOwner}, mustAmountInstruction(t, InstructionTransfer, 1))
		assertProgramError(t, err, ErrArithmeticOverflow)
		assertDataUnchanged(t, before, fixture.mint, fixture.source, fixture.destination)
	})

	t.Run("mint supply overflow", func(t *testing.T) {
		fixture := newTokenFixture(t)
		mint := mustMintState(t, fixture.mint)
		mint.Supply = math.MaxUint64
		mustEncodeMintState(t, fixture.mint, mint)
		before := snapshotData(fixture.mint, fixture.source)
		err := fixture.program.Process([]*Account{fixture.mint, fixture.source, fixture.mintAuthority}, mustAmountInstruction(t, InstructionMintTo, 1))
		assertProgramError(t, err, ErrArithmeticOverflow)
		assertDataUnchanged(t, before, fixture.mint, fixture.source)
	})
}

func TestProgramRejectsMintMismatchAndSameAccountAtomically(t *testing.T) {
	fixture := newTokenFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.mint, fixture.source, fixture.mintAuthority}, mustAmountInstruction(t, InstructionMintTo, 5))

	otherMint := newOwnedAccount(fixture.program.ID, testPubkey(10), MintStateSize)
	otherMint.IsSigner = true
	mustProcess(t, fixture.program, []*Account{otherMint}, EncodeInitializeMint(6, fixture.mintAuthority.Key))
	otherMint.IsSigner = false
	otherToken := newOwnedAccount(fixture.program.ID, testPubkey(11), TokenAccountStateSize)
	otherToken.IsSigner = true
	mustProcess(t, fixture.program, []*Account{otherToken, otherMint}, EncodeInitializeAccount(fixture.destOwner.Key))
	otherToken.IsSigner = false

	before := snapshotData(fixture.source, otherToken)
	err := fixture.program.Process([]*Account{fixture.source, otherToken, fixture.sourceOwner}, mustAmountInstruction(t, InstructionTransfer, 1))
	assertProgramError(t, err, ErrMintMismatch)
	assertDataUnchanged(t, before, fixture.source, otherToken)

	before = snapshotData(fixture.source)
	err = fixture.program.Process([]*Account{fixture.source, fixture.source, fixture.sourceOwner}, mustAmountInstruction(t, InstructionTransfer, 1))
	assertProgramError(t, err, ErrSameAccount)
	assertDataUnchanged(t, before, fixture.source)
}

func TestProgramNilSourceIsMissingAccount(t *testing.T) {
	fixture := newTokenFixture(t)
	err := fixture.program.Transfer(nil, fixture.destination, fixture.sourceOwner, 1)
	assertProgramError(t, err, ErrMissingAccount)
}

func TestProgramRejectsMalformedStateWithoutMutation(t *testing.T) {
	fixture := newTokenFixture(t)
	fixture.source.Data[0] = 0xff
	before := snapshotData(fixture.mint, fixture.source, fixture.destination)
	err := fixture.program.Process([]*Account{fixture.source, fixture.destination, fixture.sourceOwner}, mustAmountInstruction(t, InstructionTransfer, 1))
	assertProgramError(t, err, ErrInvalidState)
	assertDataUnchanged(t, before, fixture.mint, fixture.source, fixture.destination)
}

func TestProcessRejectsAccountOrderCountAndMalformedInstructionAtomically(t *testing.T) {
	fixture := newTokenFixture(t)
	mustProcess(t, fixture.program, []*Account{fixture.mint, fixture.source, fixture.mintAuthority}, mustAmountInstruction(t, InstructionMintTo, 5))

	tests := []struct {
		name        string
		accounts    []*Account
		instruction []byte
		want        ProgramError
	}{
		{
			name:        "wrong account order",
			accounts:    []*Account{fixture.source, fixture.mint, fixture.mintAuthority},
			instruction: mustAmountInstruction(t, InstructionMintTo, 1),
			want:        ErrInvalidState,
		},
		{
			name:        "too few accounts",
			accounts:    []*Account{fixture.mint, fixture.source},
			instruction: mustAmountInstruction(t, InstructionMintTo, 1),
			want:        ErrMissingAccount,
		},
		{
			name:        "too many accounts",
			accounts:    []*Account{fixture.mint, fixture.source, fixture.mintAuthority, fixture.destination},
			instruction: mustAmountInstruction(t, InstructionMintTo, 1),
			want:        ErrInvalidInstruction,
		},
		{
			name:        "nil account slot",
			accounts:    []*Account{fixture.mint, fixture.source, nil},
			instruction: mustAmountInstruction(t, InstructionMintTo, 1),
			want:        ErrMissingAccount,
		},
		{
			name:        "malformed instruction",
			accounts:    []*Account{fixture.mint, fixture.source, fixture.mintAuthority},
			instruction: []byte{0xff},
			want:        ErrInvalidInstruction,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := snapshotData(fixture.mint, fixture.source, fixture.destination)
			err := fixture.program.Process(test.accounts, test.instruction)
			assertProgramError(t, err, test.want)
			assertDataUnchanged(t, before, fixture.mint, fixture.source, fixture.destination)
		})
	}
}

func newTokenFixture(t *testing.T) *tokenFixture {
	t.Helper()
	programID := testPubkey(1)
	fixture := &tokenFixture{
		program:       Program{ID: programID},
		mint:          newOwnedAccount(programID, testPubkey(2), MintStateSize),
		source:        newOwnedAccount(programID, testPubkey(3), TokenAccountStateSize),
		destination:   newOwnedAccount(programID, testPubkey(4), TokenAccountStateSize),
		mintAuthority: &Account{Key: testPubkey(5), IsSigner: true},
		sourceOwner:   &Account{Key: testPubkey(6), IsSigner: true},
		destOwner:     &Account{Key: testPubkey(12), IsSigner: true},
	}
	fixture.mint.IsSigner = true
	mustProcess(t, fixture.program, []*Account{fixture.mint}, EncodeInitializeMint(6, fixture.mintAuthority.Key))
	fixture.mint.IsSigner = false
	fixture.source.IsSigner = true
	mustProcess(t, fixture.program, []*Account{fixture.source, fixture.mint}, EncodeInitializeAccount(fixture.sourceOwner.Key))
	fixture.source.IsSigner = false
	fixture.destination.IsSigner = true
	mustProcess(t, fixture.program, []*Account{fixture.destination, fixture.mint}, EncodeInitializeAccount(fixture.destOwner.Key))
	fixture.destination.IsSigner = false
	return fixture
}

func newOwnedAccount(programID, key Pubkey, size int) *Account {
	return &Account{
		Key:        key,
		Owner:      programID,
		Data:       make([]byte, size),
		IsWritable: true,
	}
}

func testPubkey(seed byte) Pubkey {
	var key Pubkey
	for index := range key {
		key[index] = seed + byte(index)
	}
	return key
}

func mustAmountInstruction(t *testing.T, kind InstructionKind, amount uint64) []byte {
	t.Helper()
	data, err := EncodeAmountInstruction(kind, amount)
	if err != nil {
		t.Fatalf("EncodeAmountInstruction(%d, %d): %v", kind, amount, err)
	}
	return data
}

func mustSetAuthorityInstruction(t *testing.T, kind AuthorityType, authority OptionalPubkey) []byte {
	t.Helper()
	data, err := EncodeSetAuthority(kind, authority)
	if err != nil {
		t.Fatalf("EncodeSetAuthority(%d): %v", kind, err)
	}
	return data
}

func mustProcess(t *testing.T, program Program, accounts []*Account, instruction []byte) {
	t.Helper()
	if err := program.Process(accounts, instruction); err != nil {
		t.Fatalf("Process: %v", err)
	}
}

func mustMintState(t *testing.T, account *Account) MintState {
	t.Helper()
	state, err := DecodeMintState(account.Data)
	if err != nil {
		t.Fatalf("DecodeMintState: %v", err)
	}
	return state
}

func mustTokenState(t *testing.T, account *Account) TokenAccountState {
	t.Helper()
	state, err := DecodeTokenAccountState(account.Data)
	if err != nil {
		t.Fatalf("DecodeTokenAccountState: %v", err)
	}
	return state
}

func mustEncodeMintState(t *testing.T, account *Account, state MintState) {
	t.Helper()
	if err := EncodeMintState(account.Data, state); err != nil {
		t.Fatalf("EncodeMintState: %v", err)
	}
}

func mustEncodeTokenState(t *testing.T, account *Account, state TokenAccountState) {
	t.Helper()
	if err := EncodeTokenAccountState(account.Data, state); err != nil {
		t.Fatalf("EncodeTokenAccountState: %v", err)
	}
}

func snapshotData(accounts ...*Account) [][]byte {
	snapshot := make([][]byte, len(accounts))
	for index, account := range accounts {
		snapshot[index] = append([]byte(nil), account.Data...)
	}
	return snapshot
}

func assertDataUnchanged(t *testing.T, before [][]byte, accounts ...*Account) {
	t.Helper()
	if len(before) != len(accounts) {
		t.Fatalf("snapshot count = %d, account count = %d", len(before), len(accounts))
	}
	for index, account := range accounts {
		if !bytes.Equal(before[index], account.Data) {
			t.Errorf("account %d data changed after rejected operation", index)
		}
	}
}

func assertProgramError(t *testing.T, err error, want ProgramError) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
