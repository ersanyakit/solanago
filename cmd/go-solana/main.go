package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ersany/go-solana/compiler"
	gosoldeploy "github.com/ersany/go-solana/deploy"
	sbpfelf "github.com/ersany/go-solana/elf"
	"github.com/ersany/go-solana/sbpf"
	"github.com/ersany/go-solana/sdk"
	"github.com/ersany/go-solana/svmtest"
	"github.com/ersany/go-solana/vm"
)

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "go-solana: %v\n", err)
		os.Exit(1)
	}
}

func runCLI(arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		printUsage(stderr)
		return errors.New("missing command")
	}
	switch arguments[0] {
	case "init":
		return initCommand(arguments[1:], stdout, stderr)
	case "build":
		return buildCommand(arguments[1:], stdout, stderr)
	case "keygen":
		return keygenCommand(arguments[1:], stdout, stderr)
	case "airdrop":
		return airdropCommand(arguments[1:], stdout, stderr)
	case "disassemble", "disasm":
		return disassembleCommand(arguments[1:], stdout, stderr)
	case "verify":
		return verifyCommand(arguments[1:], stdout, stderr)
	case "run":
		return runCommand(arguments[1:], stdout, stderr)
	case "test":
		return testCommand(arguments[1:], stdout, stderr)
	case "deploy":
		return deployCommand(arguments[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "usage:")
	fmt.Fprintln(output, "  go-solana init directory")
	fmt.Fprintln(output, "  go-solana build [-target scalar|solana] [-format auto|raw|elf] [-o output] [-func Function] source.go")
	fmt.Fprintln(output, "  go-solana keygen -o keypair.json")
	fmt.Fprintln(output, "  go-solana airdrop --keypair payer.json --url devnet --allow-live [--lamports 1000000000]")
	fmt.Fprintln(output, "  go-solana disassemble program.sbpf|program.so")
	fmt.Fprintln(output, "  go-solana verify program.so")
	fmt.Fprintln(output, "  go-solana run [-func Function] [-args 10,20] source.go [arguments ...]")
	fmt.Fprintln(output, "  go-solana test [directory]")
	fmt.Fprintln(output, "  go-solana deploy --program-id program-keypair.json --keypair payer.json [--buffer BUFFER_ADDRESS] [--url localhost] program.so")
}

func buildCommand(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("o", "", "output path (default program.sbpf or program.so)")
	entryFlag := flags.String("func", "", "entry function")
	target := flags.String("target", "scalar", "scalar reference ABI or Solana program ABI")
	format := flags.String("format", "auto", "auto, raw, or strict sBPFv3 ELF")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("build expects exactly one Go source file")
	}
	program, err := compiler.CompileFile(flags.Arg(0))
	if err != nil {
		return err
	}
	var executable *compiler.Executable
	switch *target {
	case "scalar":
		entry, _, selectErr := selectEntry(program, *entryFlag)
		if selectErr != nil {
			return selectErr
		}
		executable, err = compiler.Generate(program, entry)
	case "solana":
		executable, err = compiler.GenerateSolanaEntrypoint(program, *entryFlag)
	default:
		return fmt.Errorf("unknown build target %q; want scalar or solana", *target)
	}
	if err != nil {
		return err
	}
	selectedFormat := *format
	if selectedFormat == "auto" {
		if *target == "solana" {
			selectedFormat = "elf"
		} else {
			selectedFormat = "raw"
		}
	}
	artifact := executable.Bytecode
	description := "raw sBPFv3"
	switch selectedFormat {
	case "raw":
	case "elf":
		artifact, err = sbpfelf.BuildV3(executable.Bytecode, 0)
		description = "strict sBPFv3 ELF"
	default:
		return fmt.Errorf("unknown build format %q; want auto, raw, or elf", *format)
	}
	if err != nil {
		return err
	}
	outputPath := *output
	if outputPath == "" {
		outputPath = "program.sbpf"
		if selectedFormat == "elf" {
			outputPath = "program.so"
		}
	}
	if err := os.WriteFile(outputPath, artifact, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outputPath, err)
	}
	fmt.Fprintf(stdout, "wrote %s (%d bytes, %s, entry %s)\n", outputPath, len(artifact), description, executable.Entry)
	return nil
}

func disassembleCommand(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("disassemble", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("disassemble expects exactly one .sbpf file")
	}
	bytecode, err := readTextArtifact(flags.Arg(0))
	if err != nil {
		return err
	}
	disassembly, err := sbpf.DisassembleBytes(bytecode)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, disassembly)
	return nil
}

