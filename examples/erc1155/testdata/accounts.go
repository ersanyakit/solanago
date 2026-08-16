// accounts.go holds the low-level Agave ABIv1 primitives shared by every
// instruction handler in program.go, compiled together via the compiler's
// multi-file support. Every account-*record* field access goes through the
// compiler-recognized account-field intrinsics (AccountIsSigner and
// similar) instead of hand-computed record+N offsets; only the *contents*
// of this program's own account data (which Agave's ABI knows nothing
// about) use plain offsets, matching examples/gospl's convention there.
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
func AccountDataLen(record uint64) uint64
func AccountDataAddress(record uint64) uint64

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
// duplicate-marker byte can redirect to it — a generalization of the
// hand-unrolled 0..2 cascade examples/gospl and examples/cpi use, since
// this program's instructions need up to 4 accounts. The table is sized 8
// (this program's instructions need at most 4); returns 0 if index is out
// of range or the input is malformed.
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
	return BytesEqual(AccountKeyAddress(first), AccountKeyAddress(second), uint64(32))
}

// RequireOwned checks the account exists, is owned by programID, is
// writable when required, is not executable, and has exactly size bytes of
// data — the account-level checks every instruction needs before it may
// interpret the account's contents.
func RequireOwned(account uint64, programID uint64, writable bool, size uint64) uint64 {
	if account == uint64(0) {
		return uint64(2)
	}
	if BytesEqual(AccountOwnerAddress(account), programID, uint64(32)) == false {
		return uint64(3)
	}
	if writable == true {
		if AccountIsWritable(account) == false {
			return uint64(4)
		}
	}
	if AccountIsExecutable(account) == true {
		return uint64(7)
	}
	if AccountDataLen(account) != size {
		return uint64(7)
	}
	return uint64(0)
}

// RequireAuthority checks authority exists, its key matches
// expectedKeyAddress, and it signed the transaction.
func RequireAuthority(authority uint64, expectedKeyAddress uint64) uint64 {
	if authority == uint64(0) {
		return uint64(2)
	}
	if BytesEqual(AccountKeyAddress(authority), expectedKeyAddress, uint64(32)) == false {
		return uint64(6)
	}
	if AccountIsSigner(authority) == false {
		return uint64(5)
	}
	return uint64(0)
}

func RequireSigner(authority uint64) uint64 {
	if authority == uint64(0) {
		return uint64(2)
	}
	if AccountIsSigner(authority) == false {
		return uint64(5)
	}
	return uint64(0)
}
