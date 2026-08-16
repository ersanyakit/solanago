package main

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ersanyakit/solanago/examples/hellogo"
	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/loader"
	"github.com/ersanyakit/solanago/svmtest"
)

func TestParseAndFormatUIAmount(t *testing.T) {
	t.Parallel()
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

func TestRunCLIRejectsLiveEndpointBeforeLoadingKey(t *testing.T) {
	t.Parallel()
	loaded := false
	deps := dependencies{
		loadKey: func(string) (svmtest.Signer, error) {
			loaded = true
			return svmtest.Signer{}, nil
		},
		newClient: func(string) rpcClient { t.Fatal("created RPC client"); return nil },
		newSigner: svmtest.NewSigner,
		now:       time.Now,
	}
	err := runCLI([]string{
		"--program", testSigner(t).PublicKey.String(),
		"--keypair", "secret.json",
		"--url", "testnet",
		"--amount-ui", "100000",
	}, io.Discard, io.Discard, deps)
	if err == nil || !strings.Contains(err.Error(), "--allow-live") {
		t.Fatalf("error = %v, want live guard", err)
	}
	if loaded {
		t.Fatal("keypair was loaded before live endpoint guard")
	}
}

func TestInitializeFinalizesAndVerifiesEveryStage(t *testing.T) {
	payer := testSigner(t)
	program := testSigner(t).PublicKey
	mint := testSigner(t)
	token := testSigner(t)
	client := newFakeClient(program, payer.PublicKey)
	signers := []svmtest.Signer{mint, token}
	index := 0
	created, err := initialize(context.Background(), client, config{
		Program: program, Payer: payer, Decimals: 6, AmountRaw: 100_000_000_000,
	}, func() (svmtest.Signer, error) {
		if index >= len(signers) {
			return svmtest.Signer{}, errors.New("too many signer requests")
		}
		signer := signers[index]
		index++
		return signer, nil
	}, func() time.Time { return time.Unix(1, 2) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !created.FinalizedAndVerified || created.Mint != mint.PublicKey.String() || created.TokenAccount != token.PublicKey.String() {
		t.Fatalf("unexpected result: %+v", created)
	}
	if created.AmountUI != "100000" || created.MintState.SupplyRaw != 100_000_000_000 || created.TokenAccountState.AmountRaw != 100_000_000_000 {
		t.Fatalf("unexpected final state: %+v", created)
	}
	if len(created.SubmittedSignatures) != 3 || len(created.FinalizedSignatures) != 3 || client.sendCalls != 3 {
		t.Fatalf("signatures submitted=%v finalized=%v sends=%d", created.SubmittedSignatures, created.FinalizedSignatures, client.sendCalls)
	}
	if client.getCallsAfterSend != 5 {
		t.Fatalf("finalized state reads after sends = %d, want 5", client.getCallsAfterSend)
	}
}

func TestInitializePreservesAmbiguousSignatureAndDoesNotRetry(t *testing.T) {
	payer := testSigner(t)
	program := testSigner(t).PublicKey
	client := newFakeClient(program, payer.PublicKey)
	client.failSend = 2
	created, err := initialize(context.Background(), client, config{
		Program: program, Payer: payer, Decimals: 6, AmountRaw: 1,
	}, svmtest.NewSigner, time.Now, nil)
	if err == nil || !strings.Contains(err.Error(), "no retry") {
		t.Fatalf("error = %v, want ambiguous no-retry error", err)
	}
	if client.sendCalls != 2 {
		t.Fatalf("send calls = %d, want 2", client.sendCalls)
	}
	if len(created.SubmittedSignatures) != 1 || created.SubmittedSignatures[0].Signature != "signature-1" {
		t.Fatalf("submitted journal = %+v", created.SubmittedSignatures)
	}
	if len(created.NonFinalizedSignatures) != 1 || created.NonFinalizedSignatures[0].Signature != "signature-2" {
		t.Fatalf("non-finalized journal = %+v", created.NonFinalizedSignatures)
	}
	if len(created.FinalizedSignatures) != 1 || created.FinalizedAndVerified {
		t.Fatalf("finalized journal = %+v, verified=%v", created.FinalizedSignatures, created.FinalizedAndVerified)
	}
}

type fakeClient struct {
	program           sdk.Pubkey
	payer             sdk.Pubkey
	accounts          map[sdk.Pubkey]*svmtest.AccountInfo
	sendCalls         int
	failSend          int
	getCallsAfterSend int
}

func newFakeClient(program, payer sdk.Pubkey) *fakeClient {
	return &fakeClient{
		program: program,
		payer:   payer,
		accounts: map[sdk.Pubkey]*svmtest.AccountInfo{
			program: {Owner: loader.ProgramID.String(), Executable: true},
		},
	}
}

func (*fakeClient) Health(context.Context) error { return nil }

func (*fakeClient) GenesisHash(context.Context) (string, error) { return "test-genesis", nil }

func (*fakeClient) MinimumBalanceForRentExemption(_ context.Context, size uint64) (uint64, error) {
	return size * 100, nil
}

func (*fakeClient) Balance(context.Context, sdk.Pubkey) (uint64, error) { return 1_000_000_000, nil }

func (f *fakeClient) GetAccountInfo(_ context.Context, address sdk.Pubkey) (*svmtest.AccountInfo, error) {
	if f.sendCalls > 0 {
		f.getCallsAfterSend++
	}
	return f.accounts[address], nil
}

func (f *fakeClient) SendInstructions(_ context.Context, feePayer svmtest.Signer, signers []svmtest.Signer, instructions []sdk.Instruction) (string, error) {
	f.sendCalls++
	signature := "signature-" + string(rune('0'+f.sendCalls))
	if f.failSend == f.sendCalls {
		return signature, errors.New("simulated ambiguous submit")
	}
	if feePayer.PublicKey != f.payer {
		return signature, errors.New("wrong payer")
	}
	switch f.sendCalls {
	case 1:
		if len(signers) != 1 || len(instructions) != 2 {
			return signature, errors.New("invalid mint transaction")
		}
		decoded, err := hellogo.DecodeInstruction(instructions[1].Data)
		if err != nil || decoded.Kind != hellogo.InstructionInitializeMint {
			return signature, errors.New("invalid initialize-mint instruction")
		}
		mint := signers[0].PublicKey
		data := make([]byte, hellogo.MintStateSize)
		if err := hellogo.EncodeMintState(data, hellogo.MintState{
			Initialized: true, Decimals: decoded.Decimals,
			MintAuthority: hellogo.OptionalPubkey{Set: true, Key: decoded.Authority},
		}); err != nil {
			return signature, err
		}
		f.accounts[mint] = fakeStateAccount(f.program, hellogo.MintStateSize*100, data)
	case 2:
		if len(signers) != 1 || len(instructions) != 2 {
			return signature, errors.New("invalid token-account transaction")
		}
		decoded, err := hellogo.DecodeInstruction(instructions[1].Data)
		if err != nil || decoded.Kind != hellogo.InstructionInitializeAccount {
			return signature, errors.New("invalid initialize-account instruction")
		}
		token := signers[0].PublicKey
		mint := instructions[1].Accounts[1].Pubkey
		data := make([]byte, hellogo.TokenAccountStateSize)
		if err := hellogo.EncodeTokenAccountState(data, hellogo.TokenAccountState{
			Initialized: true, Mint: mint, Owner: decoded.Authority,
		}); err != nil {
			return signature, err
		}
		f.accounts[token] = fakeStateAccount(f.program, hellogo.TokenAccountStateSize*100, data)
	case 3:
		if len(signers) != 0 || len(instructions) != 1 {
			return signature, errors.New("invalid mint-to transaction")
		}
		decoded, err := hellogo.DecodeInstruction(instructions[0].Data)
		if err != nil || decoded.Kind != hellogo.InstructionMintTo {
			return signature, errors.New("invalid mint-to instruction")
		}
		mintKey := instructions[0].Accounts[0].Pubkey
		tokenKey := instructions[0].Accounts[1].Pubkey
		mintData, _ := f.accounts[mintKey].DataBytes()
		mintState, _ := hellogo.DecodeMintState(mintData)
		mintState.Supply = decoded.Amount
		mintData = make([]byte, hellogo.MintStateSize)
		_ = hellogo.EncodeMintState(mintData, mintState)
		f.accounts[mintKey] = fakeStateAccount(f.program, hellogo.MintStateSize*100, mintData)
		tokenData, _ := f.accounts[tokenKey].DataBytes()
		tokenState, _ := hellogo.DecodeTokenAccountState(tokenData)
		tokenState.Amount = decoded.Amount
		tokenData = make([]byte, hellogo.TokenAccountStateSize)
		_ = hellogo.EncodeTokenAccountState(tokenData, tokenState)
		f.accounts[tokenKey] = fakeStateAccount(f.program, hellogo.TokenAccountStateSize*100, tokenData)
	default:
		return signature, errors.New("unexpected extra send")
	}
	return signature, nil
}

func fakeStateAccount(program sdk.Pubkey, lamports uint64, data []byte) *svmtest.AccountInfo {
	return &svmtest.AccountInfo{
		Lamports: lamports,
		Owner:    program.String(),
		Data:     []any{base64.StdEncoding.EncodeToString(data), "base64"},
	}
}

func testSigner(t *testing.T) svmtest.Signer {
	t.Helper()
	signer, err := svmtest.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
