// Command gospl-init creates one mint and one token account for an already
// deployed GOSPL program, then mints an explicitly selected raw supply.
//
// GOSPL is the custom token program in examples/gospl. Its accounts are not
// classic SPL Token or Token-2022 accounts and will not appear in wallet token
// lists that only index those two official programs.
package main

import (
	"bytes"
	"context"
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

	gosoldeploy "github.com/ersanyakit/solanago/deploy"
	"github.com/ersanyakit/solanago/examples/gospl"
	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/loader"
	"github.com/ersanyakit/solanago/sdk/system"
	"github.com/ersanyakit/solanago/svmtest"
)

const minimumFeeReserveLamports = uint64(100_000)

type rpcClient interface {
	Health(context.Context) error
	GenesisHash(context.Context) (string, error)
	MinimumBalanceForRentExemption(context.Context, uint64) (uint64, error)
	Balance(context.Context, sdk.Pubkey) (uint64, error)
	GetAccountInfo(context.Context, sdk.Pubkey) (*svmtest.AccountInfo, error)
	SendInstructions(context.Context, svmtest.Signer, []svmtest.Signer, []sdk.Instruction) (string, error)
}

type dependencies struct {
	newClient func(string) rpcClient
	newSigner func() (svmtest.Signer, error)
	loadKey   func(string) (svmtest.Signer, error)
	now       func() time.Time
}

func realDependencies() dependencies {
	return dependencies{
		newClient: func(rawURL string) rpcClient { return svmtest.Client{URL: rawURL} },
		newSigner: svmtest.NewSigner,
		loadKey:   gosoldeploy.LoadKeypair,
		now:       time.Now,
	}
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
	Program   sdk.Pubkey
	Payer     svmtest.Signer
	Decimals  uint8
	AmountRaw uint64
}

type signatureRecord struct {
	Stage     string `json:"stage"`
	Signature string `json:"signature"`
}

// exactUint64 is encoded as a decimal JSON string. Token amounts and lamports
// may exceed JavaScript's exact integer range, so emitting a JSON number here
// would make the public recovery record lossy for common consumers.
type exactUint64 uint64

func (v exactUint64) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(v), 10))
}

type mintStateJSON struct {
	Initialized   bool        `json:"initialized"`
	Decimals      uint8       `json:"decimals"`
	SupplyRaw     exactUint64 `json:"supply_raw"`
	SupplyUI      string      `json:"supply_ui"`
	MintAuthority string      `json:"mint_authority"`
}

type tokenStateJSON struct {
	Initialized bool        `json:"initialized"`
	Mint        string      `json:"mint"`
	Owner       string      `json:"owner"`
	AmountRaw   exactUint64 `json:"amount_raw"`
	AmountUI    string      `json:"amount_ui"`
	Delegate    string      `json:"delegate,omitempty"`
}

type result struct {
	GeneratedAtUTC         string            `json:"generated_at_utc"`
	GenesisHash            string            `json:"genesis_hash"`
	ProgramID              string            `json:"program_id"`
	FeePayer               string            `json:"fee_payer"`
	Mint                   string            `json:"mint"`
	TokenAccount           string            `json:"token_account"`
	MintRentLamports       exactUint64       `json:"mint_rent_lamports"`
	TokenRentLamports      exactUint64       `json:"token_rent_lamports"`
	AmountRaw              exactUint64       `json:"amount_raw"`
	AmountUI               string            `json:"amount_ui"`
	Decimals               uint8             `json:"decimals"`
	SubmittedSignatures    []signatureRecord `json:"submitted_signatures"`
	NonFinalizedSignatures []signatureRecord `json:"non_finalized_signatures"`
	FinalizedSignatures    []signatureRecord `json:"finalized_signatures"`
	MintState              mintStateJSON     `json:"mint_state"`
	TokenAccountState      tokenStateJSON    `json:"token_account_state"`
	FinalizedAndVerified   bool              `json:"finalized_and_verified"`
}

type progressEvent struct {
	Event        string      `json:"event"`
	Stage        string      `json:"stage,omitempty"`
	GenesisHash  string      `json:"genesis_hash,omitempty"`
	ProgramID    string      `json:"program_id,omitempty"`
	FeePayer     string      `json:"fee_payer,omitempty"`
	Mint         string      `json:"mint,omitempty"`
	TokenAccount string      `json:"token_account,omitempty"`
	AmountRaw    exactUint64 `json:"amount_raw,omitempty"`
	AmountUI     string      `json:"amount_ui,omitempty"`
	Decimals     *uint8      `json:"decimals,omitempty"`
	Signature    string      `json:"signature,omitempty"`
}

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr, realDependencies()); err != nil {
		fmt.Fprintln(os.Stderr, "gospl-init:", err)
		os.Exit(1)
	}
}

