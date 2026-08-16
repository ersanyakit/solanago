package program

// Program is a validator-loadable smoke program. The generated wrapper passes
// Agave's r1 input-region base and r2 absolute instruction-data guest address
// unchanged.
func Program(inputAddress uint64, instructionDataAddress uint64) uint64 {
	return 0
}
