# Account-field intrinsics ("Context API v1") across two files

This example exists to demonstrate two additions together, not to do
anything on-chain by itself:

- **Multi-file compilation**: [`testdata/accounts.go`](testdata/accounts.go)
  and [`testdata/program.go`](testdata/program.go) are compiled as one
  package. `program.go`'s `Program` entrypoint calls `AccountAt`,
  `AccountIsSigner`, and `AccountIsWritable` — all defined only in
  `accounts.go` — with no cross-file redeclaration.
- **Account-field intrinsics**: `accounts.go` reimplements the same
  duplicate-record walk [`examples/cpi`](../cpi/testdata/program.go) hand-rolls
  as `NextPhysicalAccount`/`AccountAt`, but built from
  `AccountIsSigner`/`AccountIsWritable`/`AccountDataLen` instead of raw
  `record+1`/`record+2`/`record+80` arithmetic. `program.go` itself contains
  no ABIv1 byte offsets at all.

This is **not** the full high-level `Context` framework described in
[`docs/roadmap.md`](../../docs/roadmap.md) §8 (that needs struct/method
support the compiler doesn't have yet). It is a flat, compiler-recognized
family of fixed-offset accessors — sugar over the same `LoadX(record+N)`
pattern every other example already uses, kept byte-for-byte equivalent to
it (see `TestAccountFieldIntrinsicsMatchManualOffsetArithmetic` in
`compiler/account_intrinsics_test.go`).

`Program` requires account 0 to be both a signer and writable, returning `0`
on success or a small nonzero error code otherwise — enough to exercise real
control flow without needing a CPI.

## Build, verify, disassemble

```bash
go run ./cmd/go-solana build -target solana \
  -o examples/context/context.so \
  examples/context/testdata/accounts.go examples/context/testdata/program.go
go run ./cmd/go-solana verify examples/context/context.so
go run ./cmd/go-solana disassemble examples/context/context.so
```

Or via `run`, which needs its own file list flag since trailing positional
arguments after the first are already scalar call arguments:

```bash
go run ./cmd/go-solana run -func Program \
  -files examples/context/testdata/accounts.go \
  examples/context/testdata/program.go
```
