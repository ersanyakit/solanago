# Roadmap and acceptance gates

Status in this document describes code and tests in this repository. An
opt-in test's existence is not represented as a currently running validator or
a public-cluster deployment.

## 1. Scalar compiler and reference VM — implemented

- parser, `go/types` checking, and source-positioned fail-closed diagnostics
- typed CFG IR independent of the AST
- register allocation, 4096-byte stack frames, spills, calls, and fixups
- real sBPFv3 encoding, decoding, verification, and disassembly
- bounded reference VM with arithmetic, branches, calls, explicit memory,
  division-by-zero, call-depth, and instruction-count checks
- official `Add` bytes, native differential tests, fuzz targets, and CLI tests

Acceptance: `go-solana run examples/add.go` returns `30`, and an `Add`-only
build is the official 24-byte instruction sequence.

## 2. Deterministic memory subset — implemented with explicit limits

- `uint8`, `uint32`, `int32`, `%`, narrow-value normalization,
  ALU32/JMP32 selection, and conversions
- fixed arrays, composite literals, independent array copies, and bounds traps
- pointers to supported fixed-width scalars, address-taken locals, dereference,
  and pointer arithmetic generated only by checked compiler operations
- explicit guest-memory intrinsics for 8/16/32/64-bit fields and booleans
- differential and fuzz coverage for 32-bit arithmetic, arrays, pointers, and
  memory permissions

Limits remain intentional: no slices, heap/GC values, structs, pointer
results, array parameters/results, or host-pointer conversion. The scalar CLI
accepts all implemented integer widths with range-checked parsing.

## 3. Low-level Solana ABI and strict ELF — implemented

- generated physical-PC-zero Solana wrapper
- exact low-level handler signature using `r1 = MM_INPUT_START` and absolute
  instruction-data address in `r2`
- compact strict sBPFv3 `ET_DYN`/`EM_BPF` ELF writer and independent parser
- official Agave ELF fixture coverage plus Go `debug/elf` validation
- `build -target solana`, `verify`, and raw/ELF disassembly

This is a low-level artifact path. It does not compile `runtime.Context`, SDK
types, imports, structs, methods, or a Go runtime into the guest.

## 4. Host runtime, PDA, serialization, and SDK — implemented host layers

- aligned Agave ABIv1 serialization/parsing, duplicate slots, direct account
  mapping, 10 KiB realloc reservations, and malformed-input fuzzing
- `runtime.Context`/`AccountView`, signer/writable/owner checks, lamport and
  account-change rules, rollback, and exact program-error return values
- deterministic bounded serialization codecs
- Pubkey/base58, curve test, PDA/create-with-seed derivation, and official
  cross-language vectors
- host builders/codecs for System, classic SPL Token, Token-2022 base/TLV and
  selected extensions, and upgradeable loader, kept as separate interfaces

These packages are usable by native Go clients/tests. The remaining framework
work is a compiler-supported high-level guest API that can express the same
operations without manual virtual-address decoding. Token-2022 also remains a
selected typed surface rather than a builder for every extension instruction.

## 5. Syscalls and CPI — low-level path official-gated; high-level path partial

Implemented:

- source-pinned static v3 syscall hashes and a feature/version registry
- compiler-recognized bodyless intrinsics for logging, memcpy/memmove/memset/
  memcmp, PDA syscalls, and `sol_invoke_signed_c`
- exact C/Rust CPI layouts and bounded guest-memory translation
- account identity, privilege-escalation, executable-program, PDA signer, and
  atomic rollback/realloc synchronization behind an explicit `CPIExecutor`
- fail-closed host invocation unless both an executor and a program-authorization
  policy are explicitly supplied
- a low-level Go source example that builds C CPI structures in sBPF stack
  memory and emits a System Program transfer syscall
- an opt-in official-Agave deployment/execution test for that compiled example

The opt-in gate was run successfully on an ephemeral official Agave 4.2.1
validator in the 2026-08-16 development session. It observed the exact
finalized `-123456/+123456` lamport transfer and confirmed that signer and
writable escalation attempts failed without mutating either balance.

Still incomplete:

- the reference VM intentionally has no Solana syscall host execution
- no default validator-equivalent CPI executor or complete compute-meter model
- no high-level guest `Context.Invoke`, typed account-info builder, or seed API
- only a small compiler syscall surface, not every current Agave syscall

The official gate is opt-in and external, not part of ordinary unit/CI
execution, and its temporary validator is not a public deployment. Completion
now requires a high-level source API that preserves the already-tested
privilege/address boundaries plus broader syscall/CPI/runtime conformance.

