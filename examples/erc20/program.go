package erc20

import "math"

// Program executes this ERC-20 analogue's state transitions for accounts
// owned by ID. Each method validates every input and prepares all encoded
// states before mutating account data, so an error leaves state unchanged.
type Program struct {
	ID Pubkey
}

func (p Program) Process(accounts []*Account, instructionData []byte) error {
	instruction, err := DecodeInstruction(instructionData)
	if err != nil {
		return err
	}
	switch instruction.Kind {
	case InstructionInitializeMint:
		if err := requireAccounts(accounts, 1); err != nil {
			return err
		}
		return p.InitializeMint(accounts[0], instruction.Name, instruction.Symbol, instruction.Decimals, instruction.Authority)
	case InstructionInitializeBalance:
		if err := requireAccounts(accounts, 2); err != nil {
			return err
		}
		return p.InitializeBalance(accounts[0], accounts[1], instruction.Owner)
	case InstructionMintTo:
		if err := requireAccounts(accounts, 3); err != nil {
			return err
		}
		return p.MintTo(accounts[0], accounts[1], accounts[2], instruction.Amount)
	case InstructionBurn:
		if err := requireAccounts(accounts, 3); err != nil {
			return err
		}
		return p.Burn(accounts[0], accounts[1], accounts[2], instruction.Amount)
	case InstructionTransfer:
		if err := requireAccounts(accounts, 3); err != nil {
			return err
		}
		return p.Transfer(accounts[0], accounts[1], accounts[2], instruction.Amount)
	case InstructionInitializeAllowance:
		if err := requireAccounts(accounts, 4); err != nil {
			return err
		}
		return p.InitializeAllowance(accounts[0], accounts[1], accounts[2], accounts[3])
	case InstructionApprove:
		if err := requireAccounts(accounts, 2); err != nil {
			return err
		}
		return p.Approve(accounts[0], accounts[1], instruction.Amount)
	case InstructionTransferFrom:
		if err := requireAccounts(accounts, 4); err != nil {
			return err
		}
		return p.TransferFrom(accounts[0], accounts[1], accounts[2], accounts[3], instruction.Amount)
	default:
		return ErrInvalidInstruction
	}
}

// InitializeMint is the SVM analogue of setting `name`/`symbol`/`decimals`
// in an ERC-20 contract's constructor. mint must sign, mirroring every
// other example's "the new account proves it consents to its first write."
func (p Program) InitializeMint(mint *Account, name, symbol string, decimals uint8, authority Pubkey) error {
	if err := p.requireProgramAccount(mint, true, MintStateSize); err != nil {
		return err
	}
	if !mint.IsSigner {
		return ErrMissingSignature
	}
	if len(name) > MaxNameLen || len(symbol) > MaxSymbolLen {
		return ErrInvalidInstruction
	}
	existing, err := DecodeMintState(mint.Data)
	if err != nil {
		return err
	}
	if existing.Initialized {
		return ErrAlreadyInitialized
	}
	encoded, err := copyMintState(MintState{
		Initialized:   true,
		Name:          name,
		Symbol:        symbol,
		Decimals:      decimals,
		MintAuthority: OptionalPubkey{Set: true, Key: authority},
	})
	if err != nil {
		return err
	}
	copy(mint.Data, encoded)
	return nil
}

// InitializeBalance is the SVM analogue of the first time a Solidity
// contract touches `balanceOf[owner]`. owner need not sign — only the new
// balance account itself does, matching spl20's InitializeAccount.
func (p Program) InitializeBalance(balance, mint *Account, owner Pubkey) error {
	if balance == nil || mint == nil {
		return ErrMissingAccount
	}
	if balance.Key == mint.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(balance, true, BalanceStateSize); err != nil {
		return err
	}
	if !balance.IsSigner {
		return ErrMissingSignature
	}
	if err := p.requireProgramAccount(mint, false, MintStateSize); err != nil {
		return err
	}
	if _, err := initializedMint(mint.Data); err != nil {
		return err
	}
	existing, err := DecodeBalanceState(balance.Data)
	if err != nil {
		return err
	}
	if existing.Initialized {
		return ErrAlreadyInitialized
	}
	encoded, err := copyBalanceState(BalanceState{Initialized: true, Mint: mint.Key, Owner: owner})
	if err != nil {
		return err
	}
	copy(balance.Data, encoded)
	return nil
}

