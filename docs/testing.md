# Testing

## Normal gates

These commands require only Go and do not start a validator or contact a public
cluster:

```bash
go test -count=1 ./...
go vet ./...
go test -race -count=1 ./...
```

The five official-Agave tests call `t.Skip` when their environment
variables are absent. Consequently, a green ordinary `go test ./...` proves
the compiler, local verifier/VM, host runtime, SDK, CLI, and differential test
boundaries; it does not prove that an external validator ran.

The normal suite covers:

- frontend/type/IR diagnostics and unsupported syntax
- exact `Add` bytes, sBPF opcode goldens, round trips, and malformed bytecode
- register allocation, stack spills, calls, 32/64-bit arithmetic, signed
  division/remainder, arrays, pointers, and explicit memory regions
- strict ELF output through the local parser, Go `debug/elf`, and a pinned
  official Agave fixture
- aligned and direct-mapped ABIv1, duplicate accounts, account mutation rules,
  C/Rust CPI layouts, privilege validation, and rollback
- official PDA, System, classic Token, covered Token-2022 base/extensions, and
  loader wire vectors
- native-Go versus compiled-reference-VM GOSPL state-machine behavior
- GOSPL public-client amount parsing, live-RPC guard, no-retry recovery
  journal, and finalized canonical-state verification with a fake RPC
- CLI build/run/disassemble/verify/init and guarded dry-run operations

## Short fuzz campaigns

Go runs one fuzz target per command. CI's baseline smoke job currently uses:

```bash
go test ./compiler -run='^$' -fuzz=FuzzCompileUint64ConstantArithmetic -fuzztime=5s
go test ./compiler -run='^$' -fuzz=FuzzCompiledAddMatchesGo -fuzztime=5s
go test ./compiler -run='^$' -fuzz=FuzzCompiledUint32MatchesGo -fuzztime=5s
go test ./compiler -run='^$' -fuzz=FuzzCompiledInt32DivisionRemainderMatchesGo -fuzztime=5s
go test ./sbpf -run='^$' -fuzz=FuzzEncodeDecode -fuzztime=5s
go test ./sbpf -run='^$' -fuzz=FuzzDecodeNeverPanics -fuzztime=5s
go test ./sbpf -run='^$' -fuzz=FuzzMilestone2EncodeDecode -fuzztime=5s
go test ./elf -run='^$' -fuzz=FuzzParseStrictV3 -fuzztime=5s
go test ./vm -run='^$' -fuzz=FuzzVMArithmetic -fuzztime=5s
go test ./vm -run='^$' -fuzz=FuzzVMBranches -fuzztime=5s
go test ./vm -run='^$' -fuzz=FuzzVMUint32Arithmetic -fuzztime=5s
go test ./runtime -run='^$' -fuzz=FuzzAgaveABIV1Differential -fuzztime=5s
go test ./runtime -run='^$' -fuzz=FuzzAgaveABIV1DirectMappingDifferential -fuzztime=5s
go test ./runtime -run='^$' -fuzz=FuzzMemoryAndCTranslationNeverPanic -fuzztime=5s
go test ./serialization -run='^$' -fuzz=FuzzDecoderNeverPanics -fuzztime=5s
go test ./examples/gospl -run='^$' -fuzz=FuzzInstructionCodecIsCanonical -fuzztime=5s
go test ./examples/hellogo -run='^$' -fuzz=FuzzInstructionCodecIsCanonical -fuzztime=5s
```

Representative extended campaigns for the newer layers are:

```bash
go test ./compiler -run='^$' -fuzz=FuzzCompiledUint32MatchesGo -fuzztime=10s
go test ./compiler -run='^$' -fuzz=FuzzCompiledInt32DivisionRemainderMatchesGo -fuzztime=10s
go test ./sbpf -run='^$' -fuzz=FuzzMilestone2EncodeDecode -fuzztime=10s
go test ./vm -run='^$' -fuzz=FuzzVMUint32Arithmetic -fuzztime=10s
go test ./runtime -run='^$' -fuzz=FuzzAgaveABIV1Differential -fuzztime=10s
go test ./runtime -run='^$' -fuzz=FuzzAgaveABIV1DirectMappingDifferential -fuzztime=10s
go test ./runtime -run='^$' -fuzz=FuzzMemoryAndCTranslationNeverPanic -fuzztime=10s
go test ./serialization -run='^$' -fuzz=FuzzDecoderNeverPanics -fuzztime=10s
go test ./examples/gospl -run='^$' -fuzz=FuzzInstructionCodecIsCanonical -fuzztime=10s
go test ./examples/spl20 -run='^$' -fuzz=FuzzDecodeInstructionNeverPanics -fuzztime=10s
```

Longer campaigns should persist Go's generated corpus outside the repository
unless an input is reduced to a minimal regression fixture.

## Opt-in official Agave gates

