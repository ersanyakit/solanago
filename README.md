# go-solana

`go-solana` is a working low-level compiler toolchain for a restricted,
deterministic subset of Go. It can produce both raw sBPFv3 text for the local
reference VM and a strict ELF64/sBPFv3 artifact with a Solana entrypoint
wrapper. The build, signing, JSON-RPC, and new-program deployment path is
implemented in Go and does not invoke Rust, Cargo, `solana` CLI, or
`solana-program`; an official Agave binary is needed only for the opt-in local
validator acceptance tests. It is not yet an Anchor-like application
framework.

## Current status

Two compilation paths are implemented:

```text
scalar test path
Go source -> go/parser + go/types -> typed CFG IR -> register allocation
          -> sBPFv3 text -> reference VM

Solana artifact path
Go source -> the same compiler -> generated r1/r2 entrypoint wrapper
          -> sBPFv3 text -> strict ET_DYN/EM_BPF ELF
```

The compilation unit is one or more Go source files of the same package,
containing package-level functions; a function in one file may call a
function defined in another file in the same build (`go-solana build
a.go b.go`, or `compiler.CompileFiles`). Imports of external packages,
structs, methods, and a full struct/method-based `Context` source API are
still not accepted by the guest compiler. A flat, compiler-recognized family
of account-field intrinsics (`AccountIsSigner`, `AccountKeyAddress`, and
similar — see [`examples/context`](examples/context/README.md)) replaces
hand-computed ABIv1 byte offsets for the common case without requiring that
larger struct/method redesign.

### Scalar compiler smoke test

```bash
go test ./...

go run ./cmd/go-solana run examples/add.go
# result: 30

go run ./cmd/go-solana build -func Add examples/add.go
# wrote program.sbpf (24 bytes, raw sBPFv3, entry Add)

go run ./cmd/go-solana disassemble program.sbpf
# 0000: MOV64_REG r0, r1
# 0001: ADD64_REG r0, r2
# 0002: EXIT
```

The exact bytes for `Add` are:

```text
bf 10 00 00 00 00 00 00
0f 20 00 00 00 00 00 00
95 00 00 00 00 00 00 00
```

`run` selects `Main` when present and otherwise the first source function. A
specific scalar function can be selected explicitly:

```bash
go run ./cmd/go-solana run -func Add -args 10,20 examples/add.go
```

### Solana ELF smoke test

```bash
go run ./cmd/go-solana build -target solana \
  -o program.so examples/solana_noop/program.go
go run ./cmd/go-solana verify program.so
go run ./cmd/go-solana disassemble program.so
```

`-target solana` defaults to strict ELF output and requires a handler with this
low-level signature:

```go
func Program(inputAddress uint64, instructionDataAddress uint64) uint64
```

The generated wrapper preserves Agave's entry registers: `r1` is
`MM_INPUT_START`, and `r2` is the absolute guest virtual address of the
instruction data. By contrast, `go-solana run` is a scalar compiler harness
that places user arguments in `r1-r5` and reads the result from `r0`. A scalar
VM pass is therefore not a validator execution claim.

## Implemented deterministic Go subset

- `uint64`, `int64`, `uint32`, `int32`, `uint8`, and `bool`
- function parameters, local variables, assignment, and one optional result
- wrapping `+`, `-`, `*`, `/`, and `%`, with signed and unsigned behavior
- `==`, `!=`, `<`, `<=`, `>`, and `>=`
- `if`/`else`, `for`, increment/decrement, and internal calls with at most five
  arguments
- fixed arrays, local array copies, dynamic bounds checks, addressable locals,
  and pointers to supported fixed-width memory scalars
- explicit guest-memory load/store intrinsics and a small, source-pinned
  Solana syscall-intrinsic surface
- a flat account-field intrinsic family (`AccountIsSigner`, `AccountIsWritable`,
  `AccountIsExecutable`, `AccountKeyAddress`, `AccountOwnerAddress`,
  `AccountLamportsAddress`, `AccountDataLen`, `AccountDataAddress`) that
  lowers to the same const-offset-add(-load) instructions a hand-written
  `LoadX(record+uint64(N))` expression already produces

