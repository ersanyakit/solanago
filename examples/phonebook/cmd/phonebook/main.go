package main

import (
	"context"
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	gosoldeploy "github.com/ersany/go-solana/deploy"
	"github.com/ersany/go-solana/sdk"
	"github.com/ersany/go-solana/sdk/system"
	"github.com/ersany/go-solana/svmtest"
)

const (
	phonebookConfigDataLen = uint64(74)
	phonebookDataLen       = uint64(1315)
	maxNameLen            = 32
	maxContacts           = 20
	defaultFeeLamports    = uint64(100000)
)

type runtimeConfig struct {
	program sdk.Pubkey
	url     string
}

type configState struct {
	Initialized bool
	Admin       sdk.Pubkey
	Treasury    sdk.Pubkey
	FeeLamports uint64
}

type contactEntry struct {
	Address sdk.Pubkey
	Name    string
}

type phonebookState struct {
	Initialized bool
	Owner       sdk.Pubkey
	Count       uint8
	Contacts    []contactEntry
}

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "phonebook:", err)
		os.Exit(1)
	}
}

func runCLI(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("phonebook", flag.ContinueOnError)
	flags.SetOutput(stderr)

	rpcURL := flags.String("url", "devnet", "Solana RPC URL or localhost/devnet/testnet/mainnet-beta")
	programText := flags.String("program", "", "deployed phonebook program id (required)")
	allowLive := flags.Bool("allow-live", false, "explicitly permit non-loopback RPC endpoint")
	timeout := flags.Duration("timeout", 5*time.Minute, "overall operation timeout")

	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() == 0 || *programText == "" {
		return errors.New("expected one command: init-config | init-phonebook | add-contact | withdraw")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if !*allowLive && !loopbackRPC(*rpcURL) && *rpcURL != "d" && *rpcURL != "t" && *rpcURL != "m" {
		return fmt.Errorf("refusing non-loopback RPC %q without --allow-live", *rpcURL)
	}

	resolvedURL, err := normalizeRPCURL(*rpcURL)
	if err != nil {
		return err
	}
	program, err := sdk.ParsePubkey(*programText)
	if err != nil {
		return fmt.Errorf("invalid --program: %w", err)
	}

	command := flags.Arg(0)
	commandArgs := flags.Args()[1:]
	client := svmtest.Client{URL: resolvedURL}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cfg := runtimeConfig{
		program: program,
		url:     resolvedURL,
	}

	switch command {
	case "init-config":
		return runInitConfig(ctx, client, cfg, commandArgs, stdout, stderr)
	case "init-phonebook":
		return runInitPhonebook(ctx, client, cfg, commandArgs, stdout, stderr)
	case "add-contact":
		return runAddContact(ctx, client, cfg, commandArgs, stdout, stderr)
	case "withdraw":
		return runWithdraw(ctx, client, cfg, commandArgs, stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q (expected init-config | init-phonebook | add-contact | withdraw)", command)
	}
}

func runInitConfig(ctx context.Context, client svmtest.Client, cfg runtimeConfig, arguments []string, stdout, stderr io.Writer) error {
	_ = stderr
	fs := flag.NewFlagSet("init-config", flag.ContinueOnError)
	fs.SetOutput(stderr)

	payerPath := fs.String("payer", "", "payer and optional config fee signer")
	adminPath := fs.String("admin-keypair", "", "admin keypair (defaults to --payer)")
	configPath := fs.String("config-keypair", "", "program-owned config account keypair")
	treasuryText := fs.String("treasury", "", "treasury pubkey (defaults to --admin-keypair)")
	fee := fs.Uint64("fee-lamports", defaultFeeLamports, "registration fee in lamports (default 100000)")

	if err := fs.Parse(arguments); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional args")
	}
	if *payerPath == "" || *configPath == "" {
		return errors.New("required --payer and --config-keypair")
	}
	if *adminPath == "" {
		*adminPath = *payerPath
	}
	if *fee == 0 {
		return errors.New("fee must be > 0")
	}

	payer, err := gosoldeploy.LoadKeypair(*payerPath)
	if err != nil {
		return fmt.Errorf("invalid --payer: %w", err)
	}
	admin, err := gosoldeploy.LoadKeypair(*adminPath)
	if err != nil {
		return fmt.Errorf("invalid --admin-keypair: %w", err)
	}
	configSigner, err := gosoldeploy.LoadKeypair(*configPath)
	if err != nil {
		return fmt.Errorf("invalid --config-keypair: %w", err)
	}

	treasury := admin.PublicKey
	if *treasuryText != "" {
		treasury, err = sdk.ParsePubkey(*treasuryText)
		if err != nil {
			return fmt.Errorf("invalid --treasury: %w", err)
		}
	}

	if _, err := ensureProgramOwnedAccount(ctx, client, payer, configSigner, cfg.program, phonebookConfigDataLen); err != nil {
		return err
	}

	instructionData := make([]byte, 9)
	instructionData[0] = 1
	binary.LittleEndian.PutUint64(instructionData[1:], *fee)
	txInstruction := sdk.Instruction{
		ProgramID: cfg.program,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(configSigner.PublicKey, true),
			sdk.Writable(admin.PublicKey, true),
			sdk.Readonly(treasury, false),
		},
		Data: instructionData,
	}
	signers := uniqueSigners(payer, admin)
	signature, err := client.SendInstructions(ctx, payer, signers, []sdk.Instruction{txInstruction})
	if err != nil {
		return fmt.Errorf("init-config submit: %w", err)
	}

	fmt.Fprintf(stdout, "config account: %s\n", configSigner.PublicKey)
	fmt.Fprintf(stdout, "admin: %s\n", admin.PublicKey)
	fmt.Fprintf(stdout, "treasury: %s\n", treasury)
	fmt.Fprintf(stdout, "fee-lamports: %d\n", *fee)
	fmt.Fprintf(stdout, "signature: %s\n", signature)
	return nil
}

