# Payable vault (Solidity `payable`/`msg.value` analogue)

Solidity lets a function declare itself `payable` and read `msg.value`: the
EVM attaches an ETH transfer to the call itself, atomically, before the
function body runs.

```solidity
mapping(address => uint256) public balances;

function deposit() external payable {
    balances[msg.sender] += msg.value;
}

function withdraw(uint256 amount, address payable to) external {
    require(balances[msg.sender] >= amount);
    balances[msg.sender] -= amount;
    to.transfer(amount);
}
```

The SVM has no equivalent of `msg.value`. There is no implicit value
attached to a call; moving lamports is always its own explicit System
Program operation, and a program's ability to move them is asymmetric by
account ownership:

- a program may **debit** lamports only from accounts *it owns*;
- **any** account may **credit** lamports to any other account.

That asymmetry is why this example's two operations look structurally
different even though they mirror the Solidity pair one-to-one:

| Solidity | This contract | Why it differs |
| --- | --- | --- |
| `deposit() payable` | `Deposit` | The depositor's wallet is a System Program account, not owned by this contract, so this contract cannot debit it directly. A real deployment must invoke the System Program's `Transfer` instruction via CPI, with the depositor as a signing source account. |
| `withdraw(amount, to)` | `Withdraw` | The vault account *is* owned by this contract, so this contract may debit it directly and credit `recipient` directly — no CPI needed. |

[`examples/cpi/testdata/program.go`](../cpi/testdata/program.go) is the
low-level guest program that first proved this System Program CPI
(building `SolInstruction`/`SolAccountInfo` in sBPF stack memory and calling
`sol_invoke_signed_c`) against a real Agave validator — see its
[README](../cpi/README.md). `testdata/program.go` in this directory reuses
that exact CPI-construction block, unchanged apart from which two accounts
play source/destination, for `Deposit`'s CPI leg.

## Native model plus compiled guest, like `examples/spl20`/`examples/gospl`

This directory has both halves of the pattern `examples/spl20`/`examples/gospl`
established:

- `types.go`, `state.go`, `instruction.go`, `program.go` (this directory) —
  a readable **native model**: ordinary host Go structs, pointers, and
  slices the guest compiler does not accept. It is not compiled to sBPF and
  is exercised only by this package's native Go tests. It also doubles as
  the host-side wire-format package (its `Encode*`/`Decode*` functions are
  reused directly by the guest-differential tests below).
- `testdata/accounts.go`, `testdata/program.go` — the **compiled guest
  program** that the compiler accepts and emits as an sBPFv3 ELF, using the
  same 48-byte `VaultState`/80-byte `DepositState` wire layout as the native
  model above, so a guest-produced account's raw bytes decode directly with
  `DecodeVaultState`/`DecodeDepositState`.

```bash
go test -count=1 ./examples/payable
go run ./cmd/go-solana build -target solana \
  -o /tmp/payable.so examples/payable/testdata/accounts.go examples/payable/testdata/program.go
go run ./cmd/go-solana verify /tmp/payable.so
```

`contract_test.go` runs the compiled guest program in the reference VM
against real serialized Agave ABIv1 memory and cross-checks every result and
every account's resulting data and lamports against the native model above —
for `InitializeVault`, `InitializeDepositAccount`, `Withdraw`, and
`EmergencyWithdraw`. `Deposit` cannot be differentially tested the same way
(see "Native model versus compiled guest" below); instead its guest-only
tests prove every pre-CPI check passes for well-formed accounts by running
it all the way to `sol_invoke_signed_c`, where the reference VM — which has
no CPI executor by design (see `runtime/cpi.go`) — reports
`vm.ErrUnsupportedCall`. That is the strongest local proof available for the
CPI leg: it is not the same as a live-validator balance-delta proof.
Unlike `examples/cpi`, this repository has no opt-in Agave acceptance gate
for `Deposit` — none of this package's tests have been run against a real
validator, only the local reference VM.

## Native model versus compiled guest

