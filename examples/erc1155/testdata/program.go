// program.go is the actual on-chain multi-token program source, compiled
// together with accounts.go (the compiler's multi-file support). It is a
// Solana-native re-expression of ERC1155's capabilities: instead of one
// contract holding balances[id][owner]/operatorApprovals[owner][operator]
// mappings, each relationship is its own Solana account — see the host
// package's examples/erc1155/types.go for the four account layouts this
// file reads and writes (Collection, TokenType, Balance, Approval), and
// README.md for the two deliberate deviations from literal ERC1155 shape.
//
// Every ProgramError code below matches examples/erc1155's ProgramError
// enum by numeric value (1=InvalidInstruction .. 14=NotApproved); the guest
// side cannot import that Go package, so the numbers are pinned here.
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
		return ProcessInitializeCollection(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(1) {
		if instructionLength < uint64(5) {
			return uint64(1)
		}
		uriLen := LoadUint32(instructionDataAddress + uint64(1))
		if uriLen > uint32(64) {
			return uint64(1)
		}
		if instructionLength != uint64(5)+uint64(uriLen) {
			return uint64(1)
		}
		return ProcessCreateTokenType(inputAddress, instructionDataAddress, programID, uint64(uriLen))
	}
	if tag == uint32(2) {
		if instructionLength != uint64(33) {
			return uint64(1)
		}
		return ProcessInitializeBalance(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(3) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessMintTo(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(4) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessBurn(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(5) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessTransfer(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(6) {
		if instructionLength != uint64(34) {
			return uint64(1)
		}
		if LoadUint8(instructionDataAddress+uint64(33)) > uint32(1) {
			return uint64(1)
		}
		return ProcessInitializeApproval(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(7) {
		if instructionLength != uint64(2) {
			return uint64(1)
		}
		if LoadUint8(instructionDataAddress+uint64(1)) > uint32(1) {
			return uint64(1)
		}
		return ProcessSetApproval(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(8) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessTransferFrom(inputAddress, instructionDataAddress, programID)
	}
	return uint64(1)
}

// ProcessInitializeCollection creates a new Collection ("contract
// instance").
// accounts: [collection(writable,signer)]
// data:     [tag(1) authority(32)]
func ProcessInitializeCollection(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(1))
	if errorCode != uint64(0) {
		return errorCode
	}
	collection := AccountAt(inputAddress, uint64(0))
	errorCode = RequireOwned(collection, programID, true, uint64(41))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(collection)
	if errorCode != uint64(0) {
		return errorCode
	}
	data := AccountDataAddress(collection)
	if BytesZero(data, uint64(41)) == false {
		return uint64(8)
	}
	StoreUint8(data, uint32(1))
	CopyBytes(data+uint64(1), instruction+uint64(1), uint64(32))
	StoreUint64(data+uint64(33), uint64(0))
	return uint64(0)
}

// ProcessCreateTokenType defines a new token id under collection, assigning
// it collection's current next_id and incrementing that counter.
// accounts: [token_type(writable,signer), collection(writable), authority(signer)]
// data:     [tag(1) uri_len(4) uri(uriLen)]
func ProcessCreateTokenType(inputAddress uint64, instruction uint64, programID uint64, uriLen uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	tokenType := AccountAt(inputAddress, uint64(0))
	collection := AccountAt(inputAddress, uint64(1))
	authority := AccountAt(inputAddress, uint64(2))
	if tokenType == uint64(0) {
		return uint64(2)
	}
	if collection == uint64(0) {
		return uint64(2)
	}
	if SameAccount(tokenType, collection) == true {
		return uint64(13)
	}
	errorCode = RequireOwned(tokenType, programID, true, uint64(117))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(tokenType)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(collection, programID, true, uint64(41))
	if errorCode != uint64(0) {
		return errorCode
	}
	collectionData := AccountDataAddress(collection)
	if BytesZero(collectionData, uint64(41)) == true {
		return uint64(9)
	}
	if LoadUint8(collectionData) != uint32(1) {
		return uint64(7)
	}
	errorCode = RequireAuthority(authority, collectionData+uint64(1))
	if errorCode != uint64(0) {
		return errorCode
	}
	tokenData := AccountDataAddress(tokenType)
	if BytesZero(tokenData, uint64(117)) == false {
		return uint64(8)
	}

	id := LoadUint64(collectionData + uint64(33))
	newNextID := id + uint64(1)
	if newNextID < id {
		return uint64(12)
	}
	StoreUint64(collectionData+uint64(33), newNextID)

	StoreUint8(tokenData, uint32(2))
	CopyBytes(tokenData+uint64(1), AccountKeyAddress(collection), uint64(32))
	StoreUint64(tokenData+uint64(33), id)
	StoreUint64(tokenData+uint64(41), uint64(0))
	StoreUint32(tokenData+uint64(49), uint32(uriLen))
	CopyBytes(tokenData+uint64(53), instruction+uint64(5), uriLen)
	return uint64(0)
}

// ProcessInitializeBalance creates a zeroed Balance for
// (token_type.id, owner) — the equivalent of an initial balanceOf entry.
// accounts: [balance(writable,signer), token_type(readonly)]
// data:     [tag(1) owner(32)]
func ProcessInitializeBalance(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(2))
	if errorCode != uint64(0) {
		return errorCode
	}
	balance := AccountAt(inputAddress, uint64(0))
	tokenType := AccountAt(inputAddress, uint64(1))
	if balance == uint64(0) {
		return uint64(2)
	}
	if tokenType == uint64(0) {
		return uint64(2)
	}
	if SameAccount(balance, tokenType) == true {
		return uint64(13)
	}
	errorCode = RequireOwned(balance, programID, true, uint64(81))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(balance)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(tokenType, programID, false, uint64(117))
	if errorCode != uint64(0) {
		return errorCode
	}
	tokenData := AccountDataAddress(tokenType)
	if BytesZero(tokenData, uint64(117)) == true {
		return uint64(9)
	}
	if LoadUint8(tokenData) != uint32(2) {
		return uint64(7)
	}
	balanceData := AccountDataAddress(balance)
	if BytesZero(balanceData, uint64(81)) == false {
		return uint64(8)
	}
	StoreUint8(balanceData, uint32(3))
	CopyBytes(balanceData+uint64(1), tokenData+uint64(1), uint64(32))
	StoreUint64(balanceData+uint64(33), LoadUint64(tokenData+uint64(33)))
	CopyBytes(balanceData+uint64(41), instruction+uint64(1), uint64(32))
	StoreUint64(balanceData+uint64(73), uint64(0))
	return uint64(0)
}

// ProcessMintTo increases token_type.supply and balance.amount by amount.
// accounts: [collection(readonly), token_type(writable), balance(writable), authority(signer)]
// data:     [tag(1) amount(8)]
func ProcessMintTo(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(4))
	if errorCode != uint64(0) {
		return errorCode
	}
	collection := AccountAt(inputAddress, uint64(0))
	tokenType := AccountAt(inputAddress, uint64(1))
	balance := AccountAt(inputAddress, uint64(2))
	authority := AccountAt(inputAddress, uint64(3))
	if collection == uint64(0) {
		return uint64(2)
	}
	if tokenType == uint64(0) {
		return uint64(2)
	}
	if balance == uint64(0) {
		return uint64(2)
	}
	errorCode = RequireOwned(collection, programID, false, uint64(41))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(tokenType, programID, true, uint64(117))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(balance, programID, true, uint64(81))
	if errorCode != uint64(0) {
		return errorCode
	}
	collectionData := AccountDataAddress(collection)
	tokenData := AccountDataAddress(tokenType)
	balanceData := AccountDataAddress(balance)
	if LoadUint8(collectionData) != uint32(1) {
		return uint64(7)
	}
	if LoadUint8(tokenData) != uint32(2) {
		return uint64(7)
	}
	if LoadUint8(balanceData) != uint32(3) {
		return uint64(7)
	}
	if BytesEqual(tokenData+uint64(1), AccountKeyAddress(collection), uint64(32)) == false {
		return uint64(10)
	}
	if BytesEqual(balanceData+uint64(1), tokenData+uint64(1), uint64(32)) == false {
		return uint64(10)
	}
	if LoadUint64(balanceData+uint64(33)) != LoadUint64(tokenData+uint64(33)) {
		return uint64(10)
	}
	errorCode = RequireAuthority(authority, collectionData+uint64(1))
	if errorCode != uint64(0) {
		return errorCode
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	supply := LoadUint64(tokenData + uint64(41))
	balanceAmount := LoadUint64(balanceData + uint64(73))
	newSupply := supply + amount
	newBalance := balanceAmount + amount
	if newSupply < supply {
		return uint64(12)
	}
	if newBalance < balanceAmount {
		return uint64(12)
	}
	StoreUint64(tokenData+uint64(41), newSupply)
	StoreUint64(balanceData+uint64(73), newBalance)
	return uint64(0)
}

// ProcessBurn decreases token_type.supply and balance.amount by amount.
// Requires the balance owner to sign directly — no delegated/approved burn
// in this example.
// accounts: [token_type(writable), balance(writable), owner(signer)]
// data:     [tag(1) amount(8)]
func ProcessBurn(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	tokenType := AccountAt(inputAddress, uint64(0))
	balance := AccountAt(inputAddress, uint64(1))
	owner := AccountAt(inputAddress, uint64(2))
	if tokenType == uint64(0) {
		return uint64(2)
	}
	if balance == uint64(0) {
		return uint64(2)
	}
	errorCode = RequireOwned(tokenType, programID, true, uint64(117))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(balance, programID, true, uint64(81))
	if errorCode != uint64(0) {
		return errorCode
	}
	tokenData := AccountDataAddress(tokenType)
	balanceData := AccountDataAddress(balance)
	if LoadUint8(tokenData) != uint32(2) {
		return uint64(7)
	}
	if LoadUint8(balanceData) != uint32(3) {
		return uint64(7)
	}
	if BytesEqual(balanceData+uint64(1), tokenData+uint64(1), uint64(32)) == false {
		return uint64(10)
	}
	if LoadUint64(balanceData+uint64(33)) != LoadUint64(tokenData+uint64(33)) {
		return uint64(10)
	}
	errorCode = RequireAuthority(owner, balanceData+uint64(41))
	if errorCode != uint64(0) {
		return errorCode
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	supply := LoadUint64(tokenData + uint64(41))
	balanceAmount := LoadUint64(balanceData + uint64(73))
	if balanceAmount < amount {
		return uint64(11)
	}
	if supply < amount {
		return uint64(11)
	}
	StoreUint64(tokenData+uint64(41), supply-amount)
	StoreUint64(balanceData+uint64(73), balanceAmount-amount)
	return uint64(0)
}

// ProcessTransfer moves amount from source to destination, both owned by
// the signing owner. Batch transfers are just multiple Transfer
// instructions packed into one transaction client-side — see README.md.
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
		return uint64(13)
	}
	errorCode = RequireOwned(source, programID, true, uint64(81))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(destination, programID, true, uint64(81))
	if errorCode != uint64(0) {
		return errorCode
	}
	sourceData := AccountDataAddress(source)
	destinationData := AccountDataAddress(destination)
	if LoadUint8(sourceData) != uint32(3) {
		return uint64(7)
	}
	if LoadUint8(destinationData) != uint32(3) {
		return uint64(7)
	}
	if BytesEqual(sourceData+uint64(1), destinationData+uint64(1), uint64(32)) == false {
		return uint64(10)
	}
	if LoadUint64(sourceData+uint64(33)) != LoadUint64(destinationData+uint64(33)) {
		return uint64(10)
	}
	errorCode = RequireAuthority(owner, sourceData+uint64(41))
	if errorCode != uint64(0) {
		return errorCode
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	sourceBalance := LoadUint64(sourceData + uint64(73))
	destinationBalance := LoadUint64(destinationData + uint64(73))
	if sourceBalance < amount {
		return uint64(11)
	}
	newDestinationBalance := destinationBalance + amount
	if newDestinationBalance < destinationBalance {
		return uint64(12)
	}
	StoreUint64(sourceData+uint64(73), sourceBalance-amount)
	StoreUint64(destinationData+uint64(73), newDestinationBalance)
	return uint64(0)
}

// ProcessInitializeApproval creates a new approve-for-all record for
// (owner, operator) — the equivalent of setApprovalForAll's initial write.
// accounts: [approval(writable,signer), owner(signer), operator(readonly)]
// data:     [tag(1) collection(32) approved(1)]
func ProcessInitializeApproval(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	approval := AccountAt(inputAddress, uint64(0))
	owner := AccountAt(inputAddress, uint64(1))
	operator := AccountAt(inputAddress, uint64(2))
	if approval == uint64(0) {
		return uint64(2)
	}
	if owner == uint64(0) {
		return uint64(2)
	}
	if operator == uint64(0) {
		return uint64(2)
	}
	if SameAccount(owner, operator) == true {
		return uint64(13)
	}
	errorCode = RequireOwned(approval, programID, true, uint64(98))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(approval)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(owner)
	if errorCode != uint64(0) {
		return errorCode
	}
	data := AccountDataAddress(approval)
	if BytesZero(data, uint64(98)) == false {
		return uint64(8)
	}
	StoreUint8(data, uint32(4))
	CopyBytes(data+uint64(1), instruction+uint64(1), uint64(32))
	CopyBytes(data+uint64(33), AccountKeyAddress(owner), uint64(32))
	CopyBytes(data+uint64(65), AccountKeyAddress(operator), uint64(32))
	StoreUint8(data+uint64(97), LoadUint8(instruction+uint64(33)))
	return uint64(0)
}

// ProcessSetApproval flips an existing Approval's approved flag.
// accounts: [approval(writable), owner(signer)]
// data:     [tag(1) approved(1)]
func ProcessSetApproval(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(2))
	if errorCode != uint64(0) {
		return errorCode
	}
	approval := AccountAt(inputAddress, uint64(0))
	owner := AccountAt(inputAddress, uint64(1))
	if approval == uint64(0) {
		return uint64(2)
	}
	errorCode = RequireOwned(approval, programID, true, uint64(98))
	if errorCode != uint64(0) {
		return errorCode
	}
	data := AccountDataAddress(approval)
	if BytesZero(data, uint64(98)) == true {
		return uint64(9)
	}
	if LoadUint8(data) != uint32(4) {
		return uint64(7)
	}
	errorCode = RequireAuthority(owner, data+uint64(33))
	if errorCode != uint64(0) {
		return errorCode
	}
	StoreUint8(data+uint64(97), LoadUint8(instruction+uint64(1)))
	return uint64(0)
}

// ProcessTransferFrom moves amount from source to destination on behalf of
// source's owner, authorized by a matching, approved Approval account.
// accounts: [source(writable), destination(writable), approval(readonly), operator(signer)]
// data:     [tag(1) amount(8)]
func ProcessTransferFrom(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(4))
	if errorCode != uint64(0) {
		return errorCode
	}
	source := AccountAt(inputAddress, uint64(0))
	destination := AccountAt(inputAddress, uint64(1))
	approval := AccountAt(inputAddress, uint64(2))
	operator := AccountAt(inputAddress, uint64(3))
	if source == uint64(0) {
		return uint64(2)
	}
	if destination == uint64(0) {
		return uint64(2)
	}
	if approval == uint64(0) {
		return uint64(2)
	}
	if SameAccount(source, destination) == true {
		return uint64(13)
	}
	errorCode = RequireOwned(source, programID, true, uint64(81))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(destination, programID, true, uint64(81))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(approval, programID, false, uint64(98))
	if errorCode != uint64(0) {
		return errorCode
	}
	sourceData := AccountDataAddress(source)
	destinationData := AccountDataAddress(destination)
	approvalData := AccountDataAddress(approval)
	if LoadUint8(sourceData) != uint32(3) {
		return uint64(7)
	}
	if LoadUint8(destinationData) != uint32(3) {
		return uint64(7)
	}
	if LoadUint8(approvalData) != uint32(4) {
		return uint64(7)
	}
	if BytesEqual(sourceData+uint64(1), destinationData+uint64(1), uint64(32)) == false {
		return uint64(10)
	}
	if LoadUint64(sourceData+uint64(33)) != LoadUint64(destinationData+uint64(33)) {
		return uint64(10)
	}
	if BytesEqual(approvalData+uint64(1), sourceData+uint64(1), uint64(32)) == false {
		return uint64(10)
	}
	if BytesEqual(approvalData+uint64(33), sourceData+uint64(41), uint64(32)) == false {
		return uint64(14)
	}
	errorCode = RequireAuthority(operator, approvalData+uint64(65))
	if errorCode != uint64(0) {
		return errorCode
	}
	if LoadUint8(approvalData+uint64(97)) == uint32(0) {
		return uint64(14)
	}

	amount := LoadLittleUint64(instruction + uint64(1))
	sourceBalance := LoadUint64(sourceData + uint64(73))
	destinationBalance := LoadUint64(destinationData + uint64(73))
	if sourceBalance < amount {
		return uint64(11)
	}
	newDestinationBalance := destinationBalance + amount
	if newDestinationBalance < destinationBalance {
		return uint64(12)
	}
	StoreUint64(sourceData+uint64(73), sourceBalance-amount)
	StoreUint64(destinationData+uint64(73), newDestinationBalance)
	return uint64(0)
}