// MintTo increases totalSupply and balance.Amount. authority must match
// mint's MintAuthority and sign.
func (p Program) MintTo(mint, balance, authority *Account, amount uint64) error {
	if mint == nil || balance == nil {
		return ErrMissingAccount
	}
	if mint.Key == balance.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(mint, true, MintStateSize); err != nil {
		return err
	}
	if err := p.requireProgramAccount(balance, true, BalanceStateSize); err != nil {
		return err
	}
	mintState, err := initializedMint(mint.Data)
	if err != nil {
		return err
	}
	balanceState, err := initializedBalance(balance.Data)
	if err != nil {
		return err
	}
	if balanceState.Mint != mint.Key {
		return ErrMintMismatch
	}
	if err := requireAuthority(authority, mintState.MintAuthority.Key); err != nil {
		return err
	}
	if mintState.TotalSupply > math.MaxUint64-amount || balanceState.Amount > math.MaxUint64-amount {
		return ErrArithmeticOverflow
	}
	mintState.TotalSupply += amount
	balanceState.Amount += amount
	return commitMintAndBalance(mint, mintState, balance, balanceState)
}

// Burn decreases totalSupply and balance.Amount. owner must match
// balance.Owner and sign — no delegated/approved burn in this example.
func (p Program) Burn(balance, mint, owner *Account, amount uint64) error {
	if balance == nil || mint == nil {
		return ErrMissingAccount
	}
	if balance.Key == mint.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(balance, true, BalanceStateSize); err != nil {
		return err
	}
	if err := p.requireProgramAccount(mint, true, MintStateSize); err != nil {
		return err
	}
	balanceState, err := initializedBalance(balance.Data)
	if err != nil {
		return err
	}
	mintState, err := initializedMint(mint.Data)
	if err != nil {
		return err
	}
	if balanceState.Mint != mint.Key {
		return ErrMintMismatch
	}
	if err := requireAuthority(owner, balanceState.Owner); err != nil {
		return err
	}
	if balanceState.Amount < amount || mintState.TotalSupply < amount {
		return ErrInsufficientFunds
	}
	balanceState.Amount -= amount
	mintState.TotalSupply -= amount
	return commitMintAndBalance(mint, mintState, balance, balanceState)
}

// Transfer is the SVM analogue of `transfer(to, amount)`: it moves amount
// from source to destination, both the same mint, authorized by source's
// own owner signing directly (see TransferFrom for the delegated path).
func (p Program) Transfer(source, destination, owner *Account, amount uint64) error {
	if source == nil || destination == nil {
		return ErrMissingAccount
	}
	if source.Key == destination.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(source, true, BalanceStateSize); err != nil {
		return err
	}
	if err := p.requireProgramAccount(destination, true, BalanceStateSize); err != nil {
		return err
	}
	sourceState, err := initializedBalance(source.Data)
	if err != nil {
		return err
	}
	destinationState, err := initializedBalance(destination.Data)
	if err != nil {
		return err
	}
	if sourceState.Mint != destinationState.Mint {
		return ErrMintMismatch
	}
	if err := requireAuthority(owner, sourceState.Owner); err != nil {
		return err
	}
	if sourceState.Amount < amount {
		return ErrInsufficientFunds
	}
	if destinationState.Amount > math.MaxUint64-amount {
		return ErrArithmeticOverflow
	}
	sourceState.Amount -= amount
	destinationState.Amount += amount
	return commitBalancePair(source, sourceState, destination, destinationState)
}

// InitializeAllowance is the SVM analogue of the first time a Solidity
// contract touches `allowance[owner][spender]`. owner must sign; spender is
// just recorded, not authorized to do anything by this instruction alone.
func (p Program) InitializeAllowance(allowance, mint, owner, spender *Account) error {
	if allowance == nil || mint == nil || owner == nil || spender == nil {
		return ErrMissingAccount
	}
	if allowance.Key == mint.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(allowance, true, AllowanceStateSize); err != nil {
		return err
	}
	if !allowance.IsSigner {
		return ErrMissingSignature
	}
	if err := p.requireProgramAccount(mint, false, MintStateSize); err != nil {
		return err
	}
	if _, err := initializedMint(mint.Data); err != nil {
		return err
	}
	if !owner.IsSigner {
		return ErrMissingSignature
	}
	existing, err := DecodeAllowanceState(allowance.Data)
	if err != nil {
		return err
	}
	if existing.Initialized {
		return ErrAlreadyInitialized
	}
	encoded, err := copyAllowanceState(AllowanceState{
		Initialized: true,
		Mint:        mint.Key,
		Owner:       owner.Key,
		Spender:     spender.Key,
	})
	if err != nil {
		return err
	}
	copy(allowance.Data, encoded)
	return nil
}

