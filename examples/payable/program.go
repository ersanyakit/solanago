package payable

import "math"

// Program executes the payable vault's state transitions for accounts owned
// by ID. Each method validates every input and prepares all encoded states
// before mutating account data or lamports, so an error leaves accounts
// unchanged.
type Program struct {
	ID Pubkey
}

func (p Program) Process(accounts []*Account, instructionData []byte) error {
	instruction, err := DecodeInstruction(instructionData)
	if err != nil {
		return err
	}
	switch instruction.Kind {
	case InstructionInitializeVault:
		if err := requireAccounts(accounts, 1); err != nil {
			return err
		}
		return p.InitializeVault(accounts[0], instruction.Authority)
	case InstructionInitializeDepositAccount:
		if err := requireAccounts(accounts, 2); err != nil {
			return err
		}
		return p.InitializeDepositAccount(accounts[0], accounts[1], instruction.Depositor)
	case InstructionDeposit:
		if err := requireAccounts(accounts, 3); err != nil {
			return err
		}
		return p.Deposit(accounts[0], accounts[1], accounts[2], instruction.Amount)
	case InstructionWithdraw:
		if err := requireAccounts(accounts, 4); err != nil {
			return err
		}
		return p.Withdraw(accounts[0], accounts[1], accounts[2], accounts[3], instruction.Amount)
	case InstructionEmergencyWithdraw:
		if err := requireAccounts(accounts, 3); err != nil {
			return err
		}
		return p.EmergencyWithdraw(accounts[0], accounts[1], accounts[2], instruction.Amount)
	default:
		return ErrInvalidInstruction
	}
}

// InitializeVault turns a freshly created, program-owned account into an
// empty vault ledger owned by authority. It must sign, mirroring how a real
// deployment would require the new account's own keypair to authorize its
// first write. authority is recorded as the sole signer EmergencyWithdraw
// will later accept.
func (p Program) InitializeVault(vault *Account, authority Pubkey) error {
	if err := p.requireProgramAccount(vault, true, VaultStateSize); err != nil {
		return err
	}
	if !vault.IsSigner {
		return ErrMissingSignature
	}
	existing, err := DecodeVaultState(vault.Data)
	if err != nil {
		return err
	}
	if existing.Initialized {
		return ErrAlreadyInitialized
	}
	encoded, err := copyVaultState(VaultState{Initialized: true, Authority: authority})
	if err != nil {
		return err
	}
	copy(vault.Data, encoded)
	return nil
}

// InitializeDepositAccount binds a fresh program-owned account to one
// (vault, depositor) pair, the SVM equivalent of the first time a Solidity
// contract touches `balances[depositor]`. depositor need not sign here —
// only the new deposit account itself does, exactly like spl20's token
// account initialization requires the token account's own signature but
// not the eventual owner's.
func (p Program) InitializeDepositAccount(depositAccount, vault *Account, depositor Pubkey) error {
	if depositAccount == nil || vault == nil {
		return ErrMissingAccount
	}
	if depositAccount.Key == vault.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(depositAccount, true, DepositStateSize); err != nil {
		return err
	}
	if !depositAccount.IsSigner {
		return ErrMissingSignature
	}
	if err := p.requireProgramAccount(vault, false, VaultStateSize); err != nil {
		return err
	}
	if _, err := initializedVault(vault.Data); err != nil {
		return err
	}
	existing, err := DecodeDepositState(depositAccount.Data)
	if err != nil {
		return err
	}
	if existing.Initialized {
		return ErrAlreadyInitialized
	}
	encoded, err := copyDepositState(DepositState{
		Initialized: true,
		Vault:       vault.Key,
		Depositor:   depositor,
	})
	if err != nil {
		return err
	}
	copy(depositAccount.Data, encoded)
	return nil
}