func runInitPhonebook(ctx context.Context, client svmtest.Client, cfg runtimeConfig, arguments []string, stdout, stderr io.Writer) error {
	_ = cfg
	_ = stderr
	fs := flag.NewFlagSet("init-phonebook", flag.ContinueOnError)
	fs.SetOutput(stderr)

	payerPath := fs.String("payer", "", "payer keypair (and default owner)")
	ownerPath := fs.String("owner-keypair", "", "phonebook owner keypair (defaults to --payer)")
	phonebookPath := fs.String("phonebook-keypair", "", "phonebook account keypair")

	if err := fs.Parse(arguments); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional args")
	}
	if *phonebookPath == "" || *payerPath == "" {
		return errors.New("required --payer and --phonebook-keypair")
	}
	if *ownerPath == "" {
		*ownerPath = *payerPath
	}

	payer, err := gosoldeploy.LoadKeypair(*payerPath)
	if err != nil {
		return fmt.Errorf("invalid --payer: %w", err)
	}
	owner, err := gosoldeploy.LoadKeypair(*ownerPath)
	if err != nil {
		return fmt.Errorf("invalid --owner-keypair: %w", err)
	}
	phonebookSigner, err := gosoldeploy.LoadKeypair(*phonebookPath)
	if err != nil {
		return fmt.Errorf("invalid --phonebook-keypair: %w", err)
	}

	if _, err := ensureProgramOwnedAccount(ctx, client, payer, phonebookSigner, runtimeProgram(cfg), phonebookDataLen); err != nil {
		return err
	}

	instruction := sdk.Instruction{
		ProgramID: runtimeProgram(cfg),
		Accounts: []sdk.AccountMeta{
			sdk.Writable(phonebookSigner.PublicKey, true),
			sdk.Writable(owner.PublicKey, true),
		},
		Data: []byte{2},
	}
	signers := uniqueSigners(payer, owner)
	signature, err := client.SendInstructions(ctx, payer, signers, []sdk.Instruction{instruction})
	if err != nil {
		return fmt.Errorf("init-phonebook submit: %w", err)
	}

	fmt.Fprintf(stdout, "phonebook account: %s\n", phonebookSigner.PublicKey)
	fmt.Fprintf(stdout, "owner: %s\n", owner.PublicKey)
	fmt.Fprintf(stdout, "signature: %s\n", signature)
	return nil
}

