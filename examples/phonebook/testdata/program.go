package program

func LoadUint8(address uint64) uint32
func LoadUint64(address uint64) uint64
func StoreUint8(address uint64, value uint32)
func StoreUint64(address uint64, value uint64)
func InvokeSignedC(instructionAddress uint64, accountInfosAddress uint64, accountInfosLength uint64, signerSeedsAddress uint64, signerSeedsLength uint64) uint64
func AddressUint64(pointer *uint64) uint64

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

func NextPhysicalAccount(record uint64) uint64 {
	if LoadUint8(record) != uint32(255) {
		return record + uint64(8)
	}
	dataLength := LoadUint64(record + uint64(80))
	padding := (uint64(8) - dataLength%uint64(8)) % uint64(8)
	return record + uint64(96) + dataLength + uint64(10240) + padding
}

func AccountAt(inputAddress uint64, index uint64) uint64 {
	current := inputAddress + uint64(8)
	if LoadUint8(current) != uint32(255) {
		return uint64(0)
	}
	if index == uint64(0) {
		return current
	}
	for cursor := uint64(1); cursor <= index; cursor++ {
		current = NextPhysicalAccount(current)
		if LoadUint8(current) != uint32(255) {
			return uint64(0)
		}
	}
	return current
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
	if BytesZero(account+uint64(8), uint64(32)) {
		return uint64(3)
	}
	if BytesEqual(account+uint64(40), programID, uint64(32)) == false {
		return uint64(3)
	}
	if writable {
		if LoadUint8(account+uint64(2)) == uint32(0) {
			return uint64(4)
		}
	}
	if LoadUint8(account+uint64(3)) != uint32(0) {
		return uint64(4)
	}
	if LoadUint64(account+uint64(80)) != size {
		return uint64(7)
	}
	return uint64(0)
}

