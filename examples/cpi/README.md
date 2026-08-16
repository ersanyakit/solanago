# Go System Program CPI example

[`testdata/program.go`](testdata/program.go) is compiled directly to sBPFv3.
It constructs `SolInstruction`, two `SolAccountMeta` values, and three
`SolAccountInfo` values in a fixed sBPF stack array, then calls the official
`sol_invoke_signed_c` static syscall to transfer lamports through the System
Program.

```bash
go run ./cmd/go-solana build -target solana \
  -o /tmp/gosbf-cpi.so examples/cpi/testdata/program.go
go run ./cmd/go-solana verify /tmp/gosbf-cpi.so
```

`AddressUint64` exposes only a compiler-managed guest stack address. All
account pointers are the exact ABI v1 virtual addresses Agave serialized; no
host pointer, Go heap/GC object, `unsafe`, Rust, or C ABI compiler is involved.
The IR verifier tracks those stack-derived integer addresses across control
flow, helper calls, stores, and memory syscalls and rejects frame escape or
external retention.

The opt-in real test starts an empty local official Agave validator, deploys
this ELF through the Go loader, executes it, checks exact finalized
source/destination balance deltas, and requires the real runtime to reject
signer and writable privilege escalation:

```bash
GOSBF_AGAVE_BIN=/path/to/agave/bin \
GOSBF_CPI_SO=/tmp/gosbf-cpi.so \
go test -count=1 -run '^TestAgaveSystemTransferCPI$' -v ./examples/cpi
```

The test is skipped unless both environment variables are set and is not part
of the ordinary Go/CI gate. During the 2026-08-16 development session, the
exact command passed against official Agave 4.2.1 in 84.31 seconds: the System
Program CPI changed finalized balances by exactly `-123456/+123456` lamports,
and writable and signer escalation attempts were rejected without any balance
mutation.

The current source reproducibly builds an 8,472-byte ELF with SHA-256
`f3be0f629483446d480c5b9217ca7350f89b612eeaa7cd9637dac2df573d4918`,
identical to the artifact accepted by that Agave run. The opt-in test now
rebuilds the checked-in Go source and refuses a non-identical supplied ELF.

That observation belongs to an ephemeral local validator. It is not a
persistent Testnet/Mainnet deployment, and it does not imply that the reference
VM executes syscalls or that a high-level guest `Context.Invoke` API exists.
The source remains an explicit low-level ABI example.

Pinned sBPF, Agave ABIv1, entry-register, and syscall sources are linked from
the [root documentation](../../README.md#authoritative-pinned-sources).
