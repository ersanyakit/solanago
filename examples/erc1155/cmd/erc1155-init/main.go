// Command erc1155-init mints one ERC1155-style token on an already deployed
// examples/erc1155 program: it creates a new collection, defines one token
// type under it, initializes one balance account, and mints an explicitly
// selected raw amount into that balance (amount 1 is an NFT-style token; a
// larger amount is a fungible-within-the-id token, matching real ERC1155).
//
// examples/erc1155 is a custom multi-token program. Its accounts are not
// classic SPL Token or Token-2022 accounts and will not appear in wallet
// token lists that only index those two official programs.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	gosoldeploy "github.com/ersanyakit/solanago/deploy"
	"github.com/ersanyakit/solanago/examples/erc1155"
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
		return "1"
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

type config struct {
	Program   sdk.Pubkey
	Payer     svmtest.Signer
	URI       string
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

type collectionStateJSON struct {
	Initialized bool   `json:"initialized"`
	Authority   string `json:"authority"`
	NextID      uint64 `json:"next_id"`
}

type tokenTypeStateJSON struct {
	Initialized bool        `json:"initialized"`
	Collection  string      `json:"collection"`
	ID          uint64      `json:"id"`
	Supply      exactUint64 `json:"supply"`
	URI         string      `json:"uri"`
}

type balanceStateJSON struct {
	Initialized bool        `json:"initialized"`
	Collection  string      `json:"collection"`
	ID          uint64      `json:"id"`
	Owner       string      `json:"owner"`
	Amount      exactUint64 `json:"amount"`
}

type result struct {
	GeneratedAtUTC         string              `json:"generated_at_utc"`
	GenesisHash            string              `json:"genesis_hash"`
	ProgramID              string              `json:"program_id"`
	FeePayer               string              `json:"fee_payer"`
	Collection             string              `json:"collection"`
	TokenType              string              `json:"token_type"`
	Balance                string              `json:"balance"`
	URI                    string              `json:"uri"`
	AmountRaw              exactUint64         `json:"amount_raw"`
	CollectionRentLamports exactUint64         `json:"collection_rent_lamports"`
	TokenTypeRentLamports  exactUint64         `json:"token_type_rent_lamports"`
	BalanceRentLamports    exactUint64         `json:"balance_rent_lamports"`
	SubmittedSignatures    []signatureRecord   `json:"submitted_signatures"`
	NonFinalizedSignatures []signatureRecord   `json:"non_finalized_signatures"`
	FinalizedSignatures    []signatureRecord   `json:"finalized_signatures"`
	CollectionState        collectionStateJSON `json:"collection_state"`
	TokenTypeState         tokenTypeStateJSON  `json:"token_type_state"`
	BalanceState           balanceStateJSON    `json:"balance_state"`
	FinalizedAndVerified   bool                `json:"finalized_and_verified"`
}

type progressEvent struct {
	Event       string      `json:"event"`
	Stage       string      `json:"stage,omitempty"`
	GenesisHash string      `json:"genesis_hash,omitempty"`
	ProgramID   string      `json:"program_id,omitempty"`
	FeePayer    string      `json:"fee_payer,omitempty"`
	Collection  string      `json:"collection,omitempty"`
	TokenType   string      `json:"token_type,omitempty"`
	Balance     string      `json:"balance,omitempty"`
	AmountRaw   exactUint64 `json:"amount_raw,omitempty"`
	Signature   string      `json:"signature,omitempty"`
}

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr, realDependencies()); err != nil {
		fmt.Fprintln(os.Stderr, "erc1155-init:", err)
		os.Exit(1)
	}
}

