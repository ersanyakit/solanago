// program.go is the actual on-chain payable-vault program source, compiled
// together with accounts.go via the compiler's multi-file support. It is a
// low-level guest implementation of the state machine documented in
// examples/payable's native reference model (types.go, state.go,
// instruction.go, program.go in the parent directory) — same 48-byte
// VaultState / 80-byte DepositState wire layout, same instruction tags and
// lengths, so a guest-produced account's raw bytes decode directly with the
// native model's DecodeVaultState/DecodeDepositState.
//
// One instruction's account list necessarily differs from the native
// model's: Deposit here takes a fourth account, the System Program, because
// this file actually performs the CPI the native model only documents —
// see ProcessDeposit below and README.md's "Native model versus compiled
// guest" section.
//
// Every numeric error code below matches examples/payable's ProgramError
// enum (in the parent directory's types.go) by value (1=InvalidInstruction
// .. 15=InvalidAuthority); the guest side cannot import that Go package, so
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
		if instructionLength != uint64(33) {
			return uint64(1)
		}
		return ProcessInitializeVault(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(1) {
		if instructionLength != uint64(33) {
			return uint64(1)
		}
		return ProcessInitializeDepositAccount(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(2) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessDeposit(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(3) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessWithdraw(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(4) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessEmergencyWithdraw(inputAddress, instructionDataAddress, programID)
	}
	return uint64(1)
}

// ProcessInitializeVault turns a freshly created, program-owned account
// into an empty vault ledger owned by the authority pubkey carried in
// instruction data.
// accounts: [vault(writable,signer)]
// data:     [tag(1) authority(32)]
func ProcessInitializeVault(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(1))
	if errorCode != uint64(0) {
		return errorCode
	}
	vault := AccountAt(inputAddress, uint64(0))
	errorCode = RequireOwned(vault, programID, true, uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(vault)
	if errorCode != uint64(0) {
		return errorCode
	}
	data := AccountDataAddress(vault)
	if BytesZero(data, uint64(48)) == false {
		return uint64(7) // ErrAlreadyInitialized
	}
	StoreUint8(data, uint32(1))            // vaultStateTag
	StoreUint8(data+uint64(1), uint32(1))  // flagInitialized
	CopyBytes(data+uint64(16), instruction+uint64(1), uint64(32))
	return uint64(0)
}

// ProcessInitializeDepositAccount binds a fresh program-owned account to
// one (vault, depositor) pair. depositor need not sign here — only the new
// deposit account itself does, exactly like spl20's token account
// initialization requires the token account's own signature but not the
// eventual owner's.
// accounts: [depositAccount(writable,signer), vault(readonly)]
// data:     [tag(1) depositor(32)]
func ProcessInitializeDepositAccount(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(2))
	if errorCode != uint64(0) {
		return errorCode
	}
	depositAccount := AccountAt(inputAddress, uint64(0))
	vault := AccountAt(inputAddress, uint64(1))
	if depositAccount == uint64(0) {
		return uint64(2)
	}
	if vault == uint64(0) {
		return uint64(2)
	}
	if SameAccount(depositAccount, vault) == true {
		return uint64(9) // ErrSameAccount
	}
	errorCode = RequireOwned(depositAccount, programID, true, uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(depositAccount)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(vault, programID, false, uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}
	vaultData := AccountDataAddress(vault)
	if BytesZero(vaultData, uint64(48)) == true {
		return uint64(8) // ErrUninitialized
	}
	if LoadUint8(vaultData) != uint32(1) {
		return uint64(6) // ErrInvalidState
	}

	data := AccountDataAddress(depositAccount)
	if BytesZero(data, uint64(80)) == false {
		return uint64(7) // ErrAlreadyInitialized
	}
	StoreUint8(data, uint32(2))           // depositStateTag
	StoreUint8(data+uint64(1), uint32(1)) // flagInitialized
	CopyBytes(data+uint64(16), AccountKeyAddress(vault), uint64(32))
	CopyBytes(data+uint64(48), instruction+uint64(1), uint64(32))
	return uint64(0)
}

// ProcessDeposit is the payable half of the contract: it moves amount
// lamports from depositor's own System-Program-owned wallet into vault via
// a CPI Transfer (depositor must sign, since this program does not own
// that account and so cannot debit it directly), and credits
// depositAccount's tracked balance only once that CPI has actually
// succeeded.
//
// The CPI-construction block (the `var words [32]uint64` stack scratch
// array through the InvokeSignedC call) is
// examples/cpi/testdata/program.go's proven layout, unchanged except for
// which two accounts play source/destination — see that file's own
// comments for the exact SolInstruction/SolAccountMeta/SolAccountInfo byte
// layout this reproduces.
//
// accounts: [vault(writable), depositAccount(writable), depositor(writable,signer), systemProgram(readonly)]
// data:     [tag(1) amount(8)]
func ProcessDeposit(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(4))
	if errorCode != uint64(0) {
		return errorCode
	}
	vault := AccountAt(inputAddress, uint64(0))
	depositAccount := AccountAt(inputAddress, uint64(1))
	depositor := AccountAt(inputAddress, uint64(2))
	systemProgram := AccountAt(inputAddress, uint64(3))
	if vault == uint64(0) {
		return uint64(2)
	}
	if depositAccount == uint64(0) {
		return uint64(2)
	}
	if depositor == uint64(0) {
		return uint64(2)
	}
	if systemProgram == uint64(0) {
		return uint64(2)
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	if amount == uint64(0) {
		return uint64(1) // ErrInvalidInstruction
	}

	errorCode = RequireOwned(vault, programID, true, uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(depositAccount, programID, true, uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}
	if AccountIsWritable(depositor) == false {
		return uint64(4) // ErrAccountReadOnly
	}
	errorCode = RequireSigner(depositor)
	if errorCode != uint64(0) {
		return errorCode
	}
	if AccountIsExecutable(systemProgram) == false {
		return uint64(6) // ErrInvalidState: not a native program account
	}
	if BytesZero(AccountKeyAddress(systemProgram), uint64(32)) == false {
		return uint64(6) // ErrInvalidState: not the System Program's all-zero address
	}

	vaultData := AccountDataAddress(vault)
	depositData := AccountDataAddress(depositAccount)
	if LoadUint8(vaultData) != uint32(1) {
		return uint64(8) // ErrUninitialized
	}
	if LoadUint8(depositData) != uint32(2) {
		return uint64(8) // ErrUninitialized
	}
	if BytesEqual(depositData+uint64(16), AccountKeyAddress(vault), uint64(32)) == false {
		return uint64(10) // ErrVaultMismatch
	}
	if BytesEqual(depositData+uint64(48), AccountKeyAddress(depositor), uint64(32)) == false {
		return uint64(11) // ErrDepositorMismatch
	}

	depositorLamports := LoadUint64(AccountLamportsAddress(depositor))
	if depositorLamports < amount {
		return uint64(12) // ErrInsufficientLamports
	}
	vaultTotal := LoadUint64(vaultData + uint64(8))
	depositBalance := LoadUint64(depositData + uint64(8))
	newVaultTotal := vaultTotal + amount
	newDepositBalance := depositBalance + amount
	if newVaultTotal < vaultTotal {
		return uint64(14) // ErrArithmeticOverflow
	}
	if newDepositBalance < depositBalance {
		return uint64(14) // ErrArithmeticOverflow
	}

	// words is 256 bytes in the sBPF stack region. Layout (identical to
	// examples/cpi/testdata/program.go):
	//   0..39   SolInstruction
	//   40..71  two SolAccountMeta values
	//   72..83  SystemInstruction::Transfer data
	//   88..255 three SolAccountInfo values
	var words [32]uint64
	base := AddressUint64(&words[0])
	StoreUint64(base, AccountKeyAddress(systemProgram))
	StoreUint64(base+uint64(8), base+uint64(40))
	StoreUint64(base+uint64(16), uint64(2))
	StoreUint64(base+uint64(24), base+uint64(72))
	StoreUint64(base+uint64(32), uint64(12))
	StoreUint64(base+uint64(40), AccountKeyAddress(depositor))
	StoreUint64(base+uint64(48), uint64(0x0101)) // source: writable + signer
	StoreUint64(base+uint64(56), AccountKeyAddress(vault))
	StoreUint64(base+uint64(64), uint64(0x0001)) // destination: writable only
	StoreUint32(base+uint64(72), uint32(2))      // SystemInstruction::Transfer discriminator
	StoreLittleUint64(base+uint64(76), amount)
	WriteAccountInfo(base+uint64(88), depositor)
	WriteAccountInfo(base+uint64(144), vault)
	WriteAccountInfo(base+uint64(200), systemProgram)

	cpiResult := InvokeSignedC(base, base+uint64(88), uint64(3), uint64(0), uint64(0))
	if cpiResult != uint64(0) {
		return cpiResult
	}

	StoreUint64(vaultData+uint64(8), newVaultTotal)
	StoreUint64(depositData+uint64(8), newDepositBalance)
	return uint64(0)
}

// ProcessWithdraw is the pull-payment half of the contract: it debits
// depositAccount and pays recipient out of vault directly — no CPI needed,
// since vault is owned by this program.
// accounts: [vault(writable), depositAccount(writable), depositor(signer), recipient(writable)]
// data:     [tag(1) amount(8)]
func ProcessWithdraw(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(4))
	if errorCode != uint64(0) {
		return errorCode
	}
	vault := AccountAt(inputAddress, uint64(0))
	depositAccount := AccountAt(inputAddress, uint64(1))
	depositor := AccountAt(inputAddress, uint64(2))
	recipient := AccountAt(inputAddress, uint64(3))
	if vault == uint64(0) {
		return uint64(2)
	}
	if depositAccount == uint64(0) {
		return uint64(2)
	}
	if depositor == uint64(0) {
		return uint64(2)
	}
	if recipient == uint64(0) {
		return uint64(2)
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	if amount == uint64(0) {
		return uint64(1)
	}

	errorCode = RequireOwned(vault, programID, true, uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(depositAccount, programID, true, uint64(80))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(depositor)
	if errorCode != uint64(0) {
		return errorCode
	}
	if AccountIsWritable(recipient) == false {
		return uint64(4)
	}

	vaultData := AccountDataAddress(vault)
	depositData := AccountDataAddress(depositAccount)
	if LoadUint8(vaultData) != uint32(1) {
		return uint64(8)
	}
	if LoadUint8(depositData) != uint32(2) {
		return uint64(8)
	}
	if BytesEqual(depositData+uint64(16), AccountKeyAddress(vault), uint64(32)) == false {
		return uint64(10)
	}
	if BytesEqual(depositData+uint64(48), AccountKeyAddress(depositor), uint64(32)) == false {
		return uint64(11)
	}

	vaultTotal := LoadUint64(vaultData + uint64(8))
	depositBalance := LoadUint64(depositData + uint64(8))
	if depositBalance < amount {
		return uint64(13) // ErrInsufficientFunds
	}
	vaultLamports := LoadUint64(AccountLamportsAddress(vault))
	if vaultLamports < amount {
		return uint64(13)
	}
	recipientLamports := LoadUint64(AccountLamportsAddress(recipient))
	newRecipientLamports := recipientLamports + amount
	if newRecipientLamports < recipientLamports {
		return uint64(14)
	}

	StoreUint64(depositData+uint64(8), depositBalance-amount)
	StoreUint64(vaultData+uint64(8), vaultTotal-amount)
	StoreUint64(AccountLamportsAddress(vault), vaultLamports-amount)
	StoreUint64(AccountLamportsAddress(recipient), newRecipientLamports)
	return uint64(0)
}

// ProcessEmergencyWithdraw is the vault's onlyOwner rescue hatch: it pays
// recipient out of vault on nothing but the vault's recorded Authority
// signing, bypassing depositAccount entirely — same direct-lamport pattern
// as ProcessWithdraw, no CPI.
// accounts: [vault(writable), authority(signer), recipient(writable)]
// data:     [tag(1) amount(8)]
func ProcessEmergencyWithdraw(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	vault := AccountAt(inputAddress, uint64(0))
	authority := AccountAt(inputAddress, uint64(1))
	recipient := AccountAt(inputAddress, uint64(2))
	if vault == uint64(0) {
		return uint64(2)
	}
	if recipient == uint64(0) {
		return uint64(2)
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	if amount == uint64(0) {
		return uint64(1)
	}

	errorCode = RequireOwned(vault, programID, true, uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}
	if AccountIsWritable(recipient) == false {
		return uint64(4)
	}

	vaultData := AccountDataAddress(vault)
	if LoadUint8(vaultData) != uint32(1) {
		return uint64(8)
	}
	errorCode = RequireAuthority(authority, vaultData+uint64(16))
	if errorCode != uint64(0) {
		return errorCode
	}

	vaultLamports := LoadUint64(AccountLamportsAddress(vault))
	if vaultLamports < amount {
		return uint64(13)
	}
	recipientLamports := LoadUint64(AccountLamportsAddress(recipient))
	newRecipientLamports := recipientLamports + amount
	if newRecipientLamports < recipientLamports {
		return uint64(14)
	}

	StoreUint64(AccountLamportsAddress(vault), vaultLamports-amount)
	StoreUint64(AccountLamportsAddress(recipient), newRecipientLamports)
	return uint64(0)
}
