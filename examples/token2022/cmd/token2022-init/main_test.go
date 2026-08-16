package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ersany/go-solana/sdk"
	"github.com/ersany/go-solana/sdk/associatedtoken"
	"github.com/ersany/go-solana/sdk/metaplex"
	"github.com/ersany/go-solana/sdk/system"
	"github.com/ersany/go-solana/sdk/token2022"
	"github.com/ersany/go-solana/svmtest"
)

func TestParseAndFormatUIAmount(t *testing.T) {
	tests := []struct {
		text      string
		decimals  uint8
		raw       uint64
		formatted string
	}{
		{"100000", 6, 100_000_000_000, "100000"},
		{"1.25", 6, 1_250_000, "1.25"},
		{"0.000001", 6, 1, "0.000001"},
		{"18446744073709551615", 0, math.MaxUint64, "18446744073709551615"},
	}
	for _, test := range tests {
		got, err := parseUIAmount(test.text, test.decimals)
		if err != nil {
			t.Fatalf("parse %q/%d: %v", test.text, test.decimals, err)
		}
		if got != test.raw {
			t.Fatalf("parse %q/%d = %d, want %d", test.text, test.decimals, got, test.raw)
		}
		if formatted := formatUIAmount(got, test.decimals); formatted != test.formatted {
			t.Fatalf("format %d/%d = %q, want %q", got, test.decimals, formatted, test.formatted)
		}
	}
	for _, malformed := range []struct {
		text     string
		decimals uint8
	}{
		{"", 6}, {" 1", 6}, {"+1", 6}, {"-1", 6}, {"1e6", 6},
		{".1", 6}, {"1.", 6}, {"1.0000001", 6}, {"18446744073709551616", 0},
	} {
		if _, err := parseUIAmount(malformed.text, malformed.decimals); err == nil {
			t.Fatalf("parseUIAmount(%q, %d) succeeded", malformed.text, malformed.decimals)
		}
	}
}

func TestRunCLIRejectsLiveEndpointBeforeKeyLoadOrRPC(t *testing.T) {
	err := runCLI([]string{
		"--keypair", t.TempDir() + "/missing-keypair.json",
		"--url", "testnet",
		"--amount-raw", "1",
	}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--allow-live") {
		t.Fatalf("error = %v, want live endpoint guard", err)
	}
}

func TestInitializeFinalizesAndVerifiesExactTransactions(t *testing.T) {
	payer := tokenTestSigner(t)
	owner := tokenTestSigner(t).PublicKey
	mint := tokenTestSigner(t)
	client := newFakeToken2022Client(payer.PublicKey, owner, 6, 100_000_000_000)
	client.balance = client.requiredBalance()

	created, err := initializeToken2022WithDependencies(context.Background(), client, config{
		Payer: payer, Owner: owner, Decimals: 6, Amount: 100_000_000_000,
	}, tokenSignerSequence(mint), func() time.Time { return time.Unix(1, 2) })
	if err != nil {
		t.Fatal(err)
	}
	wantATA, _, err := associatedtoken.Derive(owner, mint.PublicKey, token2022.ProgramID)
	if err != nil {
		t.Fatal(err)
	}
	if !created.FinalizedAndVerified || created.Mint != mint.PublicKey.String() || created.TokenAccount != wantATA.String() {
		t.Fatalf("unexpected result: %+v", created)
	}
	if created.GeneratedAtUTC != "1970-01-01T00:00:01.000000002Z" || created.GenesisHash != "test-genesis" {
		t.Fatalf("unexpected provenance: %+v", created)
	}
	if created.AmountUI != "100000" || created.MintState.SupplyRaw != 100_000_000_000 || created.TokenAccountState.AmountRaw != 100_000_000_000 {
		t.Fatalf("unexpected verified state: %+v", created)
	}
	if created.MintState.MintAuthority != payer.PublicKey.String() || created.MintState.FreezeAuthority != "disabled" {
		t.Fatalf("unexpected mint authorities: %+v", created.MintState)
	}
	if len(created.SubmittedSignatures) != 3 || len(created.FinalizedSignatures) != 3 || len(created.NonFinalizedSignatures) != 0 || client.sendCalls != 3 {
		t.Fatalf("submitted=%v non-finalized=%v finalized=%v sends=%d", created.SubmittedSignatures, created.NonFinalizedSignatures, created.FinalizedSignatures, client.sendCalls)
	}
}