func verifyCommand(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("verify expects exactly one strict .so file")
	}
	data, err := os.ReadFile(flags.Arg(0))
	if err != nil {
		return fmt.Errorf("read %s: %w", flags.Arg(0), err)
	}
	image, err := sbpfelf.ParseStrictV3(data)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "verified %s (sBPFv%d, entry pc %d, %d text bytes)\n", flags.Arg(0), image.Version, image.EntryPC, len(image.Text))
	return nil
}

func keygenCommand(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("o", "", "new keypair output path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *output == "" {
		return errors.New("keygen expects -o keypair.json and no positional arguments")
	}
	signer, err := svmtest.NewSigner()
	if err != nil {
		return err
	}
	if err := gosoldeploy.SaveKeypair(*output, signer, false); err != nil {
		return fmt.Errorf("write keypair: %w", err)
	}
	fmt.Fprintf(stdout, "generated %s (%s)\n", *output, signer.PublicKey)
	return nil
}

func airdropCommand(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("airdrop", flag.ContinueOnError)
	flags.SetOutput(stderr)
	rpcURL := flags.String("url", "devnet", "Solana RPC URL or localhost/devnet/testnet")
	keypair := flags.String("keypair", "", "recipient keypair path (required)")
	lamports := flags.Uint64("lamports", 1_000_000_000, "lamports to request")
	allowLive := flags.Bool("allow-live", false, "explicitly permit a non-loopback RPC endpoint")
	dryRun := flags.Bool("dry-run", false, "validate and print without requesting funds")
	timeout := flags.Duration("timeout", 2*time.Minute, "airdrop finalization deadline")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *keypair == "" || *lamports == 0 {
		return errors.New("airdrop expects --keypair payer.json, a positive --lamports value, and no positional arguments")
	}
	resolvedURL, err := normalizeRPCURL(*rpcURL)
	if err != nil {
		return err
	}
	if !*allowLive && !loopbackRPC(resolvedURL) {
		return fmt.Errorf("refusing non-loopback RPC %q without --allow-live", *rpcURL)
	}
	recipient, err := gosoldeploy.LoadKeypair(*keypair)
	if err != nil {
		return fmt.Errorf("load recipient keypair: %w", err)
	}
	if *dryRun {
		fmt.Fprintf(stdout, "validated airdrop request: recipient=%s lamports=%d rpc=%s\n", recipient.PublicKey, *lamports, resolvedURL)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	signature, err := (svmtest.Client{URL: resolvedURL}).RequestAirdrop(ctx, recipient.PublicKey, *lamports)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "airdrop finalized: recipient=%s lamports=%d signature=%s\n", recipient.PublicKey, *lamports, signature)
	return nil
}

func runCommand(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	entryFlag := flags.String("func", "", "entry function")
	argumentList := flags.String("args", "", "comma-separated scalar arguments")
	maxInstructions := flags.Int("max-instructions", vm.DefaultMaxInstructions, "execution instruction limit")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() < 1 {
		return errors.New("run expects a Go source file")
	}
	program, err := compiler.CompileFile(flags.Arg(0))
	if err != nil {
		return err
	}
	entry, function, err := selectEntry(program, *entryFlag)
	if err != nil {
		return err
	}
	executable, err := compiler.Generate(program, entry)
	if err != nil {
		return err
	}

	rawArguments := flags.Args()[1:]
	if *argumentList != "" {
		if len(rawArguments) != 0 {
			return errors.New("use either -args or positional arguments, not both")
		}
		rawArguments = strings.Split(*argumentList, ",")
	}
	vmArguments, err := parseArguments(function, rawArguments)
	if err != nil {
		return err
	}
	config := vm.DefaultConfig()
	config.MaxInstructions = *maxInstructions
	machine, err := vm.NewWithConfig(executable.Instructions, config)
	if err != nil {
		return err
	}
	result, err := machine.Run(vmArguments...)
	if err != nil {
		return err
	}
	printResult(stdout, function.Result, result)
	return nil
}

func selectEntry(program *compiler.Program, requested string) (string, *compiler.Function, error) {
	if requested != "" {
		function, ok := program.Function(requested)
		if !ok {
			return "", nil, fmt.Errorf("entry function %q does not exist", requested)
		}
		return requested, function, nil
	}
	if function, ok := program.Function("Main"); ok {
		return "Main", function, nil
	}
	if len(program.Functions) == 0 {
		return "", nil, errors.New("source contains no functions")
	}
	return program.Functions[0].Name, program.Functions[0], nil
}

