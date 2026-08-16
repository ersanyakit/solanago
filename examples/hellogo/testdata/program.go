// This is the actual on-chain HelloGo program source. It is kept in testdata so
// the host Go tool does not try to link guest-memory intrinsics. go-solana
// type-checks and compiles this file directly to sBPFv3.
package program

// These declarations are explicit guest-memory operations. Their uint64
// arguments are sBPF virtual addresses, never Go pointers or heap addresses.
func LoadUint8(address uint64) uint32
func LoadUint64(address uint64) uint64
func StoreUint8(address uint64, value uint32)
func StoreUint64(address uint64, value uint64)

func BytesZero(address uint64, length uint64) bool {
	for index := uint64(0); index < length; index++ {
		if LoadUint8(address+index) != uint32(0) {
			return false
		}
	}
	return true
}

func BytesEqual(first uint64, second uint64, length uint64) bool {
	for index := uint64(0); index < length; index++ {
		if LoadUint8(first+index) != LoadUint8(second+index) {
			return false
		}
	}
	return true
}

func CopyBytes(destination uint64, source uint64, length uint64) {
	for index := uint64(0); index < length; index++ {
		StoreUint8(destination+index, LoadUint8(source+index))
	}
}

func ClearBytes(address uint64, length uint64) {
	for index := uint64(0); index < length; index++ {
		StoreUint8(address+index, uint32(0))
	}
}

// Instruction amounts start at byte one and are therefore intentionally
// unaligned. Reading them byte-by-byte avoids relying on any host alignment
// behavior while preserving the canonical little-endian wire format.
func LoadLittleUint64(address uint64) uint64 {
	value := uint64(LoadUint8(address))
	value += uint64(LoadUint8(address+uint64(1))) * uint64(256)
	value += uint64(LoadUint8(address+uint64(2))) * uint64(65536)
	value += uint64(LoadUint8(address+uint64(3))) * uint64(16777216)
	value += uint64(LoadUint8(address+uint64(4))) * uint64(4294967296)
	value += uint64(LoadUint8(address+uint64(5))) * uint64(1099511627776)
	value += uint64(LoadUint8(address+uint64(6))) * uint64(281474976710656)
	value += uint64(LoadUint8(address+uint64(7))) * uint64(72057594037927936)
	return value
}

// NextPhysicalAccount follows Agave ABIv1's guest virtual layout. A unique
// record is 96 bytes plus data, the 10 KiB permitted realloc reservation, and
// BPF u128 alignment padding. A duplicate record is exactly eight bytes.
// This arithmetic is valid for both contiguous and account-data-direct-mapped
// ABIv1 because the virtual reservation is identical in both modes.
func NextPhysicalAccount(record uint64) uint64 {
	if LoadUint8(record) != uint32(255) {
		return record + uint64(8)
	}
	dataLength := LoadUint64(record + uint64(80))
	padding := (uint64(8) - dataLength%uint64(8)) % uint64(8)
	return record + uint64(96) + dataLength + uint64(10240) + padding
}

// AccountAt resolves ABIv1 duplicate slots without exposing a host pointer.
// HelloGo instructions use at most three accounts, so resolving only 0..2 keeps
// the compiled program bounded and deterministic.
func AccountAt(inputAddress uint64, index uint64) uint64 {
	firstPhysical := inputAddress + uint64(8)
	if LoadUint8(firstPhysical) != uint32(255) {
		return uint64(0)
	}
	first := firstPhysical
	if index == uint64(0) {
		return first
	}

	secondPhysical := NextPhysicalAccount(firstPhysical)
	secondMarker := LoadUint8(secondPhysical)
	second := uint64(0)
	if secondMarker == uint32(255) {
		second = secondPhysical
	} else if secondMarker == uint32(0) {
		second = first
	} else {
		return uint64(0)
	}
	if index == uint64(1) {
		return second
	}

	thirdPhysical := NextPhysicalAccount(secondPhysical)
	thirdMarker := LoadUint8(thirdPhysical)
	if thirdMarker == uint32(255) {
		return thirdPhysical
	}
	if thirdMarker == uint32(0) {
		return first
	}
	if thirdMarker == uint32(1) {
		return second
	}
	return uint64(0)
}

func RequireAccountCount(inputAddress uint64, wanted uint64) uint64 {
	count := LoadUint64(inputAddress)
	if count < wanted {
		return uint64(2)
	}
	if count > wanted {
		return uint64(1)
	}
	return uint64(0)
}

