package gospl_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/ersanyakit/solanago/compiler"
	"github.com/ersanyakit/solanago/deploy"
	sbpfelf "github.com/ersanyakit/solanago/elf"
	"github.com/ersanyakit/solanago/examples/gospl"
	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/loader"
	"github.com/ersanyakit/solanago/sdk/system"
	"github.com/ersanyakit/solanago/svmtest"
)

// TestAgaveGOSPLFullStateMachine is the real-runtime acceptance gate for the
// custom Go GOSPL contract. It deliberately uses an external test package so
// the production client ABI is exercised exactly as downstream Go code uses
// it. The official Agave validator is only an execution oracle: compilation,
// deployment, transaction construction, signing, and state decoding are Go.
func TestAgaveGOSPLFullStateMachine(t *testing.T) {
	binDir := os.Getenv("GOSBF_AGAVE_BIN")
	programPath := os.Getenv("GOSBF_GOSPL_SO")
	if binDir == "" || programPath == "" {
		t.Skip("set GOSBF_AGAVE_BIN and GOSBF_GOSPL_SO for the official-Agave GOSPL gate")
	}
	artifact, err := os.ReadFile(programPath)
	if err != nil {
		t.Fatalf("read GOSPL ELF: %v", err)
	}
	sourceBytes, err := os.ReadFile("testdata/program.go")
	if err != nil {
		t.Fatalf("read checked-in GOSPL source: %v", err)
	}
	compiled, err := compiler.CompileSource("testdata/program.go", sourceBytes)
	if err != nil {
		t.Fatalf("compile checked-in GOSPL source: %v", err)
	}
	executable, err := compiler.GenerateSolanaEntrypoint(compiled, "Program")
	if err != nil {
		t.Fatalf("generate checked-in GOSPL entrypoint: %v", err)
	}
	wantArtifact, err := sbpfelf.BuildV3(executable.Bytecode, 0)
	if err != nil {
		t.Fatalf("build checked-in GOSPL ELF: %v", err)
	}
	if !bytes.Equal(artifact, wantArtifact) {
		t.Fatalf("GOSBF_GOSPL_SO is not the byte-exact ELF rebuilt from testdata/program.go: got %d bytes, want %d", len(artifact), len(wantArtifact))
	}

	payer := newSigner(t)
	program := newSigner(t)
	mint := newSigner(t)
	source := newSigner(t)
	destination := newSigner(t)
	destinationOwner := newSigner(t)
	delegate := newSigner(t)
	newOwner := newSigner(t)
	newMintAuthority := newSigner(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	validator, err := svmtest.StartLocalValidator(ctx, svmtest.LocalValidatorConfig{
		AgaveBinDir: binDir,
		Payer:       payer,
		LedgerDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := validator.Close(); closeErr != nil {
			t.Errorf("close validator: %v", closeErr)
		}
	}()

	deployed, err := deploy.Program(ctx, deploy.Config{
		Client:   validator.Client,
		FeePayer: payer,
		Program:  program,
	}, artifact)
	if err != nil {
		t.Fatalf("deploy GOSPL: %v (partial=%+v, validator log %s)", err, deployed, validator.LogPath)
	}
	if !deployed.Finalized || deployed.ProgramID != program.PublicKey {
		t.Fatalf("incomplete GOSPL deploy: %+v", deployed)
	}
	programInfo := accountInfo(t, ctx, validator.Client, program.PublicKey)
	if !programInfo.Executable || programInfo.Owner != loader.ProgramID.String() {
		t.Fatalf("program account executable=%v owner=%s, want upgradeable loader %s", programInfo.Executable, programInfo.Owner, loader.ProgramID)
	}

	mintRent, err := validator.Client.MinimumBalanceForRentExemption(ctx, gospl.MintStateSize)
	if err != nil {
		t.Fatal(err)
	}
	tokenRent, err := validator.Client.MinimumBalanceForRentExemption(ctx, gospl.TokenAccountStateSize)
	if err != nil {
		t.Fatal(err)
	}

	signatures := append([]string(nil), deployed.Signatures...)
	submit := func(name string, signers []svmtest.Signer, instructions ...sdk.Instruction) {
		t.Helper()
		signature, submitErr := validator.Client.SendInstructions(ctx, payer, signers, instructions)
		if submitErr != nil {
			t.Fatalf("%s: %v (validator log %s)", name, submitErr, validator.LogPath)
		}
		if signature == "" {
			t.Fatalf("%s returned an empty signature", name)
		}
		signatures = append(signatures, signature)
	}

	submit("create and initialize mint", []svmtest.Signer{mint},
		system.CreateAccount(payer.PublicKey, mint.PublicKey, mintRent, gospl.MintStateSize, program.PublicKey),
		gospl.InitializeMint(program.PublicKey, mint.PublicKey, payer.PublicKey, 6),
	)
	requireMint(t, ctx, validator.Client, program.PublicKey, mint.PublicKey, mintRent, gospl.MintState{
		Initialized:   true,
		Decimals:      6,
		MintAuthority: gospl.OptionalPubkey{Set: true, Key: payer.PublicKey},
	})

	submit("create and initialize source", []svmtest.Signer{source},
		system.CreateAccount(payer.PublicKey, source.PublicKey, tokenRent, gospl.TokenAccountStateSize, program.PublicKey),
		gospl.InitializeAccount(program.PublicKey, source.PublicKey, mint.PublicKey, payer.PublicKey),
	)
	submit("create and initialize destination", []svmtest.Signer{destination},
		system.CreateAccount(payer.PublicKey, destination.PublicKey, tokenRent, gospl.TokenAccountStateSize, program.PublicKey),
		gospl.InitializeAccount(program.PublicKey, destination.PublicKey, mint.PublicKey, destinationOwner.PublicKey),
	)

	submit("mint 1000", nil, gospl.MintTo(program.PublicKey, mint.PublicKey, source.PublicKey, payer.PublicKey, 1_000))
	submit("owner transfer 250", nil, gospl.Transfer(program.PublicKey, source.PublicKey, destination.PublicKey, payer.PublicKey, 250))
	submit("owner burn 50", []svmtest.Signer{destinationOwner}, gospl.Burn(program.PublicKey, destination.PublicKey, mint.PublicKey, destinationOwner.PublicKey, 50))
	requireMint(t, ctx, validator.Client, program.PublicKey, mint.PublicKey, mintRent, gospl.MintState{
		Initialized:   true,
		Decimals:      6,
		Supply:        950,
		MintAuthority: gospl.OptionalPubkey{Set: true, Key: payer.PublicKey},
	})
	requireToken(t, ctx, validator.Client, program.PublicKey, source.PublicKey, tokenRent, gospl.TokenAccountState{
		Initialized: true, Mint: mint.PublicKey, Owner: payer.PublicKey, Amount: 750,
	})
	requireToken(t, ctx, validator.Client, program.PublicKey, destination.PublicKey, tokenRent, gospl.TokenAccountState{
		Initialized: true, Mint: mint.PublicKey, Owner: destinationOwner.PublicKey, Amount: 200,
	})

	submit("approve delegate 120", nil, gospl.Approve(program.PublicKey, source.PublicKey, payer.PublicKey, delegate.PublicKey, 120))
	requireToken(t, ctx, validator.Client, program.PublicKey, source.PublicKey, tokenRent, gospl.TokenAccountState{
		Initialized:     true,
		Mint:            mint.PublicKey,
		Owner:           payer.PublicKey,
		Amount:          750,
		Delegate:        gospl.OptionalPubkey{Set: true, Key: delegate.PublicKey},
		DelegatedAmount: 120,
	})
	submit("delegate transfer 70", []svmtest.Signer{delegate}, gospl.Transfer(program.PublicKey, source.PublicKey, destination.PublicKey, delegate.PublicKey, 70))
	submit("delegate burn 20", []svmtest.Signer{delegate}, gospl.Burn(program.PublicKey, source.PublicKey, mint.PublicKey, delegate.PublicKey, 20))
	requireToken(t, ctx, validator.Client, program.PublicKey, source.PublicKey, tokenRent, gospl.TokenAccountState{
		Initialized:     true,
		Mint:            mint.PublicKey,
		Owner:           payer.PublicKey,
		Amount:          660,
		Delegate:        gospl.OptionalPubkey{Set: true, Key: delegate.PublicKey},
		DelegatedAmount: 30,
	})

	submit("revoke delegate", nil, gospl.Revoke(program.PublicKey, source.PublicKey, payer.PublicKey))
	requireToken(t, ctx, validator.Client, program.PublicKey, source.PublicKey, tokenRent, gospl.TokenAccountState{
		Initialized: true, Mint: mint.PublicKey, Owner: payer.PublicKey, Amount: 660,
	})

	setOwner, err := gospl.SetAuthority(program.PublicKey, source.PublicKey, payer.PublicKey, gospl.AuthorityAccountOwner,
		gospl.OptionalPubkey{Set: true, Key: newOwner.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	submit("replace source owner", nil, setOwner)
	submit("new owner transfer 1", []svmtest.Signer{newOwner}, gospl.Transfer(program.PublicKey, source.PublicKey, destination.PublicKey, newOwner.PublicKey, 1))

	setMintAuthority, err := gospl.SetAuthority(program.PublicKey, mint.PublicKey, payer.PublicKey, gospl.AuthorityMintTokens,
		gospl.OptionalPubkey{Set: true, Key: newMintAuthority.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	submit("replace mint authority", nil, setMintAuthority)
	submit("new authority mint 10", []svmtest.Signer{newMintAuthority},
		gospl.MintTo(program.PublicKey, mint.PublicKey, source.PublicKey, newMintAuthority.PublicKey, 10))
	disableMintAuthority, err := gospl.SetAuthority(program.PublicKey, mint.PublicKey, newMintAuthority.PublicKey,
		gospl.AuthorityMintTokens, gospl.OptionalPubkey{})
	if err != nil {
		t.Fatal(err)
	}
	submit("permanently disable mint authority", []svmtest.Signer{newMintAuthority}, disableMintAuthority)

	requireMint(t, ctx, validator.Client, program.PublicKey, mint.PublicKey, mintRent, gospl.MintState{
		Initialized: true,
		Decimals:    6,
		Supply:      940,
	})
	requireToken(t, ctx, validator.Client, program.PublicKey, source.PublicKey, tokenRent, gospl.TokenAccountState{
		Initialized: true,
		Mint:        mint.PublicKey,
		Owner:       newOwner.PublicKey,
		Amount:      669,
	})
	requireToken(t, ctx, validator.Client, program.PublicKey, destination.PublicKey, tokenRent, gospl.TokenAccountState{
		Initialized: true,
		Mint:        mint.PublicKey,
		Owner:       destinationOwner.PublicKey,
		Amount:      271,
	})

	t.Logf("official Agave GOSPL state machine finalized: program=%s mint=%s source=%s destination=%s transactions=%d",
		program.PublicKey, mint.PublicKey, source.PublicKey, destination.PublicKey, len(signatures))
	t.Logf("final canonical state: supply=940 decimals=6 mint_authority=disabled source_amount=669 source_owner=%s source_delegate=none destination_amount=271 destination_owner=%s",
		newOwner.PublicKey, destinationOwner.PublicKey)
	t.Logf("finalized transaction signatures: %v", signatures)
}

func newSigner(t *testing.T) svmtest.Signer {
	t.Helper()
	signer, err := svmtest.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func accountInfo(t *testing.T, ctx context.Context, client svmtest.Client, address sdk.Pubkey) *svmtest.AccountInfo {
	t.Helper()
	info, err := client.GetAccountInfo(ctx, address)
	if err != nil {
		t.Fatalf("get finalized account %s: %v", address, err)
	}
	if info == nil {
		t.Fatalf("finalized account %s does not exist", address)
	}
	return info
}

func accountData(t *testing.T, ctx context.Context, client svmtest.Client, programID, address sdk.Pubkey, minimumLamports uint64) []byte {
	t.Helper()
	info := accountInfo(t, ctx, client, address)
	if info.Executable {
		t.Fatalf("GOSPL state account %s is unexpectedly executable", address)
	}
	if info.Owner != programID.String() {
		t.Fatalf("GOSPL state account %s owner=%s, want %s", address, info.Owner, programID)
	}
	if info.Lamports < minimumLamports {
		t.Fatalf("GOSPL state account %s lamports=%d, want at least rent %d", address, info.Lamports, minimumLamports)
	}
	data, err := info.DataBytes()
	if err != nil {
		t.Fatalf("decode account %s data: %v", address, err)
	}
	return data
}

func requireMint(t *testing.T, ctx context.Context, client svmtest.Client, programID, address sdk.Pubkey, rent uint64, want gospl.MintState) {
	t.Helper()
	data := accountData(t, ctx, client, programID, address, rent)
	got, err := gospl.DecodeMintState(data)
	if err != nil {
		t.Fatalf("decode mint %s: %v (data=%x)", address, err, data)
	}
	if got != want {
		t.Fatalf("mint %s = %+v, want %+v", address, got, want)
	}
	canonical := make([]byte, gospl.MintStateSize)
	if err := gospl.EncodeMintState(canonical, want); err != nil {
		t.Fatalf("encode expected mint: %v", err)
	}
	if !bytes.Equal(data, canonical) {
		t.Fatalf("mint %s is not byte-for-byte canonical\ngot:  %x\nwant: %x", address, data, canonical)
	}
}

func requireToken(t *testing.T, ctx context.Context, client svmtest.Client, programID, address sdk.Pubkey, rent uint64, want gospl.TokenAccountState) {
	t.Helper()
	data := accountData(t, ctx, client, programID, address, rent)
	got, err := gospl.DecodeTokenAccountState(data)
	if err != nil {
		t.Fatalf("decode token account %s: %v (data=%x)", address, err, data)
	}
	if got != want {
		t.Fatalf("token account %s = %+v, want %+v", address, got, want)
	}
	canonical := make([]byte, gospl.TokenAccountStateSize)
	if err := gospl.EncodeTokenAccountState(canonical, want); err != nil {
		t.Fatalf("encode expected token account: %v", err)
	}
	if !bytes.Equal(data, canonical) {
		t.Fatalf("token account %s is not byte-for-byte canonical\ngot:  %x\nwant: %x", address, data, canonical)
	}
}
