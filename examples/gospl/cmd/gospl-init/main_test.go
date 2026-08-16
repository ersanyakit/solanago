package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ersanyakit/go-solana/examples/gospl"
	"github.com/ersanyakit/go-solana/sdk"
	"github.com/ersanyakit/go-solana/sdk/loader"
	"github.com/ersanyakit/go-solana/sdk/system"
	"github.com/ersanyakit/go-solana/svmtest"
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
		{"1.230000", 6, 1_230_000, "1.23"},
		{"0.000001", 6, 1, "0.000001"},
		{"00001.20", 2, 120, "1.2"},
		{"18446744073709551615", 0, math.MaxUint64, "18446744073709551615"},
		{"184467440737095516.15", 2, math.MaxUint64, "184467440737095516.15"},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%s_%d", test.text, test.decimals), func(t *testing.T) {
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
		})
	}

	for _, malformed := range []struct {
		text     string
		decimals uint8
	}{
		{"", 6}, {" 1", 6}, {"1 ", 6}, {"+1", 6}, {"-1", 6}, {"1e6", 6},
		{".1", 6}, {"1.", 6}, {"1.0000001", 6}, {"1,5", 6}, {"１", 6},
		{"1.0", 0}, {"18446744073709551616", 0}, {"184467440737095516.16", 2},
	} {
		if _, err := parseUIAmount(malformed.text, malformed.decimals); err == nil {
			t.Fatalf("parseUIAmount(%q, %d) succeeded", malformed.text, malformed.decimals)
		}
	}

	veryPrecise := "0." + strings.Repeat("0", math.MaxUint8-1) + "1"
	if raw, err := parseUIAmount(veryPrecise, math.MaxUint8); err != nil || raw != 1 {
		t.Fatalf("255-decimal exact parse = %d, %v", raw, err)
	}
	if formatted := formatUIAmount(1, math.MaxUint8); formatted != veryPrecise {
		t.Fatalf("255-decimal format mismatch: got len=%d want len=%d", len(formatted), len(veryPrecise))
	}
}

func TestAmountFlagsRejectAmbiguousSyntaxAndDuplicates(t *testing.T) {
	var raw rawAmountFlag
	for _, invalid := range []string{"", "+1", "-1", " 1", "1.0", "1e2", "１２"} {
		if err := raw.Set(invalid); err == nil {
			t.Fatalf("raw amount %q was accepted", invalid)
		}
		if raw.set {
			t.Fatalf("failed raw amount %q changed flag state", invalid)
		}
	}
	if err := raw.Set("18446744073709551615"); err != nil || raw.value != math.MaxUint64 {
		t.Fatalf("set max raw amount = %d, %v", raw.value, err)
	}
	if err := raw.Set("1"); err == nil || !strings.Contains(err.Error(), "only once") {
		t.Fatalf("duplicate raw error = %v", err)
	}

	var ui uiAmountFlag
	if err := ui.Set("1.25"); err != nil {
		t.Fatal(err)
	}
	if err := ui.Set("2"); err == nil || !strings.Contains(err.Error(), "only once") {
		t.Fatalf("duplicate UI error = %v", err)
	}
}