func runAddContact(ctx context.Context, client svmtest.Client, cfg runtimeConfig, arguments []string, stdout, stderr io.Writer) error {
	_ = stderr
	fs := flag.NewFlagSet("add-contact", flag.ContinueOnError)
	fs.SetOutput(stderr)

	payerPath := fs.String("payer", "", "payer keypair (defaults to --owner-keypair)")
	ownerPath := fs.String("owner-keypair", "", "owner keypair (required)")
	configText := fs.String("config", "", "config account pubkey (required)")
	phonebookText := fs.String("phonebook", "", "phonebook pubkey (required)")
	targetText := fs.String("address", "", "contact wallet address (required)")
	nameText := fs.String("name", "", "contact name (required, max 32 bytes)")

	if err := fs.Parse(arguments); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional args")
	}
	if *ownerPath == "" {
		return errors.New("required --owner-keypair")
	}
	if *payerPath == "" {
		*payerPath = *ownerPath
	}
	if *configText == "" || *phonebookText == "" {
		return errors.New("required --config and --phonebook")
	}
	if *targetText == "" {
		return errors.New("required --address")
	}
	if *nameText == "" {
		return errors.New("required --name")
	}
	name := strings.TrimSpace(*nameText)
	if len([]byte(name)) > maxNameLen {
		return fmt.Errorf("name must be <= %d bytes", maxNameLen)
	}
	if name == "" {
		return errors.New("name must not be empty")
	}

	payer, err := gosoldeploy.LoadKeypair(*payerPath)
	if err != nil {
		return fmt.Errorf("invalid --payer: %w", err)
	}
	owner, err := gosoldeploy.LoadKeypair(*ownerPath)
	if err != nil {
		return fmt.Errorf("invalid --owner-keypair: %w", err)
	}
	configAddress, err := sdk.ParsePubkey(*configText)
	if err != nil {
		return fmt.Errorf("invalid --config: %w", err)
	}
	phonebookAddress, err := sdk.ParsePubkey(*phonebookText)
	if err != nil {
		return fmt.Errorf("invalid --phonebook: %w", err)
	}
	targetAddress, err := sdk.ParsePubkey(*targetText)
	if err != nil {
		return fmt.Errorf("invalid --address: %w", err)
	}

	config, err := fetchConfigState(ctx, client, configAddress)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if !config.Initialized {
		return errors.New("config is not initialized")
	}

	phonebookInfo, err := client.GetAccountInfo(ctx, phonebookAddress)
	if err != nil {
		return fmt.Errorf("read phonebook: %w", err)
	}
	if phonebookInfo == nil {
		return errors.New("phonebook account not found; run init-phonebook first")
	}
	phonebookData, err := phonebookInfo.DataBytes()
	if err != nil {
		return fmt.Errorf("decode phonebook: %w", err)
	}
	if uint64(len(phonebookData)) < phonebookDataLen {
		return fmt.Errorf("phonebook account data too short: %d", len(phonebookData))
	}

	phonebookState, err := parsePhonebookState(phonebookData)
	if err != nil {
		return fmt.Errorf("decode phonebook state: %w", err)
	}
	if !phonebookState.Initialized {
		return errors.New("phonebook not initialized")
	}
	if !bytes.Equal(phonebookData[2:34], owner.PublicKey[:]) {
		return errors.New("phonebook owner mismatch")
	}
	if phonebookState.Count > maxContacts {
		return fmt.Errorf("invalid phonebook state count: %d", phonebookState.Count)
	}
	existingContact := false
	for _, existing := range phonebookState.Contacts {
		if existing.Address == targetAddress {
			existingContact = true
			break
		}
	}
	if !existingContact && phonebookState.Count >= maxContacts {
		return fmt.Errorf("phonebook full: max %d contacts", maxContacts)
	}

	instructionData := make([]byte, 65)
	instructionData[0] = 3
	copy(instructionData[1:33], targetAddress[:])
	copy(instructionData[33:], []byte(name))

	txInstruction := sdk.Instruction{
		ProgramID: cfg.program,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(phonebookAddress, true),
			sdk.Writable(owner.PublicKey, true),
			sdk.Readonly(configAddress, false),
			sdk.Writable(config.Treasury, true),
			sdk.Readonly(system.ProgramID, false),
		},
		Data: instructionData,
	}

	signers := uniqueSigners(payer, owner)
	signature, err := client.SendInstructions(ctx, payer, signers, []sdk.Instruction{txInstruction})
	if err != nil {
		return fmt.Errorf("add-contact submit: %w", err)
	}
	updated, err := fetchPhonebookState(ctx, client, phonebookAddress)
	if err != nil {
		return fmt.Errorf("read updated phonebook: %w", err)
	}

	fmt.Fprintf(stdout, "phonebook: %s\n", phonebookAddress)
	fmt.Fprintf(stdout, "signature: %s\n", signature)
	fmt.Fprintf(stdout, "owner: %s\n", owner.PublicKey)
	fmt.Fprintf(stdout, "contacts: %d/%d\n", updated.Count, maxContacts)
	for index, contact := range updated.Contacts {
		fmt.Fprintf(stdout, "  %d) %s => %s\n", index+1, contact.Address, contact.Name)
	}
	return nil
}