Pointers are sBPF virtual addresses, never Go host pointers. Pointer-typed
results are rejected and fixed arrays cannot cross the register ABI. The
low-level `AddressUint64` intrinsic can intentionally erase a stack pointer's
type so C syscall structures can contain guest addresses. A CFG-sensitive,
interprocedural IR check rejects returning that integer, retaining it outside
its owning stack object, laundering it through memory/helpers, or copying it
to external memory. The scalar CLI argument parser accepts every supported
numeric scalar and `bool`, with width-checked parsing and signed 32/64-bit
result formatting.

Unsupported source is rejected with source-positioned diagnostics. This
includes imports and the standard library, heap allocation, strings, slices,
maps, structs, methods, interfaces, closures, goroutines, channels, `range`,
`switch`, `defer`, and `panic`/`recover`.

## Implemented layers and their boundaries

| Layer | Implemented | Deliberate boundary |
| --- | --- | --- |
| Compiler, ISA, and VM | Typed IR, 8/32/64-bit values, arrays/pointers, allocation/spills, sBPFv3 encoding/verifier/disassembler, bounded reference execution, multi-file (same-package) compilation | The VM does not execute Solana static syscalls and is not Agave; no cross-package imports |
| Solana entry and ELF | Generated low-level `r1/r2` wrapper, strict v3 ELF writer/parser, CLI build/verify/disassemble | No relocations, writable ELF data, guest Go runtime, or high-level source wrapper |
| Runtime model | Pinned ABIv1 serialization/parsing, direct account mapping, `runtime.Context`, account privilege/change checks, program-error mapping, C CPI layouts | These are host-side Go APIs and test infrastructure; guest structs/methods and imports are still unsupported |
| PDA and syscalls | Host `CreateProgramAddress`/`FindProgramAddress` with official vectors; versioned syscall registry; compiler intrinsics for log, memory, PDA, `sol_invoke_signed_c`, and flat account-field access (`AccountIsSigner` and similar) | No typed guest seed API, no struct/method-based `Context`, and no syscall execution in the reference VM |
| CPI | C/Rust layout translation, privilege/PDA-signer validation, atomic rollback, post-CPI realloc synchronization, a compiled C-CPI example, and an opt-in official-Agave acceptance test | No reference-VM/default host executor and no high-level `Context.Invoke`; an injected executor also requires an explicit program policy |
| SDK | Host-side Pubkey/instruction types; pinned System and classic Token interfaces; Token-2022 base/TLV plus selected extension builders; upgradeable-loader codecs including `Upgrade` | SDK packages cannot be imported by guest source; not every Token-2022 extension instruction has a typed builder |
| Deploy/test tooling | JSON-RPC transaction/signing client, isolated official-validator harness, new-program upgradeable-loader deploy workflow, and an explicit `Upgrade` workflow for already-deployed programs | Existing-program deploy still fails closed (use `upgrade` instead); authority rotation/close are not implemented; partial progress is only an in-memory recovery record |

The main packages are `compiler`, `sbpf`, `elf`, `vm`, `runtime`,
`serialization`, `sdk` (including `system`, `token`, `token2022`, and
`loader`), `deploy`, `svmtest`, and `cmd/go-solana`.

## Custom GOSPL versus canonical Token-2022

[`examples/gospl/testdata/program.go`](examples/gospl/testdata/program.go) is
a low-level custom token program that the compiler accepts and emits as sBPFv3
ELF. Normal tests compile it, execute it in the reference VM against serialized
Agave ABIv1 memory, and compare its full state machine with the independent
native model in `examples/spl20`.

GOSPL is custom: its 48-byte mint, 120-byte token account, and instruction wire
format are not classic SPL Token or Token-2022. In addition to the ephemeral
official-Agave acceptance gate, the current Go-compiled ELF and one initialized
custom mint are live on Testnet. The public program, state-account, transaction,
slot, and artifact-hash evidence is recorded in
[`examples/gospl/testnet-deployment.json`](examples/gospl/testnet-deployment.json).
The recorded public program bytes are genuinely produced by this Go compiler,
but the final public deploy transaction was submitted with Agave 4.2.1's
`program deploy --buffer`. The deployment record says this resumed a buffer
created by the Go client after public-RPC polling interrupted it, but the public
record is not an end-to-end Go-only transport proof. The repository's Go-only
loader path is independently exercised against an official local Agave
validator.
See [the GOSPL guide](examples/gospl/README.md).

Separately, `examples/spl20/testnet-token.json` records a finalized Testnet
snapshot for a canonical Token-2022 mint named `GOSPLToken`. It was created by
the official Token-2022 program; it is not the custom Go program and is not
evidence that GOSPL was deployed. See [the native model and Token-2022
boundary](examples/spl20/README.md).

