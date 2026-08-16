# ERC1155-style multi-token program

A custom, compiler-written multi-token Solana program alongside
[`examples/gospl`](../gospl/README.md) (single-token) and classic SPL
Token/Token-2022. It provides the same *capabilities* as Ethereum's ERC1155
— per-id balances, per-id supply/URI, approve-for-all — re-expressed as
separate Solana accounts, this repository's established custom-program
convention. It is **not** a byte-for-byte port: Solana has no
contract-internal `mapping(...)` storage, so every ERC1155 mapping entry
(`balanceOf(owner, id)`, `isApprovedForAll(owner, operator)`, `uri(id)`)
becomes its own account here, the same way [`examples/gospl`](../gospl) and
[`examples/phonebook`](../phonebook) already model relationships as
plain, client-created accounts rather than program-derived addresses.

## Two deliberate deviations from literal ERC1155 shape

1. **No batch instruction.** ERC1155's `safeBatchTransferFrom`/`mintBatch`
   exist because one Ethereum contract call is the atomicity/gas unit.
   Solana's atomicity unit is the *transaction*, which already bundles
   multiple instructions atomically for free — so "batch transfer" here is
   just packing multiple `Transfer`/`MintTo` instructions into one
   transaction client-side. No fixed-size batch array, no artificial cap.
2. **No receiver-hook callback.** ERC1155's `onERC1155Received` safety hook
   has no Solana analog and is omitted.

Also simplified from `examples/gospl`'s exact style: an `Initialize*`
instruction that finds non-zero, non-matching-tag bytes in a supposedly
fresh account returns `ErrAlreadyInitialized` directly rather than further
distinguishing "already a valid same-type record" from "garbage" (GOSPL's
`ValidateMint`/`ValidateToken` do that extra classification). Both cases
still safely refuse to overwrite non-empty state; only the diagnostic
precision of the returned error code differs.

## Account model

Every account is a plain, client-created, program-owned account (created via
`system.CreateAccount` + an `Initialize*` instruction) — not a PDA, matching
this repo's other custom programs. The program itself verifies each
account's *stored* collection/id/owner fields match expectations, the same
way GOSPL's `Transfer` checks `source.Mint == destination.Mint`.

| Account | Size | Fields | ERC1155 equivalent |
|---|---|---|---|
| Collection | 41 | tag(1) authority(32) next_id(8) | the deployed contract instance |
| TokenType | 117 | tag(1) collection(32) id(8) supply(8) uri_len(4) uri(64) | one id's `totalSupply()`/`uri()` |
| Balance | 81 | tag(1) collection(32) id(8) owner(32) amount(8) | one `balanceOf(owner, id)` entry |
| Approval | 98 | tag(1) collection(32) owner(32) operator(32) approved(1) | one `isApprovedForAll(owner, operator)` entry |

`uri` is a fixed 64-byte cap (`MaxURILength`), mirroring the fixed-length
metadata fields already used by [`sdk/metaplex`](../../sdk/metaplex)
elsewhere in this repo. `TokenType.id` is assigned by the program from
`Collection.NextID` and returned to the client by reading back the newly
created account after the transaction, the same as reading back any other
freshly created account.

## Instructions

| # | Kind | Accounts | Data |
|---|------|----------|------|
| 0 | InitializeCollection | `[collection(w,s)]` | `authority:Pubkey` |
| 1 | CreateTokenType | `[token_type(w,s), collection(w), authority(s)]` | `uri_len:u32, uri:bytes` |
| 2 | InitializeBalance | `[balance(w,s), token_type(r)]` | `owner:Pubkey` |
| 3 | MintTo | `[collection(r), token_type(w), balance(w), authority(s)]` | `amount:u64` |
| 4 | Burn | `[token_type(w), balance(w), owner(s)]` | `amount:u64` |
| 5 | Transfer | `[source(w), destination(w), owner(s)]` | `amount:u64` |
| 6 | InitializeApproval | `[approval(w,s), owner(s), operator(r)]` | `collection:Pubkey, approved:bool` |
| 7 | SetApproval | `[approval(w), owner(s)]` | `approved:bool` |
| 8 | TransferFrom | `[source(w), destination(w), approval(r), operator(s)]` | `amount:u64` |

`Burn` requires the balance's own owner to sign directly — there is no
delegated/approved burn in this example (only `TransferFrom` uses the
approval mechanism).

## Guest program (multi-file, using this repo's account-field intrinsics)

