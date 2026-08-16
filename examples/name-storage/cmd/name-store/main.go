package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	gosoldeploy "github.com/ersanyakit/go-solana/deploy"
	"github.com/ersanyakit/go-solana/sdk"
	"github.com/ersanyakit/go-solana/sdk/system"
	"github.com/ersanyakit/go-solana/svmtest"
)

const (
	storageDataLen     = 65
	nameFieldCapacity  = 32
	maxInputNameLength = 32
)

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "name-storage:", err)
		os.Exit(1)
	}
}

func runCLI(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("name-store", flag.ContinueOnError)
	flags.SetOutput(stderr)
	urlText := flags.String("url", "devnet", "Solana RPC URL or localhost/devnet/testnet/mainnet-beta")
	programText := flags.String("program", "", "deployed name-storage program id (required)")
	payerKeypair := flags.String("payer", "", "payer + fee payer keypair path (required)")
	storageKeypair := flags.String("storage-keypair", "", "storage account keypair path (signer for storage writes)")
	nameText := flags.String("name", "", "first name")
	surnameText := flags.String("surname", "", "last name")
	allowLive := flags.Bool("allow-live", false, "explicitly permit non-loopback RPC endpoint")
	timeout := flags.Duration("timeout", 5*time.Minute, "overall operation timeout")

	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *programText == "" || *payerKeypair == "" || *storageKeypair == "" {
		return errors.New("expected --program, --payer, --storage-keypair, and --name/--surname")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}

	name := strings.TrimSpace(*nameText)
	surname := strings.TrimSpace(*surnameText)
	if name == "" {
		reader := bufio.NewReader(os.Stdin)
		value, err := readLine(reader, "Ad: ")
		if err != nil {
			return err
		}
		name = strings.TrimSpace(value)
	}
	if surname == "" {
		reader := bufio.NewReader(os.Stdin)
		value, err := readLine(reader, "Soyad: ")
		if err != nil {
			return err
		}
		surname = strings.TrimSpace(value)
	}
	if name == "" || surname == "" {
		return errors.New("name and surname must be non-empty")
	}
	if len([]byte(name)) > maxInputNameLength {
		return fmt.Errorf("name must be <= %d bytes", maxInputNameLength)
	}
	if len([]byte(surname)) > maxInputNameLength {
		return fmt.Errorf("surname must be <= %d bytes", maxInputNameLength)
	}

	resolvedURL, err := normalizeRPCURL(*urlText)
	if err != nil {
		return err
	}
	if !*allowLive && !loopbackRPC(resolvedURL) {
		return fmt.Errorf("refusing non-loopback RPC %q without --allow-live", *urlText)
	}

	program, err := sdk.ParsePubkey(*programText)
	if err != nil {
		return fmt.Errorf("invalid --program: %w", err)
	}
	payer, err := gosoldeploy.LoadKeypair(*payerKeypair)
	if err != nil {
		return fmt.Errorf("invalid --payer keypair: %w", err)
	}
	storageSigner, err := gosoldeploy.LoadKeypair(*storageKeypair)
	if err != nil {
		return fmt.Errorf("invalid --storage-keypair keypair: %w", err)
	}

	client := svmtest.Client{URL: resolvedURL}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if _, err := ensureStorageAccount(ctx, client, payer, storageSigner, program); err != nil {
		return err
	}

	instructionData, err := buildInstruction(name, surname)
	if err != nil {
		return err
	}
	writeInstruction := sdk.Instruction{
		ProgramID: program,
		Accounts:  []sdk.AccountMeta{sdk.Writable(storageSigner.PublicKey, true)},
		Data:      instructionData,
	}

	writeSig, err := client.SendInstructions(ctx, payer, []svmtest.Signer{storageSigner}, []sdk.Instruction{writeInstruction})
	if err != nil {
		return fmt.Errorf("write instruction: %w", err)
	}
	fmt.Fprintf(stdout, "write signature: %s\n", writeSig)

	info, err := client.GetAccountInfo(ctx, storageSigner.PublicKey)
	if err != nil {
		return fmt.Errorf("read storage account after write: %w", err)
	}
	if info == nil {
		return errors.New("storage account still not found after write")
	}
	rawData, err := info.DataBytes()
	if err != nil {
		return fmt.Errorf("decode storage account data: %w", err)
	}
	storedName, storedSurname, err := decodeStored(rawData)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "storage account: %s\n", storageSigner.PublicKey)
	fmt.Fprintf(stdout, "stored name: %s\nstored surname: %s\n", storedName, storedSurname)
	return nil
}

func ensureStorageAccount(ctx context.Context, client svmtest.Client, payer svmtest.Signer, storageSigner svmtest.Signer, program sdk.Pubkey) (string, error) {
	existing, err := client.GetAccountInfo(ctx, storageSigner.PublicKey)
	if err != nil {
		return "", err
	}
	if existing == nil {
		rent, err := client.MinimumBalanceForRentExemption(ctx, storageDataLen)
		if err != nil {
			return "", fmt.Errorf("query rent exemption: %w", err)
		}
		createStorage := system.CreateAccount(payer.PublicKey, storageSigner.PublicKey, rent, storageDataLen, program)
		sig, err := client.SendInstructions(ctx, payer, []svmtest.Signer{storageSigner}, []sdk.Instruction{createStorage})
		if err != nil {
			return "", fmt.Errorf("create storage account: %w", err)
		}
		return sig, nil
	}
	if existing.Owner != program.String() {
		return "", fmt.Errorf("storage account owner %q does not match program %q", existing.Owner, program.String())
	}
	return "", nil
}

func buildInstruction(name, surname string) ([]byte, error) {
	nameBytes := []byte(name)
	surnameBytes := []byte(surname)
	if len(nameBytes) > nameFieldCapacity || len(surnameBytes) > nameFieldCapacity {
		return nil, fmt.Errorf("name/surname must be <= %d bytes", nameFieldCapacity)
	}
	data := make([]byte, storageDataLen)
	data[0] = 1
	copy(data[1:33], nameBytes)
	copy(data[33:], surnameBytes)
	return data, nil
}

func decodeStored(raw []byte) (string, string, error) {
	if len(raw) < storageDataLen {
		return "", "", fmt.Errorf("storage data too short: %d", len(raw))
	}
	name := strings.TrimRight(string(raw[1:33]), "\x00")
	surname := strings.TrimRight(string(raw[33:65]), "\x00")
	return name, surname, nil
}

func readLine(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
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
		return "", fmt.Errorf("RPC URL must use http or https")
	}
	return raw, nil
}
