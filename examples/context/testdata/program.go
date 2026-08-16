// program.go is the Solana entrypoint for this example. It calls AccountAt,
// AccountIsSigner, and AccountIsWritable from accounts.go — compiled
// together as one package via `go-solana build`'s multi-file support — to
// require that account 0 is both a signer and writable, without any
// record+N offset arithmetic appearing in this file at all.
package program

func Program(inputAddress uint64, instructionDataAddress uint64) uint64 {
	account := AccountAt(inputAddress, uint64(0))
	if account == uint64(0) {
		return uint64(1) // missing account 0
	}
	if AccountIsSigner(account) == false {
		return uint64(2) // account 0 did not sign
	}
	if AccountIsWritable(account) == false {
		return uint64(3) // account 0 is not writable
	}
	return uint64(0)
}