func parseArguments(function *compiler.Function, arguments []string) ([]uint64, error) {
	if len(arguments) != len(function.Params) {
		return nil, fmt.Errorf("function %s expects %d arguments, got %d", function.Name, len(function.Params), len(arguments))
	}
	result := make([]uint64, len(arguments))
	for index, raw := range arguments {
		parameter, _ := function.Value(function.Params[index])
		value, err := parseScalar(parameter.Type, strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("argument %d (%s): %w", index+1, parameter.Type, err)
		}
		result[index] = value
	}
	return result, nil
}

func parseScalar(kind compiler.Type, raw string) (uint64, error) {
	switch kind {
	case compiler.TypeUint8:
		return strconv.ParseUint(raw, 0, 8)
	case compiler.TypeUint32:
		return strconv.ParseUint(raw, 0, 32)
	case compiler.TypeUint64:
		return strconv.ParseUint(raw, 0, 64)
	case compiler.TypeInt32:
		value, err := strconv.ParseInt(raw, 0, 32)
		return uint64(value), err
	case compiler.TypeInt64:
		value, err := strconv.ParseInt(raw, 0, 64)
		return uint64(value), err
	case compiler.TypeBool:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return 0, err
		}
		if value {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("unsupported parameter type %s", kind)
	}
}

func printResult(output io.Writer, kind compiler.Type, value uint64) {
	switch kind {
	case compiler.TypeInt32:
		fmt.Fprintf(output, "result: %d\n", int32(value))
	case compiler.TypeInt64:
		fmt.Fprintf(output, "result: %d\n", int64(value))
	case compiler.TypeBool:
		fmt.Fprintf(output, "result: %t\n", value != 0)
	default:
		fmt.Fprintf(output, "result: %d\n", value)
	}
}

func readTextArtifact(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) >= 4 && string(data[:4]) == "\x7fELF" {
		image, err := sbpfelf.ParseStrictV3(data)
		if err != nil {
			return nil, err
		}
		return image.Text, nil
	}
	return data, nil
}

func initCommand(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("init expects exactly one new project directory")
	}
	directory, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(directory); statErr == nil {
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			return readErr
		}
		if !info.IsDir() || len(entries) != 0 {
			return fmt.Errorf("init target %s must not exist or must be an empty directory", directory)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create project directory: %w", err)
	}
	files := map[string]string{
		"program.go": "package program\n\n// Program receives Agave's input base and absolute instruction-data guest address.\nfunc Program(inputAddress uint64, instructionDataAddress uint64) uint64 {\n\treturn 0\n}\n",
		"go.mod":     "module example.com/solana/program\n\ngo 1.26\n",
		"README.md":  "# Solana Go program\n\nBuild a validator-loadable artifact with:\n\n```sh\ngo-solana build -target solana program.go\n```\n",
	}
	for name, contents := range files {
		path := filepath.Join(directory, name)
		file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if openErr != nil {
			return fmt.Errorf("create %s: %w", path, openErr)
		}
		_, writeErr := io.WriteString(file, contents)
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("write %s: %w", path, writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", path, closeErr)
		}
	}
	fmt.Fprintf(stdout, "initialized %s\n", directory)
	return nil
}

func testCommand(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.SetOutput(stderr)
	realSVM := flags.Bool("real-svm", false, "also run the pinned official solana-sbpf conformance harness")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("test accepts at most one project directory")
	}
	directory := "."
	if flags.NArg() == 1 {
		directory = flags.Arg(0)
	}
	command := exec.Command("go", "test", "-count=1", "./...")
	command.Dir = directory
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("Go test gate: %w", err)
	}
	if *realSVM {
		if os.Getenv("GOSBF_AGAVE_BIN") == "" || os.Getenv("GOSBF_PROGRAM_SO") == "" {
			return errors.New("-real-svm requires GOSBF_AGAVE_BIN and GOSBF_PROGRAM_SO")
		}
		command = exec.Command("go", "test", "-count=1", "./svmtest", "-run", "^TestAgaveLocalValidator$", "-v")
		command.Dir = directory
		command.Stdout = stdout
		command.Stderr = stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("official sBPF gate: %w", err)
		}
	}
	return nil
}