func SameAccount(first uint64, second uint64) bool {
	if first == second {
		return true
	}
	return BytesEqual(first+uint64(8), second+uint64(8), uint64(32))
}

func RequireOwned(account uint64, programID uint64, writable bool, size uint64) uint64 {
	if account == uint64(0) {
		return uint64(2)
	}
	if BytesEqual(account+uint64(40), programID, uint64(32)) == false {
		return uint64(3)
	}
	if writable == true {
		if LoadUint8(account+uint64(2)) == uint32(0) {
			return uint64(4)
		}
	}
	if LoadUint8(account+uint64(3)) != uint32(0) {
		return uint64(7)
	}
	if LoadUint64(account+uint64(80)) != size {
		return uint64(7)
	}
	return uint64(0)
}

func RequireAuthority(authority uint64, expectedKey uint64) uint64 {
	if authority == uint64(0) {
		return uint64(2)
	}
	if BytesEqual(authority+uint64(8), expectedKey, uint64(32)) == false {
		return uint64(6)
	}
	if LoadUint8(authority+uint64(1)) == uint32(0) {
		return uint64(5)
	}
	return uint64(0)
}

func RequireSigner(authority uint64) uint64 {
	if authority == uint64(0) {
		return uint64(2)
	}
	if LoadUint8(authority+uint64(1)) == uint32(0) {
		return uint64(5)
	}
	return uint64(0)
}

func ValidateMint(data uint64) uint64 {
	if BytesZero(data, uint64(48)) == true {
		return uint64(9)
	}
	if LoadUint8(data) != uint32(1) {
		return uint64(7)
	}
	flags := LoadUint8(data + uint64(1))
	if flags != uint32(1) {
		if flags != uint32(3) {
			return uint64(7)
		}
	}
	if BytesZero(data+uint64(3), uint64(5)) == false {
		return uint64(7)
	}
	if flags == uint32(1) {
		if BytesZero(data+uint64(16), uint64(32)) == false {
			return uint64(7)
		}
	}
	return uint64(0)
}

func ValidateToken(data uint64) uint64 {
	if BytesZero(data, uint64(120)) == true {
		return uint64(9)
	}
	if LoadUint8(data) != uint32(2) {
		return uint64(7)
	}
	flags := LoadUint8(data + uint64(1))
	if flags != uint32(1) {
		if flags != uint32(3) {
			return uint64(7)
		}
	}
	if BytesZero(data+uint64(2), uint64(6)) == false {
		return uint64(7)
	}
	if flags == uint32(1) {
		if LoadUint64(data+uint64(16)) != uint64(0) {
			return uint64(7)
		}
		if BytesZero(data+uint64(88), uint64(32)) == false {
			return uint64(7)
		}
	}
	return uint64(0)
}

func ProcessInitializeMint(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(1))
	if errorCode != uint64(0) {
		return errorCode
	}
	if LoadUint64(instruction-uint64(8)) != uint64(34) {
		return uint64(1)
	}
	mint := AccountAt(inputAddress, uint64(0))
	errorCode = RequireOwned(mint, programID, true, uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(mint)
	if errorCode != uint64(0) {
		return errorCode
	}
	data := mint + uint64(88)
	if BytesZero(data, uint64(48)) == false {
		errorCode = ValidateMint(data)
		if errorCode == uint64(0) {
			return uint64(8)
		}
		return errorCode
	}
	StoreUint8(data, uint32(1))
	StoreUint8(data+uint64(1), uint32(3))
	StoreUint8(data+uint64(2), LoadUint8(instruction+uint64(1)))
	StoreUint64(data+uint64(8), uint64(0))
	CopyBytes(data+uint64(16), instruction+uint64(2), uint64(32))
	return uint64(0)
}

