# sBPFv3 decisions and evidence

This document records the ISA and container contract implemented by
`go-solana`. It follows the Agave dependency snapshot and `solana-sbpf` 0.23.0
source pinned below, rather than historical eBPF articles or proposal text
when those disagree with executable source.

## Encoding and registers

Each instruction slot is 8 bytes:

```text
byte 0      opcode
byte 1      src in high nibble, dst in low nibble
bytes 2..3  signed i16 offset, little-endian
bytes 4..7  signed i32 immediate, little-endian
```

`LD_DW_IMM` occupies two physical slots. Its continuation has zero
opcode/register/offset fields and carries the high immediate word. A branch,
call target, or ELF entrypoint may not enter that continuation. Evidence:
[`ebpf.rs`](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/ebpf.rs#L535-L689).

The callable ABI is `r0` result, `r1-r5` arguments, `r6-r9` call-preserved,
and `r10` frame pointer. The interpreter also tracks a hidden program counter.
Evidence: [`bytecode.md`](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/doc/bytecode.md#L3-L22)
and [`interpreter.rs`](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/interpreter.rs#L138-L165).

## Branches, calls, syscalls, and exit

- A branch target is `current_pc + 1 + sign_extend(i16 offset)`, in physical
  instruction slots.
- sBPFv3 internal `CALL_IMM` uses opcode `0x85`, `src=1`, and a signed
  PC-relative i32 immediate.
- A static syscall uses the same opcode with `src=0`; the immediate is the
  Murmur3-x86-32 symbol id.
- Root `EXIT` returns `r0`. A called `EXIT` restores the caller's return PC,
  frame pointer, and `r6-r9` values.

Evidence: [`program.rs`](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/program.rs#L57-L103),
[`interpreter.rs`](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/interpreter.rs#L538-L604),
and [`verifier.rs`](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/verifier.rs#L131-L214).

The compiler emits static calls only for exact reserved bodyless declarations
whose signatures and symbols are source-pinned. `runtime.SyscallRegistry`
describes Agave's version/feature-gated linkage. The local reference VM still
rejects every static syscall because it has no Solana host-function execution
environment; an official Agave runtime resolves those calls for deployed ELF.

## Arithmetic and division by zero

The backend uses real v3 ALU64, ALU32, JMP64, and JMP32 encodings. Unsigned
division/remainder map directly to the enabled v3 instructions. Signed
`int64`/`int32` division and remainder are software-lowered because v3 does not
enable v2's PQR signed-divide instructions.

The reference VM traps on a zero immediate or register divisor. The compiler
does not silently invent a result for an unknown runtime divisor. Signed
software lowering preserves Go's two's-complement `MinInt / -1` behavior.

## Stack, memory, and resource policy

sBPFv3 uses fixed frames. The upstream defaults are 4096 bytes per frame and
64 call-depth entries. `r10` starts above the current frame and moves on
internal calls; locals and spills use negative offsets. The project VM mirrors
those defaults and adds an explicit instruction limit. Evidence:
[`vm.rs`](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/vm.rs#L50-L166).

The standard virtual regions used here are bytecode at `0x1_00000000`, stack
at `0x2_00000000`, heap at `0x3_00000000`, and input at `0x4_00000000`.
Reference-VM input mappings are explicit host slices paired with guest virtual
addresses and read/write permissions; registers never contain a Go host
pointer.

The local verifier also enforces a 65,536-instruction policy. Upstream
`RequisiteVerifier` defines related error variants without enforcing every
policy represented by them; the stricter local cap is intentional.

## Raw text versus strict ELF

Raw text has no version field. Local raw APIs interpret it as v3, and the
scalar CLI describes it as `raw sBPFv3`. `compiler.Executable.Bytecode` remains
raw so ISA tests cannot accidentally be confused with an ELF/deployment test.

Strict ELF generation is implemented separately in `elf.BuildV3`. The compact
container is ELF64 little-endian, `ET_DYN`, `EM_BPF`, with `e_flags=3`, an
executable `PT_LOAD` at the official bytecode virtual base, a checked entry,
`.text`, string/symbol tables, and a global `entrypoint` symbol. It has no
writable segment and no relocations. `elf.ParseStrictV3` checks the supported
strict-loader shape, section-name/symbol tables and their bounds, then re-runs
the text verifier. Tests accept both locally
written artifacts and the pinned official `sbpfv3_return_ok.so` Agave fixture.

The relevant upstream loader constraints are in
[`elf.rs`](https://github.com/anza-xyz/sbpf/blob/9476336b901181d68e00c5b38252a15694d4d6aa/src/elf.rs#L487-L678).
Passing the local parser is a necessary local artifact gate; official-validator
tests remain the external execution oracle.

## Solana entrypoint versus scalar harness

`go-solana run` exercises the callable function ABI: its scalar arguments are
placed in `r1-r5` and it reads `r0`. This path is useful for differential
compiler tests but is not a Solana invocation emulator.

Agave initializes a real program differently: `r1` is `MM_INPUT_START`, and
`r2` is the absolute guest virtual address of instruction data within the
serialized input. Evidence: [Agave runtime VM](https://github.com/anza-xyz/agave/blob/1f26a441bd8b390ccaeb6c145e105ab1733042a9/program-runtime/src/vm.rs#L305-L313).

`compiler.GenerateSolanaEntrypoint` emits a root wrapper that forwards those
two registers unchanged to a low-level Go handler and returns its `uint64`
program status. The handler must explicitly decode ABIv1 guest memory. The
host `runtime.Context` implementation validates the same pinned format for
tests, but the compiler cannot yet import or lower that high-level type.

## Version discrepancies resolved in favor of implementation

- Some proposal language describes arithmetic changes as v2-or-higher, while
  current `SBPFVersion::enable_pqr()` is true only for v2. The project uses
  classic unsigned v3 division and software-lowers signed operations.
- SIMD-0377 includes a `CALLX` description that differs from current source.
  This project follows `ebpf.rs`, the bytecode document, verifier, and
  interpreter.
- The crate contains v4 support while runtime/cluster activation can differ.
  This project explicitly emits v3 and does not infer cluster enablement from
  source availability.

Relevant pinned proposals are [SIMD-0178 static syscalls](https://github.com/solana-foundation/solana-improvement-documents/blob/fc519fb3d1ef0f7624b6232bda958438feba09ce/proposals/0178-static-syscalls.md),
[SIMD-0189 strict ELF](https://github.com/solana-foundation/solana-improvement-documents/blob/fc519fb3d1ef0f7624b6232bda958438feba09ce/proposals/0189-sbpf-stricter-elf-headers.md),
and [SIMD-0377 eBPF compatibility](https://github.com/solana-foundation/solana-improvement-documents/blob/fc519fb3d1ef0f7624b6232bda958438feba09ce/proposals/0377-ebpf-isa-compatibility.md).
