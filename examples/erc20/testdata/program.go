// program.go is the actual on-chain ERC-20-analogue program source,
// compiled together with accounts.go via the compiler's multi-file support.
// It is a low-level guest implementation of the state machine documented in
// examples/erc20's native reference model (types.go, state.go,
// instruction.go, program.go in the parent directory) — same 100-byte
// MintState / 80-byte BalanceState / 112-byte AllowanceState wire layout,
// same instruction tags, account orders, and lengths, so a guest-produced
// account's raw bytes decode directly with the native model's
// DecodeMintState/DecodeBalanceState/DecodeAllowanceState.
//
// Every numeric error code below matches examples/erc20's ProgramError enum
// (in the parent directory's types.go) by value (1=InvalidInstruction ..
// 15=AllowanceMismatch); the guest side cannot import that Go package, so
// the numbers are pinned here and in accounts.go's RequireOwned/
// RequireAuthority.
package program

// Program is the Solana entrypoint. inputAddress is Agave's MM_INPUT_START;
// instructionDataAddress is the absolute guest address of the instruction
// data within that same serialized buffer.
func Program(inputAddress uint64, instructionDataAddress uint64) uint64 {
	instructionLength := LoadUint64(instructionDataAddress - uint64(8))
	if instructionLength == uint64(0) {
		return uint64(1)
	}
	programID := instructionDataAddress + instructionLength
	tag := LoadUint8(instructionDataAddress)

	if tag == uint32(0) {
		if instructionLength != uint64(76) {
			return uint64(1)
		}
		return ProcessInitializeMint(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(1) {
		if instructionLength != uint64(33) {
			return uint64(1)
		}
		return ProcessInitializeBalance(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(2) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessMintTo(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(3) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessBurn(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(4) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessTransfer(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(5) {
		if instructionLength != uint64(1) {
			return uint64(1)
		}
		return ProcessInitializeAllowance(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(6) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessApprove(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(7) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessTransferFrom(inputAddress, instructionDataAddress, programID)
	}
	return uint64(1)
}

// ProcessInitializeMint is the SVM analogue of setting `name`/`symbol`/
// `decimals` in an ERC-20 contract's constructor.
// accounts: [mint(writable,signer)]
// data:     [tag(1) name(32) symbol(10) decimals(1) authority(32)]
func ProcessInitializeMint(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(1))
	if errorCode != uint64(0) {
		return errorCode
	}
	mint := AccountAt(inputAddress, uint64(0))
	errorCode = RequireOwned(mint, programID, true, uint64(100))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(mint)
	if errorCode != uint64(0) {
		return errorCode
	}
	data := AccountDataAddress(mint)
	if BytesZero(data, uint64(100)) == false {
		return uint64(7) // ErrAlreadyInitialized
	}
	StoreUint8(data, uint32(1))
	StoreUint8(data+uint64(1), uint32(3)) // flagInitialized | flagAuthority
	CopyBytes(data+uint64(16), instruction+uint64(44), uint64(32))
	StoreUint8(data+uint64(48), LoadUint8(instruction+uint64(43)))
	CopyBytes(data+uint64(56), instruction+uint64(1), uint64(32))
	CopyBytes(data+uint64(88), instruction+uint64(33), uint64(10))
	return uint64(0)
}

// ProcessInitializeBalance is the SVM analogue of the first time a Solidity
// contract touches `balanceOf[owner]`.
// accounts: [balance(writable,signer), mint(readonly)]
// data:     [tag(1) owner(32)]
func ProcessInitializeBalance(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(2))
	if errorCode != uint64(0) {
		return errorCode
	}
	balance := AccountAt(inputAddress, uint64(0))
	mint := AccountAt(inputAddress, uint64(1))
	if balance == uint64(0) {
		return uint64(2)
	}
	if mint == uint64(0) {
		return uint64(2)
	}
	if SameAccount(balance, mint) == true {
		return uint64(9) // ErrSameAccount
	}
	errorCode = RequireOwned(balance, programID, true, uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(balance)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(mint, programID, false, uint64(100))
	if errorCode != uint64(0) {
		return errorCode
	}
	mintData := AccountDataAddress(mint)
	if BytesZero(mintData, uint64(100)) == true {
		return uint64(8) // ErrUninitialized
	}
	if LoadUint8(mintData) != uint32(1) {
		return uint64(6) // ErrInvalidState
	}

	data := AccountDataAddress(balance)
	if BytesZero(data, uint64(80)) == false {
		return uint64(7)
	}
	StoreUint8(data, uint32(2))
	StoreUint8(data+uint64(1), uint32(1))
	CopyBytes(data+uint64(16), AccountKeyAddress(mint), uint64(32))
	CopyBytes(data+uint64(48), instruction+uint64(1), uint64(32))
	return uint64(0)
}

// ProcessMintTo increases mint.totalSupply and balance.amount by amount.
// authority must match mint's stored mintAuthority and sign.
// accounts: [mint(writable), balance(writable), authority(signer)]
// data:     [tag(1) amount(8)]
func ProcessMintTo(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	mint := AccountAt(inputAddress, uint64(0))
	balance := AccountAt(inputAddress, uint64(1))
	authority := AccountAt(inputAddress, uint64(2))
	if mint == uint64(0) {
		return uint64(2)
	}
	if balance == uint64(0) {
		return uint64(2)
	}
	if SameAccount(mint, balance) == true {
		return uint64(9)
	}
	errorCode = RequireOwned(mint, programID, true, uint64(100))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(balance, programID, true, uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}
	mintData := AccountDataAddress(mint)
	balanceData := AccountDataAddress(balance)
	if LoadUint8(mintData) != uint32(1) {
		return uint64(8)
	}
	if LoadUint8(balanceData) != uint32(2) {
		return uint64(8)
	}
	if BytesEqual(balanceData+uint64(16), AccountKeyAddress(mint), uint64(32)) == false {
		return uint64(10) // ErrMintMismatch
	}
	errorCode = RequireAuthority(authority, mintData+uint64(16))
	if errorCode != uint64(0) {
		return errorCode
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	supply := LoadUint64(mintData + uint64(8))
	balanceAmount := LoadUint64(balanceData + uint64(8))
	newSupply := supply + amount
	newBalance := balanceAmount + amount
	if newSupply < supply {
		return uint64(13) // ErrArithmeticOverflow
	}
	if newBalance < balanceAmount {
		return uint64(13)
	}
	StoreUint64(mintData+uint64(8), newSupply)
	StoreUint64(balanceData+uint64(8), newBalance)
	return uint64(0)
}

// ProcessBurn decreases mint.totalSupply and balance.amount by amount.
// owner must match balance.owner and sign.
// accounts: [balance(writable), mint(writable), owner(signer)]
// data:     [tag(1) amount(8)]
func ProcessBurn(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	balance := AccountAt(inputAddress, uint64(0))
	mint := AccountAt(inputAddress, uint64(1))
	owner := AccountAt(inputAddress, uint64(2))
	if balance == uint64(0) {
		return uint64(2)
	}
	if mint == uint64(0) {
		return uint64(2)
	}
	if SameAccount(balance, mint) == true {
		return uint64(9)
	}
	errorCode = RequireOwned(balance, programID, true, uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(mint, programID, true, uint64(100))
	if errorCode != uint64(0) {
		return errorCode
	}
	balanceData := AccountDataAddress(balance)
	mintData := AccountDataAddress(mint)
	if LoadUint8(balanceData) != uint32(2) {
		return uint64(8)
	}
	if LoadUint8(mintData) != uint32(1) {
		return uint64(8)
	}
	if BytesEqual(balanceData+uint64(16), AccountKeyAddress(mint), uint64(32)) == false {
		return uint64(10)
	}
	errorCode = RequireAuthority(owner, balanceData+uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	balanceAmount := LoadUint64(balanceData + uint64(8))
	supply := LoadUint64(mintData + uint64(8))
	if balanceAmount < amount {
		return uint64(11) // ErrInsufficientFunds
	}
	if supply < amount {
		return uint64(11)
	}
	StoreUint64(balanceData+uint64(8), balanceAmount-amount)
	StoreUint64(mintData+uint64(8), supply-amount)
	return uint64(0)
}

// ProcessTransfer is the SVM analogue of `transfer(to, amount)`.
// accounts: [source(writable), destination(writable), owner(signer)]
// data:     [tag(1) amount(8)]
func ProcessTransfer(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	source := AccountAt(inputAddress, uint64(0))
	destination := AccountAt(inputAddress, uint64(1))
	owner := AccountAt(inputAddress, uint64(2))
	if source == uint64(0) {
		return uint64(2)
	}
	if destination == uint64(0) {
		return uint64(2)
	}
	if SameAccount(source, destination) == true {
		return uint64(9)
	}
	errorCode = RequireOwned(source, programID, true, uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(destination, programID, true, uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}
	sourceData := AccountDataAddress(source)
	destinationData := AccountDataAddress(destination)
	if LoadUint8(sourceData) != uint32(2) {
		return uint64(8)
	}
	if LoadUint8(destinationData) != uint32(2) {
		return uint64(8)
	}
	if BytesEqual(sourceData+uint64(16), destinationData+uint64(16), uint64(32)) == false {
		return uint64(10)
	}
	errorCode = RequireAuthority(owner, sourceData+uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	sourceAmount := LoadUint64(sourceData + uint64(8))
	destinationAmount := LoadUint64(destinationData + uint64(8))
	if sourceAmount < amount {
		return uint64(11)
	}
	newDestination := destinationAmount + amount
	if newDestination < destinationAmount {
		return uint64(13)
	}
	StoreUint64(sourceData+uint64(8), sourceAmount-amount)
	StoreUint64(destinationData+uint64(8), newDestination)
	return uint64(0)
}

// ProcessInitializeAllowance is the SVM analogue of the first time a
// Solidity contract touches `allowance[owner][spender]`.
// accounts: [allowance(writable,signer), mint(readonly), owner(signer), spender(readonly)]
// data:     [tag(1)]
func ProcessInitializeAllowance(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(4))
	if errorCode != uint64(0) {
		return errorCode
	}
	allowance := AccountAt(inputAddress, uint64(0))
	mint := AccountAt(inputAddress, uint64(1))
	owner := AccountAt(inputAddress, uint64(2))
	spender := AccountAt(inputAddress, uint64(3))
	if allowance == uint64(0) {
		return uint64(2)
	}
	if mint == uint64(0) {
		return uint64(2)
	}
	if owner == uint64(0) {
		return uint64(2)
	}
	if spender == uint64(0) {
		return uint64(2)
	}
	if SameAccount(allowance, mint) == true {
		return uint64(9)
	}
	errorCode = RequireOwned(allowance, programID, true, uint64(112))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(allowance)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(mint, programID, false, uint64(100))
	if errorCode != uint64(0) {
		return errorCode
	}
	mintData := AccountDataAddress(mint)
	if BytesZero(mintData, uint64(100)) == true {
		return uint64(8)
	}
	if LoadUint8(mintData) != uint32(1) {
		return uint64(6)
	}
	errorCode = RequireSigner(owner)
	if errorCode != uint64(0) {
		return errorCode
	}

	data := AccountDataAddress(allowance)
	if BytesZero(data, uint64(112)) == false {
		return uint64(7)
	}
	StoreUint8(data, uint32(3))
	StoreUint8(data+uint64(1), uint32(1))
	CopyBytes(data+uint64(16), AccountKeyAddress(mint), uint64(32))
	CopyBytes(data+uint64(48), AccountKeyAddress(owner), uint64(32))
	CopyBytes(data+uint64(80), AccountKeyAddress(spender), uint64(32))
	return uint64(0)
}

// ProcessApprove is the SVM analogue of `approve(spender, amount)`: it sets
// (not adds to) allowance.amount. owner must match allowance.owner and
// sign.
// accounts: [allowance(writable), owner(signer)]
// data:     [tag(1) amount(8)]
func ProcessApprove(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(2))
	if errorCode != uint64(0) {
		return errorCode
	}
	allowance := AccountAt(inputAddress, uint64(0))
	owner := AccountAt(inputAddress, uint64(1))
	errorCode = RequireOwned(allowance, programID, true, uint64(112))
	if errorCode != uint64(0) {
		return errorCode
	}
	data := AccountDataAddress(allowance)
	if LoadUint8(data) != uint32(3) {
		return uint64(8)
	}
	errorCode = RequireAuthority(owner, data+uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}
	amount := LoadLittleUint64(instruction + uint64(1))
	StoreUint64(data+uint64(8), amount)
	return uint64(0)
}

// ProcessTransferFrom is the SVM analogue of `transferFrom(from, to, amount)`:
// it moves amount from source to destination on behalf of source's owner,
// authorized by a matching allowance, decrementing it by amount.
// accounts: [source(writable), destination(writable), allowance(writable), spender(signer)]
// data:     [tag(1) amount(8)]
func ProcessTransferFrom(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(4))
	if errorCode != uint64(0) {
		return errorCode
	}
	source := AccountAt(inputAddress, uint64(0))
	destination := AccountAt(inputAddress, uint64(1))
	allowance := AccountAt(inputAddress, uint64(2))
	spender := AccountAt(inputAddress, uint64(3))
	if source == uint64(0) {
		return uint64(2)
	}
	if destination == uint64(0) {
		return uint64(2)
	}
	if allowance == uint64(0) {
		return uint64(2)
	}
	if SameAccount(source, destination) == true {
		return uint64(9)
	}
	errorCode = RequireOwned(source, programID, true, uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(destination, programID, true, uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(allowance, programID, true, uint64(112))
	if errorCode != uint64(0) {
		return errorCode
	}
	sourceData := AccountDataAddress(source)
	destinationData := AccountDataAddress(destination)
	allowanceData := AccountDataAddress(allowance)
	if LoadUint8(sourceData) != uint32(2) {
		return uint64(8)
	}
	if LoadUint8(destinationData) != uint32(2) {
		return uint64(8)
	}
	if LoadUint8(allowanceData) != uint32(3) {
		return uint64(8)
	}
	if BytesEqual(sourceData+uint64(16), destinationData+uint64(16), uint64(32)) == false {
		return uint64(10)
	}
	if BytesEqual(sourceData+uint64(16), allowanceData+uint64(16), uint64(32)) == false {
		return uint64(10)
	}
	if BytesEqual(allowanceData+uint64(48), sourceData+uint64(48), uint64(32)) == false {
		return uint64(15) // ErrAllowanceMismatch
	}
	errorCode = RequireAuthority(spender, allowanceData+uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	allowanceAmount := LoadUint64(allowanceData + uint64(8))
	sourceAmount := LoadUint64(sourceData + uint64(8))
	destinationAmount := LoadUint64(destinationData + uint64(8))
	if allowanceAmount < amount {
		return uint64(12) // ErrInsufficientAllowance
	}
	if sourceAmount < amount {
		return uint64(11)
	}
	newDestination := destinationAmount + amount
	if newDestination < destinationAmount {
		return uint64(13)
	}
	StoreUint64(sourceData+uint64(8), sourceAmount-amount)
	StoreUint64(destinationData+uint64(8), newDestination)
	StoreUint64(allowanceData+uint64(8), allowanceAmount-amount)
	return uint64(0)
}
