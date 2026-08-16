// This is a simple custom Go program for on-chain testing.
//
// It defines a one-account instruction that stores:
// - a constant initialized flag
// - first name (max 32 bytes, zero-padded)
// - last name (max 32 bytes, zero-padded)
// into the account data owned by this program.
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

func CopyBytes(destination uint64, source uint64, length uint64) {
	for index := uint64(0); index < length; index++ {
		StoreUint8(destination+index, LoadUint8(source+index))
	}
}

// NextPhysicalAccount follows Agave ABIv1's virtual account layout.
func NextPhysicalAccount(record uint64) uint64 {
	if LoadUint8(record) != uint32(255) {
		return record + uint64(8)
	}
	dataLength := LoadUint64(record + uint64(80))
	padding := (uint64(8) - dataLength%uint64(8)) % uint64(8)
	return record + uint64(96) + dataLength + uint64(10240) + padding
}

// AccountAt resolves account index 0..2 from the ABIv1 header.
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

func RequireOwned(account uint64, programID uint64, writable bool, size uint64) uint64 {
	if account == uint64(0) {
		return uint64(2)
	}
	if BytesZero(account+uint64(8), uint64(32)) == true {
		return uint64(3)
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

func RequireSigner(authority uint64) uint64 {
	if authority == uint64(0) {
		return uint64(2)
	}
	if LoadUint8(authority+uint64(1)) == uint32(0) {
		return uint64(5)
	}
	return uint64(0)
}

func BytesEqual(first uint64, second uint64, length uint64) bool {
	for index := uint64(0); index < length; index++ {
		if LoadUint8(first+index) != LoadUint8(second+index) {
			return false
		}
	}
	return true
}

func ClearStorage(data uint64) {
	accountDataSize := uint64(65)
	for index := uint64(0); index < accountDataSize; index++ {
		StoreUint8(data+index, uint32(0))
	}
}

func ProcessRecordName(inputAddress uint64, instruction uint64, programID uint64) uint64 {
	accountDataSize := uint64(65)
	nameFieldOffset := uint64(1)
	surnameFieldOffset := uint64(33)
	fieldLength := uint64(32)
	errorCode := RequireAccountCount(inputAddress, uint64(1))
	if errorCode != uint64(0) {
		return errorCode
	}
	if LoadUint64(instruction-uint64(8)) != accountDataSize {
		return uint64(1)
	}
	storage := AccountAt(inputAddress, uint64(0))
	if storage == uint64(0) {
		return uint64(2)
	}
	errorCode = RequireOwned(storage, programID, true, uint64(accountDataSize))
	if errorCode != uint64(0) {
		return errorCode
	}
	errorCode = RequireSigner(storage)
	if errorCode != uint64(0) {
		return errorCode
	}
	data := storage + uint64(88)
	ClearStorage(data)
	StoreUint8(data, uint32(1))
	CopyBytes(data+nameFieldOffset, instruction+uint64(1), fieldLength)
	CopyBytes(data+surnameFieldOffset, instruction+uint64(33), fieldLength)
	return uint64(0)
}

func Program(inputAddress uint64, instructionDataAddress uint64) uint64 {
	accountDataSize := uint64(65)
	recordInstructionTag := uint32(1)
	instructionLength := LoadUint64(instructionDataAddress - uint64(8))
	if instructionLength != accountDataSize {
		return uint64(1)
	}
	programID := instructionDataAddress + instructionLength
	tag := LoadUint8(instructionDataAddress)
	if tag != recordInstructionTag {
		return uint64(1)
	}
	return ProcessRecordName(inputAddress, instructionDataAddress, programID)
}
