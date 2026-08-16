# Architecture

## Compilation pipelines

The compiler frontend and backend are shared by two explicit artifact modes:

```text
one Go source file
  -> parser (`go/parser`, `go/ast`, `go/token`)
  -> fail-closed subset validation + `go/types`
  -> typed, AST-independent control-flow IR
  -> physical register/stack assignment
  -> sBPFv3 instruction selection + relocation patching
  -> structural verification + raw 8-byte instruction slots
       |                              |
       | scalar target                | Solana target
       v                              v
  reference VM                 generated r1/r2 wrapper
                                -> strict ELF64 container
```

`compiler.Generate` returns verified raw text. `compiler.GenerateSolanaEntrypoint`
prepends a physical-PC-zero wrapper that calls a handler with Agave's `r1` and
`r2` values unchanged. `elf.BuildV3` is a separate container layer; it does not
change instruction selection.

The scalar reference VM and Solana entrypoint are intentionally different
interfaces:

- scalar runs place up to five ordinary values in `r1-r5` and return `r0`;
- Agave enters at `entrypoint` with `r1 = MM_INPUT_START` and `r2` equal to the
  absolute guest address of instruction data;
- the current Solana handler is the low-level
  `Program(inputAddress uint64, instructionDataAddress uint64) uint64` form.

The compiler does not yet synthesize a high-level `func Program(*Context)`
adapter. Low-level programs decode guest memory explicitly.

## Package boundaries

```text
compiler/       frontend, types, IR, lowering, allocation, sBPF selection,
                low-level Solana entry wrapper and syscall intrinsics
sbpf/           v3 opcodes, instruction values, verifier, codec, disassembly
elf/            compact strict ET_DYN/EM_BPF v3 writer and parser
vm/             bounded reference interpreter and explicit guest mappings
runtime/        pinned ABIv1 serializer/parser, account views, memory maps,
                program errors, syscall registry, C/Rust CPI translation
serialization/  deterministic host-side codecs with explicit bounds
sdk/            host Pubkey/instruction model, PDA derivation, System,
                classic Token, Token-2022, and loader clients
deploy/         new-program upgradeable-loader workflow over an RPC boundary
svmtest/        signing/RPC client and isolated official-validator harness
examples/       scalar programs, low-level CPI, GOSPL, and native token oracle
```

`runtime`, `serialization`, and `sdk` are real tested host packages. They are
not automatically guest libraries: the compiler rejects imports, structs,
methods, selectors, slices, and multi-file package builds. The current compiled
GOSPL and CPI examples therefore use fixed arrays plus explicit virtual-address
operations instead of importing `runtime.Context` or `sdk.Instruction`.

## Compiler invariants

- The subset checker runs before lowering and reports source positions.
- IR carries explicit scalar, pointer, and fixed-array types plus basic-block
  terminators; it has no AST dependency.
- Every IR program is validated again before instruction selection.
- Internal and static-syscall calls have at most five register arguments.
- Pointer values are guest virtual addresses. Nonzero pointer constants and
  pointer results are rejected; fixed arrays cannot cross the call ABI.
- Address-taken locals and fixed arrays live in the current fixed stack frame.
  Dynamic indexes emit bounds checks before address calculation.
- Values are mutable virtual registers. In call-free functions, the first
  three parameters may remain in `r1-r3`; `r4-r5` stay backend scratch.
  Functions containing calls relocate all caller-clobbered parameters.
- `r0` is coalesced with a directly returned final value, while `r6-r9` hold
  call-preserved homes. Remaining values spill below `r10`.
- Addressable locals and spills share the 4096-byte frame; overflow is a
  compile error.
- Functions unreachable from the selected entry are not emitted.
- Branch offsets are patched in physical 8-byte slots and must fit signed
  16-bit offsets. Internal-call offsets must fit signed 32-bit offsets.
- The final text passes the local verifier before it is exposed or wrapped in
  ELF.

## Narrow values, signed division, and remainder

The frontend has distinct `uint8`, `uint32`, and `int32` types. `uint8` is
normalized to its low byte in a register; 32-bit arithmetic/comparison uses
real v3 ALU32 and JMP32 instructions. The backend applies Go-compatible
truncation or sign extension at type boundaries, and VM differential tests
include overflow edges.

sBPFv3 has unsigned `DIV64` but not v2's PQR `SDIV64`. Signed `/` and `%` are
software-lowered: operands are converted to magnitudes, the real unsigned
operation is emitted, and the Go sign rule is restored. This preserves the
two's-complement `MinInt / -1` result. A zero register divisor remains a
runtime error in the fail-closed reference VM.

## Raw text and strict ELF

`compiler.Executable.Bytecode` remains a raw sBPFv3 text section. The selected
entry is at physical PC zero, followed by reachable internal functions. Raw
text contains no version, symbols, or ELF headers and is the artifact used by
compiler and reference-VM tests.

`elf.BuildV3` wraps verified text in the compact strict layout selected for
this project: ELF64 little-endian, `ET_DYN`, `EM_BPF`, `e_flags=3`, an
executable `PT_LOAD`, `.text`, string/symbol tables, and a global `entrypoint`
symbol. The writer deliberately emits no writable data or relocations.
`elf.ParseStrictV3` independently checks the header/segment/entry rules and
re-verifies text; tests also parse the output with Go's `debug/elf` and accept
a pinned official Agave fixture.

`go-solana build -target scalar` defaults to raw output. `-target solana`
defaults to ELF. `disassemble` accepts either form, while `verify` requires the
strict ELF form.

## Runtime and CPI boundary

The host runtime models aligned ABIv1, duplicate account slots, direct account
data mapping, realloc reservations, account mutation rules, and exact program
error return codes. Its `Context` is useful for deterministic native tests and
for constructing reference-VM memory maps.

CPI is intentionally split into three layers:

1. the compiler can emit a source-pinned static call for
   `sol_invoke_signed_c`;
2. `runtime` translates exact C/Rust layouts and validates account identity,
   privileges, PDA signer elevation, and rollback;
3. an injected `CPIExecutor` owns actual inner execution.

There is no default executor in the host runtime, the reference VM rejects
static syscalls, and no high-level guest `Context.Invoke` API exists. On an
official validator, Agave—not the reference VM—resolves the static syscall.
The compiled System-transfer example has an opt-in official-validator gate;
its ephemeral Agave 4.2.1 run passed in the 2026-08-16 development session.
That validates this low-level path, not a general host executor or framework
API.

## Deployment boundary

`deploy.Program` can create a new upgradeable-loader program from a locally
validated strict ELF. It constructs and signs transactions in Go, verifies the
finalized buffer bytes before the final deploy, and fails closed if the program
account already exists. The returned partial record is in memory only.

Program upgrade, authority rotation/closure, durable crash recovery, and an
Anchor-like deployment manifest are not implemented. Official-validator tests
are opt-in external acceptance gates, not part of ordinary unit execution and
not evidence of a persistent public-cluster deployment.
