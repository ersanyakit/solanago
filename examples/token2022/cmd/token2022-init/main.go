package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	gosoldeploy "github.com/ersany/go-solana/deploy"
	"github.com/ersany/go-solana/sdk"
	"github.com/ersany/go-solana/sdk/associatedtoken"
	"github.com/ersany/go-solana/sdk/metaplex"
	"github.com/ersany/go-solana/sdk/system"
	"github.com/ersany/go-solana/sdk/token2022"
	"github.com/ersany/go-solana/svmtest"
)

const minimumFeeReserveLamports = uint64(100_000)
const verifyRetryCount = 8
const defaultVanityMaxAttempts = 50000

type rpcClient interface {
	Health(context.Context) error
	GenesisHash(context.Context) (string, error)
	MinimumBalanceForRentExemption(context.Context, uint64) (uint64, error)
	Balance(context.Context, sdk.Pubkey) (uint64, error)
	GetAccountInfo(context.Context, sdk.Pubkey) (*svmtest.AccountInfo, error)
	SendInstructions(context.Context, svmtest.Signer, []svmtest.Signer, []sdk.Instruction) (string, error)
}

type rawAmountFlag struct {
	set   bool
	value uint64
}

func (v *rawAmountFlag) String() string {
	if v == nil || !v.set {
		return ""
	}
	return strconv.FormatUint(v.value, 10)
}

func (v *rawAmountFlag) Set(text string) error {
	if v.set {
		return errors.New("amount-raw may be set only once")
	}
	if text == "" {
		return errors.New("amount must contain at least one ASCII decimal digit")
	}
	for _, digit := range []byte(text) {
		if digit < '0' || digit > '9' {
			return errors.New("amount must contain only ASCII decimal digits")
		}
	}
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return fmt.Errorf("amount must be an unsigned base-10 uint64: %w", err)
	}
	v.set = true
	v.value = value
	return nil
}

func isRetryableRPCTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "context deadline exceeded") ||
		strings.Contains(message, "client.timeout") ||
		strings.Contains(message, "timeout")
}

func verifyWithRetry(ctx context.Context, stage string, verifyFn func() error) error {
	for attempt := 1; attempt <= verifyRetryCount; attempt++ {
		err := verifyFn()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %w", stage, ctx.Err())
		}
		if isRetryableRPCTimeout(err) && attempt < verifyRetryCount {
			retryDelay := time.Duration(attempt) * time.Second
			timer := time.NewTimer(retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("%s: %w", stage, ctx.Err())
			case <-timer.C:
			}
			continue
		} else {
			return fmt.Errorf("%s: %w", stage, err)
		}
	}
	return fmt.Errorf("%s: verification retry budget exhausted", stage)
}

type uiAmountFlag struct {
	set   bool
	value string
}

func (v *uiAmountFlag) String() string {
	if v == nil || !v.set {
		return ""
	}
	return v.value
}

func (v *uiAmountFlag) Set(text string) error {
	if v.set {
		return errors.New("amount-ui may be set only once")
	}
	v.set = true
	v.value = text
	return nil
}

type config struct {
	Payer    svmtest.Signer
	Owner    sdk.Pubkey
	Name     string
	Symbol   string
	URI      string
	Vanity   string
	Decimals uint8
	Amount   uint64
	Atomic   bool
}

type signatureRecord struct {
	Stage     string `json:"stage"`
	Signature string `json:"signature"`
}

type exactUint64 uint64

func (v exactUint64) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(v), 10))
}

type mintState struct {
	Initialized     bool        `json:"initialized"`
	Decimals        uint8       `json:"decimals"`
	SupplyRaw       exactUint64 `json:"supply_raw"`
	SupplyUI        string      `json:"supply_ui"`
	MintAuthority   string      `json:"mint_authority"`
	FreezeAuthority string      `json:"freeze_authority"`
}

type tokenState struct {
	Initialized bool        `json:"initialized"`
	Mint        string      `json:"mint"`
	Owner       string      `json:"owner"`
	AmountRaw   exactUint64 `json:"amount_raw"`
	AmountUI    string      `json:"amount_ui"`
}

type result struct {
	GeneratedAtUTC           string            `json:"generated_at_utc"`
	GenesisHash              string            `json:"genesis_hash"`
	TokenProgram             string            `json:"token_program"`
	FeePayer                 string            `json:"fee_payer"`
	Mint                     string            `json:"mint"`
	TokenAccount             string            `json:"token_account"`
	MetaplexMetadataAccount  string            `json:"metaplex_metadata_account,omitempty"`
	Owner                    string            `json:"owner"`
	Decimals                 uint8             `json:"decimals"`
	AmountRaw                exactUint64       `json:"amount_raw"`
	AmountUI                 string            `json:"amount_ui"`
	MintRentLamports         exactUint64       `json:"mint_rent_lamports"`
	TokenAccountRentLamports exactUint64       `json:"token_account_rent_lamports"`
	SubmittedSignatures      []signatureRecord `json:"submitted_signatures"`
	NonFinalizedSignatures   []signatureRecord `json:"non_finalized_signatures"`
	FinalizedSignatures      []signatureRecord `json:"finalized_signatures"`
	MintState                mintState         `json:"mint_state"`
	TokenAccountState        tokenState        `json:"token_account_state"`
	FinalizedAndVerified     bool              `json:"finalized_and_verified"`
}

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "token2022-init:", err)
		os.Exit(1)
	}
}