func ProcessInitializeAccount(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(2))
	if errorCode != uint64(0) {
		return errorCode
	}
	if LoadUint64(instruction-uint64(8)) != uint64(33) {
		return uint64(1)
	}
	token := AccountAt(inputAddress, uint64(0))
	mint := AccountAt(inputAddress, uint64(1))
	if token == uint64(0) {
		return uint64(2)
	}
	if mint == uint64(0) {
		return uint64(2)
	}
	if SameAccount(token, mint) == true {
		return uint64(14)
	}
	errorCode = RequireOwned(token, programID, true, uint64(120))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(token)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(mint, programID, false, uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = ValidateMint(mint + uint64(88))
	if errorCode != uint64(0) {
		return errorCode
	}
	data := token + uint64(88)
	if BytesZero(data, uint64(120)) == false {
		errorCode = ValidateToken(data)
		if errorCode == uint64(0) {
			return uint64(8)
		}
		return errorCode
	}
	StoreUint8(data, uint32(2))
	StoreUint8(data+uint64(1), uint32(1))
	StoreUint64(data+uint64(8), uint64(0))
	StoreUint64(data+uint64(16), uint64(0))
	CopyBytes(data+uint64(24), mint+uint64(8), uint64(32))
	CopyBytes(data+uint64(56), instruction+uint64(1), uint64(32))
	ClearBytes(data+uint64(88), uint64(32))
	return uint64(0)
}