func RequireSigner(account uint64) uint64 {
	if account == uint64(0) {
		return uint64(2)
	}
	if LoadUint8(account+uint64(1)) == uint32(0) {
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

func ClearBytes(address uint64, length uint64) {
	for index := uint64(0); index < length; index++ {
		StoreUint8(address+index, uint32(0))
	}
}

func StoreLittleUint32(address uint64, value uint32) {
	for index := uint64(0); index < uint64(4); index++ {
		StoreUint8(address+index, value%uint32(256))
		value = value / uint32(256)
	}
}

func StoreLittleUint64(address uint64, value uint64) {
	for index := uint64(0); index < uint64(8); index++ {
		StoreUint8(address+index, uint32(value%uint64(256)))
		value = value / uint64(256)
	}
}

func LoadLittleUint64(address uint64) uint64 {
	value := uint64(0)
	for index := uint64(0); index < uint64(8); index++ {
		if index == uint64(0) {
			value = value + uint64(LoadUint8(address+index))*uint64(1)
		} else if index == uint64(1) {
			value = value + uint64(LoadUint8(address+index))*uint64(256)
		} else if index == uint64(2) {
			value = value + uint64(LoadUint8(address+index))*uint64(65536)
		} else if index == uint64(3) {
			value = value + uint64(LoadUint8(address+index))*uint64(16777216)
		} else if index == uint64(4) {
			value = value + uint64(LoadUint8(address+index))*uint64(4294967296)
		} else if index == uint64(5) {
			value = value + uint64(LoadUint8(address+index))*uint64(1099511627776)
		} else if index == uint64(6) {
			value = value + uint64(LoadUint8(address+index))*uint64(281474976710656)
		} else if index == uint64(7) {
			value = value + uint64(LoadUint8(address+index))*uint64(72057594037927936)
		}
	}
	return value
}

func RentEpoch(record uint64) uint64 {
	dataLength := LoadUint64(record + uint64(80))
	padding := (uint64(8) - dataLength%uint64(8)) % uint64(8)
	return LoadUint64(record + uint64(88) + dataLength + uint64(10240) + padding)
}

func AccountInfoFlags(record uint64) uint64 {
	return uint64(LoadUint8(record+uint64(1))) +
		uint64(LoadUint8(record+uint64(2)))*uint64(256) +
		uint64(LoadUint8(record+uint64(3)))*uint64(65536)
}

func WriteAccountInfo(address uint64, record uint64) {
	StoreUint64(address, record+uint64(8))
	StoreUint64(address+uint64(8), record+uint64(72))
	StoreUint64(address+uint64(16), LoadUint64(record+uint64(80)))
	StoreUint64(address+uint64(24), record+uint64(88))
	StoreUint64(address+uint64(32), LoadUint64(record+uint64(40)))
	StoreUint64(address+uint64(40), RentEpoch(record))
	StoreUint64(address+uint64(48), AccountInfoFlags(record))
}

func EnsureSystem(programAccount uint64) uint64 {
	if programAccount == uint64(0) {
		return uint64(4)
	}
	if LoadUint8(programAccount+uint64(3)) != uint32(1) {
		return uint64(4)
	}
	if BytesZero(programAccount+uint64(8), uint64(32)) == false {
		return uint64(4)
	}
	return uint64(0)
}

func ConfigDataLen() uint64 {
	return uint64(74)
}

func PhonebookDataLen() uint64 {
	return uint64(1315)
}

func PhonebookMaxContacts() uint64 {
	return uint64(20)
}

func PhonebookEntrySize() uint64 {
	return uint64(64)
}

func PhonebookOwnerOffset() uint64 {
	return uint64(2)
}

func PhonebookCountOffset() uint64 {
	return uint64(34)
}

func PhonebookEntriesOffset() uint64 {
	return uint64(35)
}

func ConfigAdminOffset() uint64 {
	return uint64(2)
}

func ConfigTreasuryOffset() uint64 {
	return uint64(34)
}

func ConfigFeeOffset() uint64 {
	return uint64(66)
}

func TransferSystemLamports(source uint64, destination uint64, systemProgram uint64, amount uint64) uint64 {
	if amount == uint64(0) {
		return uint64(0)
	}
	var words [32]uint64
	base := AddressUint64(&words[0])

	StoreUint64(base+uint64(0), systemProgram+uint64(8))
	StoreUint64(base+uint64(8), base+uint64(40))
	StoreUint64(base+uint64(16), uint64(2))
	StoreUint64(base+uint64(24), base+uint64(72))
	StoreUint64(base+uint64(32), uint64(12))
	StoreUint64(base+uint64(40), source+uint64(8))
	StoreUint64(base+uint64(48), uint64(0x0101))
	StoreUint64(base+uint64(56), destination+uint64(8))
	StoreUint64(base+uint64(64), uint64(0x0001))
	StoreLittleUint32(base+uint64(72), uint32(2))
	StoreLittleUint64(base+uint64(76), amount)

	WriteAccountInfo(base+uint64(88), source)
	WriteAccountInfo(base+uint64(144), destination)
	WriteAccountInfo(base+uint64(200), systemProgram)

	return InvokeSignedC(base, base+uint64(88), uint64(3), uint64(0), uint64(0))
}

func ProcessInitConfig(inputAddress uint64, instructionDataAddress uint64, programID uint64) uint64 {
	if RequireAccountCount(inputAddress, uint64(3)) != 0 {
		return uint64(2)
	}
	if LoadUint64(instructionDataAddress-uint64(8)) != uint64(9) {
		return uint64(1)
	}

	configAccount := AccountAt(inputAddress, 0)
	adminAccount := AccountAt(inputAddress, 1)
	treasuryAccount := AccountAt(inputAddress, 2)
	if RequireOwned(configAccount, programID, true, ConfigDataLen()) != 0 {
		return uint64(3)
	}
	if RequireSigner(adminAccount) != 0 {
		return uint64(5)
	}
	if BytesZero(adminAccount+uint64(40), uint64(32)) {
		return uint64(5)
	}
	if BytesZero(treasuryAccount+uint64(40), uint64(32)) {
		return uint64(6)
	}

	configData := configAccount + uint64(88)
	if LoadUint8(configData) != uint32(0) {
		return uint64(8)
	}
	ClearBytes(configData, ConfigDataLen())
	StoreUint8(configData, uint32(1))
	StoreUint8(configData+uint64(1), uint32(1))
	CopyBytes(configData+ConfigAdminOffset(), adminAccount+uint64(40), uint64(32))
	CopyBytes(configData+ConfigTreasuryOffset(), treasuryAccount+uint64(40), uint64(32))
	StoreLittleUint64(configData+ConfigFeeOffset(), LoadLittleUint64(instructionDataAddress+uint64(1)))
	return uint64(0)
}

func ProcessAddContact(inputAddress uint64, instructionDataAddress uint64, programID uint64) uint64 {
	if RequireAccountCount(inputAddress, uint64(5)) != 0 {
		return uint64(2)
	}
	if LoadUint64(instructionDataAddress-uint64(8)) != uint64(65) {
		return uint64(1)
	}

	phonebookAccount := AccountAt(inputAddress, 0)
	ownerAccount := AccountAt(inputAddress, 1)
	configAccount := AccountAt(inputAddress, 2)
	treasuryAccount := AccountAt(inputAddress, 3)
	systemProgramAccount := AccountAt(inputAddress, 4)

	if RequireOwned(phonebookAccount, programID, true, PhonebookDataLen()) != 0 {
		return uint64(3)
	}
	if RequireSigner(ownerAccount) != 0 {
		return uint64(5)
	}
	if RequireOwned(configAccount, programID, false, ConfigDataLen()) != 0 {
		return uint64(6)
	}
	if EnsureSystem(systemProgramAccount) != 0 {
		return uint64(7)
	}

	configData := configAccount + uint64(88)
	if LoadUint8(configData) != uint32(1) {
		return uint64(8)
	}
	if BytesEqual(configData+ConfigTreasuryOffset(), treasuryAccount+uint64(40), uint64(32)) == false {
		return uint64(9)
	}

	phonebookData := phonebookAccount + uint64(88)
	if LoadUint8(phonebookData) != uint32(1) {
		return uint64(9)
	}
	if BytesEqual(phonebookData+PhonebookOwnerOffset(), ownerAccount+uint64(40), uint64(32)) == false {
		return uint64(10)
	}

	targetAddress := instructionDataAddress + uint64(1)
	nameAddress := instructionDataAddress + uint64(33)
	if BytesZero(targetAddress, uint64(32)) {
		return uint64(11)
	}
	if BytesZero(nameAddress, uint64(32)) {
		return uint64(12)
	}

	maxContacts := PhonebookMaxContacts()
	entrySize := PhonebookEntrySize()
	countAddress := phonebookData + PhonebookCountOffset()
	count := uint64(LoadUint8(countAddress))
	if count > maxContacts {
		return uint64(14)
	}
	entriesBase := phonebookData + PhonebookEntriesOffset()

	for index := uint64(0); index < count; index++ {
		entry := entriesBase + index*entrySize
		if BytesEqual(entry, targetAddress, uint64(32)) {
			CopyBytes(entry+uint64(32), nameAddress, uint64(32))
			fee := LoadLittleUint64(configData + ConfigFeeOffset())
			return TransferSystemLamports(ownerAccount, treasuryAccount, systemProgramAccount, fee)
		}
	}

	if count >= maxContacts {
		return uint64(13)
	}
	newEntry := entriesBase + count*entrySize
	CopyBytes(newEntry, targetAddress, uint64(32))
	CopyBytes(newEntry+uint64(32), nameAddress, uint64(32))
	StoreUint8(countAddress, uint32(count+1))

	fee := LoadLittleUint64(configData + ConfigFeeOffset())
	return TransferSystemLamports(ownerAccount, treasuryAccount, systemProgramAccount, fee)
}

func ProcessWithdraw(inputAddress uint64, instructionDataAddress uint64, programID uint64) uint64 {
	if RequireAccountCount(inputAddress, uint64(4)) != 0 {
		return uint64(2)
	}
	if LoadUint64(instructionDataAddress-uint64(8)) != uint64(9) {
		return uint64(1)
	}

	configAccount := AccountAt(inputAddress, 0)
	adminAccount := AccountAt(inputAddress, 1)
	destinationAccount := AccountAt(inputAddress, 2)
	systemProgramAccount := AccountAt(inputAddress, 3)

	if RequireOwned(configAccount, programID, false, ConfigDataLen()) != 0 {
		return uint64(3)
	}
	if RequireSigner(adminAccount) != 0 {
		return uint64(5)
	}
	if EnsureSystem(systemProgramAccount) != 0 {
		return uint64(6)
	}

	configData := configAccount + uint64(88)
	if LoadUint8(configData) != uint32(1) {
		return uint64(7)
	}
	if BytesEqual(configData+ConfigAdminOffset(), adminAccount+uint64(40), uint64(32)) == false {
		return uint64(8)
	}
	if BytesEqual(configData+ConfigTreasuryOffset(), adminAccount+uint64(40), uint64(32)) == false {
		return uint64(9)
	}

	amount := LoadLittleUint64(instructionDataAddress + uint64(1))
	if amount == uint64(0) {
		amount = LoadUint64(adminAccount + uint64(8))
	}
	if amount == uint64(0) {
		return uint64(0)
	}
	return TransferSystemLamports(adminAccount, destinationAccount, systemProgramAccount, amount)
}

func Program(inputAddress uint64, instructionDataAddress uint64) uint64 {
	instructionLength := LoadUint64(instructionDataAddress - uint64(8))
	tag := LoadUint8(instructionDataAddress)
	programID := instructionDataAddress + instructionLength

	if instructionLength == 0 {
		return uint64(1)
	}
	if tag == uint32(1) {
		return ProcessInitConfig(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(2) {
		if instructionLength != uint64(1) {
			return uint64(1)
		}
		phonebookAccount := AccountAt(inputAddress, uint64(0))
		ownerAccount := AccountAt(inputAddress, uint64(1))
		if RequireAccountCount(inputAddress, uint64(2)) != 0 {
			return uint64(2)
		}
		if RequireOwned(phonebookAccount, programID, true, PhonebookDataLen()) != 0 {
			return uint64(3)
		}
		if RequireSigner(ownerAccount) != 0 {
			return uint64(5)
		}
		phonebookData := phonebookAccount + uint64(88)
		if LoadUint8(phonebookData) != uint32(0) {
			return uint64(8)
		}
		ClearBytes(phonebookData, PhonebookDataLen())
		StoreUint8(phonebookData, uint32(1))
		StoreUint8(phonebookData+uint64(1), uint32(1))
		CopyBytes(phonebookData+PhonebookOwnerOffset(), ownerAccount+uint64(40), uint64(32))
		StoreUint8(phonebookData+PhonebookCountOffset(), uint32(0))
		return uint64(0)
	}
	if tag == uint32(3) {
		return ProcessAddContact(inputAddress, instructionDataAddress, programID)
	}
	if tag == uint32(4) {
		return ProcessWithdraw(inputAddress, instructionDataAddress, programID)
	}
	return uint64(14)
}
