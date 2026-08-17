// accounts.go holds the low-level Agave ABIv1 primitives shared by every
// instruction handler in program.go, compiled together via the compiler's
// multi-file support. It is examples/erc1155/testdata/accounts.go's proven
// account-field-intrinsic helper set (AccountAt, RequireOwned,
// RequireAuthority, RequireSigner, and the small byte-level utilities),
// plus the CPI-construction primitives from examples/cpi/testdata/program.go
// that Deposit needs to invoke the System Program — this file is the only
// place in the repository combining both styles, which
// compiler/address_lifetime.go's stack-escape verifier permits freely since
// account-record addresses (AccountKeyAddress and friends) carry no
// stack-pointer provenance.
//
// RequireOwned and RequireAuthority return payable's own numeric
// ProgramError codes (examples/payable/types.go), not erc1155's — the two
// programs' error enums are independent, so the constants embedded in this
// copy differ from the erc1155 original at exactly the two lines noted
// below.
package program

func LoadUint8(address uint64) uint32
func LoadUint32(address uint64) uint32
func LoadUint64(address uint64) uint64
func StoreUint8(address uint64, value uint32)
func StoreUint32(address uint64, value uint32)
func StoreUint64(address uint64, value uint64)

func AccountIsSigner(record uint64) bool
func AccountIsWritable(record uint64) bool
func AccountIsExecutable(record uint64) bool
func AccountKeyAddress(record uint64) uint64
func AccountOwnerAddress(record uint64) uint64
func AccountLamportsAddress(record uint64) uint64
func AccountDataLen(record uint64) uint64
func AccountDataAddress(record uint64) uint64

// AddressUint64 exposes a compiler-managed guest stack address — the only
// way Deposit's CPI call gets a real address for its on-stack
// SolInstruction/SolAccountInfo scratch array.
func AddressUint64(pointer *uint64) uint64

// InvokeSignedC is the official sol_invoke_signed_c static syscall.
func InvokeSignedC(instructionAddress uint64, accountInfosAddress uint64, accountInfosLength uint64, signerSeedsAddress uint64, signerSeedsLength uint64) uint64

func BytesEqual(first uint64, second uint64, length uint64) bool {
	for index := uint64(0); index < length; index++ {
		if LoadUint8(first+index) != LoadUint8(second+index) {
			return false
		}
	}
	return true
}

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

// LoadLittleUint64 reads an 8-byte little-endian value from a byte-aligned
// (not necessarily 8-byte-aligned) guest address — instruction-data amounts
// start at byte one and are therefore intentionally unaligned.
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

// StoreLittleUint64 writes an 8-byte little-endian value at a byte-aligned
// guest address — used for the amount field inside a hand-built
// SystemInstruction::Transfer payload.
func StoreLittleUint64(address uint64, value uint64) {
	for index := uint64(0); index < uint64(8); index++ {
		StoreUint8(address+index, uint32(value%uint64(256)))
		value = value / uint64(256)
	}
}

// AccountNextRecord advances to the next Agave ABIv1 account record. A
// unique record is 96 bytes plus data, the 10 KiB permitted realloc
// reservation, and BPF u128 alignment padding; a duplicate record is
// exactly eight bytes.
func AccountNextRecord(record uint64) uint64 {
	if LoadUint8(record) != uint32(255) {
		return record + uint64(8)
	}
	dataLength := AccountDataLen(record)
	padding := (uint64(8) - dataLength%uint64(8)) % uint64(8)
	return record + uint64(96) + dataLength + uint64(10240) + padding
}

// AccountAt resolves the account record at index, including ABIv1 duplicate
// records, by walking physical records in a loop and remembering each
// logical slot's resolved address in a fixed-size table so a later
// duplicate-marker byte can redirect to it. The table is sized 8 (this
// program's instructions need at most 4); returns 0 if index is out of
// range or the input is malformed.
func AccountAt(inputAddress uint64, index uint64) uint64 {
	if index >= uint64(8) {
		return uint64(0)
	}
	var resolved [8]uint64
	physical := inputAddress + uint64(8)
	count := uint64(0)
	for count < uint64(8) {
		marker := LoadUint8(physical)
		actual := physical
		if marker != uint32(255) {
			if marker >= uint32(count) {
				return uint64(0)
			}
			actual = resolved[uint64(marker)]
		}
		resolved[count] = actual
		if count == index {
			return actual
		}
		physical = AccountNextRecord(physical)
		count = count + uint64(1)
	}
	return uint64(0)
}