func runWithdraw(ctx context.Context, client svmtest.Client, cfg runtimeConfig, arguments []string, stdout, stderr io.Writer) error {
	_ = stderr
	fs := flag.NewFlagSet("withdraw", flag.ContinueOnError)
	fs.SetOutput(stderr)

	adminPath := fs.String("admin-keypair", "", "admin keypair (required)")
	configText := fs.String("config", "", "config account pubkey (required)")
	destinationText := fs.String("destination", "", "destination pubkey (required)")
	amount := fs.Uint64("amount-lamports", 0, "lamports to withdraw (0 = full balance)")

	if err := fs.Parse(arguments); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional args")
	}
	if *adminPath == "" || *configText == "" {
		return errors.New("required --admin-keypair and --config")
	}

	admin, err := gosoldeploy.LoadKeypair(*adminPath)
	if err != nil {
		return fmt.Errorf("invalid --admin-keypair: %w", err)
	}
	configAddress, err := sdk.ParsePubkey(*configText)
	if err != nil {
		return fmt.Errorf("invalid --config: %w", err)
	}
	destination := admin.PublicKey
	if *destinationText != "" {
		destination, err = sdk.ParsePubkey(*destinationText)
		if err != nil {
			return fmt.Errorf("invalid --destination: %w", err)
		}
	}
	config, err := fetchConfigState(ctx, client, configAddress)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if !config.Initialized {
		return errors.New("config is not initialized")
	}
	if config.Admin != admin.PublicKey {
		return errors.New("admin does not match config.admin")
	}
	if config.Treasury != admin.PublicKey {
		return errors.New("withdraw requires treasury==admin in this sample")
	}
	if *destinationText == "" {
		return errors.New("required --destination")
	}

	instructionData := make([]byte, 9)
	instructionData[0] = 4
	binary.LittleEndian.PutUint64(instructionData[1:], *amount)
	txInstruction := sdk.Instruction{
		ProgramID: cfg.program,
		Accounts: []sdk.AccountMeta{
			sdk.Readonly(configAddress, false),
			sdk.Writable(admin.PublicKey, true),
			sdk.Writable(destination, true),
			sdk.Readonly(system.ProgramID, false),
		},
		Data: instructionData,
	}
	signature, err := client.SendInstructions(ctx, admin, []svmtest.Signer{admin}, []sdk.Instruction{txInstruction})
	if err != nil {
		return fmt.Errorf("withdraw submit: %w", err)
	}

	if *amount == 0 {
		fmt.Fprintf(stdout, "withdrawed all lamports from treasury to %s\n", destination)
	} else {
		fmt.Fprintf(stdout, "withdrawed %d lamports to %s\n", *amount, destination)
	}
	fmt.Fprintf(stdout, "signature: %s\n", signature)
	return nil
}