// Deposit is the `payable` half of the contract: it moves lamports from
// depositor's own account into vault and records the credit against
// depositAccount, the SVM equivalent of:
//
//	function deposit() external payable {
//	    balances[msg.sender] += msg.value;
//	}
//
// depositor must sign because this contract does not own it — it is a
// System Program account. On a real deployment this method's lamport move
// is not this direct field mutation; it is a CPI into the System Program's
// Transfer instruction with depositor as the signing source (see
// examples/cpi/testdata/program.go for the exact ABIv1/syscall mechanics).
// This native model performs the same signer check and the same balance
// deltas that CPI enforces, without executing the syscall.
func (p Program) Deposit(vault, depositAccount, depositor *Account, amount uint64) error {
	if depositor == nil {
		return ErrMissingAccount
	}
	if amount == 0 {
		return ErrInvalidInstruction
	}
	if err := p.requireProgramAccount(vault, true, VaultStateSize); err != nil {
		return err
	}
	if err := p.requireProgramAccount(depositAccount, true, DepositStateSize); err != nil {
		return err
	}
	if !depositor.IsWritable {
		return ErrAccountReadOnly
	}
	if !depositor.IsSigner {
		return ErrMissingSignature
	}
	vaultState, err := initializedVault(vault.Data)
	if err != nil {
		return err
	}
	depositState, err := initializedDeposit(depositAccount.Data)
	if err != nil {
		return err
	}
	if depositState.Vault != vault.Key {
		return ErrVaultMismatch
	}
	if depositState.Depositor != depositor.Key {
		return ErrDepositorMismatch
	}
	if depositor.Lamports < amount {
		return ErrInsufficientLamports
	}
	if vault.Lamports > math.MaxUint64-amount ||
		vaultState.TotalDeposited > math.MaxUint64-amount ||
		depositState.Balance > math.MaxUint64-amount {
		return ErrArithmeticOverflow
	}
	vaultState.TotalDeposited += amount
	depositState.Balance += amount
	if err := commitVaultAndDeposit(vault, vaultState, depositAccount, depositState); err != nil {
		return err
	}
	depositor.Lamports -= amount
	vault.Lamports += amount
	return nil
}

// Withdraw is the pull-payment half of the contract: it debits depositState
// and pays recipient out of vault, the SVM equivalent of:
//
//	function withdraw(uint256 amount, address payable to) external {
//	    require(balances[msg.sender] >= amount);
//	    balances[msg.sender] -= amount;
//	    to.transfer(amount);
//	}
//
// No CPI is needed here, unlike Deposit: vault is owned by this program, so
// this program may debit its lamports directly, and any account may credit
// lamports to recipient directly regardless of who owns it.
func (p Program) Withdraw(vault, depositAccount, depositor, recipient *Account, amount uint64) error {
	if recipient == nil {
		return ErrMissingAccount
	}
	if amount == 0 {
		return ErrInvalidInstruction
	}
	if err := p.requireProgramAccount(vault, true, VaultStateSize); err != nil {
		return err
	}
	if err := p.requireProgramAccount(depositAccount, true, DepositStateSize); err != nil {
		return err
	}
	if err := requireSigner(depositor); err != nil {
		return err
	}
	if !recipient.IsWritable {
		return ErrAccountReadOnly
	}
	vaultState, err := initializedVault(vault.Data)
	if err != nil {
		return err
	}
	depositState, err := initializedDeposit(depositAccount.Data)
	if err != nil {
		return err
	}
	if depositState.Vault != vault.Key {
		return ErrVaultMismatch
	}
	if depositState.Depositor != depositor.Key {
		return ErrDepositorMismatch
	}
	if depositState.Balance < amount {
		return ErrInsufficientFunds
	}
	if vault.Lamports < amount {
		return ErrInsufficientFunds
	}
	if recipient.Lamports > math.MaxUint64-amount {
		return ErrArithmeticOverflow
	}
	depositState.Balance -= amount
	vaultState.TotalDeposited -= amount
	if err := commitVaultAndDeposit(vault, vaultState, depositAccount, depositState); err != nil {
		return err
	}
	vault.Lamports -= amount
	recipient.Lamports += amount
	return nil
}