func TestRPCURLNormalizationAndLoopbackGate(t *testing.T) {
	aliases := map[string]string{
		"localhost":    "http://127.0.0.1:8899",
		"l":            "http://127.0.0.1:8899",
		"devnet":       "https://api.devnet.solana.com",
		"testnet":      "https://api.testnet.solana.com",
		"mainnet-beta": "https://api.mainnet-beta.solana.com",
	}
	for input, want := range aliases {
		got, err := normalizeRPCURL(input)
		if err != nil || got != want {
			t.Fatalf("normalizeRPCURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:8899", "http://localhost:8899", "http://[::1]:8899"} {
		if !loopbackRPC(raw) {
			t.Fatalf("loopbackRPC(%q) = false", raw)
		}
	}
	for _, raw := range []string{
		"https://api.testnet.solana.com", "http://localhost.example.com", "http://127.0.0.1@example.com", "http://2130706433",
	} {
		if loopbackRPC(raw) {
			t.Fatalf("loopbackRPC(%q) = true", raw)
		}
	}
	for _, raw := range []string{"", "localhost:8899", "ftp://127.0.0.1/resource"} {
		if _, err := normalizeRPCURL(raw); err == nil {
			t.Fatalf("normalizeRPCURL(%q) succeeded", raw)
		}
	}
}

func TestRunCLIRejectsInvalidGatesBeforeLoadingKeyOrOpeningRPC(t *testing.T) {
	program := testSigner(t).PublicKey.String()
	base := []string{"--program", program, "--keypair", "secret.json", "--amount-raw", "1"}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"live without opt-in", appendCopy(base, "--url", "testnet"), "--allow-live"},
		{"both amounts", appendCopy(base, "--amount-ui", "1"), "exactly one"},
		{"missing amount", []string{"--program", program, "--keypair", "secret.json"}, "exactly one"},
		{"zero amount", []string{"--program", program, "--keypair", "secret.json", "--amount-raw", "0"}, "positive"},
		{"too many decimals", appendCopy(base, "--decimals", "256"), "exceed uint8"},
		{"zero timeout", appendCopy(base, "--timeout", "0s"), "timeout must be positive"},
		{"invalid URL", appendCopy(base, "--url", "ftp://127.0.0.1"), "http or https"},
		{"system program", []string{"--program", system.ProgramID.String(), "--keypair", "secret.json", "--amount-raw", "1"}, "invalid deployed"},
		{"duplicate raw", appendCopy(base, "--amount-raw", "2"), "only once"},
		{"duplicate UI", []string{"--program", program, "--keypair", "secret.json", "--amount-ui", "1", "--amount-ui", "2"}, "only once"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loaded := false
			opened := false
			deps := dependencies{
				loadKey: func(string) (svmtest.Signer, error) {
					loaded = true
					return svmtest.Signer{}, nil
				},
				newClient: func(string) rpcClient {
					opened = true
					return nil
				},
				newSigner: svmtest.NewSigner,
				now:       time.Now,
			}
			err := runCLI(test.args, io.Discard, io.Discard, deps)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			if loaded || opened {
				t.Fatalf("side effect before validation: loaded=%v opened=%v", loaded, opened)
			}
		})
	}
}

