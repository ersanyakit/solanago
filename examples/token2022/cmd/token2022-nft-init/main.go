// Command token2022-nft-init mints one genuine, wallet-visible Solana NFT:
// a Token-2022 mint with decimals=0 and supply capped at exactly 1, plus a
// Metaplex Metadata account and Master Edition account. Unlike
// token2022-init's fungible/FungibleAsset metadata (metaplex.CreateV1),
// this uses metaplex.CreateNFTV1, which wallets and Explorer render in
// their Collectibles/NFT view rather than the fungible token list.
//
// Solana's NFT standard has no equivalent of ERC1155's "amount > 1 of the
// same id" — a Master Edition's supply is permanently capped at 1 by the
// on-chain program itself. An amount>1 item is a semi-fungible token, not
// a wallet-visible NFT; use token2022-init for that case instead.
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

	gosoldeploy "github.com/ersanyakit/solanago/deploy"
	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/associatedtoken"
	"github.com/ersanyakit/solanago/sdk/metaplex"
	"github.com/ersanyakit/solanago/sdk/system"
	"github.com/ersanyakit/solanago/sdk/token2022"
	"github.com/ersanyakit/solanago/svmtest"
)

const minimumFeeReserveLamports = uint64(100_000)
const verifyRetryCount = 8

// masterEditionAccountSize is mpl-token-metadata's MAX_MASTER_EDITION_LEN
// (programs/token-metadata/program/src/state/master_edition.rs):
// key(1) + supply(8) + max_supply Option<u64>(1+8) + 2 reserved = 20 bytes.
const masterEditionAccountSize = 20

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

type config struct {
	Payer  svmtest.Signer
	Owner  sdk.Pubkey
	Name   string
	Symbol string
	URI    string
}

type signatureRecord struct {
	Stage     string `json:"stage"`
	Signature string `json:"signature"`
}

type exactUint64 uint64

func (v exactUint64) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(v), 10))
}

type mintStateJSON struct {
	Initialized     bool        `json:"initialized"`
	Decimals        uint8       `json:"decimals"`
	SupplyRaw       exactUint64 `json:"supply_raw"`
	MintAuthority   string      `json:"mint_authority"`
	FreezeAuthority string      `json:"freeze_authority"`
}

type tokenStateJSON struct {
	Initialized bool        `json:"initialized"`
	Mint        string      `json:"mint"`
	Owner       string      `json:"owner"`
	AmountRaw   exactUint64 `json:"amount_raw"`
}

type result struct {
	GeneratedAtUTC          string            `json:"generated_at_utc"`
	GenesisHash             string            `json:"genesis_hash"`
	TokenProgram            string            `json:"token_program"`
	FeePayer                string            `json:"fee_payer"`
	Mint                    string            `json:"mint"`
	TokenAccount            string            `json:"token_account"`
	MetaplexMetadataAccount string            `json:"metaplex_metadata_account"`
	MasterEditionAccount    string            `json:"master_edition_account"`
	Owner                   string            `json:"owner"`
	Name                    string            `json:"name"`
	Symbol                  string            `json:"symbol"`
	URI                     string            `json:"uri"`
	SubmittedSignatures     []signatureRecord `json:"submitted_signatures"`
	NonFinalizedSignatures  []signatureRecord `json:"non_finalized_signatures"`
	FinalizedSignatures     []signatureRecord `json:"finalized_signatures"`
	MintState               mintStateJSON     `json:"mint_state"`
	TokenAccountState       tokenStateJSON    `json:"token_account_state"`
	FinalizedAndVerified    bool              `json:"finalized_and_verified"`
}

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr, realDependencies()); err != nil {
		fmt.Fprintln(os.Stderr, "token2022-nft-init:", err)
		os.Exit(1)
	}
}

