// accounts.go is compiled together with program.go as one package (the
// compiler's multi-file support): everything declared here — the intrinsic
// declarations and the AccountAt/AccountNextRecord helpers — is visible to
// Program in program.go without repeating any of it there.
package program

func LoadUint8(address uint64) uint32
func AccountIsSigner(record uint64) bool
func AccountIsWritable(record uint64) bool
func AccountDataLen(record uint64) uint64

// AccountNextRecord advances to the next Agave ABIv1 account record. Agave
// reserves 10 KiB after each account's original data for realloc headroom;
// a duplicate account slot contains only an eight-byte duplicate marker.
// This is the same arithmetic examples/cpi hand-rolls as NextPhysicalAccount,
// rewritten against the account-field intrinsics instead of raw record+N
// offsets.
func AccountNextRecord(record uint64) uint64 {
	if LoadUint8(record) != uint32(255) {
		return record + uint64(8)
	}
	dataLength := AccountDataLen(record)
	padding := (uint64(8) - dataLength%uint64(8)) % uint64(8)
	return record + uint64(96) + dataLength + uint64(10240) + padding
}

// AccountAt resolves the account record at index among the first three
// logical accounts serialized after inputAddress's account-count header,
// including ABIv1 duplicate records. The returned value is a guest virtual
// address valid for the lifetime of the invocation, or zero if index is out
// of range.
func AccountAt(inputAddress uint64, index uint64) uint64 {
	firstPhysical := inputAddress + uint64(8)
	if LoadUint8(firstPhysical) != uint32(255) {
		return uint64(0)
	}
	first := firstPhysical
	if index == uint64(0) {
		return first
	}

	secondPhysical := AccountNextRecord(firstPhysical)
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

	thirdPhysical := AccountNextRecord(secondPhysical)
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
