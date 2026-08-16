// This Go source is compiled directly to an sBPFv3 ELF. It builds Agave's
// exact C CPI structures in sBPF stack memory and invokes the System Program.
// It uses no Go runtime, host pointer, unsafe package, Rust, or C toolchain.
package program

func LoadUint8(address uint64) uint32
func LoadUint64(address uint64) uint64
func StoreUint8(address uint64, value uint32)
func StoreUint32(address uint64, value uint32)
func StoreUint64(address uint64, value uint64)
func AddressUint64(pointer *uint64) uint64
func InvokeSignedC(instructionAddress uint64, accountInfosAddress uint64, accountInfosLength uint64, signerSeedsAddress uint64, signerSeedsLength uint64) uint64

// Agave ABIv1 reserves 10 KiB after each account's original data. Duplicate
// account slots contain only an eight-byte duplicate marker record.
func NextPhysicalAccount(record uint64) uint64 {
	if LoadUint8(record) != uint32(255) {
		return record + uint64(8)
	}
	dataLength := LoadUint64(record + uint64(80))
	padding := (uint64(8) - dataLength%uint64(8)) % uint64(8)
	return record + uint64(96) + dataLength + uint64(10240) + padding
}

// AccountAt resolves the three logical slots used by this program, including
// ABIv1 duplicate records. Returned values are guest virtual addresses.
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

func BytesZero(address uint64, length uint64) bool {
	for index := uint64(0); index < length; index++ {
		if LoadUint8(address+index) != uint32(0) {
			return false
		}
	}
	return true
}

func StoreLittleUint64(address uint64, value uint64) {
	for index := uint64(0); index < uint64(8); index++ {
		StoreUint8(address+index, uint32(value%uint64(256)))
		value = value / uint64(256)
	}
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

// WriteAccountInfo writes one exact 56-byte SolAccountInfo. The pointer fields
// point back to Agave's serialized account metadata, as required by the
// syscall_parameter_address_restrictions feature.
func WriteAccountInfo(address uint64, record uint64) {
	StoreUint64(address, record+uint64(8))
	StoreUint64(address+uint64(8), record+uint64(72))
	StoreUint64(address+uint64(16), LoadUint64(record+uint64(80)))
	StoreUint64(address+uint64(24), record+uint64(88))
	StoreUint64(address+uint64(32), record+uint64(40))
	StoreUint64(address+uint64(40), RentEpoch(record))
	StoreUint64(address+uint64(48), AccountInfoFlags(record))
}

func Program(inputAddress uint64, instructionDataAddress uint64) uint64 {
	if LoadUint64(inputAddress) != uint64(3) {
		return uint64(1)
	}
	if LoadUint64(instructionDataAddress-uint64(8)) != uint64(8) {
		return uint64(1)
	}
	amount := LoadUint64(instructionDataAddress)
	if amount == uint64(0) {
		return uint64(1)
	}

	source := AccountAt(inputAddress, uint64(0))
	destination := AccountAt(inputAddress, uint64(1))
	systemProgram := AccountAt(inputAddress, uint64(2))
	if source == uint64(0) {
		return uint64(2)
	}
	if destination == uint64(0) {
		return uint64(2)
	}
	if systemProgram == uint64(0) {
		return uint64(2)
	}
	if LoadUint8(systemProgram+uint64(3)) != uint32(1) {
		return uint64(4)
	}
	if BytesZero(systemProgram+uint64(8), uint64(32)) == false {
		return uint64(4)
	}

	// words is 256 bytes in the sBPF stack region. Layout:
	//   0..39   SolInstruction
	//   40..71  two SolAccountMeta values
	//   72..83  SystemInstruction::Transfer data
	//   88..255 three SolAccountInfo values
	var words [32]uint64
	base := AddressUint64(&words[0])
	StoreUint64(base, systemProgram+uint64(8))
	StoreUint64(base+uint64(8), base+uint64(40))
	StoreUint64(base+uint64(16), uint64(2))
	StoreUint64(base+uint64(24), base+uint64(72))
	StoreUint64(base+uint64(32), uint64(12))
	StoreUint64(base+uint64(40), source+uint64(8))
	StoreUint64(base+uint64(48), uint64(0x0101))
	StoreUint64(base+uint64(56), destination+uint64(8))
	StoreUint64(base+uint64(64), uint64(0x0001))
	StoreUint32(base+uint64(72), uint32(2))
	StoreLittleUint64(base+uint64(76), amount)
	WriteAccountInfo(base+uint64(88), source)
	WriteAccountInfo(base+uint64(144), destination)
	WriteAccountInfo(base+uint64(200), systemProgram)

	return InvokeSignedC(base, base+uint64(88), uint64(3), uint64(0), uint64(0))
}
