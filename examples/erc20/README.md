# ERC-20 analogue

Solidity's ERC-20 keeps its entire state in one contract:

```solidity
contract ERC20 {
    string public name;
    string public symbol;
    uint8 public decimals;
    uint256 public totalSupply;
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    function transfer(address to, uint256 amount) external returns (bool);
    function approve(address spender, uint256 amount) external returns (bool);
    function transferFrom(address from, address to, uint256 amount) external returns (bool);
}
```

Solana account data has no dynamic per-key storage — a `mapping` can't live
inside one account — so every mapping entry here becomes its own account,
the same pattern `examples/spl20`/`examples/gospl` and `examples/erc1155`
already establish. This example mirrors Solidity's ERC-20 field-for-field
and function-for-function, unlike `examples/gospl` (SPL-Token-shaped: one
delegate per token account, no `name`/`symbol`/`totalSupply`).

| Solidity | This contract | SVM shape |
| --- | --- | --- |
| the contract itself (`name`, `symbol`, `decimals`, `totalSupply`) | `MintState` | one account per token, created by `InitializeMint` |
| `balanceOf[owner]` | `BalanceState` | one account per (mint, owner), created by `InitializeBalance` |
| `allowance[owner][spender]` | `AllowanceState` | one account per (mint, owner, spender), created by `InitializeAllowance` |
| `transfer(to, amount)` | `Transfer` | owner signs directly |
| `approve(spender, amount)` | `Approve` | **sets**, not adds to, the allowance — same semantics as Solidity's `approve` |
| `transferFrom(from, to, amount)` | `TransferFrom` | spender signs; decrements the allowance by amount (OpenZeppelin's classic decrement-on-spend) |
| a mint/issuance function | `MintTo` | authorized by `MintState.MintAuthority` |
| (commonly added) `burn` | `Burn` | balance owner signs directly |

## Native model plus compiled guest, like `examples/spl20`/`examples/gospl`/`examples/payable`

- `types.go`, `state.go`, `instruction.go`, `program.go` (this directory) —
  a readable **native model**: ordinary host Go structs, pointers, and
  slices the guest compiler does not accept. It also doubles as the
  host-side wire-format package.
- `testdata/accounts.go`, `testdata/program.go` — the **compiled guest
  program**, using the exact same 100-byte `MintState` / 80-byte
  `BalanceState` / 112-byte `AllowanceState` wire layout, so a
  guest-produced account's raw bytes decode directly with
  `DecodeMintState`/`DecodeBalanceState`/`DecodeAllowanceState`.

```bash
go test -count=1 ./examples/erc20
go run ./cmd/go-solana build -target solana \
  -o /tmp/erc20.so examples/erc20/testdata/accounts.go examples/erc20/testdata/program.go
go run ./cmd/go-solana verify /tmp/erc20.so
```

Unlike `examples/payable`, this program never moves lamports — every
instruction is pure account-data arithmetic — so `contract_test.go` gets
full differential coverage for the entire instruction set: it runs the
compiled guest program in the reference VM against real serialized Agave
ABIv1 memory and cross-checks every result and every account's resulting
bytes against the native model above, with no CPI-shaped gap to document.

## Instruction account order

| Instruction | Accounts, in order | Notes |
| --- | --- | --- |
| `InitializeMint` | writable+signer mint | New account signs its own first write. |
| `InitializeBalance` | writable+signer balance, mint | `owner` (carried in instruction data) need not sign, matching `spl20.InitializeAccount`. |
| `MintTo` | writable mint, writable balance, signer authority | `authority` must match `MintState.MintAuthority`. |
| `Burn` | writable balance, writable mint, signer owner | |
| `Transfer` | writable source, writable destination, signer owner | |
| `InitializeAllowance` | writable+signer allowance, mint, signer owner, spender | Both the new allowance account and `owner` sign; `spender` is just recorded. |
| `Approve` | writable allowance, signer owner | Sets `AllowanceState.Amount` to the given value — it does not add to it. |
| `TransferFrom` | writable source, writable destination, writable allowance, signer spender | Requires `allowance.Amount >= amount`; decrements it by `amount`. |

Instruction data has exact lengths and little-endian `uint64` amounts. `name`
is zero-padded to 32 bytes, `symbol` to 10 bytes — Solidity's `string`
fields have no length limit, but Solana account data does not support
unbounded fields any more than it supports mappings.

## What a real deployment would still need

This model intentionally does not implement:

- **decimals-aware display formatting** — `Decimals` is stored and returned
  as-is; converting a raw `TotalSupply`/`Amount` to a human-readable value
  is a client-side concern, same as real SPL tokens;
- **renouncing the mint authority** — Solidity ERC-20s commonly support
  transferring ownership to the zero address; `MintState.MintAuthority` is
  an `OptionalPubkey` for exactly that extension, but no instruction here
  clears it, so `MintTo` always trusts whichever authority `InitializeMint`
  recorded;
- **PDA-derived accounts** — as with `examples/payable`, a real deployment
  would likely derive `MintState`/`BalanceState`/`AllowanceState` addresses
  as program-derived addresses instead of fresh signing keypairs.

These are scoping choices to keep the state machine legible, not gaps
discovered by testing.
