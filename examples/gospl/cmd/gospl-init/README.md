# `gospl-init`

This command creates and initializes one custom GOSPL mint account and one
custom GOSPL token account for an already-deployed program, then mints the
requested supply. The mint authority and token-account owner are the public
key from `--keypair`; the newly created mint and token-account signers exist
only in memory.

Live RPC endpoints are rejected unless `--allow-live` is present. Exactly one
amount form is required:

- `--amount-raw 100000000000` is the raw smallest-unit quantity;
- `--amount-ui 100000 --decimals 6` converts exactly to the same raw quantity
  without floating-point arithmetic.

Example for Testnet:

```bash
go run ./examples/gospl/cmd/gospl-init \
  --program PROGRAM_ADDRESS \
  --keypair /secure/path/payer.json \
  --url testnet \
  --allow-live \
  --decimals 6 \
  --amount-ui 100000
```

A successful run submits exactly three transactions (mint creation,
token-account creation, and mint-to). Before submitting, it checks that the
finalized payer balance covers both rent exemptions plus a 100,000-lamport fee
reserve. It waits for each transaction to finalize and byte-verifies the state
needed by the next stage. After mint-to it re-reads both accounts at finalized
commitment and verifies their program owner, rent balance, canonical state
encoding, authority, mint, supply, and token balance.

The command never retries a failed or ambiguous submission. A signature
returned together with an error may only be the locally derived transaction
signature, so it is recorded under `non_finalized_signatures`, not falsely
under `submitted_signatures`. Only a successful finalized RPC result is put in
both `submitted_signatures` and `finalized_signatures`. The public addresses
and exact planned amount are written to the progress journal before the first
transaction; if that journal cannot be written, the command fails before
spending lamports.

All raw token quantities and lamport values in JSON are decimal strings. This
preserves the complete `uint64` range in JavaScript and other JSON consumers
whose numeric type cannot exactly represent integers above 2^53. UI amounts
are parsed and formatted with integer arithmetic only; exponent notation,
signs, non-ASCII digits, excess fractional precision, duplicate amount flags,
and zero mint amounts are rejected.

GOSPL is a custom program. Its accounts are deliberately not classic SPL
Token or Token-2022 accounts, so wallets and Explorer token tabs that only
index those official programs will not list them as standard SPL assets. Use
the returned account addresses and the program-specific codecs to inspect
their state.