func TestRunCLIAllowLiveExactAmountAndLosslessJSON(t *testing.T) {
	payer := testSigner(t)
	program := testSigner(t).PublicKey
	mint := testSigner(t)
	token := testSigner(t)
	client := newFakeClient(program, payer.PublicKey)
	client.balance = client.requiredBalance()
	var openedURL string
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := runCLI([]string{
		"--program", program.String(),
		"--keypair", "payer.json",
		"--url", "testnet",
		"--allow-live",
		"--decimals", "2",
		"--amount-ui", "184467440737095516.15",
	}, &stdout, &stderr, dependencies{
		newClient: func(rawURL string) rpcClient {
			openedURL = rawURL
			return client
		},
		newSigner: signerSequence(mint, token),
		loadKey: func(path string) (svmtest.Signer, error) {
			if path != "payer.json" {
				return svmtest.Signer{}, fmt.Errorf("unexpected key path %q", path)
			}
			return payer, nil
		},
		now: func() time.Time { return time.Unix(123, 456) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if openedURL != "https://api.testnet.solana.com" {
		t.Fatalf("opened URL = %q", openedURL)
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if got := output["amount_raw"]; got != "18446744073709551615" {
		t.Fatalf("amount_raw JSON = %#v, want exact decimal string", got)
	}
	if got := output["mint_rent_lamports"]; got != "4800" {
		t.Fatalf("mint_rent_lamports JSON = %#v", got)
	}
	mintState, ok := output["mint_state"].(map[string]any)
	if !ok || mintState["supply_raw"] != "18446744073709551615" {
		t.Fatalf("mint state JSON = %#v", output["mint_state"])
	}
	if !strings.Contains(stderr.String(), `"event":"planned"`) ||
		!strings.Contains(stderr.String(), `"amount_raw":"18446744073709551615"`) ||
		!strings.Contains(stderr.String(), `"event":"verified"`) {
		t.Fatalf("recovery progress missing exact plan/verification:\n%s", stderr.String())
	}
}

func TestInitializeFinalizesAndVerifiesEveryStage(t *testing.T) {
	payer := testSigner(t)
	program := testSigner(t).PublicKey
	mint := testSigner(t)
	token := testSigner(t)
	client := newFakeClient(program, payer.PublicKey)
	client.balance = client.requiredBalance()
	var events []progressEvent
	created, err := initialize(context.Background(), client, config{
		Program: program, Payer: payer, Decimals: 6, AmountRaw: 100_000_000_000,
	}, signerSequence(mint, token), func() time.Time { return time.Unix(1, 2) }, func(event progressEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.FinalizedAndVerified || created.Mint != mint.PublicKey.String() || created.TokenAccount != token.PublicKey.String() {
		t.Fatalf("unexpected result: %+v", created)
	}
	if created.GeneratedAtUTC != "1970-01-01T00:00:01.000000002Z" || created.GenesisHash != "test-genesis" {
		t.Fatalf("unexpected provenance: %+v", created)
	}
	if created.AmountUI != "100000" || created.MintState.SupplyRaw != 100_000_000_000 || created.TokenAccountState.AmountRaw != 100_000_000_000 {
		t.Fatalf("unexpected final state: %+v", created)
	}
	if len(created.SubmittedSignatures) != 3 || len(created.FinalizedSignatures) != 3 || len(created.NonFinalizedSignatures) != 0 || client.sendCalls != 3 {
		t.Fatalf("submitted=%v non-finalized=%v finalized=%v sends=%d", created.SubmittedSignatures, created.NonFinalizedSignatures, created.FinalizedSignatures, client.sendCalls)
	}
	if client.getCallsAfterSend != 5 {
		t.Fatalf("finalized state reads after sends = %d, want 5", client.getCallsAfterSend)
	}
	wantEvents := []string{"planned", "finalized", "finalized", "finalized", "verified"}
	if len(events) != len(wantEvents) {
		t.Fatalf("progress events = %+v", events)
	}
	for index, want := range wantEvents {
		if events[index].Event != want {
			t.Fatalf("event[%d] = %+v, want %q", index, events[index], want)
		}
	}
	if events[0].Decimals == nil || *events[0].Decimals != 6 || events[0].AmountRaw != exactUint64(100_000_000_000) {
		t.Fatalf("planned event lost exact parameters: %+v", events[0])
	}
}

func TestInitializeRejectsInsufficientBalanceBeforeAnySubmission(t *testing.T) {
	payer := testSigner(t)
	program := testSigner(t).PublicKey
	mint := testSigner(t)
	token := testSigner(t)
	client := newFakeClient(program, payer.PublicKey)
	client.balance = client.requiredBalance() - 1
	created, err := initialize(context.Background(), client, config{
		Program: program, Payer: payer, Decimals: 6, AmountRaw: 1,
	}, signerSequence(mint, token), time.Now, nil)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("below rent plus conservative fee reserve %d", client.requiredBalance())) {
		t.Fatalf("error = %v", err)
	}
	if created == nil || created.MintRentLamports != exactUint64(client.mintRent) || created.TokenRentLamports != exactUint64(client.tokenRent) {
		t.Fatalf("partial rent journal = %+v", created)
	}
	if client.balanceCalls != 1 || client.sendCalls != 0 || len(created.SubmittedSignatures) != 0 || len(created.FinalizedSignatures) != 0 {
		t.Fatalf("balance calls=%d sends=%d result=%+v", client.balanceCalls, client.sendCalls, created)
	}
}

func TestInitializeRejectsRentOverflowBeforeBalanceOrSubmission(t *testing.T) {
	payer := testSigner(t)
	program := testSigner(t).PublicKey
	client := newFakeClient(program, payer.PublicKey)
	client.mintRent = math.MaxUint64
	client.tokenRent = 1
	created, err := initialize(context.Background(), client, config{
		Program: program, Payer: payer, Decimals: 0, AmountRaw: 1,
	}, signerSequence(testSigner(t), testSigner(t)), time.Now, nil)
	if err == nil || !strings.Contains(err.Error(), "overflows uint64") {
		t.Fatalf("error = %v", err)
	}
	if created == nil || client.balanceCalls != 0 || client.sendCalls != 0 {
		t.Fatalf("created=%+v balanceCalls=%d sendCalls=%d", created, client.balanceCalls, client.sendCalls)
	}
}

func TestInitializePreservesNonFinalizedSignatureAndNeverRetries(t *testing.T) {
	for _, failStage := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("stage_%d", failStage), func(t *testing.T) {
			payer := testSigner(t)
			program := testSigner(t).PublicKey
			mint := testSigner(t)
			token := testSigner(t)
			client := newFakeClient(program, payer.PublicKey)
			client.balance = client.requiredBalance()
			client.failSend = failStage
			client.failAfterApply = true
			created, err := initialize(context.Background(), client, config{
				Program: program, Payer: payer, Decimals: 6, AmountRaw: 7,
			}, signerSequence(mint, token), time.Now, nil)
			if err == nil || !strings.Contains(err.Error(), "no finalized proof") || !strings.Contains(err.Error(), "no retry was attempted") {
				t.Fatalf("error = %v", err)
			}
			if client.sendCalls != failStage {
				t.Fatalf("send calls = %d, want exactly %d", client.sendCalls, failStage)
			}
			if len(created.SubmittedSignatures) != failStage-1 || len(created.FinalizedSignatures) != failStage-1 {
				t.Fatalf("submitted=%v finalized=%v", created.SubmittedSignatures, created.FinalizedSignatures)
			}
			if len(created.NonFinalizedSignatures) != 1 || created.NonFinalizedSignatures[0].Signature != fmt.Sprintf("signature-%d", failStage) {
				t.Fatalf("non-finalized journal = %+v", created.NonFinalizedSignatures)
			}
			if created.FinalizedAndVerified {
				t.Fatal("ambiguous run was marked verified")
			}
			// The fake applies the failed call before returning the transport error.
			// This models the dangerous case where the chain accepted it; the exact
			// send count proves initialize still did not retry or continue.
			if failStage == 1 && client.accounts[mint.PublicKey] == nil ||
				failStage == 2 && client.accounts[token.PublicKey] == nil {
				t.Fatal("test did not model an accepted-but-unknown transaction")
			}
		})
	}
}

func TestInitializeUnknownOutcomeWithoutSignatureStillNeverRetries(t *testing.T) {
	payer := testSigner(t)
	program := testSigner(t).PublicKey
	client := newFakeClient(program, payer.PublicKey)
	client.balance = client.requiredBalance()
	client.failSend = 1
	client.failAfterApply = true
	client.failWithEmptySignature = true
	created, err := initialize(context.Background(), client, config{
		Program: program, Payer: payer, Decimals: 0, AmountRaw: 1,
	}, signerSequence(testSigner(t), testSigner(t)), time.Now, nil)
	if err == nil || !strings.Contains(err.Error(), "before a transaction signature") || !strings.Contains(err.Error(), "no retry") {
		t.Fatalf("error = %v", err)
	}
	if client.sendCalls != 1 || len(created.NonFinalizedSignatures) != 0 || len(created.SubmittedSignatures) != 0 {
		t.Fatalf("sends=%d result=%+v", client.sendCalls, created)
	}
}

func TestInitializeRejectsCorruptFinalizedStateBeforeContinuing(t *testing.T) {
	tests := []struct {
		name        string
		stage       int
		addressKind string
		corrupt     func(*svmtest.AccountInfo)
		want        string
	}{
		{"mint reserved byte", 1, "mint", corruptDataByte(3, 1), "decode mint"},
		{"token unexpected amount", 2, "token", corruptDataByte(8, 1), "does not match expected"},
		{"post-mint supply", 3, "mint", corruptDataByte(8, 1), "does not match expected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payer := testSigner(t)
			program := testSigner(t).PublicKey
			mint := testSigner(t)
			token := testSigner(t)
			client := newFakeClient(program, payer.PublicKey)
			client.balance = client.requiredBalance()
			client.corruptAtSend = test.stage
			if test.addressKind == "mint" {
				client.corruptAddress = mint.PublicKey
			} else {
				client.corruptAddress = token.PublicKey
			}
			client.corrupt = test.corrupt
			created, err := initialize(context.Background(), client, config{
				Program: program, Payer: payer, Decimals: 6, AmountRaw: 7,
			}, signerSequence(mint, token), time.Now, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if client.sendCalls != test.stage {
				t.Fatalf("send calls = %d, want %d", client.sendCalls, test.stage)
			}
			if len(created.FinalizedSignatures) != test.stage || len(created.SubmittedSignatures) != test.stage || len(created.NonFinalizedSignatures) != 0 {
				t.Fatalf("signature evidence = %+v", created)
			}
			if created.FinalizedAndVerified {
				t.Fatal("corrupt state was marked verified")
			}
		})
	}
}

func TestVerifiedStateDataRejectsInvalidFinalizedMetadataAndEncoding(t *testing.T) {
	program := testSigner(t).PublicKey
	address := testSigner(t).PublicKey
	validData := make([]byte, gospl.MintStateSize)
	if err := gospl.EncodeMintState(validData, gospl.MintState{
		Initialized: true, MintAuthority: gospl.OptionalPubkey{Set: true, Key: testSigner(t).PublicKey},
	}); err != nil {
		t.Fatal(err)
	}
	valid := fakeStateAccount(program, 500, validData)
	tests := []struct {
		name string
		info *svmtest.AccountInfo
		want string
	}{
		{"missing", nil, "does not exist"},
		{"executable", mutateInfo(valid, func(info *svmtest.AccountInfo) { info.Executable = true }), "unexpectedly executable"},
		{"wrong owner", mutateInfo(valid, func(info *svmtest.AccountInfo) { info.Owner = loader.ProgramID.String() }), "owner="},
		{"below rent", mutateInfo(valid, func(info *svmtest.AccountInfo) { info.Lamports = 499 }), "want at least 500"},
		{"non-canonical RPC tuple", mutateInfo(valid, func(info *svmtest.AccountInfo) { info.Data = base64.StdEncoding.EncodeToString(validData) }), "two-element"},
		{"wrong RPC encoding", mutateInfo(valid, func(info *svmtest.AccountInfo) { info.Data = []any{"AA==", "base58"} }), "canonical base64"},
		{"invalid base64", mutateInfo(valid, func(info *svmtest.AccountInfo) { info.Data = []any{"%%%", "base64"} }), "decode Solana RPC"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newFakeClient(program, testSigner(t).PublicKey)
			client.accounts[address] = test.info
			_, err := verifiedStateData(context.Background(), client, program, address, 500)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestInitializeFailsClosedWhenRecoveryProgressCannotBeWritten(t *testing.T) {
	payer := testSigner(t)
	program := testSigner(t).PublicKey
	client := newFakeClient(program, payer.PublicKey)
	client.balance = client.requiredBalance()
	calls := 0
	created, err := initialize(context.Background(), client, config{
		Program: program, Payer: payer, Decimals: 0, AmountRaw: 1,
	}, signerSequence(testSigner(t), testSigner(t)), time.Now, func(progressEvent) error {
		calls++
		return errors.New("disk full")
	})
	if err == nil || !strings.Contains(err.Error(), "write recovery progress") {
		t.Fatalf("error = %v", err)
	}
	if created == nil || calls != 1 || client.sendCalls != 0 || client.balanceCalls != 0 {
		t.Fatalf("created=%+v progressCalls=%d balanceCalls=%d sends=%d", created, calls, client.balanceCalls, client.sendCalls)
	}
}

func TestInitializeStopsAfterFinalizedProgressWriteFailure(t *testing.T) {
	payer := testSigner(t)
	program := testSigner(t).PublicKey
	mint := testSigner(t)
	client := newFakeClient(program, payer.PublicKey)
	client.balance = client.requiredBalance()
	created, err := initialize(context.Background(), client, config{
		Program: program, Payer: payer, Decimals: 0, AmountRaw: 1,
	}, signerSequence(mint, testSigner(t)), time.Now, func(event progressEvent) error {
		if event.Event == "finalized" {
			return errors.New("journal device failed")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "write recovery progress") {
		t.Fatalf("error = %v", err)
	}
	if client.sendCalls != 1 || client.accounts[mint.PublicKey] == nil {
		t.Fatalf("first finalized mutation was not modeled: sends=%d", client.sendCalls)
	}
	if len(created.SubmittedSignatures) != 1 || len(created.FinalizedSignatures) != 1 || len(created.NonFinalizedSignatures) != 0 {
		t.Fatalf("signature evidence = %+v", created)
	}
	if created.FinalizedAndVerified {
		t.Fatal("partial run was marked fully verified")
	}
}

type fakeClient struct {
	program                sdk.Pubkey
	payer                  sdk.Pubkey
	accounts               map[sdk.Pubkey]*svmtest.AccountInfo
	mintRent               uint64
	tokenRent              uint64
	balance                uint64
	balanceCalls           int
	sendCalls              int
	failSend               int
	failAfterApply         bool
	failWithEmptySignature bool
	getCallsAfterSend      int
	corruptAtSend          int
	corruptAddress         sdk.Pubkey
	corrupt                func(*svmtest.AccountInfo)
}

func newFakeClient(program, payer sdk.Pubkey) *fakeClient {
	return &fakeClient{
		program:   program,
		payer:     payer,
		mintRent:  gospl.MintStateSize * 100,
		tokenRent: gospl.TokenAccountStateSize * 100,
		balance:   1_000_000_000,
		accounts: map[sdk.Pubkey]*svmtest.AccountInfo{
			program: {Owner: loader.ProgramID.String(), Executable: true},
		},
	}
}

func (*fakeClient) Health(context.Context) error { return nil }

func (*fakeClient) GenesisHash(context.Context) (string, error) { return "test-genesis", nil }

func (f *fakeClient) MinimumBalanceForRentExemption(_ context.Context, size uint64) (uint64, error) {
	switch size {
	case gospl.MintStateSize:
		return f.mintRent, nil
	case gospl.TokenAccountStateSize:
		return f.tokenRent, nil
	default:
		return 0, fmt.Errorf("unexpected rent size %d", size)
	}
}

func (f *fakeClient) Balance(_ context.Context, address sdk.Pubkey) (uint64, error) {
	f.balanceCalls++
	if address != f.payer {
		return 0, fmt.Errorf("balance address %s, want payer %s", address, f.payer)
	}
	return f.balance, nil
}

func (f *fakeClient) GetAccountInfo(_ context.Context, address sdk.Pubkey) (*svmtest.AccountInfo, error) {
	if f.sendCalls > 0 {
		f.getCallsAfterSend++
	}
	info := cloneAccountInfo(f.accounts[address])
	if info != nil && f.corrupt != nil && f.sendCalls == f.corruptAtSend && address == f.corruptAddress {
		f.corrupt(info)
	}
	return info, nil
}

func (f *fakeClient) SendInstructions(_ context.Context, feePayer svmtest.Signer, signers []svmtest.Signer, instructions []sdk.Instruction) (string, error) {
	f.sendCalls++
	signature := fmt.Sprintf("signature-%d", f.sendCalls)
	failing := f.failSend == f.sendCalls
	if failing && !f.failAfterApply {
		if f.failWithEmptySignature {
			signature = ""
		}
		return signature, errors.New("simulated submit outcome unknown")
	}
	if feePayer.PublicKey != f.payer {
		return signature, errors.New("wrong payer")
	}
	if err := f.applyInstructions(signers, instructions); err != nil {
		return signature, err
	}
	if failing {
		if f.failWithEmptySignature {
			signature = ""
		}
		return signature, errors.New("simulated response loss after acceptance")
	}
	return signature, nil
}

func (f *fakeClient) applyInstructions(signers []svmtest.Signer, instructions []sdk.Instruction) error {
	switch f.sendCalls {
	case 1:
		if len(signers) != 1 {
			return errors.New("mint transaction signer count")
		}
		mint := signers[0].PublicKey
		want := []sdk.Instruction{
			system.CreateAccount(f.payer, mint, f.mintRent, gospl.MintStateSize, f.program),
			gospl.InitializeMint(f.program, mint, f.payer, instructionsDecimals(instructions)),
		}
		if !reflect.DeepEqual(instructions, want) {
			return fmt.Errorf("mint transaction mismatch:\n got %#v\nwant %#v", instructions, want)
		}
		decoded, err := gospl.DecodeInstruction(instructions[1].Data)
		if err != nil {
			return err
		}
		data := make([]byte, gospl.MintStateSize)
		if err := gospl.EncodeMintState(data, gospl.MintState{
			Initialized: true, Decimals: decoded.Decimals,
			MintAuthority: gospl.OptionalPubkey{Set: true, Key: decoded.Authority},
		}); err != nil {
			return err
		}
		f.accounts[mint] = fakeStateAccount(f.program, f.mintRent, data)
	case 2:
		if len(signers) != 1 || len(instructions) != 2 {
			return errors.New("token-account transaction shape")
		}
		token := signers[0].PublicKey
		decoded, err := gospl.DecodeInstruction(instructions[1].Data)
		if err != nil {
			return err
		}
		mint := instructions[1].Accounts[1].Pubkey
		want := []sdk.Instruction{
			system.CreateAccount(f.payer, token, f.tokenRent, gospl.TokenAccountStateSize, f.program),
			gospl.InitializeAccount(f.program, token, mint, f.payer),
		}
		if !reflect.DeepEqual(instructions, want) {
			return fmt.Errorf("token transaction mismatch:\n got %#v\nwant %#v", instructions, want)
		}
		data := make([]byte, gospl.TokenAccountStateSize)
		if err := gospl.EncodeTokenAccountState(data, gospl.TokenAccountState{
			Initialized: true, Mint: mint, Owner: decoded.Authority,
		}); err != nil {
			return err
		}
		f.accounts[token] = fakeStateAccount(f.program, f.tokenRent, data)
	case 3:
		if len(signers) != 0 || len(instructions) != 1 {
			return errors.New("mint-to transaction shape")
		}
		decoded, err := gospl.DecodeInstruction(instructions[0].Data)
		if err != nil || decoded.Kind != gospl.InstructionMintTo {
			return errors.New("invalid mint-to instruction")
		}
		mintKey := instructions[0].Accounts[0].Pubkey
		tokenKey := instructions[0].Accounts[1].Pubkey
		want := []sdk.Instruction{gospl.MintTo(f.program, mintKey, tokenKey, f.payer, decoded.Amount)}
		if !reflect.DeepEqual(instructions, want) {
			return fmt.Errorf("mint-to transaction mismatch:\n got %#v\nwant %#v", instructions, want)
		}
		mintData, err := f.accounts[mintKey].DataBytes()
		if err != nil {
			return err
		}
		mintState, err := gospl.DecodeMintState(mintData)
		if err != nil {
			return err
		}
		mintState.Supply = decoded.Amount
		mintData = make([]byte, gospl.MintStateSize)
		if err := gospl.EncodeMintState(mintData, mintState); err != nil {
			return err
		}
		f.accounts[mintKey] = fakeStateAccount(f.program, f.mintRent, mintData)
		tokenData, err := f.accounts[tokenKey].DataBytes()
		if err != nil {
			return err
		}
		tokenState, err := gospl.DecodeTokenAccountState(tokenData)
		if err != nil {
			return err
		}
		tokenState.Amount = decoded.Amount
		tokenData = make([]byte, gospl.TokenAccountStateSize)
		if err := gospl.EncodeTokenAccountState(tokenData, tokenState); err != nil {
			return err
		}
		f.accounts[tokenKey] = fakeStateAccount(f.program, f.tokenRent, tokenData)
	default:
		return errors.New("unexpected extra send")
	}
	return nil
}

func instructionsDecimals(instructions []sdk.Instruction) uint8 {
	if len(instructions) != 2 || len(instructions[1].Data) != 34 {
		return 0
	}
	return instructions[1].Data[1]
}

func (f *fakeClient) requiredBalance() uint64 {
	return f.mintRent + f.tokenRent + minimumFeeReserveLamports
}

func fakeStateAccount(program sdk.Pubkey, lamports uint64, data []byte) *svmtest.AccountInfo {
	return &svmtest.AccountInfo{
		Lamports: lamports,
		Owner:    program.String(),
		Data:     []any{base64.StdEncoding.EncodeToString(data), "base64"},
	}
}

func cloneAccountInfo(info *svmtest.AccountInfo) *svmtest.AccountInfo {
	if info == nil {
		return nil
	}
	clone := *info
	if tuple, ok := info.Data.([]any); ok {
		clone.Data = append([]any(nil), tuple...)
	}
	return &clone
}

func mutateInfo(info *svmtest.AccountInfo, mutate func(*svmtest.AccountInfo)) *svmtest.AccountInfo {
	clone := cloneAccountInfo(info)
	mutate(clone)
	return clone
}

func corruptDataByte(index int, value byte) func(*svmtest.AccountInfo) {
	return func(info *svmtest.AccountInfo) {
		data, err := info.DataBytes()
		if err != nil || index < 0 || index >= len(data) {
			panic("invalid test corruption")
		}
		data[index] = value
		info.Data = []any{base64.StdEncoding.EncodeToString(data), "base64"}
	}
}

func signerSequence(signers ...svmtest.Signer) func() (svmtest.Signer, error) {
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

func appendCopy(base []string, values ...string) []string {
	result := append([]string(nil), base...)
	return append(result, values...)
}

func testSigner(t *testing.T) svmtest.Signer {
	t.Helper()
	signer, err := svmtest.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
