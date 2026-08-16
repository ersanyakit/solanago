# token2022-init

This command creates a real Token-2022 mint/account pair and mints supply for
the test wallet/account owner you choose. It is host-side tooling built on the
project's Go Token-2022 package and `svmtest` JSON-RPC client.

When `--name`/`--symbol`/`--uri` are set, this command creates a Metaplex
Token Metadata account (the `Create` / "new API" instruction, discriminator
42 — not the legacy `CreateMetadataAccountV3`) for the mint. Wallets,
explorers, and DEX frontends (Raydium's UI included) resolve a token's
displayed name/symbol from this account; without it a mint typically shows
up with a placeholder name derived from its own address instead of the real
name/symbol.

The mint itself carries no Token-2022 metadata extension. An earlier version
of this tool additionally set `MetadataPointer` (self-pointing at the mint)
and wrote Token-2022's native metadata extension there — but
mpl-token-metadata's dispatcher routes any instruction touching a
Token-2022-owned account away from the legacy processor entirely, and its
`Create` handler additionally rejects a mint whose `MetadataPointer` doesn't
already target the Metaplex Metadata PDA with no separate authority. Setting
both produced `MetadataError::InstructionNotSupported` (custom program error
`0x99`) on every metadata-creation attempt, so only the Metaplex account is
written now.

## usage

```bash
go run ./examples/token2022/cmd/token2022-init \
  --keypair /absolute/path/payer.json \
  --url testnet \
  --allow-live \
  --decimals 6 \
  --name DEMODEMO \
  --symbol DEMO \
  --amount-ui 100000
```

Supported amount forms:

- `--amount-raw <uint64>` raw smallest-unit amount
- `--amount-ui 100000` converted exactly from UI with `--decimals`

The command:
1) creates a new mint account,
2) initializes the mint (`MintAuthority` = payer), and — if `--name`/`--symbol`/
   `--uri` are set — creates a Metaplex Token Metadata account for the mint,
3) creates the canonical Associated Token Account (ATA) for `--owner` (default
   payer) — the same address wallets, explorers, and DEXes (Raydium included)
   derive on their own, so the minted balance is discoverable without extra
   setup,
4) mints the requested amount to that token account.

It waits for each transaction to finalize and returns a JSON journal including
submit/finalization signatures and byte-canonical on-chain state checks.
Transactions are submitted exactly once: an unknown outcome is recorded under
`non_finalized_signatures` and is never retried automatically.
`finalized_and_verified` is true only after the mint supply and token-account
balance have both been re-read at finalized commitment and verified.

By default the four steps above are submitted as separate, individually
verified transactions (safer to diagnose if one fails ambiguously, at the
cost of extra round trips). Pass `--atomic` to submit steps 1, 2, 3, and 4 as
a single transaction instead — it either all lands or none of it does, but a
failure gives less detail about which part failed and the combined
instructions must fit Solana's single-transaction size/compute budget.

## example: `helloEarth`

Example for testnet:

```bash
go run ./examples/token2022/cmd/token2022-init \
  --keypair /absolute/path/payer.json \
  --url testnet \
  --allow-live \
  --decimals 6 \
  --name DEMODEMO \
  --symbol DEMO \
  --uri https://example.com/token.json \
  --amount-raw 100000000000
```

By default name/symbol are `DEMODEMO`/`DEMO`; supply `1000` with `--decimals 6`
means `1000 * 10^6 = 1000000000` raw.