## 6. Custom GOSPL token program — compiled low-level implementation

Two distinct artifacts now coexist:

- `examples/spl20` is the independent native-Go state-machine oracle. Its
  exact struct/slice-based source is not a guest compilation unit.
- `examples/gospl/testdata/program.go` is the low-level Go guest source. It is
  compiled to sBPFv3, executed over serialized ABIv1 memory, and differentially
  checked against that oracle across initialize, mint, transfer, burn,
  approve/revoke, authority changes, malformed input, privileges, overflow,
  duplicate accounts, and direct mapping.

An opt-in test can build the ELF, start an empty official Agave validator,
deploy through Go-authored upgradeable-loader transactions, exercise the full
state machine, and compare finalized account bytes. It is skipped unless
`GOSBF_AGAVE_BIN` and `GOSBF_GOSPL_SO` are supplied.

The final 2026-08-16 acceptance run used official Agave 4.2.1 and the ELF
rebuilt from the checked-in Go source. It passed 66 finalized transactions,
finalized ProgramData byte verification, and the complete canonical end-state
comparison in 336.56 seconds.

GOSPL remains a custom 48-byte/120-byte token format and is not classic SPL
Token or Token-2022. A persistent Testnet program and initialized custom mint
are now recorded in `examples/gospl/testnet-deployment.json`; this does not make
the custom accounts compatible with wallet SPL-token indexes.

## 7. Official-validator and deployment tooling — new deploy and upgrade implemented

Implemented:

- isolated `solana-test-validator` process/RPC harness with inspectable logs
- legacy transaction construction, canonical Ed25519 signing, finalized RPC
  checks, and JSON-RPC account decoding
- Go-only new-program upgradeable-loader workflow: create buffer, write chunks,
  verify finalized bytes, deploy, and verify executable program state
- Go-only upgrade workflow (`deploy.Upgrade`, `sdk/loader.Upgrade`, CLI
  `upgrade`) for an already-deployed program: fails closed client-side if the
  program doesn't exist, the caller's authority doesn't match on-chain state,
  or the new ELF exceeds the allocated `MaxDataLen`, before any transaction is
  sent; verified end-to-end on a live devnet program in the 2026-08-17
  development session
- CLI key generation, guarded airdrop, strict-ELF verification, guarded
  deploy/upgrade, and opt-in real-SVM test selection
- preloaded-program and Go-only deployment acceptance tests, skipped without
  the external official Agave binary and ELF environment variables

Not implemented:

- durable on-disk deploy journal/restart recovery
- authority rotation, close/finalize workflows, verifiable/reproducible build
  metadata, and public-cluster release automation
- automated public-cluster release promotion and continuous deployment evidence

Public RPC use requires the CLI's explicit `--allow-live` gate. A dry run or
local validator result must not be described as Testnet/Mainnet deployment.
The separate recorded Testnet program has finalized public RPC and exact-byte
evidence, but its final deploy transaction used Agave's `program deploy
--buffer`; it is evidence for the Go-compiled artifact, not an end-to-end
Go-only public transport run.

## 8. High-level framework experience — partial

Implemented:

- multi-file compilation of one package (`compiler.CompileFiles`/`ParseFiles`,
  `go-solana build a.go b.go`): a function in one file may call a function
  defined in another file in the same build; a deliberate safe cross-package
  import/module model is still future work, so external imports remain
  rejected regardless of file count
- a flat, compiler-recognized account-field intrinsic family (`AccountIsSigner`
  and similar) that removes hand-computed ABIv1 offsets for the common
  read-only case — see `examples/context`. This is deliberately *not* the
  compiler-supported `Context`/Pubkey/account-view type system below: it is
  a family of plain functions, not structs or methods, because the compiler
  does not support either yet
- explicit upgrade workflow (§7); recovery is still an in-memory record, not
  durable

The remaining Anchor-like milestone includes:

- compiler-supported `Context`, Pubkey, account views, and deterministic state
  types without a Go heap or GC — this needs guest struct/method support,
  which is a larger, separate change from the flat intrinsics above
- declarative account constraints, typed instructions/errors, client/IDL
  generation, and safer syscall/CPI wrappers
- complete syscall/runtime feature coverage for the selected Agave pin
- durable crash recovery for deploy/upgrade
- broader official-runtime conformance across supported Agave/sBPF versions

No high-level API should be called production-ready until generated guest code
is tested byte-for-byte at the ABI boundary and exercised by the matching
official runtime.