[`examples/erc1155`](examples/erc1155/README.md) is a second custom program
alongside GOSPL: a multi-token contract providing ERC1155's capabilities
(per-id balances, per-id supply/URI, approve-for-all) re-expressed as
separate Solana accounts rather than a byte-for-byte port — two deliberate
deviations (no batch instruction; Solana's transaction-level atomicity makes
one unnecessary, and no receiver-hook callback) are documented there. It is
built from two files compiled together via multi-file support and uses the
account-field intrinsics instead of hand-computed account-record offsets.
Its correctness oracle is the reference VM executing the compiled program
against real serialized ABIv1 memory through a full instruction lifecycle,
asserting exact account bytes at each step — it does not have GOSPL's
separate native-model cross-check or opt-in official-Agave gate.

## Verification

The normal, dependency-free gates are:

```bash
go test -count=1 ./...
go vet ./...
go test -race -count=1 ./...
```

They cover frontend/IR rejection, official byte goldens, encoder/decoder and
ELF round trips, malformed input, VM arithmetic/memory/calls/resource limits,
ABIv1/account/CPI validation, SDK wire goldens, native-versus-compiled GOSPL
differential behavior, CLI integration, and fuzz seeds. Tests that launch an
official Agave binary are skipped unless their environment variables are set;
they are not part of an ordinary `go test ./...` run. Exact opt-in commands and
fuzz campaigns are in [docs/testing.md](docs/testing.md).

In the 2026-08-16 development session, the opt-in compiled CPI gate passed on
an ephemeral official Agave 4.2.1 validator: a System Program CPI produced the
exact finalized `-123456/+123456` lamport deltas, and signer/writable
escalation attempts were rejected without balance mutation. This is a
session-local acceptance result, not a normal CI gate, persistent validator,
or public-cluster deployment. Reproduction instructions are in
[`examples/cpi`](examples/cpi/README.md).

The final compiled GOSPL gate also passed on official Agave 4.2.1 in the same
session. The ELF rebuilt from `examples/gospl/testdata/program.go` was deployed
through the Go loader, its finalized ProgramData bytes were checked against the
ELF, and 66 finalized transactions in total (15 GOSPL state transitions plus
loader deployment transactions) ended at supply `940`, source amount `669`,
destination amount `271`, and disabled mint authority. The
ephemeral program id was `2GmWanPwRiQ8brKiwgCNnNcLjB1HsUCbDzEvCW2q8i9W`.
This is official-runtime acceptance, not a persistent public-cluster deploy.

After deploying GOSPL, the Go-only [`gospl-init`](examples/gospl/cmd/gospl-init/README.md)
client creates each custom mint/token account together with its initialization
in the same transaction, then mints an exact raw or decimal supply. It uses no
floating point, requires `--allow-live` for a public RPC, never retries an
ambiguous submission, and re-reads canonical account bytes at finalized
commitment:

```bash
go run ./examples/gospl/cmd/gospl-init \
  --program PROGRAM_ADDRESS \
  --keypair funded-payer.json \
  --url testnet --allow-live \
  --decimals 6 --amount-ui 100000
```

For canonical SPL Token-2022 mints (wallet-visible token account format), use:

```bash
go run ./examples/token2022/cmd/token2022-init \
  --keypair funded-payer.json \
  --url testnet --allow-live \
  --decimals 6 --amount-ui 100000
```

This creates a real Token-2022 mint and token account and mints `100000` UI units.
`token2022-init` currently uses base Token-2022 mint/account instructions only;
metadata (name/symbol/URI) extension setup is not yet implemented in this flow.

The `deploy` command is intentionally guarded. It validates strict ELF, refuses
non-loopback RPC endpoints unless `--allow-live` is explicit, and only creates
new programs. Existing finalized loader buffers can be resumed explicitly with
`--buffer`. The current public Testnet deployment is recorded in
`examples/gospl/testnet-deployment.json`.

The complete Go-only build/deploy flow is:

```bash
go run ./cmd/go-solana build -target solana -o program.so program.go
go run ./cmd/go-solana verify program.so
go run ./cmd/go-solana keygen -o program-keypair.json
go run ./cmd/go-solana deploy \
  --program-id program-keypair.json \
  --keypair funded-payer.json \
  --url localhost program.so
```