func runCLI(arguments []string, stdout, stderr io.Writer, deps dependencies) error {
	flags := flag.NewFlagSet("gospl-init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	programText := flags.String("program", "", "deployed GOSPL program address (required)")
	keypairPath := flags.String("keypair", "", "fee payer, mint authority, and token owner keypair (required)")
	rpcURL := flags.String("url", "localhost", "Solana RPC URL or localhost/devnet/testnet/mainnet-beta")
	decimals := flags.Uint("decimals", 6, "mint decimal places (0..255; default 6)")
	var amountUI uiAmountFlag
	flags.Var(&amountUI, "amount-ui", "UI token amount, converted exactly using --decimals")
	var amountRaw rawAmountFlag
	flags.Var(&amountRaw, "amount-raw", "raw smallest-unit amount (mutually exclusive with --amount-ui)")
	allowLive := flags.Bool("allow-live", false, "explicitly permit a non-loopback RPC endpoint")
	timeout := flags.Duration("timeout", 5*time.Minute, "whole initialization deadline")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *programText == "" || *keypairPath == "" {
		return errors.New("expected --program ADDRESS, --keypair FILE, and exactly one of --amount-raw or --amount-ui")
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
	programID, err := sdk.ParsePubkey(*programText)
	if err != nil || programID == system.ProgramID || programID == loader.ProgramID {
		return fmt.Errorf("invalid deployed GOSPL program address %q", *programText)
	}
	if deps.loadKey == nil || deps.newClient == nil || deps.newSigner == nil || deps.now == nil {
		return errors.New("internal dependency is nil")
	}
	payer, err := deps.loadKey(*keypairPath)
	if err != nil {
		return fmt.Errorf("load payer keypair: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := deps.newClient(resolvedURL)
	created, err := initialize(ctx, client, config{
		Program:   programID,
		Payer:     payer,
		Decimals:  uint8(*decimals),
		AmountRaw: raw,
	}, deps.newSigner, deps.now, func(event progressEvent) error {
		return json.NewEncoder(stderr).Encode(event)
	})
	if err != nil {
		if created != nil {
			encoded, marshalErr := json.Marshal(created)
			if marshalErr == nil {
				fmt.Fprintf(stderr, "partial public recovery journal: %s\n", encoded)
			}
		}
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(created)
}

func initialize(
	ctx context.Context,
	client rpcClient,
	config config,
	newSigner func() (svmtest.Signer, error),
	now func() time.Time,
	progress func(progressEvent) error,
) (*result, error) {
	if client == nil {
		return nil, errors.New("RPC client is nil")
	}
	if err := svmtest.ValidateSigner(config.Payer); err != nil {
		return nil, fmt.Errorf("invalid payer: %w", err)
	}
	if config.Program == system.ProgramID || config.Program == loader.ProgramID {
		return nil, errors.New("invalid GOSPL program address")
	}
	if config.AmountRaw == 0 {
		return nil, errors.New("mint amount must be positive")
	}
	if newSigner == nil || now == nil {
		return nil, errors.New("internal dependency is nil")
	}
	if err := client.Health(ctx); err != nil {
		return nil, fmt.Errorf("RPC health: %w", err)
	}
	genesisHash, err := client.GenesisHash(ctx)
	if err != nil {
		return nil, fmt.Errorf("get genesis hash: %w", err)
	}
	if genesisHash == "" {
		return nil, errors.New("get genesis hash: RPC returned an empty hash")
	}
	programInfo, err := client.GetAccountInfo(ctx, config.Program)
	if err != nil {
		return nil, fmt.Errorf("read program account: %w", err)
	}
	if programInfo == nil || !programInfo.Executable || programInfo.Owner != loader.ProgramID.String() {
		return nil, fmt.Errorf("program %s is not a finalized executable owned by %s", config.Program, loader.ProgramID)
	}
	mint, err := newSigner()
	if err != nil {
		return nil, fmt.Errorf("generate in-memory mint signer: %w", err)
	}
	tokenAccount, err := newSigner()
	if err != nil {
		return nil, fmt.Errorf("generate in-memory token-account signer: %w", err)
	}
	if err := svmtest.ValidateSigner(mint); err != nil {
		return nil, fmt.Errorf("generated mint signer: %w", err)
	}
	if err := svmtest.ValidateSigner(tokenAccount); err != nil {
		return nil, fmt.Errorf("generated token-account signer: %w", err)
	}
	if mint.PublicKey == tokenAccount.PublicKey || mint.PublicKey == config.Payer.PublicKey || tokenAccount.PublicKey == config.Payer.PublicKey {
		return nil, errors.New("generated signer collision")
	}
	created := &result{
		GeneratedAtUTC:         now().UTC().Format(time.RFC3339Nano),
		GenesisHash:            genesisHash,
		ProgramID:              config.Program.String(),
		FeePayer:               config.Payer.PublicKey.String(),
		Mint:                   mint.PublicKey.String(),
		TokenAccount:           tokenAccount.PublicKey.String(),
		AmountRaw:              exactUint64(config.AmountRaw),
		AmountUI:               formatUIAmount(config.AmountRaw, config.Decimals),
		Decimals:               config.Decimals,
		SubmittedSignatures:    make([]signatureRecord, 0, 3),
		NonFinalizedSignatures: make([]signatureRecord, 0, 1),
		FinalizedSignatures:    make([]signatureRecord, 0, 3),
	}
	emit := func(event progressEvent) error {
		if progress == nil {
			return nil
		}
		if err := progress(event); err != nil {
			return fmt.Errorf("write recovery progress event %q: %w", event.Event, err)
		}
		return nil
	}
	plannedDecimals := created.Decimals
	if err := emit(progressEvent{
		Event: "planned", GenesisHash: created.GenesisHash, ProgramID: created.ProgramID,
		FeePayer: created.FeePayer, Mint: created.Mint, TokenAccount: created.TokenAccount,
		AmountRaw: created.AmountRaw, AmountUI: created.AmountUI, Decimals: &plannedDecimals,
	}); err != nil {
		return created, err
	}
	generatedAccounts := []struct {
		label   string
		address sdk.Pubkey
	}{{"mint", mint.PublicKey}, {"token account", tokenAccount.PublicKey}}
	for _, generated := range generatedAccounts {
		label, address := generated.label, generated.address
		existing, readErr := client.GetAccountInfo(ctx, address)
		if readErr != nil {
			return created, fmt.Errorf("check generated %s address: %w", label, readErr)
		}
		if existing != nil {
			return created, fmt.Errorf("generated %s address %s already exists", label, address)
		}
	}
	mintRentLamports, err := client.MinimumBalanceForRentExemption(ctx, gospl.MintStateSize)
	if err != nil {
		return created, fmt.Errorf("query mint rent: %w", err)
	}
	created.MintRentLamports = exactUint64(mintRentLamports)
	tokenRentLamports, err := client.MinimumBalanceForRentExemption(ctx, gospl.TokenAccountStateSize)
	if err != nil {
		return created, fmt.Errorf("query token-account rent: %w", err)
	}
	created.TokenRentLamports = exactUint64(tokenRentLamports)
	if mintRentLamports > math.MaxUint64-tokenRentLamports {
		return created, errors.New("rent requirement overflows uint64")
	}
	rentTotal := mintRentLamports + tokenRentLamports
	if rentTotal > math.MaxUint64-minimumFeeReserveLamports {
		return created, errors.New("rent requirement overflows uint64")
	}
	requiredBalance := rentTotal + minimumFeeReserveLamports
	balance, err := client.Balance(ctx, config.Payer.PublicKey)
	if err != nil {
		return created, fmt.Errorf("query payer balance: %w", err)
	}
	if balance < requiredBalance {
		return created, fmt.Errorf("payer balance %d is below rent plus conservative fee reserve %d", balance, requiredBalance)
	}

	submit := func(stage string, signers []svmtest.Signer, instructions ...sdk.Instruction) error {
		signature, submitErr := client.SendInstructions(ctx, config.Payer, signers, instructions)
		if submitErr != nil {
			if signature == "" {
				return fmt.Errorf("%s failed before a transaction signature was returned; no retry was attempted: %w", stage, submitErr)
			}
			record := signatureRecord{Stage: stage, Signature: signature}
			created.NonFinalizedSignatures = append(created.NonFinalizedSignatures, record)
			baseErr := fmt.Errorf("%s has no finalized proof; no retry was attempted (non-finalized signature %q): %w", stage, signature, submitErr)
			if emitErr := emit(progressEvent{Event: "non_finalized", Stage: stage, Signature: signature}); emitErr != nil {
				return errors.Join(baseErr, emitErr)
			}
			return baseErr
		}
		if signature == "" {
			return fmt.Errorf("%s returned no finalized signature", stage)
		}
		record := signatureRecord{Stage: stage, Signature: signature}
		created.SubmittedSignatures = append(created.SubmittedSignatures, record)
		created.FinalizedSignatures = append(created.FinalizedSignatures, record)
		if err := emit(progressEvent{Event: "finalized", Stage: stage, Signature: signature}); err != nil {
			return err
		}
		return nil
	}

	if err := submit("create_and_initialize_mint", []svmtest.Signer{mint},
		system.CreateAccount(config.Payer.PublicKey, mint.PublicKey, mintRentLamports, gospl.MintStateSize, config.Program),
		gospl.InitializeMint(config.Program, mint.PublicKey, config.Payer.PublicKey, config.Decimals),
	); err != nil {
		return created, err
	}
	wantMint := gospl.MintState{
		Initialized:   true,
		Decimals:      config.Decimals,
		MintAuthority: gospl.OptionalPubkey{Set: true, Key: config.Payer.PublicKey},
	}
	if _, err := verifyMint(ctx, client, config.Program, mint.PublicKey, mintRentLamports, wantMint); err != nil {
		return created, fmt.Errorf("verify finalized mint initialization: %w", err)
	}

	if err := submit("create_and_initialize_token_account", []svmtest.Signer{tokenAccount},
		system.CreateAccount(config.Payer.PublicKey, tokenAccount.PublicKey, tokenRentLamports, gospl.TokenAccountStateSize, config.Program),
		gospl.InitializeAccount(config.Program, tokenAccount.PublicKey, mint.PublicKey, config.Payer.PublicKey),
	); err != nil {
		return created, err
	}
	wantToken := gospl.TokenAccountState{
		Initialized: true,
		Mint:        mint.PublicKey,
		Owner:       config.Payer.PublicKey,
	}
	if _, err := verifyToken(ctx, client, config.Program, tokenAccount.PublicKey, tokenRentLamports, wantToken); err != nil {
		return created, fmt.Errorf("verify finalized token-account initialization: %w", err)
	}
	if _, err := verifyMint(ctx, client, config.Program, mint.PublicKey, mintRentLamports, wantMint); err != nil {
		return created, fmt.Errorf("verify mint before mint-to: %w", err)
	}

	if err := submit("mint_to", nil,
		gospl.MintTo(config.Program, mint.PublicKey, tokenAccount.PublicKey, config.Payer.PublicKey, config.AmountRaw),
	); err != nil {
		return created, err
	}
	wantMint.Supply = config.AmountRaw
	wantToken.Amount = config.AmountRaw
	mintState, err := verifyMint(ctx, client, config.Program, mint.PublicKey, mintRentLamports, wantMint)
	if err != nil {
		return created, fmt.Errorf("verify finalized minted supply: %w", err)
	}
	tokenState, err := verifyToken(ctx, client, config.Program, tokenAccount.PublicKey, tokenRentLamports, wantToken)
	if err != nil {
		return created, fmt.Errorf("verify finalized token balance: %w", err)
	}
	created.MintState = mintStateOutput(mintState)
	created.TokenAccountState = tokenStateOutput(tokenState, config.Decimals)
	created.FinalizedAndVerified = true
	if err := emit(progressEvent{Event: "verified", ProgramID: created.ProgramID, Mint: created.Mint, TokenAccount: created.TokenAccount}); err != nil {
		return created, err
	}
	return created, nil
}

func verifyMint(ctx context.Context, client rpcClient, programID, address sdk.Pubkey, rent uint64, want gospl.MintState) (gospl.MintState, error) {
	data, err := verifiedStateData(ctx, client, programID, address, rent)
	if err != nil {
		return gospl.MintState{}, err
	}
	got, err := gospl.DecodeMintState(data)
	if err != nil {
		return gospl.MintState{}, fmt.Errorf("decode mint: %w", err)
	}
	if got != want {
		return gospl.MintState{}, fmt.Errorf("mint state %+v does not match expected %+v", got, want)
	}
	canonical := make([]byte, gospl.MintStateSize)
	if err := gospl.EncodeMintState(canonical, got); err != nil {
		return gospl.MintState{}, fmt.Errorf("re-encode mint: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return gospl.MintState{}, errors.New("mint data is not byte-for-byte canonical")
	}
	return got, nil
}

func verifyToken(ctx context.Context, client rpcClient, programID, address sdk.Pubkey, rent uint64, want gospl.TokenAccountState) (gospl.TokenAccountState, error) {
	data, err := verifiedStateData(ctx, client, programID, address, rent)
	if err != nil {
		return gospl.TokenAccountState{}, err
	}
	got, err := gospl.DecodeTokenAccountState(data)
	if err != nil {
		return gospl.TokenAccountState{}, fmt.Errorf("decode token account: %w", err)
	}
	if got != want {
		return gospl.TokenAccountState{}, fmt.Errorf("token-account state %+v does not match expected %+v", got, want)
	}
	canonical := make([]byte, gospl.TokenAccountStateSize)
	if err := gospl.EncodeTokenAccountState(canonical, got); err != nil {
		return gospl.TokenAccountState{}, fmt.Errorf("re-encode token account: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return gospl.TokenAccountState{}, errors.New("token-account data is not byte-for-byte canonical")
	}
	return got, nil
}

func verifiedStateData(ctx context.Context, client rpcClient, programID, address sdk.Pubkey, minimumLamports uint64) ([]byte, error) {
	info, err := client.GetAccountInfo(ctx, address)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("account %s does not exist at finalized commitment", address)
	}
	if info.Executable {
		return nil, fmt.Errorf("state account %s is unexpectedly executable", address)
	}
	if info.Owner != programID.String() {
		return nil, fmt.Errorf("state account %s owner=%s, want %s", address, info.Owner, programID)
	}
	if info.Lamports < minimumLamports {
		return nil, fmt.Errorf("state account %s has %d lamports, want at least %d", address, info.Lamports, minimumLamports)
	}
	return info.DataBytes()
}

func mintStateOutput(state gospl.MintState) mintStateJSON {
	authority := "disabled"
	if state.MintAuthority.Set {
		authority = state.MintAuthority.Key.String()
	}
	return mintStateJSON{
		Initialized:   state.Initialized,
		Decimals:      state.Decimals,
		SupplyRaw:     exactUint64(state.Supply),
		SupplyUI:      formatUIAmount(state.Supply, state.Decimals),
		MintAuthority: authority,
	}
}

func tokenStateOutput(state gospl.TokenAccountState, decimals uint8) tokenStateJSON {
	delegate := ""
	if state.Delegate.Set {
		delegate = state.Delegate.Key.String()
	}
	return tokenStateJSON{
		Initialized: state.Initialized,
		Mint:        state.Mint.String(),
		Owner:       state.Owner.String(),
		AmountRaw:   exactUint64(state.Amount),
		AmountUI:    formatUIAmount(state.Amount, decimals),
		Delegate:    delegate,
	}
}

func parseUIAmount(text string, decimals uint8) (uint64, error) {
	if text == "" || strings.TrimSpace(text) != text || strings.HasPrefix(text, "+") || strings.HasPrefix(text, "-") {
		return 0, errors.New("amount must be an unsigned plain decimal without surrounding whitespace")
	}
	parts := strings.Split(text, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts) == 2 && parts[1] == "" {
		return 0, errors.New("amount must use plain decimal notation")
	}
	if len(parts) == 2 && len(parts[1]) > int(decimals) {
		return 0, fmt.Errorf("amount has %d fractional digits, but decimals is %d", len(parts[1]), decimals)
	}
	digits := parts[0]
	if len(parts) == 2 {
		digits += parts[1]
		digits += strings.Repeat("0", int(decimals)-len(parts[1]))
	} else {
		digits += strings.Repeat("0", int(decimals))
	}
	var value uint64
	for _, digit := range []byte(digits) {
		if digit < '0' || digit > '9' {
			return 0, errors.New("amount must contain only ASCII decimal digits")
		}
		part := uint64(digit - '0')
		if value > (math.MaxUint64-part)/10 {
			return 0, errors.New("amount does not fit uint64 raw units")
		}
		value = value*10 + part
	}
	return value, nil
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
	switch raw {
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
		return "", errors.New("RPC URL must use http or https")
	}
	return raw, nil
}
