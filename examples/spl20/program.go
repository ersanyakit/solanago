package spl20

import "math"

// Program executes the example's token state transitions for accounts owned
// by ID. Each method validates every input and prepares all encoded states
// before mutating account data, so an error leaves state unchanged.
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
		return p.InitializeMint(accounts[0], instruction.Authority, instruction.Decimals)
	case InstructionInitializeAccount:
		if err := requireAccounts(accounts, 2); err != nil {
			return err
		}
		return p.InitializeAccount(accounts[0], accounts[1], instruction.Authority)
	case InstructionTransfer:
		if err := requireAccounts(accounts, 3); err != nil {
			return err
		}
		return p.Transfer(accounts[0], accounts[1], accounts[2], instruction.Amount)
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
	case InstructionApprove:
		if err := requireAccounts(accounts, 3); err != nil {
			return err
		}
		return p.Approve(accounts[0], accounts[1], accounts[2].Key, instruction.Amount)
	case InstructionRevoke:
		if err := requireAccounts(accounts, 2); err != nil {
			return err
		}
		return p.Revoke(accounts[0], accounts[1])
	case InstructionSetAuthority:
		if err := requireAccounts(accounts, 2); err != nil {
			return err
		}
		return p.SetAuthority(accounts[0], accounts[1], instruction.AuthorityType, instruction.NewAuthority)
	default:
		return ErrInvalidInstruction
	}
}

func (p Program) InitializeMint(mint *Account, authority Pubkey, decimals uint8) error {
	if err := p.requireProgramAccount(mint, true, MintStateSize); err != nil {
		return err
	}
	if !mint.IsSigner {
		return ErrMissingSignature
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
		Decimals:      decimals,
		MintAuthority: OptionalPubkey{Set: true, Key: authority},
	})
	if err != nil {
		return err
	}
	copy(mint.Data, encoded)
	return nil
}

func (p Program) InitializeAccount(token, mint *Account, owner Pubkey) error {
	if token == nil || mint == nil {
		return ErrMissingAccount
	}
	if token.Key == mint.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(token, true, TokenAccountStateSize); err != nil {
		return err
	}
	if !token.IsSigner {
		return ErrMissingSignature
	}
	if err := p.requireProgramAccount(mint, false, MintStateSize); err != nil {
		return err
	}
	if _, err := initializedMint(mint.Data); err != nil {
		return err
	}
	existing, err := DecodeTokenAccountState(token.Data)
	if err != nil {
		return err
	}
	if existing.Initialized {
		return ErrAlreadyInitialized
	}
	encoded, err := copyTokenState(TokenAccountState{
		Initialized: true,
		Mint:        mint.Key,
		Owner:       owner,
	})
	if err != nil {
		return err
	}
	copy(token.Data, encoded)
	return nil
}