`Deposit`'s guest account list is **four** accounts (vault, deposit account,
depositor, System Program) where the native model's is **three** — the
guest program actually performs the CPI, so it needs the System Program
account to invoke; the native model only documents what that CPI would do.
This is the one place the two representations' wire format intentionally
diverges; every other instruction's accounts and data layout match exactly.

## Accounts

Two account kinds, fixed-width like `examples/spl20`'s mint/token-account
split:

- **Vault** (`VaultStateSize` = 48 bytes) — one ledger per vault: whether
  it's initialized, the running total of lamports it's tracked as
  deposited, and the `Authority` pubkey allowed to call
  `EmergencyWithdraw`. The SVM analogue of a single contract-level storage
  slot plus an `owner` variable.
- **Deposit account** (`DepositStateSize` = 80 bytes) — one per (vault,
  depositor) pair: which vault it belongs to, which depositor it belongs
  to, and that depositor's balance. The SVM analogue of one slot in
  `mapping(address => uint256) public balances`, materialized as its own
  account because Solana account data has no dynamic per-key storage.

## Instruction account order

| Instruction | Accounts, in order | Notes |
| --- | --- | --- |
| `InitializeVault` | writable+signer vault | New account signs its own first write, like `spl20.InitializeMint`. The `authority` pubkey carried in instruction data becomes the only signer `EmergencyWithdraw` will accept. |
| `InitializeDepositAccount` | writable+signer deposit account, vault | Binds the deposit account to `vault` and the `depositor` pubkey carried in instruction data; `depositor` itself need not sign, like `spl20.InitializeAccount`. |
| `Deposit` | writable vault, writable deposit account, writable+signer depositor (native); same three plus a fourth readonly System Program account (compiled guest) | Moves `amount` lamports depositor → vault and credits the deposit account's balance. See "Native model versus compiled guest" below for why the account count differs. |
| `Withdraw` | writable vault, writable deposit account, signer depositor, writable recipient | Moves `amount` lamports vault → recipient and debits the deposit account's balance. `recipient` need not be the depositor. |
| `EmergencyWithdraw` | writable vault, signer authority, writable recipient | Moves `amount` lamports vault → recipient. `authority` must match `VaultState.Authority`, the pubkey passed to `InitializeVault`. Bypasses every `DepositState` entirely — see the caveat below. |

Instruction data has exact lengths and a little-endian `uint64` amount,
matching `examples/spl20`'s wire conventions.

## `EmergencyWithdraw` is an intentional trust escape hatch

`EmergencyWithdraw` is an `onlyOwner`-style rescue function — the SVM
equivalent of:

```solidity
function emergencyWithdraw(uint256 amount, address payable to) external onlyOwner {
    to.transfer(amount);
}
```

It authorizes purely on `authority` matching `VaultState.Authority` and
signing; it does not consult, update, or care about any `DepositState`. That
means a completed emergency withdrawal can leave `vault` holding fewer
lamports than depositors are collectively recorded as owning in their
individual `DepositState.Balance` fields — `Withdraw` calls after that point
will still report a nonzero balance for those depositors even though the
lamports backing it are gone. This mirrors exactly the trust concentration
an `onlyOwner` rescue function creates in Solidity: the authority key can
unilaterally drain funds depositors believe are theirs. `Program.ID` does
not gate who may be named `Authority` at `InitializeVault` time — a
production deployment should back it with something depositors can verify
in advance (a known multisig, a timelock, or omitting the instruction
entirely), not silently trust whoever happened to initialize the vault.

## What a real deployment would still need

This model intentionally does not implement:

- **rent-exemption accounting** — a real vault must keep enough lamports to
  stay rent-exempt; `Withdraw` here only checks the tracked ledger balance,
  not `Rent::minimum_balance`;
- **live-validator proof of the `Deposit` CPI** — see "Native model plus
  compiled guest" above for exactly what is and isn't verified locally;
- **PDA-derived vault addresses** — a real deployment would likely derive
  the vault (and possibly the deposit account) as a program-derived address
  instead of a freshly generated signing keypair, so the program itself can
  be the sole authority over account creation.

These are scoping choices to keep the state machine legible, not gaps
discovered by testing.