func runCLI(arguments []string, stdout, stderr io.Writer, deps dependencies) error {
	flags := flag.NewFlagSet("erc1155-init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	programText := flags.String("program", "", "deployed erc1155 program address (required)")
	keypairPath := flags.String("keypair", "", "fee payer, collection authority, and balance owner keypair (required)")
	rpcURL := flags.String("url", "localhost", "Solana RPC URL or localhost/devnet/testnet/mainnet-beta")
	uri := flags.String("uri", "", "token type metadata URI, up to erc1155.MaxURILength bytes (required)")
	var amountRaw rawAmountFlag
	amountRaw.value = 1
	flags.Var(&amountRaw, "amount-raw", "raw amount to mint into the new balance (default 1, an NFT-style token)")
	allowLive := flags.Bool("allow-live", false, "explicitly permit a non-loopback RPC endpoint")
	timeout := flags.Duration("timeout", 5*time.Minute, "whole initialization deadline")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *programText == "" || *keypairPath == "" || *uri == "" {
		return errors.New("expected --program ADDRESS, --keypair FILE, and --uri URI")
	}
	if len(*uri) > erc1155.MaxURILength {
		return fmt.Errorf("uri length %d exceeds erc1155.MaxURILength %d", len(*uri), erc1155.MaxURILength)
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	raw := amountRaw.value
	if !amountRaw.set {
		raw = 1
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
		return fmt.Errorf("invalid deployed erc1155 program address %q", *programText)
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
		URI:       *uri,
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
		return nil, errors.New("invalid erc1155 program address")
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

	collection, err := newSigner()
	if err != nil {
		return nil, fmt.Errorf("generate in-memory collection signer: %w", err)
	}
	tokenType, err := newSigner()
	if err != nil {
		return nil, fmt.Errorf("generate in-memory token-type signer: %w", err)
	}
	balance, err := newSigner()
	if err != nil {
		return nil, fmt.Errorf("generate in-memory balance signer: %w", err)
	}
	for _, generated := range []svmtest.Signer{collection, tokenType, balance} {
		if err := svmtest.ValidateSigner(generated); err != nil {
			return nil, fmt.Errorf("generated signer: %w", err)
		}
	}
	keys := map[sdk.Pubkey]bool{
		config.Payer.PublicKey: true, collection.PublicKey: true,
		tokenType.PublicKey: true, balance.PublicKey: true,
	}
	if len(keys) != 4 {
		return nil, errors.New("generated signer collision")
	}

	created := &result{
		GeneratedAtUTC:         now().UTC().Format(time.RFC3339Nano),
		GenesisHash:            genesisHash,
		ProgramID:              config.Program.String(),
		FeePayer:               config.Payer.PublicKey.String(),
		Collection:             collection.PublicKey.String(),
		TokenType:              tokenType.PublicKey.String(),
		Balance:                balance.PublicKey.String(),
		URI:                    config.URI,
		AmountRaw:              exactUint64(config.AmountRaw),
		SubmittedSignatures:    make([]signatureRecord, 0, 4),
		NonFinalizedSignatures: make([]signatureRecord, 0, 1),
		FinalizedSignatures:    make([]signatureRecord, 0, 4),
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
	if err := emit(progressEvent{
		Event: "planned", GenesisHash: created.GenesisHash, ProgramID: created.ProgramID,
		FeePayer: created.FeePayer, Collection: created.Collection, TokenType: created.TokenType,
		Balance: created.Balance, AmountRaw: created.AmountRaw,
	}); err != nil {
		return created, err
	}

	for _, generated := range []struct {
		label   string
		address sdk.Pubkey
	}{{"collection", collection.PublicKey}, {"token type", tokenType.PublicKey}, {"balance", balance.PublicKey}} {
		existing, readErr := client.GetAccountInfo(ctx, generated.address)
		if readErr != nil {
			return created, fmt.Errorf("check generated %s address: %w", generated.label, readErr)
		}
		if existing != nil {
			return created, fmt.Errorf("generated %s address %s already exists", generated.label, generated.address)
		}
	}

	collectionRent, err := client.MinimumBalanceForRentExemption(ctx, erc1155.CollectionStateSize)
	if err != nil {
		return created, fmt.Errorf("query collection rent: %w", err)
	}
	created.CollectionRentLamports = exactUint64(collectionRent)
	tokenTypeRent, err := client.MinimumBalanceForRentExemption(ctx, erc1155.TokenTypeStateSize)
	if err != nil {
		return created, fmt.Errorf("query token-type rent: %w", err)
	}
	created.TokenTypeRentLamports = exactUint64(tokenTypeRent)
	balanceRent, err := client.MinimumBalanceForRentExemption(ctx, erc1155.BalanceStateSize)
	if err != nil {
		return created, fmt.Errorf("query balance rent: %w", err)
	}
	created.BalanceRentLamports = exactUint64(balanceRent)

	requiredBalance := collectionRent + tokenTypeRent + balanceRent + minimumFeeReserveLamports
	payerBalance, err := client.Balance(ctx, config.Payer.PublicKey)
	if err != nil {
		return created, fmt.Errorf("query payer balance: %w", err)
	}
	if payerBalance < requiredBalance {
		return created, fmt.Errorf("payer balance %d is below rent plus conservative fee reserve %d", payerBalance, requiredBalance)
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

	if err := submit("create_and_initialize_collection", []svmtest.Signer{collection},
		system.CreateAccount(config.Payer.PublicKey, collection.PublicKey, collectionRent, erc1155.CollectionStateSize, config.Program),
		erc1155.InitializeCollection(config.Program, collection.PublicKey, config.Payer.PublicKey),
	); err != nil {
		return created, err
	}
	wantCollection := erc1155.CollectionState{Initialized: true, Authority: config.Payer.PublicKey, NextID: 0}
	if _, err := verifyCollection(ctx, client, config.Program, collection.PublicKey, collectionRent, wantCollection); err != nil {
		return created, fmt.Errorf("verify finalized collection initialization: %w", err)
	}

	createTokenType, err := erc1155.CreateTokenType(config.Program, tokenType.PublicKey, collection.PublicKey, config.Payer.PublicKey, config.URI)
	if err != nil {
		return created, fmt.Errorf("build create_token_type instruction: %w", err)
	}
	if err := submit("create_and_initialize_token_type", []svmtest.Signer{tokenType},
		system.CreateAccount(config.Payer.PublicKey, tokenType.PublicKey, tokenTypeRent, erc1155.TokenTypeStateSize, config.Program),
		createTokenType,
	); err != nil {
		return created, err
	}
	wantTokenType := erc1155.TokenTypeState{Initialized: true, Collection: collection.PublicKey, ID: 0, Supply: 0, URI: config.URI}
	if _, err := verifyTokenType(ctx, client, config.Program, tokenType.PublicKey, tokenTypeRent, wantTokenType); err != nil {
		return created, fmt.Errorf("verify finalized token-type initialization: %w", err)
	}
	wantCollection.NextID = 1
	if _, err := verifyCollection(ctx, client, config.Program, collection.PublicKey, collectionRent, wantCollection); err != nil {
		return created, fmt.Errorf("verify collection next_id after create_token_type: %w", err)
	}

	if err := submit("create_and_initialize_balance", []svmtest.Signer{balance},
		system.CreateAccount(config.Payer.PublicKey, balance.PublicKey, balanceRent, erc1155.BalanceStateSize, config.Program),
		erc1155.InitializeBalance(config.Program, balance.PublicKey, tokenType.PublicKey, config.Payer.PublicKey),
	); err != nil {
		return created, err
	}
	wantBalance := erc1155.BalanceState{Initialized: true, Collection: collection.PublicKey, ID: 0, Owner: config.Payer.PublicKey, Amount: 0}
	if _, err := verifyBalance(ctx, client, config.Program, balance.PublicKey, balanceRent, wantBalance); err != nil {
		return created, fmt.Errorf("verify finalized balance initialization: %w", err)
	}

	if err := submit("mint_to", nil,
		erc1155.MintTo(config.Program, collection.PublicKey, tokenType.PublicKey, balance.PublicKey, config.Payer.PublicKey, config.AmountRaw),
	); err != nil {
		return created, err
	}
	wantTokenType.Supply = config.AmountRaw
	wantBalance.Amount = config.AmountRaw
	tokenTypeState, err := verifyTokenType(ctx, client, config.Program, tokenType.PublicKey, tokenTypeRent, wantTokenType)
	if err != nil {
		return created, fmt.Errorf("verify finalized minted supply: %w", err)
	}
	balanceState, err := verifyBalance(ctx, client, config.Program, balance.PublicKey, balanceRent, wantBalance)
	if err != nil {
		return created, fmt.Errorf("verify finalized minted balance: %w", err)
	}
	collectionState, err := verifyCollection(ctx, client, config.Program, collection.PublicKey, collectionRent, wantCollection)
	if err != nil {
		return created, fmt.Errorf("verify collection after mint: %w", err)
	}

	created.CollectionState = collectionStateOutput(collectionState)
	created.TokenTypeState = tokenTypeStateOutput(tokenTypeState)
	created.BalanceState = balanceStateOutput(balanceState)
	created.FinalizedAndVerified = true
	if err := emit(progressEvent{Event: "verified", ProgramID: created.ProgramID, Collection: created.Collection, TokenType: created.TokenType, Balance: created.Balance}); err != nil {
		return created, err
	}
	return created, nil
}

func verifyCollection(ctx context.Context, client rpcClient, programID, address sdk.Pubkey, rent uint64, want erc1155.CollectionState) (erc1155.CollectionState, error) {
	data, err := verifiedStateData(ctx, client, programID, address, rent)
	if err != nil {
		return erc1155.CollectionState{}, err
	}
	got, err := erc1155.DecodeCollectionState(data)
	if err != nil {
		return erc1155.CollectionState{}, fmt.Errorf("decode collection: %w", err)
	}
	if got != want {
		return erc1155.CollectionState{}, fmt.Errorf("collection state %+v does not match expected %+v", got, want)
	}
	canonical := make([]byte, erc1155.CollectionStateSize)
	if err := erc1155.EncodeCollectionState(canonical, got); err != nil {
		return erc1155.CollectionState{}, fmt.Errorf("re-encode collection: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return erc1155.CollectionState{}, errors.New("collection data is not byte-for-byte canonical")
	}
	return got, nil
}

func verifyTokenType(ctx context.Context, client rpcClient, programID, address sdk.Pubkey, rent uint64, want erc1155.TokenTypeState) (erc1155.TokenTypeState, error) {
	data, err := verifiedStateData(ctx, client, programID, address, rent)
	if err != nil {
		return erc1155.TokenTypeState{}, err
	}
	got, err := erc1155.DecodeTokenTypeState(data)
	if err != nil {
		return erc1155.TokenTypeState{}, fmt.Errorf("decode token type: %w", err)
	}
	if got != want {
		return erc1155.TokenTypeState{}, fmt.Errorf("token-type state %+v does not match expected %+v", got, want)
	}
	canonical := make([]byte, erc1155.TokenTypeStateSize)
	if err := erc1155.EncodeTokenTypeState(canonical, got); err != nil {
		return erc1155.TokenTypeState{}, fmt.Errorf("re-encode token type: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return erc1155.TokenTypeState{}, errors.New("token-type data is not byte-for-byte canonical")
	}
	return got, nil
}

func verifyBalance(ctx context.Context, client rpcClient, programID, address sdk.Pubkey, rent uint64, want erc1155.BalanceState) (erc1155.BalanceState, error) {
	data, err := verifiedStateData(ctx, client, programID, address, rent)
	if err != nil {
		return erc1155.BalanceState{}, err
	}
	got, err := erc1155.DecodeBalanceState(data)
	if err != nil {
		return erc1155.BalanceState{}, fmt.Errorf("decode balance: %w", err)
	}
	if got != want {
		return erc1155.BalanceState{}, fmt.Errorf("balance state %+v does not match expected %+v", got, want)
	}
	canonical := make([]byte, erc1155.BalanceStateSize)
	if err := erc1155.EncodeBalanceState(canonical, got); err != nil {
		return erc1155.BalanceState{}, fmt.Errorf("re-encode balance: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return erc1155.BalanceState{}, errors.New("balance data is not byte-for-byte canonical")
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

func collectionStateOutput(state erc1155.CollectionState) collectionStateJSON {
	return collectionStateJSON{Initialized: state.Initialized, Authority: state.Authority.String(), NextID: state.NextID}
}

func tokenTypeStateOutput(state erc1155.TokenTypeState) tokenTypeStateJSON {
	return tokenTypeStateJSON{
		Initialized: state.Initialized, Collection: state.Collection.String(),
		ID: state.ID, Supply: exactUint64(state.Supply), URI: state.URI,
	}
}

func balanceStateOutput(state erc1155.BalanceState) balanceStateJSON {
	return balanceStateJSON{
		Initialized: state.Initialized, Collection: state.Collection.String(),
		ID: state.ID, Owner: state.Owner.String(), Amount: exactUint64(state.Amount),
	}
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
