package main

import (
	"bytes"
	"crypto/sha256"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	gosoldeploy "github.com/ersany/go-solana/deploy"
	"github.com/ersany/go-solana/sdk"
	"github.com/ersany/go-solana/sdk/system"
	"github.com/ersany/go-solana/sdk/token2022"
	"github.com/ersany/go-solana/svmtest"
)

const minimumFeeReserveLamports = uint64(100_000)
const verifyRetryCount = 8
const defaultVanityMaxAttempts = 50000
const raydiumDevnetTransactionEndpoint = "https://transaction-v1-devnet.raydium.io"
const addLiquidityCandidatePath = "/transaction/add-liquidity"
const addLiquidityCandidatePathV2 = "/transaction/add-liquidity-v2"
var associatedTokenProgram = sdk.MustParsePubkey("ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL")
var defaultMultisendRecipients = []string{
	"12reyx6vYapGAjoNohg4mwRjqykzUjKpDoGssWGwtj4j",
	"8psNvWTrdNTiVRNzAgsou9kETXNJm2SXZyaKuJraVRtf",
	"9UnZnrFJ1CXCmCorgGU9NvYkX5np1h4v4ympx3Nrdw3v",
	"Ee5psttzQsFjKngy3tiuiLjobxqXaUChYfj4EXtCQMGv",
	"7nV8REk2mBVHXYJNfEJumkSLWhJhQdUtW953Whp3YYX",
}

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
	Payer              svmtest.Signer
	Owner              sdk.Pubkey
	Name               string
	Symbol             string
	URI                string
	Vanity             string
	Decimals           uint8
	Amount             uint64
	MultisendRecipients []sdk.Pubkey
	AddRaydiumLiquidity bool
	RPCURL              string
	RaydiumEndpoint     string
	RaydiumAmountLamports uint64
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

type raydiumLiquidityState struct {
	Requested      bool        `json:"requested"`
	Enabled        bool        `json:"enabled"`
	AmountLamports exactUint64 `json:"amount_lamports"`
	Endpoint       string      `json:"endpoint"`
	Error          string      `json:"error,omitempty"`
}

type result struct {
	GeneratedAtUTC           string            `json:"generated_at_utc"`
	GenesisHash              string            `json:"genesis_hash"`
	TokenProgram             string            `json:"token_program"`
	FeePayer                 string            `json:"fee_payer"`
	Mint                     string            `json:"mint"`
	TokenAccount             string            `json:"token_account"`
	Owner                    string            `json:"owner"`
	Decimals                 uint8             `json:"decimals"`
	AmountRaw                exactUint64       `json:"amount_raw"`
	AmountUI                 string            `json:"amount_ui"`
	MintRentLamports         exactUint64       `json:"mint_rent_lamports"`
	TokenAccountRentLamports exactUint64       `json:"token_account_rent_lamports"`
	RaydiumLiquidity         raydiumLiquidityState `json:"raydium_liquidity"`
	SubmittedSignatures      []signatureRecord `json:"submitted_signatures"`
	NonFinalizedSignatures   []signatureRecord `json:"non_finalized_signatures"`
	FinalizedSignatures      []signatureRecord `json:"finalized_signatures"`
	MintState                mintState         `json:"mint_state"`
	TokenAccountState        tokenState        `json:"token_account_state"`
	FinalizedAndVerified     bool              `json:"finalized_and_verified"`
}

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "token2022-init-multisend:", err)
		os.Exit(1)
	}
}

