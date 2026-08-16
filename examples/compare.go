package main

func CompareExample() uint64 {
	return Compare(30, 20)
}

func Compare(a uint64, b uint64) uint64 {
	if a >= b {
		return 1
	}
	return 0
}