// EmergencyWithdraw is an `onlyOwner` rescue hatch: it pays recipient out of
// vault on nothing but the vault's recorded Authority signing, bypassing
// every per-depositor DepositState entirely. It is the SVM equivalent of:
//
//	function emergencyWithdraw(uint256 amount, address payable to) external onlyOwner {
//	    to.transfer(amount);
//	}
//
// Like Withdraw, no CPI is needed: vault is owned by this program, so this
// program may debit it directly and credit recipient directly.
//
// Because it does not touch any DepositState or vaultState.TotalDeposited,
// a completed emergency withdrawal can leave vault holding fewer lamports
// than depositors are collectively recorded as owning — recording that
// deliberately, rather than silently keeping the ledger looking whole, is
// the honest trade-off of giving one key unilateral pull rights over
// everyone else's deposits.
func (p Program) EmergencyWithdraw(vault, authority, recipient *Account, amount uint64) error {
	if recipient == nil {
		return ErrMissingAccount
	}
	if amount == 0 {
		return ErrInvalidInstruction
	}
	if err := p.requireProgramAccount(vault, true, VaultStateSize); err != nil {
		return err
	}
	if !recipient.IsWritable {
		return ErrAccountReadOnly
	}
	vaultState, err := initializedVault(vault.Data)
	if err != nil {
		return err
	}
	if err := requireAuthority(authority, vaultState.Authority); err != nil {
		return err
	}
	if vault.Lamports < amount {
		return ErrInsufficientFunds
	}
	if recipient.Lamports > math.MaxUint64-amount {
		return ErrArithmeticOverflow
	}
	vault.Lamports -= amount
	recipient.Lamports += amount
	return nil
}

// BalanceOf reads a deposit account's tracked balance without mutating
// anything, the read-only analogue of a public Solidity getter for
// `balances[depositor]`.
func BalanceOf(depositAccountData []byte) (uint64, error) {
	state, err := initializedDeposit(depositAccountData)
	if err != nil {
		return 0, err
	}
	return state.Balance, nil
}

func (p Program) requireProgramAccount(account *Account, writable bool, size int) error {
	if account == nil {
		return ErrMissingAccount
	}
	if account.Owner != p.ID {
		return ErrInvalidProgramOwner
	}
	if writable && !account.IsWritable {
		return ErrAccountReadOnly
	}
	if len(account.Data) != size {
		return ErrInvalidState
	}
	return nil
}

func requireSigner(account *Account) error {
	if account == nil {
		return ErrMissingAccount
	}
	if !account.IsSigner {
		return ErrMissingSignature
	}
	return nil
}

func requireAuthority(account *Account, expected Pubkey) error {
	if account == nil {
		return ErrMissingAccount
	}
	if account.Key != expected {
		return ErrInvalidAuthority
	}
	if !account.IsSigner {
		return ErrMissingSignature
	}
	return nil
}

func initializedVault(data []byte) (VaultState, error) {
	state, err := DecodeVaultState(data)
	if err != nil {
		return VaultState{}, err
	}
	if !state.Initialized {
		return VaultState{}, ErrUninitialized
	}
	return state, nil
}

func initializedDeposit(data []byte) (DepositState, error) {
	state, err := DecodeDepositState(data)
	if err != nil {
		return DepositState{}, err
	}
	if !state.Initialized {
		return DepositState{}, ErrUninitialized
	}
	return state, nil
}

func requireAccounts(accounts []*Account, count int) error {
	if len(accounts) < count {
		return ErrMissingAccount
	}
	if len(accounts) > count {
		return ErrInvalidInstruction
	}
	for _, account := range accounts {
		if account == nil {
			return ErrMissingAccount
		}
	}
	return nil
}

func commitVaultAndDeposit(vault *Account, vaultState VaultState, depositAccount *Account, depositState DepositState) error {
	vaultData, err := copyVaultState(vaultState)
	if err != nil {
		return err
	}
	depositData, err := copyDepositState(depositState)
	if err != nil {
		return err
	}
	copy(vault.Data, vaultData)
	copy(depositAccount.Data, depositData)
	return nil
}