- [`testdata/accounts.go`](testdata/accounts.go): the shared low-level
  toolkit — `LoadUint8`/`StoreUint64`/etc. guest-memory intrinsics, the
  compiler's account-field intrinsics (`AccountIsSigner`, `AccountKeyAddress`,
  `AccountDataAddress`, and similar — see the top-level
  [README](../../README.md#implemented-deterministic-go-subset)), a
  loop-based `AccountAt` that generalizes `examples/gospl`'s hand-unrolled
  0..2 duplicate-account cascade to a fixed 8-slot resolution table (this
  program's instructions use up to 4 accounts), and `RequireOwned`/
  `RequireSigner`/`RequireAuthority` privilege-check helpers built entirely
  from those intrinsics — no hand-computed `record+N` account-record offsets
  appear anywhere in this file.
- [`testdata/program.go`](testdata/program.go): the `Program(inputAddress,
  instructionDataAddress) uint64` entrypoint, instruction-tag dispatch, and
  one `Process*` handler per instruction.

Compiled together via the compiler's multi-file support:

```bash
go run ./cmd/go-solana build -target solana \
  -o examples/erc1155/erc1155.so \
  examples/erc1155/testdata/accounts.go examples/erc1155/testdata/program.go
go run ./cmd/go-solana verify examples/erc1155/erc1155.so
go run ./cmd/go-solana disassemble examples/erc1155/erc1155.so
```

## Host package

[`types.go`](types.go), [`codec.go`](codec.go), and [`instruction.go`](instruction.go)
follow `examples/gospl`'s exact file split: an `InstructionKind` byte
discriminant, decoded Go structs for the four account layouts, a
`ProgramError` enum matching the guest program's numeric error codes
1:1 (the guest side cannot import this package, so the codes are pinned by
value in `program.go`'s comments), and `Encode*`/`Decode*` wire codecs plus
`sdk.Instruction`-building functions per instruction.

## Tests

- `codec_test.go` — byte-exact round-trip tests for all four account
  layouts and all nine instructions, plus malformed-input rejection.
- `contract_test.go` — compiles `accounts.go`+`program.go` together and
  confirms the result is a valid, re-parseable strict sBPFv3 ELF (the same
  smoke test every other example has).
- `program_test.go` — the real correctness oracle: runs the *compiled*
  program in the reference VM against real serialized Agave ABIv1 memory
  through a full lifecycle (init collection → create token type → init two
  balances → mint → transfer → burn → init+set approval → transferFrom),
  asserting exact decoded account state after every step, plus negative
  cases (wrong authority, missing signature, insufficient balance, transfer
  across different token ids, revoked approval) each asserting the exact
  `ProgramError` code returned. Unlike `examples/gospl` (cross-checked
  against the independent `examples/spl20` native model), there is no
  parallel native reference here — asserting exact account bytes from real
  compiled-bytecode execution is the stronger and simpler oracle for a
  single program with no second implementation to keep in sync.

```bash
go test ./examples/erc1155/...
```

## CLI client and Devnet deployment

[`cmd/erc1155-init`](cmd/erc1155-init) mirrors `examples/gospl/cmd/gospl-init`:
it creates a collection, defines one token type, initializes a balance, and
mints a raw amount, verifying finalized on-chain state byte-for-byte after
each stage. The program has been deployed and exercised live on Devnet.

## Wallet-visible items (real Token-2022 NFTs)

The account this program creates (`Balance`, `TokenType`, `Collection`) will
**never** show up as an NFT in a wallet or in Solana Explorer, no matter what
changes are made to this program: wallets and Explorer only render an
account as an NFT when it's a real SPL Token/Token-2022 mint carrying a
Metaplex Metadata account *and* a Metaplex Master Edition account — a custom
program's own account layout, however it's shaped, is invisible to that
standard.

For the `amount == 1` case — a true, unique item — the honest equivalent is
[`examples/token2022/cmd/token2022-nft-init`](../token2022/cmd/token2022-nft-init):
a separate tool that mints a real Token-2022 NFT (`metaplex.CreateNFTV1`,
`sdk/metaplex`) with decimals 0 and supply permanently capped at 1, which
does render in a wallet's Collectibles/NFT view. An `amount > 1` item has no
wallet-visible equivalent on Solana at all — a Master Edition's supply is
capped at exactly 1 by the mpl-token-metadata program itself
(`EditionsMustHaveExactlyOneToken`), so a multi-copy ERC1155 item stays a
semi-fungible token (`examples/token2022/cmd/token2022-init`'s
`FungibleAsset` metadata, not this package) and always shows in a wallet's
fungible token list, never its Collectibles tab. This is a property of
Solana's NFT standard, not a limitation of this program.

## Not included in this pass

- No opt-in official-Agave acceptance test (compare `examples/gospl`'s
  `GOSBF_AGAVE_BIN`-gated test) — a heavier, separate gate requiring a local
  Agave binary.
- No verified Metaplex collection linking multiple `token2022-nft-init`
  items together (an unverified `collection` field could be added cheaply,
  but wallets generally only group items under a *verified* collection, and
  this repo has no `VerifyCollectionV1` builder yet) — left as future work.