func RequireAccountCount(inputAddress uint64, wanted uint64) uint64 {
	count := LoadUint64(inputAddress)
	if count < wanted {
		return uint64(2) // ErrMissingAccount
	}
	if count > wanted {
		return uint64(1) // ErrInvalidInstruction
	}
	return uint64(0)
}

func SameAccount(first uint64, second uint64) bool {
	if first == second {
		return true
	}
	return BytesEqual(AccountKeyAddress(first), AccountKeyAddress(second), uint64(32))
}

// RequireOwned checks the account exists, is owned by programID, is
// writable when required, is not executable, and has exactly size bytes of
// data.
func RequireOwned(account uint64, programID uint64, writable bool, size uint64) uint64 {
	if account == uint64(0) {
		return uint64(2) // ErrMissingAccount
	}
	if BytesEqual(AccountOwnerAddress(account), programID, uint64(32)) == false {
		return uint64(3) // ErrInvalidProgramOwner
	}
	if writable == true {
		if AccountIsWritable(account) == false {
			return uint64(4) // ErrAccountReadOnly
		}
	}
	if AccountIsExecutable(account) == true {
		return uint64(6) // ErrInvalidState
	}
	if AccountDataLen(account) != size {
		return uint64(6) // ErrInvalidState (differs from erc1155's accounts.go, which returns 7 here)
	}
	return uint64(0)
}

// RequireAuthority checks authority exists, its key matches
// expectedKeyAddress, and it signed the transaction.
func RequireAuthority(authority uint64, expectedKeyAddress uint64) uint64 {
	if authority == uint64(0) {
		return uint64(2) // ErrMissingAccount
	}
	if BytesEqual(AccountKeyAddress(authority), expectedKeyAddress, uint64(32)) == false {
		return uint64(15) // ErrInvalidAuthority (differs from erc1155's accounts.go, which returns 6 here)
	}
	if AccountIsSigner(authority) == false {
		return uint64(5) // ErrMissingSignature
	}
	return uint64(0)
}

func RequireSigner(authority uint64) uint64 {
	if authority == uint64(0) {
		return uint64(2) // ErrMissingAccount
	}
	if AccountIsSigner(authority) == false {
		return uint64(5) // ErrMissingSignature
	}
	return uint64(0)
}

// RentEpoch and AccountInfoFlags/WriteAccountInfo below are
// examples/cpi/testdata/program.go's proven CPI-construction helpers,
// copied near-verbatim (RentEpoch's dataLength load now goes through the
// AccountDataLen intrinsic instead of a hand-computed record+80 offset;
// the two read the same field). WriteAccountInfo itself is rewritten in
// terms of the account-field intrinsics above instead of hand-computed
// offsets, since this file already declares them and their offsets are
// exactly the ones the original hand-rolled version used.

func RentEpoch(record uint64) uint64 {
	dataLength := AccountDataLen(record)
	padding := (uint64(8) - dataLength%uint64(8)) % uint64(8)
	return LoadUint64(record + uint64(88) + dataLength + uint64(10240) + padding)
}

func AccountInfoFlags(record uint64) uint64 {
	return uint64(LoadUint8(record+uint64(1))) +
		uint64(LoadUint8(record+uint64(2)))*uint64(256) +
		uint64(LoadUint8(record+uint64(3)))*uint64(65536)
}

// WriteAccountInfo writes one exact 56-byte SolAccountInfo. The pointer
// fields point back to Agave's serialized account metadata, as required by
// the syscall_parameter_address_restrictions feature.
func WriteAccountInfo(address uint64, record uint64) {
	StoreUint64(address, AccountKeyAddress(record))
	StoreUint64(address+uint64(8), AccountLamportsAddress(record))
	StoreUint64(address+uint64(16), AccountDataLen(record))
	StoreUint64(address+uint64(24), AccountDataAddress(record))
	StoreUint64(address+uint64(32), AccountOwnerAddress(record))
	StoreUint64(address+uint64(40), RentEpoch(record))
	StoreUint64(address+uint64(48), AccountInfoFlags(record))
}