The external acceptance tests require an official distribution containing
`solana-test-validator`. `GOSBF_AGAVE_BIN` is the directory that contains that
binary, not the binary path itself. Each test uses a temporary ledger and
leaves its validator log available for the duration of the test.

First build a minimal Solana artifact:

```bash
go run ./cmd/go-solana build -target solana \
  -o program.so examples/solana_noop/program.go
```

The preload gate lets the official validator load the strict ELF at genesis
and submits a real transaction:

```bash
GOSBF_AGAVE_BIN=/path/to/agave/bin \
GOSBF_PROGRAM_SO="$PWD/program.so" \
go test -count=1 -run '^TestAgaveLocalValidator$' -v ./svmtest
```

The Go-only deploy gate starts an empty validator, creates an upgradeable-loader
buffer, writes and verifies the ELF, deploys a new program, and invokes it:

```bash
GOSBF_AGAVE_BIN=/path/to/agave/bin \
GOSBF_PROGRAM_SO="$PWD/program.so" \
go test -count=1 -run '^TestAgaveGoOnlyDeployAndInvoke$' -v ./svmtest
```

The CLI wrapper currently selects the preload gate after the normal Go tests:

```bash
GOSBF_AGAVE_BIN=/path/to/agave/bin \
GOSBF_PROGRAM_SO="$PWD/program.so" \
go run ./cmd/go-solana test -real-svm .
```

### Compiled GOSPL gate

Build the low-level custom token program, then opt in to its full official
validator state-machine test:

```bash
go run ./cmd/go-solana build -target solana \
  -o gospl.so examples/gospl/testdata/program.go

GOSBF_AGAVE_BIN=/path/to/agave/bin \
GOSBF_GOSPL_SO="$PWD/gospl.so" \
go test -count=1 -run '^TestAgaveGOSPLFullStateMachine$' \
  -v ./examples/gospl
```

This gate deploys only to the test's ephemeral local validator and by itself
does not prove a public deployment. Persistent Testnet GOSPL evidence is
recorded separately in `examples/gospl/testnet-deployment.json`. The canonical
Token-2022 Testnet snapshot in `examples/spl20/testnet-token.json` remains
separate and must not be used as evidence for this custom program.

The final 2026-08-16 run passed on official Agave 4.2.1 in 336.56 seconds with
66 finalized transactions (15 GOSPL state transitions plus deployment) and
byte-exact ProgramData/account-state checks.
Its temporary program id was
`2GmWanPwRiQ8brKiwgCNnNcLjB1HsUCbDzEvCW2q8i9W`; it is not a public address.
The accepted ELF SHA-256 was
`7c8cf1c24eac5ed76d82df7a672099c29205fbddda39d024d4a25b936dc69fc9`.

### Compiled HelloGo gate

The separately named `examples/hellogo` custom-contract fixture has the same
source-to-ELF byte-binding rule and its own opt-in environment variable:

```bash
go run ./cmd/go-solana build -target solana \
  -o hellogo.so examples/hellogo/testdata/program.go

GOSBF_AGAVE_BIN=/path/to/agave/bin \
GOSBF_HELLOGO_SO="$PWD/hellogo.so" \
go test -count=1 -run '^TestAgaveHelloGoFullStateMachine$' \
  -v ./examples/hellogo
```

This test is available but was not part of the recorded official-runtime runs
described above; without both variables it is skipped.

### Compiled System Program CPI gate

The CPI example builds exact C ABI structures in guest stack memory and emits
`sol_invoke_signed_c`. Build it and run its opt-in official-runtime test with:

```bash
go run ./cmd/go-solana build -target solana \
  -o cpi.so examples/cpi/testdata/program.go

GOSBF_AGAVE_BIN=/path/to/agave/bin \
GOSBF_CPI_SO="$PWD/cpi.so" \
go test -count=1 -run '^TestAgaveSystemTransferCPI$' \
  -v ./examples/cpi
```

In the 2026-08-16 development session, this exact gate passed on an ephemeral
official Agave 4.2.1 validator in 84.31 seconds. It verified finalized source
and destination balance deltas of exactly `-123456/+123456` lamports and
rejected both writable and signer privilege escalation without mutation.

The test remains skipped without both environment variables and is not in the
normal CI gate. Its result is evidence for this compiled low-level CPI path,
not a high-level `Context.Invoke` API, persistent local service, or
Testnet/Mainnet program deployment.

### External binary provenance used for the recorded runs

The recorded final runs used the official
[Agave v4.2.1 release](https://github.com/anza-xyz/agave/releases/tag/v4.2.1).
The downloaded release archive matched SHA-256
`9fb744917877acc68ae2421aef8d7f44f0d5eb16428e9d3db2c98b1ae61fd239`.
The validator binary reported
`solana-test-validator 4.2.1 (src:c4b48df9; feat:21b0d33a, client:Agave)`.
The harness still accepts an explicit `GOSBF_AGAVE_BIN`; callers reproducing a
release gate must verify their external distribution rather than treating the
path alone as provenance.
