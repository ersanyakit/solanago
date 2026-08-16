# SPL-20-style native token oracle

There is no official Solana standard named “SPL-20”. Solana's canonical token
programs are the original SPL Token Program and Token-2022. The `Spl20` name in
Solana's ERC-20 comparison is a rough analogy, not another token program
specification.

This directory contains an **ERC-20-like custom native-Go reference model**.
It is not a replacement for either canonical token program and is not
compatible with their account or instruction encodings.

Implemented reference operations are:

- initialize mint and token accounts;
- mint tokens with a revocable mint authority;
- owner/delegate transfers and burns with bounded allowance;
- approve and revoke delegate;
- change/remove mint authority and change token-account owner;
- deterministic fixed-width state and instruction encoding;
- owner, signer, writable, mint, overflow, underflow, and malformed-state
  checks;
- prepare-before-commit behavior so failed operations do not mutate accounts.

The native test account's `Owner`, `IsSigner`, and `IsWritable` fields model
privileges supplied by the Solana runtime. They are controlled by the native
test harness; a compiled program must derive those privileges from ABIv1 and
must never let instruction bytes invent them.

The model follows selected high-level state-transition rules of the canonical
token programs, including same-mint transfer validation, mint-authority
checks, one delegate allowance per token account, and checked supply changes.
Its private wire format is deliberately different: mint and token-account
layouts are 48 and 120 bytes, not the classic program's 82-byte Mint and
165-byte Account formats.

## Instruction account order

| Instruction | Accounts, in order |
| --- | --- |
| `InitializeMint` | writable mint |
| `InitializeAccount` | writable token account, mint |
| `Transfer` | writable source, writable destination, signer owner/delegate |
| `MintTo` | writable mint, writable destination, signer mint authority |
| `Burn` | writable source, writable mint, signer owner/delegate |
| `Approve` | writable source, signer owner, delegate identity |
| `Revoke` | writable source, signer owner |
| `SetAuthority` | writable mint/token account, current signer authority |

Instruction data has exact lengths and little-endian `uint64` amounts. These
tags/layouts must not be sent to either canonical Token Program.

Official sources used to select the modeled semantics and document that
boundary are:

- https://solana.com/developers/evm-to-svm/erc20
- https://solana.com/docs/tokens
- https://solana.com/docs/tokens/basics
- https://github.com/solana-program/token/blob/f5285693a93135a144e24859c84d26ac20037a3a/interface/src/instruction.rs
- https://github.com/solana-program/token/blob/f5285693a93135a144e24859c84d26ac20037a3a/program/src/processor.rs
- https://github.com/solana-program/token/blob/f5285693a93135a144e24859c84d26ac20037a3a/interface/src/state.rs
- https://github.com/solana-program/token-2022/tree/567074d43dc87522846728cc0b598bca27df764a

## Native source versus compiled GOSPL

The files in this directory use ordinary host Go structs, pointers, byte
slices, methods, and account views. The guest compiler still rejects those
features, so this exact source remains a native oracle:

```bash
go test -count=1 ./examples/spl20
```

That does not mean the custom state machine is compiler-only future work.
[`../gospl/testdata/program.go`](../gospl/testdata/program.go) is a separate,
low-level guest implementation of the same 48-byte/120-byte format. It uses
explicit ABIv1 guest addresses, is compiled to sBPFv3, and is differentially
checked against this directory's native model. An opt-in test can deploy that
GOSPL ELF to an ephemeral official Agave validator.

The distinction is important:

- `examples/spl20`: readable native oracle; this exact source is not emitted
  as sBPF;
- `examples/gospl`: compiled low-level custom program with the same private
  state/wire semantics;
- `sdk/token` and `sdk/token2022`: host clients/codecs for the two canonical
  programs, not guest implementations of either program.

See the [GOSPL guide](../gospl/README.md) for build and opt-in validator
commands.

## Canonical Testnet Token-2022 mint

A separate canonical Token-2022 mint was created on Solana Testnet to provide
a wallet/RPC-visible token while preserving the custom-program boundary:

- mint: `7iYGbsx7X1i6Jsm3nBivyVQPKwRgn2qdvtaZdARaA5rE`
- name/symbol: `GOSPLToken` / `GOSPL`
- decimals: `6`
- recorded initial supply: `100000` (`100000000000` raw units)
- owner ATA: `CkycveQc2PNkuzGJgTEbKZgreCndWgkQPYEjoL2ymhUr`
- token program: Token-2022
  (`TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb`)

[`testnet-token.json`](testnet-token.json) records the cluster genesis hash,
addresses, finalized transaction signatures/slots, authority state, and the
last RPC verification snapshot. It is historical evidence at the recorded
slots, not a live re-query performed by the unit suite.

The JSON explicitly records `artifactKind: canonical-token-2022-mint` and
`customGoProgramDeployed: false`. This mint was created and is governed by the
official Token-2022 program. It is not a deployment, program id, or execution
result of the custom GOSPL Go program.