func runCLI(arguments []string, stdout, stderr io.Writer, deps dependencies) error {
	flags := flag.NewFlagSet("token2022-nft-init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	keypairPath := flags.String("keypair", "", "fee payer, mint authority, and update authority keypair (required)")
	rpcURL := flags.String("url", "localhost", "Solana RPC URL or localhost/devnet/testnet/mainnet-beta")
	name := flags.String("name", "", "NFT name (required)")
	symbol := flags.String("symbol", "", "NFT symbol (optional)")
	uri := flags.String("uri", "", "NFT metadata URI (required)")
	ownerText := flags.String("owner", "", "NFT owner public key (default fee payer)")
	allowLive := flags.Bool("allow-live", false, "explicitly permit a non-loopback RPC endpoint")
	timeout := flags.Duration("timeout", 5*time.Minute, "whole initialization deadline")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *keypairPath == "" || *name == "" || *uri == "" {
		return errors.New("expected --keypair FILE, --name NAME, and --uri URI")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	resolvedURL, err := normalizeRPCURL(*rpcURL)
	if err != nil {
		return err
	}
	if !*allowLive && !loopbackRPC(resolvedURL) {
		return fmt.Errorf("refusing non-loopback RPC %q without --allow-live", *rpcURL)
	}
	if deps.loadKey == nil || deps.newClient == nil || deps.newSigner == nil || deps.now == nil {
		return errors.New("internal dependency is nil")
	}
	payer, err := deps.loadKey(*keypairPath)
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
	client := deps.newClient(resolvedURL)
	created, err := initialize(ctx, client, config{
		Payer:  payer,
		Owner:  owner,
		Name:   *name,
		Symbol: *symbol,
		URI:    *uri,
	}, deps.newSigner, deps.now)
	if err != nil {
		if created != nil && created.Mint != "" {
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
	return encoder.Encode(created)
}

func initialize(
	ctx context.Context,
	client rpcClient,
	cfg config,
	newSigner func() (svmtest.Signer, error),
	now func() time.Time,
) (*result, error) {
	if client == nil {
		return nil, errors.New("RPC client is nil")
	}
	if err := svmtest.ValidateSigner(cfg.Payer); err != nil {
		return nil, fmt.Errorf("invalid payer: %w", err)
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
	programInfo, err := client.GetAccountInfo(ctx, token2022.ProgramID)
	if err != nil {
		return nil, fmt.Errorf("read Token-2022 program: %w", err)
	}
	if programInfo == nil || !programInfo.Executable {
		return nil, fmt.Errorf("Token-2022 program %s is not a deployed executable", token2022.ProgramID)
	}

	mint, err := newSigner()
	if err != nil {
		return nil, fmt.Errorf("generate in-memory mint signer: %w", err)
	}
	if err := svmtest.ValidateSigner(mint); err != nil {
		return nil, fmt.Errorf("generated mint signer: %w", err)
	}
	if mint.PublicKey == cfg.Payer.PublicKey || mint.PublicKey == cfg.Owner {
		return nil, errors.New("generated signer collision")
	}

	tokenAccountAddress, _, err := associatedtoken.Derive(cfg.Owner, mint.PublicKey, token2022.ProgramID)
	if err != nil {
		return nil, fmt.Errorf("derive associated token account: %w", err)
	}
	metadataAddress, _, err := metaplex.DeriveMetadataAddress(mint.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive metaplex metadata account: %w", err)
	}
	masterEditionAddress, _, err := metaplex.DeriveMasterEditionAddress(mint.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive master edition account: %w", err)
	}

	created := &result{
		GeneratedAtUTC:          now().UTC().Format(time.RFC3339Nano),
		GenesisHash:             genesisHash,
		TokenProgram:            token2022.ProgramID.String(),
		FeePayer:                cfg.Payer.PublicKey.String(),
		Mint:                    mint.PublicKey.String(),
		TokenAccount:            tokenAccountAddress.String(),
		MetaplexMetadataAccount: metadataAddress.String(),
		MasterEditionAccount:    masterEditionAddress.String(),
		Owner:                   cfg.Owner.String(),
		Name:                    cfg.Name,
		Symbol:                  cfg.Symbol,
		URI:                     cfg.URI,
		SubmittedSignatures:     make([]signatureRecord, 0, 4),
		NonFinalizedSignatures:  make([]signatureRecord, 0, 1),
		FinalizedSignatures:     make([]signatureRecord, 0, 4),
	}

	if exists, err := client.GetAccountInfo(ctx, mint.PublicKey); err != nil {
		return created, fmt.Errorf("check generated mint address: %w", err)
	} else if exists != nil {
		return created, fmt.Errorf("generated mint address %s already exists", mint.PublicKey)
	}

	mintRentLamports, err := client.MinimumBalanceForRentExemption(ctx, token2022.MintSize)
	if err != nil {
		return created, fmt.Errorf("query mint rent: %w", err)
	}
	tokenAccountSize, err := token2022.CalculateAccountLen([]token2022.ExtensionType{token2022.ExtensionImmutableOwner})
	if err != nil {
		return created, fmt.Errorf("calculate token account size: %w", err)
	}
	tokenAccountRentLamports, err := client.MinimumBalanceForRentExemption(ctx, uint64(tokenAccountSize))
	if err != nil {
		return created, fmt.Errorf("query token account rent: %w", err)
	}
	metadataRentLamports, err := client.MinimumBalanceForRentExemption(ctx, metaplex.MetadataAccountSize)
	if err != nil {
		return created, fmt.Errorf("query metaplex metadata rent: %w", err)
	}
	createFeeBase, err := client.MinimumBalanceForRentExemption(ctx, metaplex.CreateFeeSizeScalar)
	if err != nil {
		return created, fmt.Errorf("query metaplex create fee: %w", err)
	}
	masterEditionRentLamports, err := client.MinimumBalanceForRentExemption(ctx, masterEditionAccountSize)
	if err != nil {
		return created, fmt.Errorf("query master edition rent: %w", err)
	}

	requiredBalance := minimumFeeReserveLamports
	for _, component := range []uint64{
		mintRentLamports, tokenAccountRentLamports, metadataRentLamports,
		createFeeBase + metaplex.CreateFeeOffsetLamports, masterEditionRentLamports,
	} {
		if component > math.MaxUint64-requiredBalance {
			return created, errors.New("rent requirement overflows uint64")
		}
		requiredBalance += component
	}
	balance, err := client.Balance(ctx, cfg.Payer.PublicKey)
	if err != nil {
		return created, fmt.Errorf("query payer balance: %w", err)
	}
	if balance < requiredBalance {
		return created, fmt.Errorf("payer balance %d is below rent plus fee reserve %d", balance, requiredBalance)
	}

	submit := func(stage string, signers []svmtest.Signer, instructions ...sdk.Instruction) error {
		signature, submitErr := client.SendInstructions(ctx, cfg.Payer, signers, instructions)
		if submitErr != nil {
			if signature == "" {
				return fmt.Errorf("%s failed before a transaction signature was returned; no retry was attempted: %w", stage, submitErr)
			}
			record := signatureRecord{Stage: stage, Signature: signature}
			created.NonFinalizedSignatures = append(created.NonFinalizedSignatures, record)
			return fmt.Errorf("%s has no finalized proof; no retry was attempted (non-finalized signature %q): %w", stage, signature, submitErr)
		}
		if signature == "" {
			return fmt.Errorf("%s returned no finalized signature", stage)
		}
		record := signatureRecord{Stage: stage, Signature: signature}
		created.SubmittedSignatures = append(created.SubmittedSignatures, record)
		created.FinalizedSignatures = append(created.FinalizedSignatures, record)
		return nil
	}

	// Stage 1: create and initialize the mint (decimals 0). Unlike
	// token2022-init's fungible flow, the freeze authority must be set (to
	// the payer here) rather than left unset: mpl-token-metadata's Create
	// rejects a NonFungible mint with no freeze authority (custom program
	// error 0x82, MetadataError::NoFreezeAuthoritySet).
	payerAuthority := token2022.OptionalPubkey{Set: true, Value: cfg.Payer.PublicKey}
	if err := submit("create_and_initialize_mint", []svmtest.Signer{mint},
		system.CreateAccount(cfg.Payer.PublicKey, mint.PublicKey, mintRentLamports, token2022.MintSize, token2022.ProgramID),
		token2022.InitializeMint2(mint.PublicKey, cfg.Payer.PublicKey, payerAuthority, 0),
	); err != nil {
		return created, err
	}
	if err := verifyWithRetry(ctx, "verify finalized mint", func() error {
		_, err := verifyMint(ctx, client, mint.PublicKey, mintRentLamports, token2022.Mint{
			MintAuthority: payerAuthority, FreezeAuthority: payerAuthority, Decimals: 0, Initialized: true,
		}, true)
		return err
	}); err != nil {
		return created, err
	}

	// Stage 2: create the associated token account.
	createATA, ataAddress, err := associatedtoken.CreateIdempotent(cfg.Payer.PublicKey, cfg.Owner, mint.PublicKey, token2022.ProgramID)
	if err != nil {
		return created, fmt.Errorf("build associated token account instruction: %w", err)
	}
	if ataAddress != tokenAccountAddress {
		return created, errors.New("derived associated token account mismatch")
	}
	if err := submit("create_and_initialize_account", nil, createATA); err != nil {
		return created, err
	}
	if err := verifyWithRetry(ctx, "verify finalized token account", func() error {
		return verifyTokenAccount(ctx, client, tokenAccountAddress, tokenAccountRentLamports, mint.PublicKey, cfg.Owner, 0)
	}); err != nil {
		return created, err
	}

	// Stage 3: mint the single unit. This must land before stage 4 —
	// mpl-token-metadata's create_master_edition_v3 processor requires
	// mint.supply == 1 already (EditionsMustHaveExactlyOneToken), and
	// Create itself never mints tokens.
	mintTo, err := token2022.MintTo(mint.PublicKey, tokenAccountAddress, cfg.Payer.PublicKey, nil, 1)
	if err != nil {
		return created, fmt.Errorf("build MintTo instruction: %w", err)
	}
	if err := submit("mint_to", nil, mintTo); err != nil {
		return created, err
	}
	if err := verifyWithRetry(ctx, "verify finalized mint supply", func() error {
		_, err := verifyMint(ctx, client, mint.PublicKey, mintRentLamports, token2022.Mint{
			MintAuthority: payerAuthority, FreezeAuthority: payerAuthority, Decimals: 0, Initialized: true, Supply: 1,
		}, true)
		return err
	}); err != nil {
		return created, err
	}
	if err := verifyWithRetry(ctx, "verify finalized token balance", func() error {
		return verifyTokenAccount(ctx, client, tokenAccountAddress, tokenAccountRentLamports, mint.PublicKey, cfg.Owner, 1)
	}); err != nil {
		return created, err
	}

	// Stage 4: create the Metadata + Master Edition accounts together. This
	// permanently transfers mint authority to the master edition PDA,
	// capping supply at 1 forever — no separate "disable mint authority"
	// step is needed or possible afterward.
	createNFT, gotMetadata, gotMasterEdition, err := metaplex.CreateNFTV1(
		mint.PublicKey, cfg.Payer.PublicKey, cfg.Payer.PublicKey, cfg.Payer.PublicKey, token2022.ProgramID, false,
		cfg.Name, cfg.Symbol, cfg.URI, false,
	)
	if err != nil {
		return created, fmt.Errorf("build metaplex NFT instruction: %w", err)
	}
	if gotMetadata != metadataAddress || gotMasterEdition != masterEditionAddress {
		return created, errors.New("derived metaplex metadata/master-edition account mismatch")
	}
	if err := submit("create_metaplex_nft", nil, createNFT); err != nil {
		return created, err
	}
	// mpl-token-metadata's create_master_edition_v3 transfers mint
	// authority to the master edition PDA as a documented side effect (see
	// metaplex.CreateNFTV1's doc comment); what it does to freeze authority
	// is not pinned anywhere in this repo, so that field is read back and
	// reported as-observed below rather than asserted against a guess.
	masterEditionAuthority := token2022.OptionalPubkey{Set: true, Value: masterEditionAddress}
	var finalMint token2022.Mint
	if err := verifyWithRetry(ctx, "verify finalized mint authority transfer", func() error {
		var verifyErr error
		finalMint, verifyErr = verifyMint(ctx, client, mint.PublicKey, mintRentLamports, token2022.Mint{
			MintAuthority: masterEditionAuthority, Decimals: 0, Initialized: true, Supply: 1,
		}, false)
		return verifyErr
	}); err != nil {
		return created, err
	}
	if err := verifyWithRetry(ctx, "verify finalized metaplex metadata", func() error {
		return verifyMetaplexMetadata(ctx, client, metadataAddress, mint.PublicKey, cfg.Name, cfg.Symbol)
	}); err != nil {
		return created, err
	}
	if err := verifyWithRetry(ctx, "verify finalized master edition", func() error {
		return verifyMasterEdition(ctx, client, masterEditionAddress, masterEditionRentLamports)
	}); err != nil {
		return created, err
	}

	created.MintState = mintStateJSON{
		Initialized: finalMint.Initialized, Decimals: finalMint.Decimals, SupplyRaw: exactUint64(finalMint.Supply),
		MintAuthority: formatOptionalPubkey(finalMint.MintAuthority), FreezeAuthority: formatOptionalPubkey(finalMint.FreezeAuthority),
	}
	created.TokenAccountState = tokenStateJSON{
		Initialized: true, Mint: mint.PublicKey.String(), Owner: cfg.Owner.String(), AmountRaw: exactUint64(1),
	}
	created.FinalizedAndVerified = true
	return created, nil
}

// verifyMint checks mint state against want and returns the fully decoded
// mint. When checkFreezeAuthority is false, the caller has an unpinned
// expectation for that field (see the stage-4 call site) and only wants the
// decoded value back, not an assertion against a guess.
func verifyMint(ctx context.Context, client rpcClient, mint sdk.Pubkey, rentLamports uint64, want token2022.Mint, checkFreezeAuthority bool) (token2022.Mint, error) {
	account, err := client.GetAccountInfo(ctx, mint)
	if err != nil {
		return token2022.Mint{}, err
	}
	if account == nil {
		return token2022.Mint{}, errors.New("mint account not found")
	}
	if account.Owner != token2022.ProgramID.String() {
		return token2022.Mint{}, fmt.Errorf("mint owner %q, want %q", account.Owner, token2022.ProgramID)
	}
	if account.Lamports < rentLamports {
		return token2022.Mint{}, fmt.Errorf("mint lamports %d below rent requirement %d", account.Lamports, rentLamports)
	}
	raw, err := decodeAccountData(account)
	if err != nil {
		return token2022.Mint{}, err
	}
	gotWithExtensions, err := token2022.DecodeMintWithExtensions(raw)
	if err != nil {
		return token2022.Mint{}, err
	}
	got := gotWithExtensions.Base
	if got.Initialized != want.Initialized {
		return token2022.Mint{}, fmt.Errorf("mint initialized=%v, want %v", got.Initialized, want.Initialized)
	}
	if got.Decimals != want.Decimals {
		return token2022.Mint{}, fmt.Errorf("mint decimals %d, want %d", got.Decimals, want.Decimals)
	}
	if got.MintAuthority != want.MintAuthority {
		return token2022.Mint{}, fmt.Errorf("mint authority mismatch: got %s want %s", formatOptionalPubkey(got.MintAuthority), formatOptionalPubkey(want.MintAuthority))
	}
	if checkFreezeAuthority && got.FreezeAuthority != want.FreezeAuthority {
		return token2022.Mint{}, fmt.Errorf("mint freeze authority mismatch: got %s want %s", formatOptionalPubkey(got.FreezeAuthority), formatOptionalPubkey(want.FreezeAuthority))
	}
	if got.Supply != want.Supply {
		return token2022.Mint{}, fmt.Errorf("mint supply %d, want %d", got.Supply, want.Supply)
	}
	canonical, err := token2022.EncodeMintWithExtensions(gotWithExtensions)
	if err != nil {
		return token2022.Mint{}, fmt.Errorf("re-encode mint: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return token2022.Mint{}, errors.New("mint data is not byte-for-byte canonical")
	}
	return got, nil
}

func verifyTokenAccount(ctx context.Context, client rpcClient, tokenAccount sdk.Pubkey, rentLamports uint64, mint, owner sdk.Pubkey, wantAmount uint64) error {
	account, err := client.GetAccountInfo(ctx, tokenAccount)
	if err != nil {
		return err
	}
	if account == nil {
		return errors.New("token account not found")
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
	if got.Mint != mint {
		return fmt.Errorf("token account mint %s, want %s", got.Mint, mint)
	}
	if got.Owner != owner {
		return fmt.Errorf("token account owner %s, want %s", got.Owner, owner)
	}
	if got.Amount != wantAmount {
		return fmt.Errorf("token account amount %d, want %d", got.Amount, wantAmount)
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
// name, symbol) — see the identical, more-detailed comment in
// examples/token2022/cmd/token2022-init for why this can't be byte-canonical.
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
	const keyAndAuthoritySize = 1 + sdk.PubkeySize
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
	// The program right-pads name/symbol/uri with null bytes ("puffing") on
	// every write, so trailing NULs are expected.
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

// verifyMasterEdition checks the fields this tool cares about — supply (the
// count of prints made from this edition, must be 0) and max_supply (must
// be Some(0), i.e. PrintSupply::Zero: no further prints are ever possible).
// It deliberately does not decode the leading Key discriminant byte, since
// its exact ordinal is not pinned anywhere else in this repo.
func verifyMasterEdition(ctx context.Context, client rpcClient, masterEditionAccount sdk.Pubkey, rentLamports uint64) error {
	account, err := client.GetAccountInfo(ctx, masterEditionAccount)
	if err != nil {
		return err
	}
	if account == nil {
		return errors.New("master edition account not found")
	}
	if account.Owner != metaplex.ProgramID.String() {
		return fmt.Errorf("master edition account owner %q, want %q", account.Owner, metaplex.ProgramID)
	}
	if account.Lamports < rentLamports {
		return fmt.Errorf("master edition lamports %d below rent requirement %d", account.Lamports, rentLamports)
	}
	raw, err := decodeAccountData(account)
	if err != nil {
		return err
	}
	if len(raw) < masterEditionAccountSize {
		return fmt.Errorf("master edition account length %d, want at least %d", len(raw), masterEditionAccountSize)
	}
	supply := binary.LittleEndian.Uint64(raw[1:9])
	if supply != 0 {
		return fmt.Errorf("master edition print supply %d, want 0", supply)
	}
	if raw[9] != 1 {
		return fmt.Errorf("master edition max_supply option byte %d, want 1 (Some)", raw[9])
	}
	maxSupply := binary.LittleEndian.Uint64(raw[10:18])
	if maxSupply != 0 {
		return fmt.Errorf("master edition max_supply %d, want 0 (PrintSupply::Zero)", maxSupply)
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

func formatOptionalPubkey(value token2022.OptionalPubkey) string {
	if !value.Set {
		return "(none)"
	}
	return value.Value.String()
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
		}
		return fmt.Errorf("%s: %w", stage, err)
	}
	return fmt.Errorf("%s: verification retry budget exhausted", stage)
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
		return "", errors.New("RPC URL must use http or https")
	}
	return raw, nil
}
