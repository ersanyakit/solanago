# GOSPL custom token program

`testdata/program.go` is the low-level Go guest source used to produce the
custom GOSPL sBPFv3 ELF. It is accepted by the current compiler and uses only
the deterministic subset plus explicit guest virtual-memory loads/stores. It
does not import Rust, C, `unsafe`, Cargo, the Go runtime, `runtime.Context`, or
the host SDK, and it never exposes a Go host pointer to the program.

Build and verify it with the repository's Go-only commands:

```bash
go run ./cmd/go-solana build -target solana \
  -o gospl.so examples/gospl/testdata/program.go
go run ./cmd/go-solana verify gospl.so
go run ./cmd/go-solana disassemble gospl.so
```

The generated Solana wrapper forwards Agave ABIv1 registers unchanged: `r1`
is `MM_INPUT_START`, and `r2` is the absolute guest virtual address of the
instruction bytes. The guest program manually walks ABIv1 account records and
validates duplicate slots, account ownership, signer/writable privileges,
exact state lengths, canonical state bytes, matching mint keys, delegate
allowances, and checked `uint64` arithmetic before mutation.

The host package in this directory provides deterministic instruction
builders and state codecs. Supported operations are:

- initialize mint and token account;
- mint, transfer, and burn;
- approve and revoke a delegate allowance;
- replace or permanently disable mint authority;
- replace account owner while clearing any delegate.

The account being initialized is writable and must sign the transaction. The
client builders encode that privilege, and the guest program verifies it. A
create-account and initialize pair can therefore be submitted atomically
without leaving a pre-created zeroed account open to third-party
initialization.

Mint state is exactly 48 bytes and token-account state is exactly 120 bytes.
The wire format intentionally matches the independent native-Go oracle in
`examples/spl20`.

GOSPL is a custom token program. Its layouts and instructions are not classic
SPL Token or Token-2022 compatible, and the three programs must not be
conflated.

## Normal compiler and differential gate

The ordinary package tests compile `testdata/program.go`, generate the Solana
entry wrapper, execute it in the local reference VM over both contiguous and
direct-mapped Agave ABIv1 memory, and compare state transitions with the native
oracle:

```bash
go test -count=1 ./examples/gospl
```

This gate does not start Agave. It proves the repository's compiler, ABI
fixtures, reference VM, and differential oracle boundary only.

## Opt-in official Agave gate

The opt-in test starts an empty official Agave validator, deploys the supplied
ELF through upgradeable-loader transactions assembled and signed entirely in
Go, creates rent-exempt mint and token accounts, and exercises every GOSPL
state transition. It then reads finalized account data over JSON-RPC and
requires byte-for-byte equality with the canonical Go codecs:

```bash
GOSBF_AGAVE_BIN=/path/to/agave/bin \
GOSBF_GOSPL_SO="$PWD/gospl.so" \
go test -count=1 -run '^TestAgaveGOSPLFullStateMachine$' \
  -v ./examples/gospl
```

The test is skipped unless both environment variables are set, so the normal
unit/differential/fuzz gates do not depend on an installed validator. Each
opt-in run deploys to its temporary local validator. It remains distinct from
the persistent public Testnet deployment recorded later in this document.

On 2026-08-16, the final source-built ELF passed this gate against official
Agave 4.2.1 in 336.56 seconds. The Go loader verified finalized ProgramData,
all 66 transactions finalized (15 GOSPL state transitions plus loader
deployment transactions), and the canonical end state was supply `940`, source
amount `669`, destination amount `271`, with mint authority disabled. The
temporary program id was
`2GmWanPwRiQ8brKiwgCNnNcLjB1HsUCbDzEvCW2q8i9W`.
The accepted 39,112-byte ELF had SHA-256
`7c8cf1c24eac5ed76d82df7a672099c29205fbddda39d024d4a25b936dc69fc9`;
the gate rebuilt the checked-in Go source and required byte equality before
starting the validator.

## Persistent Testnet deployment

The current Go-compiled GOSPL ELF is deployed on Testnet as program
`5d1Stqjy52hK34xcmgLepXhDNnTFYTizXDXwNKLtmaoS`. The finalized ProgramData
dump is byte-for-byte identical to the source-built strict sBPFv3 ELF. A custom
mint and token account owned by this program were initialized with six decimal
places and `100000` display units. Public addresses, transaction signatures,
slots, the artifact hash, and the exact compatibility boundary are recorded in
[`testnet-deployment.json`](testnet-deployment.json).

This public record proves the deployed program bytes came from the Go source.
It is not an end-to-end Go-only deployment-transport proof: the deployment
record says the Go client created and filled the loader buffer, but the final
public deploy transaction was submitted with Agave 4.2.1's `program deploy
--buffer` after public-RPC polling interrupted that client. The Go-only
new-program loader path is separately tested against an official local Agave
validator.

This deployment still does not create a classic SPL Token or Token-2022 mint,
and it has no Metaplex name/symbol metadata. Wallet token lists are therefore
not expected to index the custom account layout. The public Testnet mint in
`examples/spl20/testnet-token.json` remains a separate canonical Token-2022
mint created by the official Token-2022 program; it is not an instance or
deployment of GOSPL.

The pinned sBPF, Agave ABIv1, and entry-register sources used by this program
are listed in the [root documentation](../../README.md#authoritative-pinned-sources).