func runCLI(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("token2022-init-multisend", flag.ContinueOnError)
	flags.SetOutput(stderr)

	keypairPath := flags.String("keypair", "", "fee payer and mint authority keypair (required)")
	rpcURL := flags.String("url", "localhost", "Solana RPC URL or localhost/devnet/testnet/mainnet-beta")
	decimals := flags.Uint("decimals", 6, "mint decimal places (0..255; default 6)")
	name := flags.String("name", "DEMODEMO", "token name (default DEMODEMO)")
	symbol := flags.String("symbol", "DEMO", "token symbol (default DEMO)")
	uri := flags.String("uri", "", "metadata URI (optional)")
	vanitySuffix := flags.String("vanity-suffix", "", "require generated mint address to end with this string (optional)")
	multisendRecipientsText := flags.String("multisend-recipients", strings.Join(defaultMultisendRecipients, ","), "comma-separated recipient wallets for automatic multisend")
	ownerText := flags.String("owner", "", "token account owner public key (default fee payer)")
	allowLive := flags.Bool("allow-live", false, "explicitly permit a non-loopback RPC endpoint")
	addRaydiumLiquidity := flags.Bool("add-raydium-liquidity", true, "attempt to add liquidity on Raydium after token initialization (devnet only)")
	raydiumEndpoint := flags.String("raydium-liquidity-endpoint", raydiumDevnetTransactionEndpoint, "base Raydium Transaction API endpoint")
	raydiumAmountSOL := flags.String("raydium-liquidity-amount-sol", "0.5", "initial SOL amount for Raydium liquidity")
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
	raydiumAmountLamports, err := parseUIAmount(*raydiumAmountSOL, token2022.NativeMintDecimals)
	if err != nil {
		return fmt.Errorf("invalid --raydium-liquidity-amount-sol: %w", err)
	}
	if *addRaydiumLiquidity && raydiumAmountLamports == 0 {
		return errors.New("raydium-liquidity-amount-sol must be greater than 0")
	}
	if !*addRaydiumLiquidity {
		raydiumAmountLamports = 0
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
	if *addRaydiumLiquidity && !isDevnetRPCURL(resolvedURL) {
		return errors.New("raydium liquidity integration in this command is currently limited to devnet; use --url devnet")
	}
	if *addRaydiumLiquidity && !isDevnetRPCURL(*raydiumEndpoint) {
		return errors.New("raydium-liquidity-endpoint must be a Raydium devnet endpoint")
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
	recipients, err := parseRecipientList(*multisendRecipientsText)
	if err != nil {
		return fmt.Errorf("parse --multisend-recipients: %w", err)
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
		RaydiumLiquidity:       raydiumLiquidityState{Requested: *addRaydiumLiquidity, Enabled: false, AmountLamports: exactUint64(raydiumAmountLamports), Endpoint: *raydiumEndpoint},
		SubmittedSignatures:    make([]signatureRecord, 0, 3),
		NonFinalizedSignatures: make([]signatureRecord, 0, 3),
		FinalizedSignatures:    make([]signatureRecord, 0, 3),
	}

	createdClient := svmtest.Client{URL: resolvedURL}
	resultState, err := initializeToken2022(ctx, createdClient, config{
		Payer:               payer,
		Owner:               owner,
		Name:                *name,
		Symbol:              *symbol,
		URI:                 *uri,
		Vanity:              *vanitySuffix,
		Decimals:            uint8(*decimals),
		Amount:              raw,
		MultisendRecipients: recipients,
		AddRaydiumLiquidity: *addRaydiumLiquidity,
		RPCURL:              resolvedURL,
		RaydiumEndpoint:     *raydiumEndpoint,
		RaydiumAmountLamports: raydiumAmountLamports,
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
	if len(cfg.MultisendRecipients) > 0 && cfg.Owner != cfg.Payer.PublicKey {
		return out, errors.New("multisend recipient mode requires token account owner to equal fee payer")
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
	out.RaydiumLiquidity = raydiumLiquidityState{
		Requested:      cfg.AddRaydiumLiquidity,
		Enabled:        false,
		AmountLamports: exactUint64(cfg.RaydiumAmountLamports),
		Endpoint:       cfg.RaydiumEndpoint,
	}
	baseSignatureCapacity := 3 + len(cfg.MultisendRecipients)*3
	if cfg.AddRaydiumLiquidity {
		baseSignatureCapacity++
	}
	nonFinalizedCap := 1
	if len(cfg.MultisendRecipients)*2 > nonFinalizedCap {
		nonFinalizedCap = len(cfg.MultisendRecipients) * 2
	}
	out.SubmittedSignatures = make([]signatureRecord, 0, baseSignatureCapacity)
	out.NonFinalizedSignatures = make([]signatureRecord, 0, nonFinalizedCap)
	out.FinalizedSignatures = make([]signatureRecord, 0, baseSignatureCapacity)

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
	if mint.PublicKey == cfg.Owner {
		return out, errors.New("generated signer collision with owner")
	}

	ownerTokenAccount, _, err := sdk.FindProgramAddress(
		[][]byte{cfg.Owner[:], token2022.ProgramID[:], mint.PublicKey[:]},
		associatedTokenProgram,
	)
	if err != nil {
		return out, fmt.Errorf("derive owner token account: %w", err)
	}
	ownerTokenAccountInfo, err := client.GetAccountInfo(ctx, ownerTokenAccount)
	if err != nil {
		return out, fmt.Errorf("check owner token account %s: %w", ownerTokenAccount, err)
	}
	var existingSourceTokenAmount uint64
	needsCreateOwnerTokenAccount := ownerTokenAccountInfo == nil
	if ownerTokenAccountInfo != nil {
		if ownerTokenAccountInfo.Owner != token2022.ProgramID.String() {
			return out, fmt.Errorf("owner token account %s is owned by %s, expected %s", ownerTokenAccount, ownerTokenAccountInfo.Owner, token2022.ProgramID)
		}
		rawOwnerToken, err := decodeAccountData(ownerTokenAccountInfo)
		if err != nil {
			return out, fmt.Errorf("read owner token account %s: %w", ownerTokenAccount, err)
		}
		ownerTokenAccountState, err := token2022.DecodeAccount(rawOwnerToken)
		if err != nil {
			return out, fmt.Errorf("decode owner token account %s: %w", ownerTokenAccount, err)
		}
		if ownerTokenAccountState.State != token2022.AccountInitialized {
			return out, fmt.Errorf("owner token account %s is uninitialized", ownerTokenAccount)
		}
		if ownerTokenAccountState.Mint != mint.PublicKey {
			return out, fmt.Errorf("owner token account %s has wrong mint %s, want %s", ownerTokenAccount, ownerTokenAccountState.Mint, mint.PublicKey)
		}
		if ownerTokenAccountState.Owner != cfg.Owner {
			return out, fmt.Errorf("owner token account %s has wrong owner %s, want %s", ownerTokenAccount, ownerTokenAccountState.Owner, cfg.Owner)
		}
		existingSourceTokenAmount = ownerTokenAccountState.Amount
	}

	out.Mint = mint.PublicKey.String()
	out.TokenAccount = ownerTokenAccount.String()
	wantMetadata := buildExpectedMetadata(cfg, mint.PublicKey)

	for _, generated := range []sdk.Pubkey{mint.PublicKey} {
		exists, err := client.GetAccountInfo(ctx, generated)
		if err != nil {
			return out, fmt.Errorf("check generated address %s: %w", generated, err)
		}
		if exists != nil {
			return out, fmt.Errorf("generated address %s already exists", generated)
		}
	}

	createSize, mintRentSize, err := mintAccountSize(cfg.Name, cfg.Symbol, cfg.URI)
	if err != nil {
		return out, fmt.Errorf("calculate mint account sizes: %w", err)
	}
	mintRentLamports, err := client.MinimumBalanceForRentExemption(ctx, mintRentSize)
	if err != nil {
		return out, fmt.Errorf("query mint rent: %w", err)
	}
	tokenAccountRentLamports, err := client.MinimumBalanceForRentExemption(ctx, token2022.AccountSize)
	if err != nil {
		return out, fmt.Errorf("query token account rent: %w", err)
	}
	out.MintRentLamports = exactUint64(mintRentLamports)
	out.TokenAccountRentLamports = exactUint64(tokenAccountRentLamports)

	ownerTokenAccountRentLamports := uint64(0)
	if needsCreateOwnerTokenAccount {
		ownerTokenAccountRentLamports = tokenAccountRentLamports
	}
	if mintRentLamports > math.MaxUint64-ownerTokenAccountRentLamports {
		return out, errors.New("rent requirement overflows uint64")
	}
	rentTotal := mintRentLamports + ownerTokenAccountRentLamports
	if rentTotal > math.MaxUint64-minimumFeeReserveLamports {
		return out, errors.New("rent requirement overflows uint64")
	}
	if cfg.AddRaydiumLiquidity && cfg.RaydiumAmountLamports > 0 {
		if rentTotal+minimumFeeReserveLamports > math.MaxUint64-cfg.RaydiumAmountLamports {
			return out, errors.New("required balance requirement overflows uint64")
		}
		rentTotal += cfg.RaydiumAmountLamports
	}
	recipientRentLamports := uint64(len(cfg.MultisendRecipients)) * tokenAccountRentLamports
	if len(cfg.MultisendRecipients) > 0 && tokenAccountRentLamports != 0 && recipientRentLamports/tokenAccountRentLamports != uint64(len(cfg.MultisendRecipients)) {
		return out, errors.New("recipient rent requirement overflows uint64")
	}
	requiredBalance := rentTotal + minimumFeeReserveLamports
	if requiredBalance > math.MaxUint64-recipientRentLamports {
		return out, errors.New("recipient rent requirement overflows uint64")
	}
	requiredBalance += recipientRentLamports
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

	// Stage 1: create mint, pre-initialize the metadata-pointer extension (which
	// must be set before InitializeMint locks the account layout), then initialize
	// the base mint.
	stage1Instructions := []sdk.Instruction{
		system.CreateAccount(cfg.Payer.PublicKey, mint.PublicKey, mintRentLamports, createSize, token2022.ProgramID),
	}
	if wantMetadata != nil {
		stage1Instructions = append(stage1Instructions, initializeMetadataPointerInstruction(mint.PublicKey, cfg.Payer.PublicKey, mint.PublicKey))
	}
	stage1Instructions = append(stage1Instructions, token2022.InitializeMint2(mint.PublicKey, cfg.Payer.PublicKey, token2022.OptionalPubkey{}, cfg.Decimals))
	if err := submit("create_and_initialize_mint", []svmtest.Signer{mint}, stage1Instructions...); err != nil {
		return out, err
	}
	wantMint := token2022.Mint{
		MintAuthority: token2022.OptionalPubkey{Set: true, Value: cfg.Payer.PublicKey},
		Decimals:      cfg.Decimals,
		Initialized:   true,
	}
	if err := verifyWithRetry(ctx, "verify finalized mint", func() error {
		return verifyToken2022Mint(ctx, client, mint.PublicKey, uint64(out.MintRentLamports), wantMint, nil)
	}); err != nil {
		return out, err
	}
	if wantMetadata != nil {
		initializeTokenMetadataInstruction, err := buildInitializeTokenMetadataInstruction(
			mint.PublicKey,
			cfg.Payer.PublicKey,
			cfg.Payer.PublicKey,
			cfg.Name,
			cfg.Symbol,
			cfg.URI,
		)
		if err != nil {
			return out, fmt.Errorf("build token metadata instruction: %w", err)
		}
		if err := submit("initialize_metadata", nil, initializeTokenMetadataInstruction); err != nil {
			return out, err
		}
		if err := verifyWithRetry(ctx, "verify finalized metadata", func() error {
			return verifyToken2022Mint(ctx, client, mint.PublicKey, uint64(out.MintRentLamports), wantMint, wantMetadata)
		}); err != nil {
			return out, err
		}
	}

	// Stage 2: create token account and initialize owner.
	if needsCreateOwnerTokenAccount {
		if err := submit("create_and_initialize_account", nil, createAssociatedTokenAccountInstruction(
			cfg.Payer.PublicKey,
			ownerTokenAccount,
			cfg.Owner,
			mint.PublicKey,
			token2022.ProgramID,
		)); err != nil {
			return out, err
		}
	}
	wantToken := token2022.Account{
		Mint:            mint.PublicKey,
		Owner:           cfg.Owner,
		State:           token2022.AccountInitialized,
		Amount:          existingSourceTokenAmount,
		CloseAuthority:  token2022.OptionalPubkey{},
		Delegate:        token2022.OptionalPubkey{},
		DelegatedAmount: 0,
		IsNative:        token2022.OptionalU64{},
	}
	if err := verifyWithRetry(ctx, "verify finalized token account", func() error {
		return verifyToken2022TokenAccount(ctx, client, ownerTokenAccount, tokenAccountRentLamports, mint.PublicKey, cfg.Owner, wantToken)
	}); err != nil {
		return out, err
	}
	if err := verifyWithRetry(ctx, "recheck mint before mint-to", func() error {
		return verifyToken2022Mint(ctx, client, mint.PublicKey, uint64(out.MintRentLamports), token2022.Mint{
		MintAuthority: token2022.OptionalPubkey{Set: true, Value: cfg.Payer.PublicKey},
		Decimals:      cfg.Decimals,
		Initialized:   true,
		Supply:        0,
			}, wantMetadata)
	}); err != nil {
		return out, err
	}

	// Stage 3: mint tokens.
	mintTo, err := token2022.MintTo(mint.PublicKey, ownerTokenAccount, cfg.Payer.PublicKey, nil, cfg.Amount)
	if err != nil {
		return out, fmt.Errorf("build MintTo instruction: %w", err)
	}
	if err := submit("mint_to", nil, mintTo); err != nil {
		return out, err
	}
	wantMint.Supply = cfg.Amount
	wantToken.Amount = existingSourceTokenAmount + cfg.Amount
	if err := verifyWithRetry(ctx, "verify finalized mint supply", func() error {
		return verifyToken2022Mint(ctx, client, mint.PublicKey, uint64(out.MintRentLamports), wantMint, wantMetadata)
	}); err != nil {
		return out, err
	}
	if err := verifyWithRetry(ctx, "verify finalized token balance", func() error {
		return verifyToken2022TokenAccount(ctx, client, ownerTokenAccount, tokenAccountRentLamports, mint.PublicKey, cfg.Owner, wantToken)
	}); err != nil {
		return out, err
	}
	remainingSourceAmount, err := multisendToRecipients(
		ctx,
		cfg,
		client,
		mint.PublicKey,
		ownerTokenAccount,
		cfg.Owner,
		tokenAccountRentLamports,
		existingSourceTokenAmount+cfg.Amount,
		submit,
		newSigner,
		verifyWithRetry,
	)
	if err != nil {
		return out, err
	}
	if cfg.AddRaydiumLiquidity {
		if err := configureRaydiumLiquidityOnDevnet(ctx, cfg); err != nil {
			out.RaydiumLiquidity.Error = err.Error()
			return out, err
		}
		out.RaydiumLiquidity.Enabled = true
	}
	wantToken.Amount = remainingSourceAmount
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
		AmountRaw:   exactUint64(wantToken.Amount),
		AmountUI:    formatUIAmount(wantToken.Amount, cfg.Decimals),
	}
	out.FinalizedAndVerified = true

	return out, nil
}

func configureRaydiumLiquidityOnDevnet(ctx context.Context, cfg config) error {
	if cfg.RaydiumEndpoint == "" {
		return errors.New("raydium-liquidity-endpoint is required when --add-raydium-liquidity is enabled")
	}
	if cfg.RaydiumAmountLamports == 0 {
		return errors.New("raydium-liquidity-amount must be greater than 0")
	}
	if !isDevnetRPCURL(cfg.RPCURL) {
		return fmt.Errorf("raydium liquidity integration in this command is limited to devnet RPC endpoints; got %q", cfg.RPCURL)
	}
	base := strings.TrimRight(cfg.RaydiumEndpoint, "/")
	if err := probeRaydiumAddLiquidityEndpoint(ctx, base); err != nil {
		return err
	}
	return nil
}

func probeRaydiumAddLiquidityEndpoint(ctx context.Context, baseURL string) error {
	candidates := []string{
		baseURL + addLiquidityCandidatePath,
		baseURL + addLiquidityCandidatePathV2,
	}
	for _, candidate := range candidates {
		status, body, err := doRaydiumHTTP(ctx, http.MethodPost, candidate, []byte("{}"))
		if err != nil {
			return fmt.Errorf("raydium add-liquidity probe failed for %q: %w", candidate, err)
		}
		if status == http.StatusNotFound {
			continue
		}
		if status >= 200 && status < 300 {
			return fmt.Errorf("raydium add-liquidity endpoint exists at %q but expected request shape is not implemented: %s", candidate, strings.TrimSpace(body))
		}
		return fmt.Errorf("raydium add-liquidity endpoint at %q returned HTTP %d", candidate, status)
	}
	return errors.New("raydium public devnet transaction API does not expose a supported add-liquidity endpoint; cannot auto-create liquidity")
}

func doRaydiumHTTP(ctx context.Context, method, endpoint string, payload []byte) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, "", fmt.Errorf("build raydium request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: 12 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("execute raydium request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("read raydium response: %w", err)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		text = "(empty response)"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, text, fmt.Errorf("http %d: %s", resp.StatusCode, text)
	}
	return resp.StatusCode, text, nil
}

func parseRecipientList(raw string) ([]sdk.Pubkey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	recipients := make([]sdk.Pubkey, 0, len(parts))
	for idx, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		recipient, err := sdk.ParsePubkey(part)
		if err != nil {
			return nil, fmt.Errorf("recipient %d (%q): %w", idx, part, err)
		}
		recipients = append(recipients, recipient)
	}
	return recipients, nil
}

func splitAmountForRecipients(total uint64, recipientCount int) (uint64, uint64, error) {
	if recipientCount < 0 {
		return 0, 0, errors.New("recipient count must be non-negative")
	}
	if recipientCount == 0 {
		return 0, total, nil
	}
	count := uint64(recipientCount)
	return total / count, total % count, nil
}

func multisendToRecipients(
	ctx context.Context,
	cfg config,
	client rpcClient,
	mint sdk.Pubkey,
	sourceTokenAccount sdk.Pubkey,
	sourceOwner sdk.Pubkey,
	tokenAccountRentLamports uint64,
	sourceInitialAmount uint64,
	submit func(stage string, signers []svmtest.Signer, instructions ...sdk.Instruction) error,
	newSigner func() (svmtest.Signer, error),
	verifyWithRetry func(context.Context, string, func() error) error,
) (uint64, error) {
	if len(cfg.MultisendRecipients) == 0 {
		return sourceInitialAmount, nil
	}
	perRecipient, remainder, err := splitAmountForRecipients(cfg.Amount, len(cfg.MultisendRecipients))
	if err != nil {
		return 0, err
	}
	if perRecipient == 0 && remainder == 0 {
		return sourceInitialAmount, nil
	}

	recipientTokenAmount := make([]uint64, len(cfg.MultisendRecipients))
	for i := range recipientTokenAmount {
		recipientTokenAmount[i] = perRecipient
	}
	recipientTokenAmount[0] += remainder

	remainingAmount := sourceInitialAmount
	for i, recipient := range cfg.MultisendRecipients {
		transferAmount := recipientTokenAmount[i]
		tokenAccount, err := newSigner()
		if err != nil {
			return 0, fmt.Errorf("generate token account for multisend recipient %s: %w", recipient, err)
		}
		if err := svmtest.ValidateSigner(tokenAccount); err != nil {
			return 0, fmt.Errorf("generated recipient token account: %w", err)
		}
		if err := submit(fmt.Sprintf("multisend_%d_create_account", i), []svmtest.Signer{tokenAccount},
			system.CreateAccount(cfg.Payer.PublicKey, tokenAccount.PublicKey, tokenAccountRentLamports, token2022.AccountSize, token2022.ProgramID),
			token2022.InitializeAccount2(tokenAccount.PublicKey, mint, recipient),
		); err != nil {
			return 0, err
		}
		if err := verifyWithRetry(ctx, fmt.Sprintf("verify multisend_%d_token_account_after_create", i), func() error {
			return verifyToken2022TokenAccount(ctx, client, tokenAccount.PublicKey, tokenAccountRentLamports, mint, recipient, token2022.Account{
				Mint:            mint,
				Owner:           recipient,
				State:           token2022.AccountInitialized,
				Amount:          0,
				CloseAuthority:  token2022.OptionalPubkey{},
				Delegate:        token2022.OptionalPubkey{},
				DelegatedAmount: 0,
				IsNative:        token2022.OptionalU64{},
			})
		}); err != nil {
			return 0, err
		}
		if transferAmount == 0 {
			continue
		}
		transfer, err := token2022.Transfer(sourceTokenAccount, tokenAccount.PublicKey, sourceOwner, nil, transferAmount)
		if err != nil {
			return 0, fmt.Errorf("build multisend transfer %d: %w", i, err)
		}
		if err := submit(fmt.Sprintf("multisend_%d_transfer", i), nil, transfer); err != nil {
			return 0, err
		}
		if err := verifyWithRetry(ctx, fmt.Sprintf("verify multisend_%d_token_account", i), func() error {
			return verifyToken2022TokenAccount(ctx, client, tokenAccount.PublicKey, tokenAccountRentLamports, mint, recipient, token2022.Account{
				Mint:            mint,
				Owner:           recipient,
				State:           token2022.AccountInitialized,
				Amount:          transferAmount,
				CloseAuthority:  token2022.OptionalPubkey{},
				Delegate:        token2022.OptionalPubkey{},
				DelegatedAmount: 0,
				IsNative:        token2022.OptionalU64{},
			})
		}); err != nil {
			return 0, err
		}
		remainingAmount -= transferAmount
	}
	if err := verifyWithRetry(ctx, "verify finalized source token balance after multisend", func() error {
		return verifyToken2022TokenAccount(ctx, client, sourceTokenAccount, tokenAccountRentLamports, mint, sourceOwner, token2022.Account{
			Mint:            mint,
			Owner:           sourceOwner,
			State:           token2022.AccountInitialized,
			Amount:          remainingAmount,
			CloseAuthority:  token2022.OptionalPubkey{},
			Delegate:        token2022.OptionalPubkey{},
			DelegatedAmount: 0,
			IsNative:        token2022.OptionalU64{},
		})
	}); err != nil {
		return 0, err
	}
	return remainingAmount, nil
}

func createAssociatedTokenAccountInstruction(
	funder sdk.Pubkey,
	associatedTokenAccount sdk.Pubkey,
	owner sdk.Pubkey,
	mint sdk.Pubkey,
	tokenProgram sdk.Pubkey,
) sdk.Instruction {
	return sdk.Instruction{
		ProgramID: associatedTokenProgram,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(funder, true),
			sdk.Writable(associatedTokenAccount, false),
			sdk.Readonly(owner, false),
			sdk.Readonly(mint, false),
			sdk.Readonly(system.ProgramID, false),
			sdk.Readonly(tokenProgram, false),
			sdk.Readonly(token2022.RentSysvar, false),
		},
		Data: []byte{1},
	}
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
	expectedMetadata *tokenMetadataExpectation,
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
	if expectedMetadata != nil {
		extensions, err := verifyExpectedTokenMetadata(gotWithExtensions.Extensions, expectedMetadata)
		if err != nil {
			return err
		}
		if expectedMetadata.UpdateAuthority != extensions.UpdateAuthority {
			return fmt.Errorf("metadata update authority %s, want %s", extensions.UpdateAuthority, expectedMetadata.UpdateAuthority)
		}
		if expectedMetadata.Mint != extensions.Mint {
			return fmt.Errorf("metadata mint %s, want %s", extensions.Mint, expectedMetadata.Mint)
		}
		if expectedMetadata.Name != extensions.Name {
			return fmt.Errorf("metadata name %q, want %q", extensions.Name, expectedMetadata.Name)
		}
		if expectedMetadata.Symbol != extensions.Symbol {
			return fmt.Errorf("metadata symbol %q, want %q", extensions.Symbol, expectedMetadata.Symbol)
		}
		if expectedMetadata.URI != extensions.URI {
			return fmt.Errorf("metadata uri %q, want %q", extensions.URI, expectedMetadata.URI)
		}
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
	got, err := token2022.DecodeAccount(raw)
	if err != nil {
		return err
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
	canonical, err := token2022.EncodeAccount(got)
	if err != nil {
		return fmt.Errorf("re-encode token account: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return errors.New("token account data is not byte-for-byte canonical")
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

type tokenMetadataState struct {
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

func mintAccountSize(name, symbol, uri string) (uint64, uint64, error) {
	if name == "" && symbol == "" && uri == "" {
		size := uint64(token2022.MintSize)
		return size, size, nil
	}
	extensionPayload, err := tokenMetadataPayloadLength(name, symbol, uri)
	if err != nil {
		return 0, 0, fmt.Errorf("metadata payload length: %w", err)
	}
	base, err := token2022.CalculateMintLen([]token2022.ExtensionType{token2022.ExtensionMetadataPointer})
	if err != nil {
		return 0, 0, fmt.Errorf("metadata pointer extension size: %w", err)
	}
	if base < token2022.MintSize {
		return 0, 0, errors.New("invalid metadata pointer size underflow")
	}
	if extensionPayload > math.MaxInt-base-4 {
		return 0, 0, errors.New("calculated mint size overflow")
	}
	createSize := uint64(base)
	mintSize := uint64(base + 4 + extensionPayload)
	return createSize, mintSize, nil
}

func tokenMetadataPayloadLength(name, symbol, uri string) (int, error) {
	const fixedBase = 80 // updateAuthority + mint + 4+nameLen + 4+symbolLen + 4+uriLen + 4+extraMetadataLen
	total := fixedBase + len(name) + len(symbol) + len(uri)
	if total > int(^uint16(0)) {
		return 0, errors.New("metadata payload length exceeds extension limit")
	}
	return total, nil
}

func verifyExpectedTokenMetadata(extensions []token2022.Extension, expected *tokenMetadataExpectation) (tokenMetadataState, error) {
	var state tokenMetadataState
	var hasMetadataPointer bool
	var metadataRaw []byte

	for _, extension := range extensions {
		switch extension.Type {
		case token2022.ExtensionMetadataPointer:
			hasMetadataPointer = true
		case token2022.ExtensionTokenMetadata:
			if metadataRaw != nil {
				return state, errors.New("duplicate token metadata extension")
			}
			metadataRaw = append([]byte(nil), extension.Data...)
		}
	}

	if !hasMetadataPointer {
		return state, errors.New("metadata pointer extension missing")
	}
	if metadataRaw == nil {
		return state, errors.New("token metadata extension missing")
	}
	state, err := parseTokenMetadata(metadataRaw)
	if err != nil {
		return state, err
	}
	if state.Name != expected.Name {
		return state, fmt.Errorf("metadata name mismatch: got %q, want %q", state.Name, expected.Name)
	}
	if state.Symbol != expected.Symbol {
		return state, fmt.Errorf("metadata symbol mismatch: got %q, want %q", state.Symbol, expected.Symbol)
	}
	if state.URI != expected.URI {
		return state, fmt.Errorf("metadata URI mismatch: got %q, want %q", state.URI, expected.URI)
	}
	if state.UpdateAuthority != expected.UpdateAuthority {
		return state, fmt.Errorf("metadata update authority mismatch: got %s, want %s", state.UpdateAuthority, expected.UpdateAuthority)
	}
	if state.Mint != expected.Mint {
		return state, fmt.Errorf("metadata mint mismatch: got %s, want %s", state.Mint, expected.Mint)
	}
	return state, nil
}

func parseTokenMetadata(data []byte) (tokenMetadataState, error) {
	var state tokenMetadataState
	if len(data) < 80 {
		return state, errors.New("token metadata too short")
	}
	offset := 0
	updateAuthority, err := sdk.PubkeyFromBytes(data[offset : offset+sdk.PubkeySize])
	if err != nil {
		return state, err
	}
	offset += sdk.PubkeySize
	mint, err := sdk.PubkeyFromBytes(data[offset : offset+sdk.PubkeySize])
	if err != nil {
		return state, err
	}
	offset += sdk.PubkeySize
	state.UpdateAuthority = updateAuthority
	state.Mint = mint

	state.Name, offset, err = readBorshString(data, offset)
	if err != nil {
		return state, err
	}
	state.Symbol, offset, err = readBorshString(data, offset)
	if err != nil {
		return state, err
	}
	state.URI, offset, err = readBorshString(data, offset)
	if err != nil {
		return state, err
	}
	if offset+4 > len(data) {
		return state, errors.New("token metadata missing additional-metadata length")
	}
	extraLen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
	offset += 4
	if offset+extraLen != len(data) {
		return state, errors.New("token metadata has malformed additional-metadata vector")
	}
	return state, nil
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

func appendBorshString(dst []byte, value string) ([]byte, error) {
	if len(value) > int(^uint32(0)) {
		return nil, errors.New("string exceeds Borsh u32 length")
	}
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(value)))
	return append(dst, value...), nil
}

func buildInitializeTokenMetadataInstruction(mint, updateAuthority, mintAuthority sdk.Pubkey, name, symbol, uri string) (sdk.Instruction, error) {
	payload, err := appendBorshString(nil, name)
	if err != nil {
		return sdk.Instruction{}, err
	}
	payload, err = appendBorshString(payload, symbol)
	if err != nil {
		return sdk.Instruction{}, err
	}
	payload, err = appendBorshString(payload, uri)
	if err != nil {
		return sdk.Instruction{}, err
	}
	discriminator := tokenMetadataInitializeDiscriminator()
	data := make([]byte, len(discriminator)+len(payload))
	copy(data, discriminator)
	copy(data[len(discriminator):], payload)

	return sdk.Instruction{
		ProgramID: token2022.ProgramID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(mint, false),
			sdk.Readonly(updateAuthority, false),
			sdk.Readonly(mint, false),
			sdk.Readonly(mintAuthority, true),
		},
		Data: data,
	}, nil
}

func tokenMetadataInitializeDiscriminator() []byte {
	sum := sha256.Sum256([]byte("spl_token_metadata_interface:initialize_account"))
	return sum[:8]
}

func initializeMetadataPointerInstruction(mint, authority, metadataAddress sdk.Pubkey) sdk.Instruction {
	data := []byte{39, 0}
	data = append(data, authority[:]...)
	data = append(data, metadataAddress[:]...)
	return sdk.Instruction{
		ProgramID: token2022.ProgramID,
		Accounts:  []sdk.AccountMeta{sdk.Writable(mint, false)},
		Data:      data,
	}
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

func isDevnetRPCURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(parsed.Hostname()), "devnet")
}

func normalizeRPCURL(raw string) (string, error) {
	switch strings.ToLower(raw) {
	case "l", "localhost":
		return "http://127.0.0.1:8899", nil
	case "d", "dev", "devnet":
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