// Approve is the SVM analogue of `approve(spender, amount)`: it sets (not
// adds to) allowance.Amount. owner must match allowance.Owner and sign.
func (p Program) Approve(allowance, owner *Account, amount uint64) error {
	if err := p.requireProgramAccount(allowance, true, AllowanceStateSize); err != nil {
		return err
	}
	state, err := initializedAllowance(allowance.Data)
	if err != nil {
		return err
	}
	if err := requireAuthority(owner, state.Owner); err != nil {
		return err
	}
	state.Amount = amount
	return commitAllowance(allowance, state)
}

// TransferFrom is the SVM analogue of `transferFrom(from, to, amount)`: it
// moves amount from source to destination on behalf of source's owner,
// authorized by a matching allowance, decrementing it by amount (the
// classic OpenZeppelin decrement-on-spend behavior).
func (p Program) TransferFrom(source, destination, allowance, spender *Account, amount uint64) error {
	if source == nil || destination == nil {
		return ErrMissingAccount
	}
	if source.Key == destination.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(source, true, BalanceStateSize); err != nil {
		return err
	}
	if err := p.requireProgramAccount(destination, true, BalanceStateSize); err != nil {
		return err
	}
	if err := p.requireProgramAccount(allowance, true, AllowanceStateSize); err != nil {
		return err
	}
	sourceState, err := initializedBalance(source.Data)
	if err != nil {
		return err
	}
	destinationState, err := initializedBalance(destination.Data)
	if err != nil {
		return err
	}
	allowanceState, err := initializedAllowance(allowance.Data)
	if err != nil {
		return err
	}
	if sourceState.Mint != destinationState.Mint || sourceState.Mint != allowanceState.Mint {
		return ErrMintMismatch
	}
	if allowanceState.Owner != sourceState.Owner {
		return ErrAllowanceMismatch
	}
	if err := requireAuthority(spender, allowanceState.Spender); err != nil {
		return err
	}
	if allowanceState.Amount < amount {
		return ErrInsufficientAllowance
	}
	if sourceState.Amount < amount {
		return ErrInsufficientFunds
	}
	if destinationState.Amount > math.MaxUint64-amount {
		return ErrArithmeticOverflow
	}
	sourceState.Amount -= amount
	destinationState.Amount += amount
	allowanceState.Amount -= amount
	if err := commitBalancePair(source, sourceState, destination, destinationState); err != nil {
		return err
	}
	return commitAllowance(allowance, allowanceState)
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

func initializedMint(data []byte) (MintState, error) {
	state, err := DecodeMintState(data)
	if err != nil {
		return MintState{}, err
	}
	if !state.Initialized {
		return MintState{}, ErrUninitialized
	}
	return state, nil
}

func initializedBalance(data []byte) (BalanceState, error) {
	state, err := DecodeBalanceState(data)
	if err != nil {
		return BalanceState{}, err
	}
	if !state.Initialized {
		return BalanceState{}, ErrUninitialized
	}
	return state, nil
}

func initializedAllowance(data []byte) (AllowanceState, error) {
	state, err := DecodeAllowanceState(data)
	if err != nil {
		return AllowanceState{}, err
	}
	if !state.Initialized {
		return AllowanceState{}, ErrUninitialized
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

func commitAllowance(account *Account, state AllowanceState) error {
	encoded, err := copyAllowanceState(state)
	if err != nil {
		return err
	}
	copy(account.Data, encoded)
	return nil
}

func commitBalancePair(first *Account, firstState BalanceState, second *Account, secondState BalanceState) error {
	firstData, err := copyBalanceState(firstState)
	if err != nil {
		return err
	}
	secondData, err := copyBalanceState(secondState)
	if err != nil {
		return err
	}
	copy(first.Data, firstData)
	copy(second.Data, secondData)
	return nil
}

func commitMintAndBalance(mint *Account, mintState MintState, balance *Account, balanceState BalanceState) error {
	mintData, err := copyMintState(mintState)
	if err != nil {
		return err
	}
	balanceData, err := copyBalanceState(balanceState)
	if err != nil {
		return err
	}
	copy(mint.Data, mintData)
	copy(balance.Data, balanceData)
	return nil
}