func ProcessMintTo(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	if LoadUint64(instruction-uint64(8)) != uint64(9) {
		return uint64(1)
	}
	mint := AccountAt(inputAddress, uint64(0))
	destination := AccountAt(inputAddress, uint64(1))
	authority := AccountAt(inputAddress, uint64(2))
	if mint == uint64(0) {
		return uint64(2)
	}
	if destination == uint64(0) {
		return uint64(2)
	}
	if SameAccount(mint, destination) == true {
		return uint64(14)
	}
	errorCode = RequireOwned(mint, programID, true, uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(destination, programID, true, uint64(120))
	if errorCode != uint64(0) {
		return errorCode
	}
	mintData := mint + uint64(88)
	destinationData := destination + uint64(88)
	errorCode = ValidateMint(mintData)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = ValidateToken(destinationData)
	if errorCode != uint64(0) {
		return errorCode
	}
	if BytesEqual(destinationData+uint64(24), mint+uint64(8), uint64(32)) == false {
		return uint64(10)
	}
	if LoadUint8(mintData+uint64(1)) != uint32(3) {
		return uint64(15)
	}
	errorCode = RequireAuthority(authority, mintData+uint64(16))
	if errorCode != uint64(0) {
		return errorCode
	}
	amount := LoadLittleUint64(instruction + uint64(1))
	supply := LoadUint64(mintData + uint64(8))
	balance := LoadUint64(destinationData + uint64(8))
	newSupply := supply + amount
	newBalance := balance + amount
	if newSupply < supply {
		return uint64(13)
	}
	if newBalance < balance {
		return uint64(13)
	}
	StoreUint64(mintData+uint64(8), newSupply)
	StoreUint64(destinationData+uint64(8), newBalance)
	return uint64(0)
}

func ProcessTransfer(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	if LoadUint64(instruction-uint64(8)) != uint64(9) {
		return uint64(1)
	}
	source := AccountAt(inputAddress, uint64(0))
	destination := AccountAt(inputAddress, uint64(1))
	authority := AccountAt(inputAddress, uint64(2))
	if source == uint64(0) {
		return uint64(2)
	}
	if destination == uint64(0) {
		return uint64(2)
	}
	if SameAccount(source, destination) == true {
		return uint64(14)
	}
	errorCode = RequireOwned(source, programID, true, uint64(120))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(destination, programID, true, uint64(120))
	if errorCode != uint64(0) {
		return errorCode
	}
	sourceData := source + uint64(88)
	destinationData := destination + uint64(88)
	errorCode = ValidateToken(sourceData)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = ValidateToken(destinationData)
	if errorCode != uint64(0) {
		return errorCode
	}
	if BytesEqual(sourceData+uint64(24), destinationData+uint64(24), uint64(32)) == false {
		return uint64(10)
	}
	errorCode = RequireSigner(authority)
	if errorCode != uint64(0) {
		return errorCode
	}
	amount := LoadLittleUint64(instruction + uint64(1))
	delegated := uint64(0)
	if BytesEqual(authority+uint64(8), sourceData+uint64(56), uint64(32)) == false {
		if LoadUint8(sourceData+uint64(1)) != uint32(3) {
			return uint64(6)
		}
		if BytesEqual(authority+uint64(8), sourceData+uint64(88), uint64(32)) == false {
			return uint64(6)
		}
		if LoadUint64(sourceData+uint64(16)) < amount {
			return uint64(12)
		}
		delegated = uint64(1)
	}
	sourceBalance := LoadUint64(sourceData + uint64(8))
	destinationBalance := LoadUint64(destinationData + uint64(8))
	if sourceBalance < amount {
		return uint64(11)
	}
	newDestinationBalance := destinationBalance + amount
	if newDestinationBalance < destinationBalance {
		return uint64(13)
	}
	StoreUint64(sourceData+uint64(8), sourceBalance-amount)
	StoreUint64(destinationData+uint64(8), newDestinationBalance)
	if delegated == uint64(1) {
		allowance := LoadUint64(sourceData + uint64(16))
		StoreUint64(sourceData+uint64(16), allowance-amount)
	}
	return uint64(0)
}

func ProcessBurn(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	if LoadUint64(instruction-uint64(8)) != uint64(9) {
		return uint64(1)
	}
	source := AccountAt(inputAddress, uint64(0))
	mint := AccountAt(inputAddress, uint64(1))
	authority := AccountAt(inputAddress, uint64(2))
	if source == uint64(0) {
		return uint64(2)
	}
	if mint == uint64(0) {
		return uint64(2)
	}
	if SameAccount(source, mint) == true {
		return uint64(14)
	}
	errorCode = RequireOwned(source, programID, true, uint64(120))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireOwned(mint, programID, true, uint64(48))
	if errorCode != uint64(0) {
		return errorCode
	}
	sourceData := source + uint64(88)
	mintData := mint + uint64(88)
	errorCode = ValidateToken(sourceData)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = ValidateMint(mintData)
	if errorCode != uint64(0) {
		return errorCode
	}
	if BytesEqual(sourceData+uint64(24), mint+uint64(8), uint64(32)) == false {
		return uint64(10)
	}
	errorCode = RequireSigner(authority)
	if errorCode != uint64(0) {
		return errorCode
	}
	amount := LoadLittleUint64(instruction + uint64(1))
	delegated := uint64(0)
	if BytesEqual(authority+uint64(8), sourceData+uint64(56), uint64(32)) == false {
		if LoadUint8(sourceData+uint64(1)) != uint32(3) {
			return uint64(6)
		}
		if BytesEqual(authority+uint64(8), sourceData+uint64(88), uint64(32)) == false {
			return uint64(6)
		}
		if LoadUint64(sourceData+uint64(16)) < amount {
			return uint64(12)
		}
		delegated = uint64(1)
	}
	balance := LoadUint64(sourceData + uint64(8))
	supply := LoadUint64(mintData + uint64(8))
	if balance < amount {
		return uint64(11)
	}
	if supply < amount {
		return uint64(11)
	}
	StoreUint64(sourceData+uint64(8), balance-amount)
	StoreUint64(mintData+uint64(8), supply-amount)
	if delegated == uint64(1) {
		allowance := LoadUint64(sourceData + uint64(16))
		StoreUint64(sourceData+uint64(16), allowance-amount)
	}
	return uint64(0)
}

func ProcessApprove(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(3))
	if errorCode != uint64(0) {
		return errorCode
	}
	if LoadUint64(instruction-uint64(8)) != uint64(9) {
		return uint64(1)
	}
	source := AccountAt(inputAddress, uint64(0))
	owner := AccountAt(inputAddress, uint64(1))
	delegate := AccountAt(inputAddress, uint64(2))
	errorCode = RequireOwned(source, programID, true, uint64(120))
	if errorCode != uint64(0) {
		return errorCode
	}
	sourceData := source + uint64(88)
	errorCode = ValidateToken(sourceData)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireAuthority(owner, sourceData+uint64(56))
	if errorCode != uint64(0) {
		return errorCode
	}
	if delegate == uint64(0) {
		return uint64(2)
	}
	amount := LoadLittleUint64(instruction + uint64(1))
	StoreUint8(sourceData+uint64(1), uint32(3))
	StoreUint64(sourceData+uint64(16), amount)
	CopyBytes(sourceData+uint64(88), delegate+uint64(8), uint64(32))
	return uint64(0)
}

func ProcessRevoke(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(2))
	if errorCode != uint64(0) {
		return errorCode
	}
	if LoadUint64(instruction-uint64(8)) != uint64(1) {
		return uint64(1)
	}
	source := AccountAt(inputAddress, uint64(0))
	owner := AccountAt(inputAddress, uint64(1))
	errorCode = RequireOwned(source, programID, true, uint64(120))
	if errorCode != uint64(0) {
		return errorCode
	}
	sourceData := source + uint64(88)
	errorCode = ValidateToken(sourceData)
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireAuthority(owner, sourceData+uint64(56))
	if errorCode != uint64(0) {
		return errorCode
	}
	StoreUint8(sourceData+uint64(1), uint32(1))
	StoreUint64(sourceData+uint64(16), uint64(0))
	ClearBytes(sourceData+uint64(88), uint64(32))
	return uint64(0)
}