func ensureProgramOwnedAccount(ctx context.Context, client svmtest.Client, payer svmtest.Signer, accountSigner svmtest.Signer, program sdk.Pubkey, size uint64) (bool, error) {
	existing, err := client.GetAccountInfo(ctx, accountSigner.PublicKey)
	if err != nil {
		return false, err
	}
	if existing == nil {
		rent, err := client.MinimumBalanceForRentExemption(ctx, size)
		if err != nil {
			return false, fmt.Errorf("query rent for %s: %w", accountSigner.PublicKey, err)
		}
		create := system.CreateAccount(payer.PublicKey, accountSigner.PublicKey, rent, size, program)
		signature, err := client.SendInstructions(ctx, payer, []svmtest.Signer{accountSigner}, []sdk.Instruction{create})
		if err != nil {
			return false, fmt.Errorf("create %s: %w", accountSigner.PublicKey, err)
		}
		_ = signature
		return true, nil
	}
	if existing.Executable {
		return false, fmt.Errorf("account %s is executable; expected data account", accountSigner.PublicKey)
	}
	if existing.Owner != program.String() {
		return false, fmt.Errorf("account %s owner is %s, expected %s", accountSigner.PublicKey, existing.Owner, program.String())
	}
	raw, err := existing.DataBytes()
	if err == nil && uint64(len(raw)) != size {
		return false, fmt.Errorf("account %s data length mismatch: %d != %d", accountSigner.PublicKey, len(raw), size)
	}
	return false, nil
}

func fetchConfigState(ctx context.Context, client svmtest.Client, address sdk.Pubkey) (configState, error) {
	accountInfo, err := client.GetAccountInfo(ctx, address)
	if err != nil {
		return configState{}, err
	}
	if accountInfo == nil {
		return configState{}, errors.New("account not found")
	}
	raw, err := accountInfo.DataBytes()
	if err != nil {
		return configState{}, err
	}
	return parseConfigState(raw)
}

func fetchPhonebookState(ctx context.Context, client svmtest.Client, address sdk.Pubkey) (phonebookState, error) {
	accountInfo, err := client.GetAccountInfo(ctx, address)
	if err != nil {
		return phonebookState{}, err
	}
	if accountInfo == nil {
		return phonebookState{}, errors.New("account not found")
	}
	raw, err := accountInfo.DataBytes()
	if err != nil {
		return phonebookState{}, err
	}
	return parsePhonebookState(raw)
}

func parseConfigState(raw []byte) (configState, error) {
	if len(raw) < int(phonebookConfigDataLen) {
		return configState{}, fmt.Errorf("config data length is too short: %d", len(raw))
	}
	initialized := raw[0] == 1
	admin, err := sdk.PubkeyFromBytes(raw[2:34])
	if err != nil {
		return configState{}, err
	}
	treasury, err := sdk.PubkeyFromBytes(raw[34:66])
	if err != nil {
		return configState{}, err
	}
	fee := binary.LittleEndian.Uint64(raw[66:74])
	return configState{
		Initialized: initialized,
		Admin:       admin,
		Treasury:    treasury,
		FeeLamports: fee,
	}, nil
}

func parsePhonebookState(raw []byte) (phonebookState, error) {
	if len(raw) < int(phonebookDataLen) {
		return phonebookState{}, fmt.Errorf("phonebook data length is too short: %d", len(raw))
	}
	owner, err := sdk.PubkeyFromBytes(raw[2:34])
	if err != nil {
		return phonebookState{}, err
	}
	count := raw[34]
	entriesOffset := uint64(35)
	entrySize := uint64(64)
	state := phonebookState{
		Initialized: raw[0] == 1,
		Owner:       owner,
		Count:       count,
		Contacts:    make([]contactEntry, 0, count),
	}
	for index := 0; index < int(count) && index < maxContacts; index++ {
		base := entriesOffset + uint64(index)*entrySize
		address, err := sdk.PubkeyFromBytes(raw[base : base+32])
		if err != nil {
			return phonebookState{}, err
		}
		name := strings.TrimRight(string(raw[base+32:base+64]), "\x00")
		state.Contacts = append(state.Contacts, contactEntry{Address: address, Name: name})
	}
	return state, nil
}

func runtimeProgram(cfg runtimeConfig) sdk.Pubkey {
	return cfg.program
}

func uniqueSigners(values ...svmtest.Signer) []svmtest.Signer {
	seen := make(map[string]bool, len(values))
	result := make([]svmtest.Signer, 0, len(values))
	for _, value := range values {
		public := value.PublicKey.String()
		if seen[public] {
			continue
		}
		seen[public] = true
		result = append(result, value)
	}
	return result
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

func bytesAsPubkey(raw []byte) sdk.Pubkey {
	key, _ := sdk.PubkeyFromBytes(raw)
	return key
}