A separate `upgrade` command replaces an already-deployed program's code in
place. Unlike `deploy`, it takes the program's existing address (not a
keypair — the program account does not sign an upgrade) and an explicit
upgrade-authority keypair; it fails closed if the program doesn't exist yet,
if the on-chain authority doesn't match, or if the new ELF is larger than the
`MaxDataLen` the program was allocated at its first deploy (that ceiling
cannot grow):

```bash
go run ./cmd/go-solana build -target solana -o program.so program.go
go run ./cmd/go-solana upgrade \
  --program PROGRAM_ADDRESS \
  --authority upgrade-authority.json \
  --keypair funded-payer.json \
  --url localhost program.so
```

For Devnet, Testnet, or Mainnet, select that RPC URL and add `--allow-live`;
the payer must already be funded. The loader verifies the finalized buffer
bytes before deployment and then verifies the finalized Program/ProgramData
accounts, upgrade authority, ELF bytes, and zero padding byte-for-byte.

## Authoritative pinned sources

The backend targets the sBPFv3 behavior in `solana-sbpf` 0.23.0 selected by
the pinned Agave snapshot:

- [Agave dependency pin](https://github.com/anza-xyz/agave/blob/12b5c7e4df705927b2f7f579f3aa606aa4bde1c0/Cargo.toml#L469)
- [`solana-sbpf` v0.23.0 source](https://github.com/anza-xyz/sbpf/tree/9476336b901181d68e00c5b38252a15694d4d6aa)
- [register ABI and bytecode layout](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/doc/bytecode.md#L3-L46)
- [opcodes, 8-byte encoding, and decoder](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/ebpf.rs#L349-L689)
- [version-specific call rules](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/program.rs#L57-L103)
- [verifier register/jump rules](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/verifier.rs#L131-L421)
- [interpreter arithmetic, calls, and exit](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/interpreter.rs#L180-L604)
- [stack and call-depth defaults](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/vm.rs#L50-L166)
- [strict v3 ELF loader](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/elf.rs#L487-L678)

The Solana-facing host layers use explicit source pins as well:

- [Agave ABIv1 serialization](https://github.com/anza-xyz/agave/blob/12b5c7e4df705927b2f7f579f3aa606aa4bde1c0/program-runtime/src/serialization.rs)
- [Agave entry-register initialization](https://github.com/anza-xyz/agave/blob/1f26a441bd8b390ccaeb6c145e105ab1733042a9/program-runtime/src/vm.rs#L305-L313)
- [Agave syscall registry](https://github.com/anza-xyz/agave/blob/12b5c7e4df705927b2f7f579f3aa606aa4bde1c0/syscalls/src/lib.rs)
- [Solana SDK syscall C definitions](https://github.com/anza-xyz/solana-sdk/blob/7437469d1ab5bddbf665f3a1a69aefb422c33e36/define-syscall/src/definitions.rs)
- [Solana SDK program entrypoint ABI](https://github.com/anza-xyz/solana-sdk/blob/7437469d1ab5bddbf665f3a1a69aefb422c33e36/program-entrypoint/src/lib.rs)
- [Solana SDK PDA implementation](https://github.com/anza-xyz/solana-sdk/blob/7437469d1ab5bddbf665f3a1a69aefb422c33e36/pubkey/src/lib.rs)
- [System Program source pin](https://github.com/solana-program/system/tree/f61ddfe278125ea7624ba5df66baad5d01b9dccd)
- [classic SPL Token source pin](https://github.com/solana-program/token/tree/f5285693a93135a144e24859c84d26ac20037a3a)
- [Token-2022 source pin](https://github.com/solana-program/token-2022/tree/567074d43dc87522846728cc0b598bca27df764a)

The final external acceptance run used the official
[Agave v4.2.1 release](https://github.com/anza-xyz/agave/releases/tag/v4.2.1).
Its downloaded release archive matched the published SHA-256 digest
`9fb744917877acc68ae2421aef8d7f44f0d5eb16428e9d3db2c98b1ae61fd239`,
and `solana-test-validator --version` reported
`4.2.1 (src:c4b48df9; feat:21b0d33a, client:Agave)`.

Detailed ISA decisions and known version discrepancies are recorded in
[docs/sbpf.md](docs/sbpf.md); feature status and remaining high-level work are
tracked in [docs/roadmap.md](docs/roadmap.md).