func runCLI(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("token2022-init", flag.ContinueOnError)
	flags.SetOutput(stderr)

	keypairPath := flags.String("keypair", "", "fee payer and mint authority keypair (required)")
	rpcURL := flags.String("url", "localhost", "Solana RPC URL or localhost/devnet/testnet/mainnet-beta")
	decimals := flags.Uint("decimals", 6, "mint decimal places (0..255; default 6)")
	name := flags.String("name", "DEMODEMO", "token name (default DEMODEMO)")
	symbol := flags.String("symbol", "DEMO", "token symbol (default DEMO)")
	uri := flags.String("uri", "", "metadata URI (optional)")
	vanitySuffix := flags.String("vanity-suffix", "", "require generated mint address to end with this string (optional)")
	ownerText := flags.String("owner", "", "token account owner public key (default fee payer)")
	allowLive := flags.Bool("allow-live", false, "explicitly permit a non-loopback RPC endpoint")
	atomic := flags.Bool("atomic", false, "submit mint creation, ATA creation, and mint-to as one transaction instead of three verified stages")
	var amountUI uiAmountFlag
	flags.Var(&amountUI, "amount-ui", "UI token amount, converted exactly using --decimals")
	var amountRaw rawAmountFlag
	flags.Var(&amountRaw, "amount-raw", "raw smallest-unit amount (mutually exclusive with --amount-ui)")
	timeout := flags.Duration("timeout", 5*time.Minute, "whole initialization deadline")

	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *keypairPath == "" {
		return errors.New("expected --keypair KEYPAIR and exactly one of --amount-raw or --amount-ui")
	}
	if *decimals > math.MaxUint8 {
		return fmt.Errorf("decimals %d exceed uint8", *decimals)
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if amountRaw.set == amountUI.set {
		return errors.New("set exactly one of --amount-raw or --amount-ui")
	}

	raw := amountRaw.value
	if amountUI.set {
		var err error
		raw, err = parseUIAmount(amountUI.value, uint8(*decimals))
		if err != nil {
			return fmt.Errorf("invalid --amount-ui: %w", err)
		}
	}
	if raw == 0 {
		return errors.New("mint amount must be positive")
	}

	resolvedURL, err := normalizeRPCURL(*rpcURL)
	if err != nil {
		return err
	}
	if !*allowLive && !loopbackRPC(resolvedURL) {
		return fmt.Errorf("refusing non-loopback RPC %q without --allow-live", *rpcURL)
	}

	payer, err := gosoldeploy.LoadKeypair(*keypairPath)
	if err != nil {
		return fmt.Errorf("load keypair: %w", err)
	}
	if err := svmtest.ValidateSigner(payer); err != nil {
		return fmt.Errorf("invalid keypair: %w", err)
	}

	owner := payer.PublicKey
	if *ownerText != "" {
		parsedOwner, parseErr := sdk.ParsePubkey(*ownerText)
		if parseErr != nil {
			return fmt.Errorf("parse --owner: %w", parseErr)
		}
		owner = parsedOwner
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	created := result{
		GeneratedAtUTC:         time.Now().UTC().Format(time.RFC3339),
		FeePayer:               payer.PublicKey.String(),
		TokenProgram:           token2022.ProgramID.String(),
		Decimals:               uint8(*decimals),
		AmountRaw:              exactUint64(raw),
		AmountUI:               formatUIAmount(raw, uint8(*decimals)),
		Owner:                  owner.String(),
		SubmittedSignatures:    make([]signatureRecord, 0, 3),
		NonFinalizedSignatures: make([]signatureRecord, 0, 3),
		FinalizedSignatures:    make([]signatureRecord, 0, 3),
	}

	createdClient := svmtest.Client{URL: resolvedURL}
	resultState, err := initializeToken2022(ctx, createdClient, config{
		Payer:    payer,
		Owner:    owner,
		Name:     *name,
		Symbol:   *symbol,
		URI:      *uri,
		Vanity:   *vanitySuffix,
		Decimals: uint8(*decimals),
		Amount:   raw,
		Atomic:   *atomic,
	})
	if err != nil {
		created = resultState
		if created.Mint != "" {
			// keep partial journal on partial output
			encoded, marshalErr := json.Marshal(created)
			if marshalErr == nil {
				fmt.Fprintf(stderr, "partial public recovery journal: %s\n", encoded)
			}
		}
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(resultState)
}

func initializeToken2022(ctx context.Context, client rpcClient, cfg config) (result, error) {
	return initializeToken2022WithDependencies(ctx, client, cfg, svmtest.NewSigner, time.Now)
}

func initializeToken2022WithDependencies(
	ctx context.Context,
	client rpcClient,
	cfg config,
	newSigner func() (svmtest.Signer, error),
	now func() time.Time,
) (result, error) {
	var out result
	if client == nil {
		return out, errors.New("RPC client is nil")
	}
	if err := svmtest.ValidateSigner(cfg.Payer); err != nil {
		return out, fmt.Errorf("invalid payer: %w", err)
	}
	if cfg.Amount == 0 {
		return out, errors.New("mint amount must be positive")
	}
	if newSigner == nil || now == nil {
		return out, errors.New("internal dependency is nil")
	}
	if err := client.Health(ctx); err != nil {
		return out, fmt.Errorf("rpc health: %w", err)
	}
	genesis, err := client.GenesisHash(ctx)
	if err != nil {
		return out, fmt.Errorf("get genesis hash: %w", err)
	}
	if genesis == "" {
		return out, errors.New("rpc returned empty genesis hash")
	}
	programInfo, err := client.GetAccountInfo(ctx, token2022.ProgramID)
	if err != nil {
		return out, fmt.Errorf("read Token-2022 program %s: %w", token2022.ProgramID, err)
	}
	if programInfo == nil {
		return out, fmt.Errorf("Token-2022 program %s is not deployed on this cluster", token2022.ProgramID)
	}
	if !programInfo.Executable {
		return out, fmt.Errorf("Token-2022 program %s is not executable", token2022.ProgramID)
	}
	out.GeneratedAtUTC = now().UTC().Format(time.RFC3339Nano)
	out.GenesisHash = genesis
	out.TokenProgram = token2022.ProgramID.String()
	out.FeePayer = cfg.Payer.PublicKey.String()
	out.Decimals = cfg.Decimals
	out.Owner = cfg.Owner.String()
	out.AmountRaw = exactUint64(cfg.Amount)
	out.AmountUI = formatUIAmount(cfg.Amount, cfg.Decimals)
	out.SubmittedSignatures = make([]signatureRecord, 0, 3)
	out.NonFinalizedSignatures = make([]signatureRecord, 0, 1)
	out.FinalizedSignatures = make([]signatureRecord, 0, 3)

	mint, attempts, err := newMintSignerWithVanity(newSigner, cfg.Vanity)
	if err != nil {
		return out, fmt.Errorf("generate in-memory mint signer: %w", err)
	}
	if cfg.Vanity != "" && attempts == 0 {
		return out, errors.New("vanity suffix not found within limit")
	}
	if err := svmtest.ValidateSigner(mint); err != nil {
		return out, fmt.Errorf("generated mint signer: %w", err)
	}
	if mint.PublicKey == cfg.Payer.PublicKey {
		return out, errors.New("generated signer collision")
	}
	if mint.PublicKey == cfg.Owner {
		return out, errors.New("generated signer collision with owner")
	}

	// The token account is the canonical Associated Token Account (ATA) for
	// (owner, mint) under Token-2022, not an arbitrary keypair account.
	// Wallets, explorers, and DEX frontends (Raydium included) derive this
	// same address to locate a holder's balance; minting into anything else
	// makes the balance invisible to them.
	tokenAccountAddress, _, err := associatedtoken.Derive(cfg.Owner, mint.PublicKey, token2022.ProgramID)
	if err != nil {
		return out, fmt.Errorf("derive associated token account: %w", err)
	}

	out.Mint = mint.PublicKey.String()
	out.TokenAccount = tokenAccountAddress.String()
	wantMetadata := buildExpectedMetadata(cfg, mint.PublicKey)

	// Wallets, explorers, and DEX frontends (Raydium's UI included) still
	// resolve a token's displayed name/symbol from the Metaplex Token
	// Metadata PDA, not from Token-2022's own metadata extension above; a
	// mint that only carries the extension shows up with a placeholder name.
	var metaplexMetadataAddress sdk.Pubkey
	if wantMetadata != nil {
		metaplexMetadataAddress, _, err = metaplex.DeriveMetadataAddress(mint.PublicKey)
		if err != nil {
			return out, fmt.Errorf("derive metaplex metadata account: %w", err)
		}
		out.MetaplexMetadataAccount = metaplexMetadataAddress.String()
	}

	if exists, err := client.GetAccountInfo(ctx, mint.PublicKey); err != nil {
		return out, fmt.Errorf("check generated address %s: %w", mint.PublicKey, err)
	} else if exists != nil {
		return out, fmt.Errorf("generated address %s already exists", mint.PublicKey)
	}

	// The mint carries no Token-2022 extensions: a MetadataPointer extension
	// pointing at itself (as earlier versions of this tool set up) makes
	// mpl-token-metadata's Create instruction reject the mint outright
	// (validate_mint requires any existing MetadataPointer to target the
	// Metaplex Metadata PDA with no separate authority), so metadata lives
	// only in the Metaplex account created below.
	createSize := uint64(token2022.MintSize)
	mintRentLamports, err := client.MinimumBalanceForRentExemption(ctx, createSize)
	if err != nil {
		return out, fmt.Errorf("query mint rent: %w", err)
	}
	// The associated-token-account program always attaches the ImmutableOwner
	// extension when creating a Token-2022 account, so the on-chain size (and
	// therefore rent) is larger than the bare 165-byte base account.
	tokenAccountSize, err := token2022.CalculateAccountLen([]token2022.ExtensionType{token2022.ExtensionImmutableOwner})
	if err != nil {
		return out, fmt.Errorf("calculate token account size: %w", err)
	}
	tokenAccountRentLamports, err := client.MinimumBalanceForRentExemption(ctx, uint64(tokenAccountSize))
	if err != nil {
		return out, fmt.Errorf("query token account rent: %w", err)
	}
	out.MintRentLamports = exactUint64(mintRentLamports)
	out.TokenAccountRentLamports = exactUint64(tokenAccountRentLamports)

	// Create charges the Metadata account's own rent plus a flat ~0.01 SOL
	// protocol fee (get_create_fee(): a rent-exemption query for
	// CreateFeeSizeScalar bytes, plus CreateFeeOffsetLamports) — computed
	// live rather than hard-coded since the rent component depends on the
	// cluster's rent schedule.
	var metaplexMetadataRentLamports uint64
	if wantMetadata != nil {
		metadataAccountRent, err := client.MinimumBalanceForRentExemption(ctx, metaplex.MetadataAccountSize)
		if err != nil {
			return out, fmt.Errorf("query metaplex metadata rent: %w", err)
		}
		createFeeBase, err := client.MinimumBalanceForRentExemption(ctx, metaplex.CreateFeeSizeScalar)
		if err != nil {
			return out, fmt.Errorf("query metaplex create fee: %w", err)
		}
		if createFeeBase > math.MaxUint64-metaplex.CreateFeeOffsetLamports {
			return out, errors.New("rent requirement overflows uint64")
		}
		createFee := createFeeBase + metaplex.CreateFeeOffsetLamports
		if metadataAccountRent > math.MaxUint64-createFee {
			return out, errors.New("rent requirement overflows uint64")
		}
		metaplexMetadataRentLamports = metadataAccountRent + createFee
	}

	if mintRentLamports > math.MaxUint64-tokenAccountRentLamports {
		return out, errors.New("rent requirement overflows uint64")
	}
	rentTotal := mintRentLamports + tokenAccountRentLamports
	if metaplexMetadataRentLamports > math.MaxUint64-rentTotal {
		return out, errors.New("rent requirement overflows uint64")
	}
	rentTotal += metaplexMetadataRentLamports
	if rentTotal > math.MaxUint64-minimumFeeReserveLamports {
		return out, errors.New("rent requirement overflows uint64")
	}

	requiredBalance := rentTotal + minimumFeeReserveLamports
	balance, err := client.Balance(ctx, cfg.Payer.PublicKey)
	if err != nil {
		return out, fmt.Errorf("query payer balance: %w", err)
	}
	if balance < requiredBalance {
		return out, fmt.Errorf("payer balance %d is below rent plus reserve %d", balance, requiredBalance)
	}

	submit := func(stage string, signers []svmtest.Signer, instructions ...sdk.Instruction) error {
		signature, submitErr := client.SendInstructions(ctx, cfg.Payer, signers, instructions)
		if submitErr != nil {
			if signature == "" {
				return fmt.Errorf("%s failed before a transaction signature was returned; no retry was attempted: %w", stage, submitErr)
			}
			record := signatureRecord{Stage: stage, Signature: signature}
			out.NonFinalizedSignatures = append(out.NonFinalizedSignatures, record)
			return fmt.Errorf("%s has no finalized proof; no retry was attempted (non-finalized signature %q): %w", stage, signature, submitErr)
		}
		if signature == "" {
			return fmt.Errorf("%s returned no finalized signature", stage)
		}
		record := signatureRecord{Stage: stage, Signature: signature}
		out.SubmittedSignatures = append(out.SubmittedSignatures, record)
		out.FinalizedSignatures = append(out.FinalizedSignatures, record)
		return nil
	}

	if cfg.Atomic {
		if err := runAtomic(ctx, client, cfg, mint, tokenAccountAddress, metaplexMetadataAddress, createSize, mintRentLamports, tokenAccountRentLamports, wantMetadata, submit); err != nil {
			return out, err
		}
	} else if err := runStaged(ctx, client, cfg, mint, tokenAccountAddress, metaplexMetadataAddress, createSize, mintRentLamports, tokenAccountRentLamports, wantMetadata, submit); err != nil {
		return out, err
	}

	out.MintState = mintState{
		Initialized:     true,
		Decimals:        cfg.Decimals,
		SupplyRaw:       exactUint64(cfg.Amount),
		SupplyUI:        formatUIAmount(cfg.Amount, cfg.Decimals),
		MintAuthority:   cfg.Payer.PublicKey.String(),
		FreezeAuthority: "disabled",
	}
	out.TokenAccountState = tokenState{
		Initialized: true,
		Mint:        mint.PublicKey.String(),
		Owner:       cfg.Owner.String(),
		AmountRaw:   exactUint64(cfg.Amount),
		AmountUI:    formatUIAmount(cfg.Amount, cfg.Decimals),
	}
	out.FinalizedAndVerified = true

	return out, nil
}

type submitFunc func(stage string, signers []svmtest.Signer, instructions ...sdk.Instruction) error

// runStaged submits mint creation, associated-token-account creation, and
// mint-to as separate transactions, verifying each one's finalized on-chain
// state before building the next. This costs extra round trips but means a
// dropped or ambiguous transaction leaves a precisely diagnosable partial
// state (see the SubmittedSignatures/NonFinalizedSignatures journal) instead
// of an opaque failure.
func runStaged(
	ctx context.Context,
	client rpcClient,
	cfg config,
	mint svmtest.Signer,
	tokenAccountAddress, metaplexMetadataAddress sdk.Pubkey,
	createSize, mintRentLamports, tokenAccountRentLamports uint64,
	wantMetadata *tokenMetadataExpectation,
	submit submitFunc,
) error {
	// Stage 1: create and initialize the mint (no extensions — see the
	// rent-budget comment above for why metadata is not written here).
	if err := submit("create_and_initialize_mint", []svmtest.Signer{mint},
		system.CreateAccount(cfg.Payer.PublicKey, mint.PublicKey, mintRentLamports, createSize, token2022.ProgramID),
		token2022.InitializeMint2(mint.PublicKey, cfg.Payer.PublicKey, token2022.OptionalPubkey{}, cfg.Decimals),
	); err != nil {
		return err
	}
	wantMint := token2022.Mint{
		MintAuthority: token2022.OptionalPubkey{Set: true, Value: cfg.Payer.PublicKey},
		Decimals:      cfg.Decimals,
		Initialized:   true,
	}
	if err := verifyWithRetry(ctx, "verify finalized mint", func() error {
		return verifyToken2022Mint(ctx, client, mint.PublicKey, mintRentLamports, wantMint)
	}); err != nil {
		return err
	}
	if wantMetadata != nil {
		createMetaplexMetadata, metaplexAddress, err := metaplex.CreateV1(
			mint.PublicKey, cfg.Payer.PublicKey, cfg.Payer.PublicKey, cfg.Payer.PublicKey, token2022.ProgramID, false,
			cfg.Name, cfg.Symbol, cfg.URI, cfg.Decimals, true,
		)
		if err != nil {
			return fmt.Errorf("build metaplex metadata instruction: %w", err)
		}
		if metaplexAddress != metaplexMetadataAddress {
			return errors.New("derived metaplex metadata account mismatch")
		}
		if err := submit("create_metaplex_metadata", nil, createMetaplexMetadata); err != nil {
			return err
		}
		if err := verifyWithRetry(ctx, "verify finalized metaplex metadata", func() error {
			return verifyMetaplexMetadata(ctx, client, metaplexMetadataAddress, mint.PublicKey, cfg.Name, cfg.Symbol)
		}); err != nil {
			return err
		}
	}

	// Stage 2: create the associated token account. CreateIdempotent is a
	// no-op (not an error) if it already exists, matching how every other
	// tool that funds an ATA behaves.
	createATA, ataAddress, err := associatedtoken.CreateIdempotent(cfg.Payer.PublicKey, cfg.Owner, mint.PublicKey, token2022.ProgramID)
	if err != nil {
		return fmt.Errorf("build associated token account instruction: %w", err)
	}
	if ataAddress != tokenAccountAddress {
		return errors.New("derived associated token account mismatch")
	}
	if err := submit("create_and_initialize_account", nil, createATA); err != nil {
		return err
	}
	wantToken := token2022.Account{
		Mint:            mint.PublicKey,
		Owner:           cfg.Owner,
		State:           token2022.AccountInitialized,
		Amount:          0,
		CloseAuthority:  token2022.OptionalPubkey{},
		Delegate:        token2022.OptionalPubkey{},
		DelegatedAmount: 0,
		IsNative:        token2022.OptionalU64{},
	}
	if err := verifyWithRetry(ctx, "verify finalized token account", func() error {
		return verifyToken2022TokenAccount(ctx, client, tokenAccountAddress, tokenAccountRentLamports, mint.PublicKey, cfg.Owner, wantToken)
	}); err != nil {
		return err
	}
	if err := verifyWithRetry(ctx, "recheck mint before mint-to", func() error {
		return verifyToken2022Mint(ctx, client, mint.PublicKey, mintRentLamports, token2022.Mint{
			MintAuthority: token2022.OptionalPubkey{Set: true, Value: cfg.Payer.PublicKey},
			Decimals:      cfg.Decimals,
			Initialized:   true,
			Supply:        0,
		})
	}); err != nil {
		return err
	}

	// Stage 3: mint tokens.
	mintTo, err := token2022.MintTo(mint.PublicKey, tokenAccountAddress, cfg.Payer.PublicKey, nil, cfg.Amount)
	if err != nil {
		return fmt.Errorf("build MintTo instruction: %w", err)
	}
	if err := submit("mint_to", nil, mintTo); err != nil {
		return err
	}
	wantMint.Supply = cfg.Amount
	wantToken.Amount = cfg.Amount
	if err := verifyWithRetry(ctx, "verify finalized mint supply", func() error {
		return verifyToken2022Mint(ctx, client, mint.PublicKey, mintRentLamports, wantMint)
	}); err != nil {
		return err
	}
	if err := verifyWithRetry(ctx, "verify finalized token balance", func() error {
		return verifyToken2022TokenAccount(ctx, client, tokenAccountAddress, tokenAccountRentLamports, mint.PublicKey, cfg.Owner, wantToken)
	}); err != nil {
		return err
	}
	return nil
}

// runAtomic submits mint creation, metadata, associated-token-account
// creation, and mint-to as a single transaction: it either lands as a whole
// or not at all, so there is no partial on-chain state to reason about. The
// trade-off is that the instructions must fit Solana's single-transaction
// size/compute-unit budget, and a mid-stage error offers less detail than
// the staged path's per-transaction journal.
func runAtomic(
	ctx context.Context,
	client rpcClient,
	cfg config,
	mint svmtest.Signer,
	tokenAccountAddress, metaplexMetadataAddress sdk.Pubkey,
	createSize, mintRentLamports, tokenAccountRentLamports uint64,
	wantMetadata *tokenMetadataExpectation,
	submit submitFunc,
) error {
	instructions := []sdk.Instruction{
		system.CreateAccount(cfg.Payer.PublicKey, mint.PublicKey, mintRentLamports, createSize, token2022.ProgramID),
		token2022.InitializeMint2(mint.PublicKey, cfg.Payer.PublicKey, token2022.OptionalPubkey{}, cfg.Decimals),
	}
	if wantMetadata != nil {
		createMetaplexMetadata, metaplexAddress, err := metaplex.CreateV1(
			mint.PublicKey, cfg.Payer.PublicKey, cfg.Payer.PublicKey, cfg.Payer.PublicKey, token2022.ProgramID, false,
			cfg.Name, cfg.Symbol, cfg.URI, cfg.Decimals, true,
		)
		if err != nil {
			return fmt.Errorf("build metaplex metadata instruction: %w", err)
		}
		if metaplexAddress != metaplexMetadataAddress {
			return errors.New("derived metaplex metadata account mismatch")
		}
		instructions = append(instructions, createMetaplexMetadata)
	}
	createATA, ataAddress, err := associatedtoken.CreateIdempotent(cfg.Payer.PublicKey, cfg.Owner, mint.PublicKey, token2022.ProgramID)
	if err != nil {
		return fmt.Errorf("build associated token account instruction: %w", err)
	}
	if ataAddress != tokenAccountAddress {
		return errors.New("derived associated token account mismatch")
	}
	instructions = append(instructions, createATA)
	mintTo, err := token2022.MintTo(mint.PublicKey, tokenAccountAddress, cfg.Payer.PublicKey, nil, cfg.Amount)
	if err != nil {
		return fmt.Errorf("build MintTo instruction: %w", err)
	}
	instructions = append(instructions, mintTo)

	if err := submit("atomic_create_mint_ata_and_mint_to", []svmtest.Signer{mint}, instructions...); err != nil {
		return err
	}

	wantMint := token2022.Mint{
		MintAuthority: token2022.OptionalPubkey{Set: true, Value: cfg.Payer.PublicKey},
		Decimals:      cfg.Decimals,
		Initialized:   true,
		Supply:        cfg.Amount,
	}
	if err := verifyWithRetry(ctx, "verify finalized mint", func() error {
		return verifyToken2022Mint(ctx, client, mint.PublicKey, mintRentLamports, wantMint)
	}); err != nil {
		return err
	}
	if wantMetadata != nil {
		if err := verifyWithRetry(ctx, "verify finalized metaplex metadata", func() error {
			return verifyMetaplexMetadata(ctx, client, metaplexMetadataAddress, mint.PublicKey, cfg.Name, cfg.Symbol)
		}); err != nil {
			return err
		}
	}
	wantToken := token2022.Account{
		Mint:   mint.PublicKey,
		Owner:  cfg.Owner,
		State:  token2022.AccountInitialized,
		Amount: cfg.Amount,
	}
	if err := verifyWithRetry(ctx, "verify finalized token account", func() error {
		return verifyToken2022TokenAccount(ctx, client, tokenAccountAddress, tokenAccountRentLamports, mint.PublicKey, cfg.Owner, wantToken)
	}); err != nil {
		return err
	}
	return nil
}

func newMintSignerWithVanity(newSigner func() (svmtest.Signer, error), vanitySuffix string) (svmtest.Signer, int, error) {
	if vanitySuffix == "" {
		mint, err := newSigner()
		return mint, 0, err
	}
	for attempt := 1; attempt <= defaultVanityMaxAttempts; attempt++ {
		mint, err := newSigner()
		if err != nil {
			return svmtest.Signer{}, attempt - 1, fmt.Errorf("generate in-memory mint signer: %w", err)
		}
		if strings.HasSuffix(mint.PublicKey.String(), vanitySuffix) {
			return mint, attempt, nil
		}
	}
	return svmtest.Signer{}, defaultVanityMaxAttempts, fmt.Errorf("could not find mint with suffix %q after %d attempts", vanitySuffix, defaultVanityMaxAttempts)
}

func verifyToken2022Mint(
	ctx context.Context,
	client rpcClient,
	mint sdk.Pubkey,
	rentLamports uint64,
	want token2022.Mint,
) error {
	account, err := client.GetAccountInfo(ctx, mint)
	if err != nil {
		return err
	}
	if account == nil {
		return errors.New("mint account not found")
	}
	if account.Executable {
		return errors.New("mint account is unexpectedly executable")
	}
	if account.Owner != token2022.ProgramID.String() {
		return fmt.Errorf("mint owner %q, want %q", account.Owner, token2022.ProgramID)
	}
	if account.Lamports < rentLamports {
		return fmt.Errorf("mint lamports %d below rent requirement %d", account.Lamports, rentLamports)
	}
	raw, err := decodeAccountData(account)
	if err != nil {
		return err
	}
	gotWithExtensions, err := token2022.DecodeMintWithExtensions(raw)
	if err != nil {
		return err
	}
	got := gotWithExtensions.Base
	if got.Initialized != want.Initialized {
		return fmt.Errorf("mint initialized=%v, want %v", got.Initialized, want.Initialized)
	}
	if got.Decimals != want.Decimals {
		return fmt.Errorf("mint decimals %d, want %d", got.Decimals, want.Decimals)
	}
	if got.MintAuthority != want.MintAuthority {
		return fmt.Errorf("mint authority mismatch: got %q want %q", formatOptionalPubkey(got.MintAuthority), formatOptionalPubkey(want.MintAuthority))
	}
	if got.FreezeAuthority != want.FreezeAuthority {
		return fmt.Errorf("mint freeze authority mismatch: got %q want %q", formatOptionalPubkey(got.FreezeAuthority), formatOptionalPubkey(want.FreezeAuthority))
	}
	if got.Supply != want.Supply {
		return fmt.Errorf("mint supply %d, want %d", got.Supply, want.Supply)
	}
	canonical, err := token2022.EncodeMintWithExtensions(gotWithExtensions)
	if err != nil {
		return fmt.Errorf("re-encode mint: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return errors.New("mint data is not byte-for-byte canonical")
	}
	return nil
}

func verifyToken2022TokenAccount(ctx context.Context, client rpcClient, tokenAccount sdk.Pubkey, rentLamports uint64, mint, owner sdk.Pubkey, want token2022.Account) error {
	account, err := client.GetAccountInfo(ctx, tokenAccount)
	if err != nil {
		return err
	}
	if account == nil {
		return errors.New("token account not found")
	}
	if account.Executable {
		return errors.New("token account is unexpectedly executable")
	}
	if account.Owner != token2022.ProgramID.String() {
		return fmt.Errorf("token account owner %q, want %q", account.Owner, token2022.ProgramID)
	}
	if account.Lamports < rentLamports {
		return fmt.Errorf("token account lamports %d below rent requirement %d", account.Lamports, rentLamports)
	}
	raw, err := decodeAccountData(account)
	if err != nil {
		return err
	}
	gotWithExtensions, err := token2022.DecodeAccountWithExtensions(raw)
	if err != nil {
		return err
	}
	got := gotWithExtensions.Base
	hasImmutableOwner := false
	for _, extension := range gotWithExtensions.Extensions {
		if extension.Type == token2022.ExtensionImmutableOwner {
			hasImmutableOwner = true
		}
	}
	if !hasImmutableOwner {
		return errors.New("associated token account missing immutable-owner extension")
	}
	if got.State != want.State {
		return fmt.Errorf("token account state %d, want %d", got.State, want.State)
	}
	if got.Mint != mint {
		return fmt.Errorf("token account mint %s, want %s", got.Mint, mint)
	}
	if got.Owner != owner {
		return fmt.Errorf("token account owner %s, want %s", got.Owner, owner)
	}
	if got.Amount != want.Amount {
		return fmt.Errorf("token account amount %d, want %d", got.Amount, want.Amount)
	}
	if got.CloseAuthority != want.CloseAuthority {
		return fmt.Errorf("token account close authority mismatch: got %q want %q", formatOptionalPubkey(got.CloseAuthority), formatOptionalPubkey(want.CloseAuthority))
	}
	if got.Delegate != want.Delegate {
		return fmt.Errorf("token account delegate mismatch: got %q want %q", formatOptionalPubkey(got.Delegate), formatOptionalPubkey(want.Delegate))
	}
	if got.IsNative != want.IsNative {
		return fmt.Errorf("token account is_native mismatch: got %s want %s", formatOptionalU64(got.IsNative), formatOptionalU64(want.IsNative))
	}
	if got.DelegatedAmount != want.DelegatedAmount {
		return fmt.Errorf("token account delegated amount %d, want %d", got.DelegatedAmount, want.DelegatedAmount)
	}
	canonical, err := token2022.EncodeAccountWithExtensions(gotWithExtensions)
	if err != nil {
		return fmt.Errorf("re-encode token account: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return errors.New("token account data is not byte-for-byte canonical")
	}
	return nil
}

// verifyMetaplexMetadata checks only the fields this tool writes (mint,
// name, symbol). It does not model the full mpl-token-metadata account
// layout (creators, collection, uses, and later-added optional fields all
// vary by program version), so unlike the byte-canonical Token-2022 checks
// above it cannot assert the account is fully canonical — only that our
// write took effect.
func verifyMetaplexMetadata(ctx context.Context, client rpcClient, metadataAccount, mint sdk.Pubkey, wantName, wantSymbol string) error {
	account, err := client.GetAccountInfo(ctx, metadataAccount)
	if err != nil {
		return err
	}
	if account == nil {
		return errors.New("metaplex metadata account not found")
	}
	if account.Owner != metaplex.ProgramID.String() {
		return fmt.Errorf("metaplex metadata account owner %q, want %q", account.Owner, metaplex.ProgramID)
	}
	raw, err := decodeAccountData(account)
	if err != nil {
		return err
	}
	const keyAndAuthoritySize = 1 + sdk.PubkeySize // key discriminator + update_authority
	if len(raw) < keyAndAuthoritySize+sdk.PubkeySize {
		return errors.New("metaplex metadata account too short")
	}
	gotMint, err := sdk.PubkeyFromBytes(raw[keyAndAuthoritySize : keyAndAuthoritySize+sdk.PubkeySize])
	if err != nil {
		return err
	}
	if gotMint != mint {
		return fmt.Errorf("metaplex metadata mint %s, want %s", gotMint, mint)
	}
	offset := keyAndAuthoritySize + sdk.PubkeySize
	gotName, offset, err := readBorshString(raw, offset)
	if err != nil {
		return fmt.Errorf("decode metaplex metadata name: %w", err)
	}
	gotSymbol, _, err := readBorshString(raw, offset)
	if err != nil {
		return fmt.Errorf("decode metaplex metadata symbol: %w", err)
	}
	// The program right-pads name/symbol/uri with null bytes out to their
	// max length ("puffing", state::puff_out_data_fields) on every write,
	// including Create — the stored Borsh string length reflects the padded
	// length, not the value we supplied, so trailing NULs are expected.
	gotName = strings.TrimRight(gotName, "\x00")
	gotSymbol = strings.TrimRight(gotSymbol, "\x00")
	if gotName != wantName {
		return fmt.Errorf("metaplex metadata name %q, want %q", gotName, wantName)
	}
	if gotSymbol != wantSymbol {
		return fmt.Errorf("metaplex metadata symbol %q, want %q", gotSymbol, wantSymbol)
	}
	return nil
}

func decodeAccountData(info *svmtest.AccountInfo) ([]byte, error) {
	if info == nil {
		return nil, errors.New("nil account info")
	}
	data, err := info.DataBytes()
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("empty account data")
	}
	return data, nil
}

type tokenMetadataExpectation struct {
	UpdateAuthority sdk.Pubkey
	Mint            sdk.Pubkey
	Name            string
	Symbol          string
	URI             string
}

func buildExpectedMetadata(cfg config, mint sdk.Pubkey) *tokenMetadataExpectation {
	if cfg.Name == "" && cfg.Symbol == "" && cfg.URI == "" {
		return nil
	}
	return &tokenMetadataExpectation{
		UpdateAuthority: cfg.Payer.PublicKey,
		Mint:            mint,
		Name:            cfg.Name,
		Symbol:          cfg.Symbol,
		URI:             cfg.URI,
	}
}

func readBorshString(data []byte, offset int) (string, int, error) {
	if offset+4 > len(data) {
		return "", 0, errors.New("borsh string missing prefix")
	}
	length := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
	offset += 4
	if offset+length > len(data) {
		return "", 0, errors.New("borsh string truncated")
	}
	return string(data[offset : offset+length]), offset + length, nil
}

func parseUIAmount(text string, decimals uint8) (uint64, error) {
	if strings.TrimSpace(text) != text {
		return 0, errors.New("amount must contain an unsigned plain decimal without surrounding whitespace")
	}
	if text == "" {
		return 0, errors.New("amount must contain at least one ASCII decimal digit")
	}
	if strings.HasPrefix(text, "+") {
		return 0, errors.New("amount may not start with plus sign")
	}
	parts := strings.Split(text, ".")
	if len(parts) > 2 {
		return 0, errors.New("amount must use plain decimal notation")
	}
	if len(parts[0]) == 0 {
		return 0, errors.New("amount must contain at least one digit before decimal point")
	}
	if len(parts) == 2 && parts[1] == "" {
		return 0, errors.New("amount must contain at least one digit after decimal point")
	}
	for _, part := range parts {
		if part == "" {
			continue
		}
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return 0, errors.New("amount must contain only ASCII decimal digits")
		}
	}
	if len(parts) == 2 && len(parts[1]) > int(decimals) {
		return 0, fmt.Errorf("amount has %d fractional digits, but decimals is %d", len(parts[1]), decimals)
	}
	digits := parts[0]
	if len(parts) == 2 {
		digits += parts[1]
		if pad := int(decimals) - len(parts[1]); pad > 0 {
			digits += strings.Repeat("0", pad)
		}
	} else if decimals > 0 {
		digits += strings.Repeat("0", int(decimals))
	}
	result, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, errors.New("amount does not fit uint64 raw units")
	}
	return result, nil
}

func formatUIAmount(raw uint64, decimals uint8) string {
	digits := strconv.FormatUint(raw, 10)
	if decimals == 0 {
		return digits
	}
	decimalCount := int(decimals)
	if len(digits) <= decimalCount {
		digits = strings.Repeat("0", decimalCount-len(digits)+1) + digits
	}
	cut := len(digits) - decimalCount
	fraction := strings.TrimRight(digits[cut:], "0")
	if fraction == "" {
		return digits[:cut]
	}
	return digits[:cut] + "." + fraction
}

func formatOptionalPubkey(value token2022.OptionalPubkey) string {
	if !value.Set {
		return "(none)"
	}
	return value.Value.String()
}

func formatOptionalU64(value token2022.OptionalU64) string {
	if !value.Set {
		return "(none)"
	}
	return strconv.FormatUint(value.Value, 10)
}

func loopbackRPC(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func normalizeRPCURL(raw string) (string, error) {
	switch strings.ToLower(raw) {
	case "l", "localhost":
		return "http://127.0.0.1:8899", nil
	case "d", "devnet":
		return "https://api.devnet.solana.com", nil
	case "t", "testnet":
		return "https://api.testnet.solana.com", nil
	case "m", "mainnet", "mainnet-beta":
		return "https://api.mainnet-beta.solana.com", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid RPC URL %q", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("RPC URL must use http or https")
	}
	return raw, nil
}