func (p Program) Transfer(source, destination, authority *Account, amount uint64) error {
	if source == nil || destination == nil {
		return ErrMissingAccount
	}
	if source.Key == destination.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(source, true, TokenAccountStateSize); err != nil {
		return err
	}
	if err := p.requireProgramAccount(destination, true, TokenAccountStateSize); err != nil {
		return err
	}
	sourceState, err := initializedToken(source.Data)
	if err != nil {
		return err
	}
	destinationState, err := initializedToken(destination.Data)
	if err != nil {
		return err
	}
	if sourceState.Mint != destinationState.Mint {
		return ErrMintMismatch
	}
	delegated, err := authorizeToken(sourceState, authority, amount)
	if err != nil {
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
	if delegated {
		sourceState.DelegatedAmount -= amount
	}
	return commitTokenPair(source, sourceState, destination, destinationState)
}

func (p Program) MintTo(mint, destination, authority *Account, amount uint64) error {
	if mint == nil || destination == nil {
		return ErrMissingAccount
	}
	if mint.Key == destination.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(mint, true, MintStateSize); err != nil {
		return err
	}
	if err := p.requireProgramAccount(destination, true, TokenAccountStateSize); err != nil {
		return err
	}
	mintState, err := initializedMint(mint.Data)
	if err != nil {
		return err
	}
	destinationState, err := initializedToken(destination.Data)
	if err != nil {
		return err
	}
	if destinationState.Mint != mint.Key {
		return ErrMintMismatch
	}
	if !mintState.MintAuthority.Set {
		return ErrAuthorityDisabled
	}
	if err := requireAuthority(authority, mintState.MintAuthority.Key); err != nil {
		return err
	}
	if mintState.Supply > math.MaxUint64-amount || destinationState.Amount > math.MaxUint64-amount {
		return ErrArithmeticOverflow
	}
	mintState.Supply += amount
	destinationState.Amount += amount
	return commitMintAndToken(mint, mintState, destination, destinationState)
}

func (p Program) Burn(source, mint, authority *Account, amount uint64) error {
	if source == nil || mint == nil {
		return ErrMissingAccount
	}
	if source.Key == mint.Key {
		return ErrSameAccount
	}
	if err := p.requireProgramAccount(source, true, TokenAccountStateSize); err != nil {
		return err
	}
	if err := p.requireProgramAccount(mint, true, MintStateSize); err != nil {
		return err
	}
	sourceState, err := initializedToken(source.Data)
	if err != nil {
		return err
	}
	mintState, err := initializedMint(mint.Data)
	if err != nil {
		return err
	}
	if sourceState.Mint != mint.Key {
		return ErrMintMismatch
	}
	delegated, err := authorizeToken(sourceState, authority, amount)
	if err != nil {
		return err
	}
	if sourceState.Amount < amount || mintState.Supply < amount {
		return ErrInsufficientFunds
	}
	sourceState.Amount -= amount
	mintState.Supply -= amount
	if delegated {
		sourceState.DelegatedAmount -= amount
	}
	return commitMintAndToken(mint, mintState, source, sourceState)
}

func (p Program) Approve(source, owner *Account, delegate Pubkey, amount uint64) error {
	if err := p.requireProgramAccount(source, true, TokenAccountStateSize); err != nil {
		return err
	}
	state, err := initializedToken(source.Data)
	if err != nil {
		return err
	}
	if err := requireAuthority(owner, state.Owner); err != nil {
		return err
	}
	state.Delegate = OptionalPubkey{Set: true, Key: delegate}
	state.DelegatedAmount = amount
	return commitToken(source, state)
}

func (p Program) Revoke(source, owner *Account) error {
	if err := p.requireProgramAccount(source, true, TokenAccountStateSize); err != nil {
		return err
	}
	state, err := initializedToken(source.Data)
	if err != nil {
		return err
	}
	if err := requireAuthority(owner, state.Owner); err != nil {
		return err
	}
	state.Delegate = OptionalPubkey{}
	state.DelegatedAmount = 0
	return commitToken(source, state)
}

func (p Program) SetAuthority(target, current *Account, kind AuthorityType, authority OptionalPubkey) error {
	switch kind {
	case AuthorityMintTokens:
		if err := p.requireProgramAccount(target, true, MintStateSize); err != nil {
			return err
		}
		state, err := initializedMint(target.Data)
		if err != nil {
			return err
		}
		if !state.MintAuthority.Set {
			return ErrAuthorityDisabled
		}
		if err := requireAuthority(current, state.MintAuthority.Key); err != nil {
			return err
		}
		state.MintAuthority = authority
		return commitMint(target, state)
	case AuthorityAccountOwner:
		if !authority.Set {
			return ErrInvalidInstruction
		}
		if err := p.requireProgramAccount(target, true, TokenAccountStateSize); err != nil {
			return err
		}
		state, err := initializedToken(target.Data)
		if err != nil {
			return err
		}
		if err := requireAuthority(current, state.Owner); err != nil {
			return err
		}
		state.Owner = authority.Key
		state.Delegate = OptionalPubkey{}
		state.DelegatedAmount = 0
		return commitToken(target, state)
	default:
		return ErrInvalidInstruction
	}
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

func authorizeToken(state TokenAccountState, authority *Account, amount uint64) (bool, error) {
	if authority == nil {
		return false, ErrMissingAccount
	}
	if !authority.IsSigner {
		return false, ErrMissingSignature
	}
	if authority.Key == state.Owner {
		return false, nil
	}
	if !state.Delegate.Set || authority.Key != state.Delegate.Key {
		return false, ErrInvalidAuthority
	}
	if state.DelegatedAmount < amount {
		return false, ErrInsufficientAllowance
	}
	return true, nil
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

func initializedToken(data []byte) (TokenAccountState, error) {
	state, err := DecodeTokenAccountState(data)
	if err != nil {
		return TokenAccountState{}, err
	}
	if !state.Initialized {
		return TokenAccountState{}, ErrUninitialized
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

func commitMint(account *Account, state MintState) error {
	encoded, err := copyMintState(state)
	if err != nil {
		return err
	}
	copy(account.Data, encoded)
	return nil
}

func commitToken(account *Account, state TokenAccountState) error {
	encoded, err := copyTokenState(state)
	if err != nil {
		return err
	}
	copy(account.Data, encoded)
	return nil
}

func commitTokenPair(first *Account, firstState TokenAccountState, second *Account, secondState TokenAccountState) error {
	firstData, err := copyTokenState(firstState)
	if err != nil {
		return err
	}
	secondData, err := copyTokenState(secondState)
	if err != nil {
		return err
	}
	copy(first.Data, firstData)
	copy(second.Data, secondData)
	return nil
}

func commitMintAndToken(mint *Account, mintState MintState, token *Account, tokenState TokenAccountState) error {
	mintData, err := copyMintState(mintState)
	if err != nil {
		return err
	}
	tokenData, err := copyTokenState(tokenState)
	if err != nil {
		return err
	}
	copy(mint.Data, mintData)
	copy(token.Data, tokenData)
	return nil
}