func TestInitializeAtomicSubmitsSingleTransaction(t *testing.T) {
	payer := tokenTestSigner(t)
	owner := tokenTestSigner(t).PublicKey
	mint := tokenTestSigner(t)
	client := newFakeToken2022Client(payer.PublicKey, owner, 6, 100_000_000_000)
	client.balance = client.requiredBalance()
	client.atomicMode = true

	created, err := initializeToken2022WithDependencies(context.Background(), client, config{
		Payer: payer, Owner: owner, Decimals: 6, Amount: 100_000_000_000, Atomic: true,
	}, tokenSignerSequence(mint), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	wantATA, _, err := associatedtoken.Derive(owner, mint.PublicKey, token2022.ProgramID)
	if err != nil {
		t.Fatal(err)
	}
	if !created.FinalizedAndVerified || created.Mint != mint.PublicKey.String() || created.TokenAccount != wantATA.String() {
		t.Fatalf("unexpected result: %+v", created)
	}
	if created.MintState.SupplyRaw != 100_000_000_000 || created.TokenAccountState.AmountRaw != 100_000_000_000 {
		t.Fatalf("unexpected verified state: %+v", created)
	}
	if client.sendCalls != 1 {
		t.Fatalf("atomic mode sent %d transactions, want 1", client.sendCalls)
	}
	if len(created.SubmittedSignatures) != 1 || len(created.FinalizedSignatures) != 1 || len(created.NonFinalizedSignatures) != 0 {
		t.Fatalf("submitted=%v finalized=%v non-finalized=%v", created.SubmittedSignatures, created.FinalizedSignatures, created.NonFinalizedSignatures)
	}
}

func TestInitializeStagedWithMetadataCreatesMetaplexAccount(t *testing.T) {
	payer := tokenTestSigner(t)
	owner := tokenTestSigner(t).PublicKey
	mint := tokenTestSigner(t)
	client := newFakeToken2022Client(payer.PublicKey, owner, 6, 100_000_000_000)
	client.name, client.symbol, client.uri = "WIWIW", "WIWIW", ""
	client.balance = client.requiredBalance()

	created, err := initializeToken2022WithDependencies(context.Background(), client, config{
		Payer: payer, Owner: owner, Decimals: 6, Amount: 100_000_000_000,
		Name: "WIWIW", Symbol: "WIWIW",
	}, tokenSignerSequence(mint), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	wantMetadataAddress, _, err := metaplex.DeriveMetadataAddress(mint.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !created.FinalizedAndVerified || created.MetaplexMetadataAccount != wantMetadataAddress.String() {
		t.Fatalf("unexpected result: %+v", created)
	}
	// mint, metaplex metadata, ATA, mint-to: four separate verified transactions.
	if client.sendCalls != 4 {
		t.Fatalf("staged metadata run sent %d transactions, want 4", client.sendCalls)
	}
}

func TestInitializeAtomicWithMetadataCreatesMetaplexAccount(t *testing.T) {
	payer := tokenTestSigner(t)
	owner := tokenTestSigner(t).PublicKey
	mint := tokenTestSigner(t)
	client := newFakeToken2022Client(payer.PublicKey, owner, 6, 100_000_000_000)
	client.name, client.symbol, client.uri = "WIWIW", "WIWIW", ""
	client.balance = client.requiredBalance()
	client.atomicMode = true

	created, err := initializeToken2022WithDependencies(context.Background(), client, config{
		Payer: payer, Owner: owner, Decimals: 6, Amount: 100_000_000_000, Atomic: true,
		Name: "WIWIW", Symbol: "WIWIW",
	}, tokenSignerSequence(mint), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	wantMetadataAddress, _, err := metaplex.DeriveMetadataAddress(mint.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !created.FinalizedAndVerified || created.MetaplexMetadataAccount != wantMetadataAddress.String() {
		t.Fatalf("unexpected result: %+v", created)
	}
	if client.sendCalls != 1 {
		t.Fatalf("atomic metadata run sent %d transactions, want 1", client.sendCalls)
	}
}

func TestInitializeRejectsInsufficientBalanceBeforeSubmission(t *testing.T) {
	payer := tokenTestSigner(t)
	client := newFakeToken2022Client(payer.PublicKey, payer.PublicKey, 6, 1)
	client.balance = client.requiredBalance() - 1
	created, err := initializeToken2022WithDependencies(context.Background(), client, config{
		Payer: payer, Owner: payer.PublicKey, Decimals: 6, Amount: 1,
	}, tokenSignerSequence(tokenTestSigner(t), tokenTestSigner(t)), time.Now)
	if err == nil || !strings.Contains(err.Error(), "below rent plus reserve") {
		t.Fatalf("error = %v, want insufficient-balance error", err)
	}
	if client.balanceCalls != 1 || client.sendCalls != 0 {
		t.Fatalf("balance calls=%d sends=%d", client.balanceCalls, client.sendCalls)
	}
	if len(created.SubmittedSignatures) != 0 || len(created.FinalizedSignatures) != 0 || created.FinalizedAndVerified {
		t.Fatalf("insufficient run recorded finalized work: %+v", created)
	}
}

func TestInitializePreservesAmbiguousSignatureAndNeverRetries(t *testing.T) {
	payer := tokenTestSigner(t)
	client := newFakeToken2022Client(payer.PublicKey, payer.PublicKey, 6, 7)
	client.balance = client.requiredBalance()
	client.failSend = 2
	client.failAfterApply = true

	created, err := initializeToken2022WithDependencies(context.Background(), client, config{
		Payer: payer, Owner: payer.PublicKey, Decimals: 6, Amount: 7,
	}, tokenSignerSequence(tokenTestSigner(t), tokenTestSigner(t)), time.Now)
	if err == nil || !strings.Contains(err.Error(), "no finalized proof") || !strings.Contains(err.Error(), "no retry was attempted") {
		t.Fatalf("error = %v, want fail-closed ambiguous-submission error", err)
	}
	if client.sendCalls != 2 {
		t.Fatalf("send calls = %d, want exactly 2", client.sendCalls)
	}
	if len(created.SubmittedSignatures) != 1 || len(created.FinalizedSignatures) != 1 {
		t.Fatalf("submitted=%v finalized=%v", created.SubmittedSignatures, created.FinalizedSignatures)
	}
	if len(created.NonFinalizedSignatures) != 1 || created.NonFinalizedSignatures[0] != (signatureRecord{Stage: "create_and_initialize_account", Signature: "signature-2"}) {
		t.Fatalf("non-finalized journal = %+v", created.NonFinalizedSignatures)
	}
	if created.FinalizedAndVerified {
		t.Fatal("ambiguous run was marked verified")
	}
}

func TestInitializeRejectsSuccessfulResponseWithoutSignature(t *testing.T) {
	payer := tokenTestSigner(t)
	client := newFakeToken2022Client(payer.PublicKey, payer.PublicKey, 0, 1)
	client.balance = client.requiredBalance()
	client.emptySuccessAt = 1
	created, err := initializeToken2022WithDependencies(context.Background(), client, config{
		Payer: payer, Owner: payer.PublicKey, Amount: 1,
	}, tokenSignerSequence(tokenTestSigner(t), tokenTestSigner(t)), time.Now)
	if err == nil || !strings.Contains(err.Error(), "no finalized signature") {
		t.Fatalf("error = %v", err)
	}
	if client.sendCalls != 1 || len(created.SubmittedSignatures) != 0 || len(created.FinalizedSignatures) != 0 || created.FinalizedAndVerified {
		t.Fatalf("empty-signature response was trusted: sends=%d result=%+v", client.sendCalls, created)
	}
}

func TestVerificationRejectsNonCanonicalToken2022State(t *testing.T) {
	payer := tokenTestSigner(t)
	client := newFakeToken2022Client(payer.PublicKey, payer.PublicKey, 6, 1)

	mintAddress := tokenTestSigner(t).PublicKey
	mintState := token2022.Mint{Initialized: true, Decimals: 6}
	mintData, err := token2022.EncodeMint(mintState)
	if err != nil {
		t.Fatal(err)
	}
	mintData[50] = 1 // Freeze authority is None; ignored payload must stay zero.
	client.accounts[mintAddress] = tokenStateAccount(client.mintRent, mintData)
	if err := verifyToken2022Mint(context.Background(), client, mintAddress, client.mintRent, mintState); err == nil || !strings.Contains(err.Error(), "byte-for-byte canonical") {
		t.Fatalf("mint verification error = %v", err)
	}

	tokenAddress := tokenTestSigner(t).PublicKey
	accountState := token2022.Account{Mint: mintAddress, Owner: payer.PublicKey, State: token2022.AccountInitialized}
	accountData, err := token2022.EncodeAccountWithExtensions(token2022.AccountWithExtensions{
		Base:       accountState,
		Extensions: []token2022.Extension{{Type: token2022.ExtensionImmutableOwner}},
	})
	if err != nil {
		t.Fatal(err)
	}
	accountData[76] = 1 // Delegate is None; ignored payload must stay zero.
	client.accounts[tokenAddress] = tokenStateAccount(client.tokenRent, accountData)
	if err := verifyToken2022TokenAccount(context.Background(), client, tokenAddress, client.tokenRent, mintAddress, payer.PublicKey, accountState); err == nil || !strings.Contains(err.Error(), "byte-for-byte canonical") {
		t.Fatalf("token-account verification error = %v", err)
	}
}

func TestVerifyMetaplexMetadata(t *testing.T) {
	payer := tokenTestSigner(t)
	client := newFakeToken2022Client(payer.PublicKey, payer.PublicKey, 6, 1)

	mintAddress := tokenTestSigner(t).PublicKey
	metadataAddress, _, err := metaplex.DeriveMetadataAddress(mintAddress)
	if err != nil {
		t.Fatal(err)
	}

	buildMetadata := func(name, symbol string) []byte {
		data := []byte{4} // Key::MetadataV1
		data = append(data, payer.PublicKey[:]...)
		data = append(data, mintAddress[:]...)
		// mpl-token-metadata right-pads name/symbol/uri to their max length
		// ("puffing") on every write; verifyMetaplexMetadata must trim that.
		data = puffTestBorshString(data, name, 32)
		data = puffTestBorshString(data, symbol, 10)
		data = puffTestBorshString(data, "", 200)
		return data
	}
	setAccount := func(owner string, data []byte) {
		client.accounts[metadataAddress] = &svmtest.AccountInfo{
			Owner: owner,
			Data:  []any{base64.StdEncoding.EncodeToString(data), "base64"},
		}
	}

	setAccount(metaplex.ProgramID.String(), buildMetadata("WIWIW", "WIWIW"))
	if err := verifyMetaplexMetadata(context.Background(), client, metadataAddress, mintAddress, "WIWIW", "WIWIW"); err != nil {
		t.Fatalf("verifyMetaplexMetadata() = %v, want nil", err)
	}

	setAccount(metaplex.ProgramID.String(), buildMetadata("WRONG", "WIWIW"))
	if err := verifyMetaplexMetadata(context.Background(), client, metadataAddress, mintAddress, "WIWIW", "WIWIW"); err == nil || !strings.Contains(err.Error(), "metaplex metadata name") {
		t.Fatalf("name mismatch error = %v", err)
	}

	setAccount(system.ProgramID.String(), buildMetadata("WIWIW", "WIWIW"))
	if err := verifyMetaplexMetadata(context.Background(), client, metadataAddress, mintAddress, "WIWIW", "WIWIW"); err == nil || !strings.Contains(err.Error(), "account owner") {
		t.Fatalf("owner mismatch error = %v", err)
	}
}

func appendTestBorshString(dst []byte, value string) []byte {
	length := make([]byte, 4)
	binary.LittleEndian.PutUint32(length, uint32(len(value)))
	dst = append(dst, length...)
	return append(dst, value...)
}

// puffTestBorshString mimics mpl-token-metadata's puff_out_data_fields:
// name/symbol/uri are right-padded with NUL bytes to their max length on
// every on-chain write.
func puffTestBorshString(dst []byte, value string, maxLen int) []byte {
	return appendTestBorshString(dst, value+strings.Repeat("\x00", maxLen-len(value)))
}

type fakeToken2022Client struct {
	payer            sdk.Pubkey
	owner            sdk.Pubkey
	decimals         uint8
	amount           uint64
	name             string
	symbol           string
	uri              string
	mint             sdk.Pubkey
	tokenAccount     sdk.Pubkey
	metaplexMetadata sdk.Pubkey
	accounts         map[sdk.Pubkey]*svmtest.AccountInfo
	mintRent         uint64
	tokenRent        uint64
	tokenAccountSize uint64
	metaplexAcctRent uint64
	metaplexFeeBase  uint64
	atomicMode       bool
	balance          uint64
	balanceCalls     int
	sendCalls        int
	failSend         int
	failAfterApply   bool
	emptySuccessAt   int
}

func newFakeToken2022Client(payer, owner sdk.Pubkey, decimals uint8, amount uint64) *fakeToken2022Client {
	tokenAccountSize, err := token2022.CalculateAccountLen([]token2022.ExtensionType{token2022.ExtensionImmutableOwner})
	if err != nil {
		panic(err)
	}
	return &fakeToken2022Client{
		payer: payer, owner: owner, decimals: decimals, amount: amount,
		mintRent: token2022.MintSize * 100, tokenRent: uint64(tokenAccountSize) * 100,
		tokenAccountSize: uint64(tokenAccountSize),
		metaplexAcctRent: metaplex.MetadataAccountSize * 100,
		metaplexFeeBase:  metaplex.CreateFeeSizeScalar * 100,
		balance:          1_000_000_000,
		accounts: map[sdk.Pubkey]*svmtest.AccountInfo{
			token2022.ProgramID: {Owner: "BPFLoaderUpgradeab1e11111111111111111111111", Executable: true},
		},
	}
}

func (*fakeToken2022Client) Health(context.Context) error { return nil }

func (*fakeToken2022Client) GenesisHash(context.Context) (string, error) {
	return "test-genesis", nil
}

func (f *fakeToken2022Client) MinimumBalanceForRentExemption(_ context.Context, size uint64) (uint64, error) {
	switch size {
	case token2022.MintSize:
		return f.mintRent, nil
	case f.tokenAccountSize:
		return f.tokenRent, nil
	case metaplex.MetadataAccountSize:
		return f.metaplexAcctRent, nil
	case metaplex.CreateFeeSizeScalar:
		return f.metaplexFeeBase, nil
	default:
		return 0, fmt.Errorf("unexpected rent size %d", size)
	}
}

func (f *fakeToken2022Client) Balance(_ context.Context, address sdk.Pubkey) (uint64, error) {
	f.balanceCalls++
	if address != f.payer {
		return 0, fmt.Errorf("balance address %s, want %s", address, f.payer)
	}
	return f.balance, nil
}

func (f *fakeToken2022Client) GetAccountInfo(_ context.Context, address sdk.Pubkey) (*svmtest.AccountInfo, error) {
	return cloneTokenAccountInfo(f.accounts[address]), nil
}

func (f *fakeToken2022Client) SendInstructions(_ context.Context, payer svmtest.Signer, signers []svmtest.Signer, instructions []sdk.Instruction) (string, error) {
	f.sendCalls++
	signature := fmt.Sprintf("signature-%d", f.sendCalls)
	failing := f.failSend == f.sendCalls
	if failing && !f.failAfterApply {
		return signature, errors.New("simulated ambiguous submit")
	}
	if payer.PublicKey != f.payer {
		return signature, errors.New("wrong fee payer")
	}
	if err := f.apply(signers, instructions); err != nil {
		return signature, err
	}
	if failing {
		return signature, errors.New("simulated response loss after acceptance")
	}
	if f.emptySuccessAt == f.sendCalls {
		return "", nil
	}
	return signature, nil
}

func (f *fakeToken2022Client) apply(signers []svmtest.Signer, instructions []sdk.Instruction) error {
	if f.atomicMode {
		return f.applyAtomic(signers, instructions)
	}
	wantsMetadata := f.name != "" || f.symbol != "" || f.uri != ""
	stages := []string{"mint"}
	if wantsMetadata {
		stages = append(stages, "metaplex")
	}
	stages = append(stages, "ata", "mintTo")
	if f.sendCalls > len(stages) {
		return errors.New("unexpected extra submission")
	}
	switch stages[f.sendCalls-1] {
	case "mint":
		return f.applyCreateMint(signers, instructions)
	case "metaplex":
		return f.applyCreateMetaplexMetadata(signers, instructions)
	case "ata":
		return f.applyCreateATA(signers, instructions)
	case "mintTo":
		return f.applyMintTo(signers, instructions)
	}
	return nil
}

func (f *fakeToken2022Client) applyCreateMint(signers []svmtest.Signer, instructions []sdk.Instruction) error {
	if len(signers) != 1 || svmtest.ValidateSigner(signers[0]) != nil {
		return errors.New("mint transaction must have exactly one valid generated signer")
	}
	f.mint = signers[0].PublicKey
	want := []sdk.Instruction{
		system.CreateAccount(f.payer, f.mint, f.mintRent, token2022.MintSize, token2022.ProgramID),
		token2022.InitializeMint2(f.mint, f.payer, token2022.OptionalPubkey{}, f.decimals),
	}
	if !reflect.DeepEqual(instructions, want) {
		return fmt.Errorf("mint signer/account metas or data mismatch:\n got %#v\nwant %#v", instructions, want)
	}
	data, err := token2022.EncodeMint(token2022.Mint{
		MintAuthority: token2022.SomePubkey(f.payer), Decimals: f.decimals, Initialized: true,
	})
	if err != nil {
		return err
	}
	f.accounts[f.mint] = tokenStateAccount(f.mintRent, data)
	return nil
}

func (f *fakeToken2022Client) applyCreateMetaplexMetadata(signers []svmtest.Signer, instructions []sdk.Instruction) error {
	if len(signers) != 0 {
		return errors.New("metaplex metadata transaction must not carry extra signers")
	}
	want, metadataAddress, err := metaplex.CreateV1(f.mint, f.payer, f.payer, f.payer, token2022.ProgramID, false, f.name, f.symbol, f.uri, f.decimals, true)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(instructions, []sdk.Instruction{want}) {
		return fmt.Errorf("metaplex metadata instruction mismatch:\n got %#v\nwant %#v", instructions, []sdk.Instruction{want})
	}
	f.metaplexMetadata = metadataAddress
	data := []byte{4} // Key::MetadataV1
	data = append(data, f.payer[:]...)
	data = append(data, f.mint[:]...)
	data = puffTestBorshString(data, f.name, 32)
	data = puffTestBorshString(data, f.symbol, 10)
	data = puffTestBorshString(data, f.uri, 200)
	f.accounts[metadataAddress] = &svmtest.AccountInfo{
		Owner: metaplex.ProgramID.String(),
		Data:  []any{base64.StdEncoding.EncodeToString(data), "base64"},
	}
	return nil
}

func (f *fakeToken2022Client) applyCreateATA(signers []svmtest.Signer, instructions []sdk.Instruction) error {
	if len(signers) != 0 {
		return errors.New("associated-token-account transaction must not carry extra signers")
	}
	createATA, ata, err := associatedtoken.CreateIdempotent(f.payer, f.owner, f.mint, token2022.ProgramID)
	if err != nil {
		return err
	}
	f.tokenAccount = ata
	want := []sdk.Instruction{createATA}
	if !reflect.DeepEqual(instructions, want) {
		return fmt.Errorf("associated token account metas or data mismatch:\n got %#v\nwant %#v", instructions, want)
	}
	data, err := token2022.EncodeAccountWithExtensions(token2022.AccountWithExtensions{
		Base:       token2022.Account{Mint: f.mint, Owner: f.owner, State: token2022.AccountInitialized},
		Extensions: []token2022.Extension{{Type: token2022.ExtensionImmutableOwner}},
	})
	if err != nil {
		return err
	}
	f.accounts[f.tokenAccount] = tokenStateAccount(f.tokenRent, data)
	return nil
}

func (f *fakeToken2022Client) applyMintTo(signers []svmtest.Signer, instructions []sdk.Instruction) error {
	if len(signers) != 0 {
		return errors.New("mint-to must use the fee payer authority without extra signers")
	}
	mintTo, err := token2022.MintTo(f.mint, f.tokenAccount, f.payer, nil, f.amount)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(instructions, []sdk.Instruction{mintTo}) {
		return fmt.Errorf("mint-to signer/account metas or data mismatch:\n got %#v\nwant %#v", instructions, []sdk.Instruction{mintTo})
	}
	mintData, err := token2022.EncodeMint(token2022.Mint{
		MintAuthority: token2022.SomePubkey(f.payer), Supply: f.amount, Decimals: f.decimals, Initialized: true,
	})
	if err != nil {
		return err
	}
	accountData, err := token2022.EncodeAccountWithExtensions(token2022.AccountWithExtensions{
		Base:       token2022.Account{Mint: f.mint, Owner: f.owner, Amount: f.amount, State: token2022.AccountInitialized},
		Extensions: []token2022.Extension{{Type: token2022.ExtensionImmutableOwner}},
	})
	if err != nil {
		return err
	}
	f.accounts[f.mint] = tokenStateAccount(f.mintRent, mintData)
	f.accounts[f.tokenAccount] = tokenStateAccount(f.tokenRent, accountData)
	return nil
}

func (f *fakeToken2022Client) applyAtomic(signers []svmtest.Signer, instructions []sdk.Instruction) error {
	if f.sendCalls != 1 {
		return errors.New("atomic mode must submit exactly one transaction")
	}
	if len(signers) != 1 || svmtest.ValidateSigner(signers[0]) != nil {
		return errors.New("atomic transaction must have exactly one valid generated signer")
	}
	f.mint = signers[0].PublicKey

	want := []sdk.Instruction{
		system.CreateAccount(f.payer, f.mint, f.mintRent, token2022.MintSize, token2022.ProgramID),
		token2022.InitializeMint2(f.mint, f.payer, token2022.OptionalPubkey{}, f.decimals),
	}
	wantsMetadata := f.name != "" || f.symbol != "" || f.uri != ""
	if wantsMetadata {
		createMetadata, metadataAddress, err := metaplex.CreateV1(f.mint, f.payer, f.payer, f.payer, token2022.ProgramID, false, f.name, f.symbol, f.uri, f.decimals, true)
		if err != nil {
			return err
		}
		f.metaplexMetadata = metadataAddress
		want = append(want, createMetadata)
	}
	createATA, ata, err := associatedtoken.CreateIdempotent(f.payer, f.owner, f.mint, token2022.ProgramID)
	if err != nil {
		return err
	}
	f.tokenAccount = ata
	want = append(want, createATA)
	mintTo, err := token2022.MintTo(f.mint, f.tokenAccount, f.payer, nil, f.amount)
	if err != nil {
		return err
	}
	want = append(want, mintTo)
	if !reflect.DeepEqual(instructions, want) {
		return fmt.Errorf("atomic instruction mismatch:\n got %#v\nwant %#v", instructions, want)
	}

	mintData, err := token2022.EncodeMint(token2022.Mint{
		MintAuthority: token2022.SomePubkey(f.payer), Supply: f.amount, Decimals: f.decimals, Initialized: true,
	})
	if err != nil {
		return err
	}
	accountData, err := token2022.EncodeAccountWithExtensions(token2022.AccountWithExtensions{
		Base:       token2022.Account{Mint: f.mint, Owner: f.owner, Amount: f.amount, State: token2022.AccountInitialized},
		Extensions: []token2022.Extension{{Type: token2022.ExtensionImmutableOwner}},
	})
	if err != nil {
		return err
	}
	f.accounts[f.mint] = tokenStateAccount(f.mintRent, mintData)
	f.accounts[f.tokenAccount] = tokenStateAccount(f.tokenRent, accountData)
	if wantsMetadata {
		data := []byte{4} // Key::MetadataV1
		data = append(data, f.payer[:]...)
		data = append(data, f.mint[:]...)
		data = appendTestBorshString(data, f.name)
		data = appendTestBorshString(data, f.symbol)
		data = appendTestBorshString(data, f.uri)
		f.accounts[f.metaplexMetadata] = &svmtest.AccountInfo{
			Owner: metaplex.ProgramID.String(),
			Data:  []any{base64.StdEncoding.EncodeToString(data), "base64"},
		}
	}
	return nil
}

func (f *fakeToken2022Client) requiredBalance() uint64 {
	total := f.mintRent + f.tokenRent + minimumFeeReserveLamports
	if f.name != "" || f.symbol != "" || f.uri != "" {
		total += f.metaplexAcctRent + f.metaplexFeeBase + metaplex.CreateFeeOffsetLamports
	}
	return total
}

func tokenStateAccount(lamports uint64, data []byte) *svmtest.AccountInfo {
	return &svmtest.AccountInfo{
		Lamports: lamports,
		Owner:    token2022.ProgramID.String(),
		Data:     []any{base64.StdEncoding.EncodeToString(data), "base64"},
	}
}

func cloneTokenAccountInfo(info *svmtest.AccountInfo) *svmtest.AccountInfo {
	if info == nil {
		return nil
	}
	clone := *info
	if tuple, ok := info.Data.([]any); ok {
		clone.Data = append([]any(nil), tuple...)
	}
	return &clone
}

func tokenSignerSequence(signers ...svmtest.Signer) func() (svmtest.Signer, error) {
	index := 0
	return func() (svmtest.Signer, error) {
		if index >= len(signers) {
			return svmtest.Signer{}, errors.New("too many signer requests")
		}
		signer := signers[index]
		index++
		return signer, nil
	}
}

func tokenTestSigner(t *testing.T) svmtest.Signer {
	t.Helper()
	signer, err := svmtest.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
