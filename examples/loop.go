package main

func LoopExample() uint64 {
	return Sum(10)
}

func Sum(n uint64) uint64 {
	var total uint64 = 0
	var i uint64 = 0
	for i < n {
		total = total + i
		i = i + 1
	}
	return total
}