func ProcessSetAuthority(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	errorCode := RequireAccountCount(inputAddress, uint64(2))
	if errorCode != uint64(0) {
		return errorCode
	}
	if LoadUint64(instruction-uint64(8)) != uint64(35) {
		return uint64(1)
	}
	authorityType := LoadUint8(instruction + uint64(1))
	option := LoadUint8(instruction + uint64(2))
	if option > uint32(1) {
		return uint64(1)
	}
	if option == uint32(0) {
		if BytesZero(instruction+uint64(3), uint64(32)) == false {
			return uint64(1)
		}
	}
	target := AccountAt(inputAddress, uint64(0))
	current := AccountAt(inputAddress, uint64(1))
	if authorityType == uint32(0) {
		errorCode = RequireOwned(target, programID, true, uint64(48))
		if errorCode != uint64(0) {
			return errorCode
		}
		data := target + uint64(88)
		errorCode = ValidateMint(data)
		if errorCode != uint64(0) {
			return errorCode
		}
		if LoadUint8(data+uint64(1)) != uint32(3) {
			return uint64(15)
		}
		errorCode = RequireAuthority(current, data+uint64(16))
		if errorCode != uint64(0) {
			return errorCode
		}
		if option == uint32(1) {
			StoreUint8(data+uint64(1), uint32(3))
			CopyBytes(data+uint64(16), instruction+uint64(3), uint64(32))
		} else {
			StoreUint8(data+uint64(1), uint32(1))
			ClearBytes(data+uint64(16), uint64(32))
		}
		return uint64(0)
	}
	if authorityType == uint32(1) {
		if option != uint32(1) {
			return uint64(1)
		}
		errorCode = RequireOwned(target, programID, true, uint64(120))
		if errorCode != uint64(0) {
			return errorCode
		}
		data := target + uint64(88)
		errorCode = ValidateToken(data)
		if errorCode != uint64(0) {
			return errorCode
		}
		errorCode = RequireAuthority(current, data+uint64(56))
		if errorCode != uint64(0) {
			return errorCode
		}
		CopyBytes(data+uint64(56), instruction+uint64(3), uint64(32))
		StoreUint8(data+uint64(1), uint32(1))
		StoreUint64(data+uint64(16), uint64(0))
		ClearBytes(data+uint64(88), uint64(32))
		return uint64(0)
	}
	return uint64(1)
}

// Program is called by the generated Solana wrapper. Agave passes r1 as the
// input base and r2 as the absolute guest virtual address of instruction data.
func Program(inputAddress uint64, instructionDataAddress uint64) uint64 {
	instructionLength := LoadUint64(instructionDataAddress - uint64(8))
	if instructionLength == uint64(0) {
		return uint64(1)
	}
	programID := instructionDataAddress + instructionLength
	tag := LoadUint8(instructionDataAddress)
	if tag == uint32(0) {
		if instructionLength != uint64(34) {
			return uint64(1)
		}
		return ProcessInitializeMint(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(1) {
		if instructionLength != uint64(33) {
			return uint64(1)
		}
		return ProcessInitializeAccount(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(2) {
		if instructionLength != uint64(9) {
			return uint64(1)
		}
		return ProcessTransfer(inputAddress, instructionDataAddress, programID)
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
		return ProcessApprove(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(6) {
		if instructionLength != uint64(1) {
			return uint64(1)
		}
		return ProcessRevoke(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(7) {
		if instructionLength != uint64(35) {
			return uint64(1)
		}
		authorityType := LoadUint8(instructionDataAddress + uint64(1))
		option := LoadUint8(instructionDataAddress + uint64(2))
		if authorityType > uint32(1) {
			return uint64(1)
		}
		if option > uint32(1) {
			return uint64(1)
		}
		if option == uint32(0) {
			if BytesZero(instructionDataAddress+uint64(3), uint64(32)) == false {
				return uint64(1)
			}
			if authorityType == uint32(1) {
				return uint64(1)
			}
		}
		return ProcessSetAuthority(inputAddress, instructionDataAddress, programID)
	}
	return uint64(1)
}