func deployCommand(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("deploy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	programID := flags.String("program-id", "", "program keypair path (required)")
	bufferFlag := flags.String("buffer", "", "resume with an existing finalized upgradeable-loader buffer address")
	rpcURL := flags.String("url", "localhost", "Solana RPC URL or localhost/devnet/testnet/mainnet-beta")
	keypair := flags.String("keypair", "", "fee payer keypair path (required)")
	allowLive := flags.Bool("allow-live", false, "explicitly permit a non-loopback RPC endpoint")
	dryRun := flags.Bool("dry-run", false, "validate and print the command without deploying")
	maxLen := flags.Int("max-len", 0, "maximum upgradeable ELF size (default current ELF size)")
	chunkSize := flags.Int("chunk-size", gosoldeploy.DefaultChunkSize, "loader buffer write size")
	timeout := flags.Duration("timeout", 20*time.Minute, "whole deployment deadline")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 || *programID == "" || *keypair == "" {
		return errors.New("deploy expects one program.so, --program-id program-keypair.json, and --keypair payer.json")
	}
	data, err := os.ReadFile(flags.Arg(0))
	if err != nil {
		return fmt.Errorf("read %s: %w", flags.Arg(0), err)
	}
	if _, err := sbpfelf.ParseStrictV3(data); err != nil {
		return fmt.Errorf("refusing to deploy an invalid artifact: %w", err)
	}
	resolvedURL, err := normalizeRPCURL(*rpcURL)
	if err != nil {
		return err
	}
	if !*allowLive && !loopbackRPC(resolvedURL) {
		return fmt.Errorf("refusing non-loopback RPC %q without --allow-live", *rpcURL)
	}
	programSigner, err := gosoldeploy.LoadKeypair(*programID)
	if err != nil {
		return fmt.Errorf("load program keypair: %w", err)
	}
	payer, err := gosoldeploy.LoadKeypair(*keypair)
	if err != nil {
		return fmt.Errorf("load payer keypair: %w", err)
	}
	var buffer sdk.Pubkey
	if *bufferFlag != "" {
		buffer, err = sdk.ParsePubkey(*bufferFlag)
		if err != nil {
			return fmt.Errorf("invalid --buffer address: %w", err)
		}
	}
	if *dryRun {
		selectedMaxLen := *maxLen
		if selectedMaxLen == 0 {
			selectedMaxLen = len(data)
		}
		if *bufferFlag != "" {
			fmt.Fprintf(stdout, "validated Go-only deploy resume: program=%s payer=%s buffer=%s rpc=%s elf=%d max-len=%d chunk=%d\n",
				programSigner.PublicKey, payer.PublicKey, buffer, resolvedURL, len(data), selectedMaxLen, *chunkSize)
		} else {
			fmt.Fprintf(stdout, "validated Go-only deploy: program=%s payer=%s rpc=%s elf=%d max-len=%d chunk=%d\n",
				programSigner.PublicKey, payer.PublicKey, resolvedURL, len(data), selectedMaxLen, *chunkSize)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	config := gosoldeploy.Config{
		Client:     svmtest.Client{URL: resolvedURL},
		FeePayer:   payer,
		Program:    programSigner,
		MaxDataLen: *maxLen,
		ChunkSize:  *chunkSize,
		Progress: func(stage gosoldeploy.Stage) {
			if stage.Kind == "write" {
				fmt.Fprintf(stderr, "finalized write offset=%d length=%d signature=%s\n", stage.Offset, stage.Length, stage.Signature)
			} else {
				fmt.Fprintf(stderr, "finalized %s signature=%s\n", stage.Kind, stage.Signature)
			}
		},
	}
	var result *gosoldeploy.Result
	if *bufferFlag != "" {
		result, err = gosoldeploy.ResumeProgram(ctx, config, buffer, data)
	} else {
		result, err = gosoldeploy.Program(ctx, config, data)
	}
	if err != nil {
		if hasPartialDeployJournal(result) {
			encoded, _ := json.Marshal(result)
			fmt.Fprintf(stderr, "partial deploy journal: %s\n", encoded)
		}
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(encoded))
	return nil
}

func hasPartialDeployJournal(result *gosoldeploy.Result) bool {
	return result != nil && (len(result.Signatures) != 0 || len(result.SubmittedSignatures) != 0 || result.BufferAddress != (sdk.Pubkey{}))
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
