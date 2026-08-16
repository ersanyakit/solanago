package compiler

import (
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/ersanyakit/solanago/sbpf"
	"github.com/ersanyakit/solanago/vm"
)

func TestMilestone2RejectsMemoryEscapesAndBadIntrinsics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, source, want string
	}{
		{"pointer result", "package p\nfunc F(p *uint32) *uint32 { return p }", "pointer results are not supported"},
		{"slice", "package p\nfunc F() { var v []uint32; _ = v }", "slices are not supported"},
		{"array ABI", "package p\nfunc F(v [2]uint32) uint32 { return v[0] }", "cannot cross the sBPF register ABI"},
		{"bad intrinsic", "package p\nfunc LoadUint32(address int64) uint32\nfunc F() uint32 { return LoadUint32(0) }", "must have signature"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CompileSource(test.name+".go", []byte(test.source))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestUint32AndInt32Differential(t *testing.T) {
	t.Parallel()
	unsigned := compileExecutable(t, `package main
func F(a uint32, b uint32) uint32 {
	if b == uint32(0) { b = uint32(1) }
	return ((a + b) * uint32(17) - a) / b % uint32(97)
}`, "F")
	signed := compileExecutable(t, `package main
func F(a int32, b int32) int32 {
	if b == int32(0) { b = int32(1) }
	return (a / b) + (a % b)
}`, "F")
	unsignedVM, err := vm.New(unsigned.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	signedVM, err := vm.New(signed.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	inputs := [][2]uint32{{0, 0}, {math.MaxUint32, 1}, {1 << 31, 3}, {17, 97}}
	generator := rand.New(rand.NewPCG(7, 11))
	for range 500 {
		inputs = append(inputs, [2]uint32{generator.Uint32(), generator.Uint32()})
	}
	for _, input := range inputs {
		a, b := input[0], input[1]
		unsignedDivisor := b
		if unsignedDivisor == 0 {
			unsignedDivisor = 1
		}
		wantUnsigned := ((a+unsignedDivisor)*17 - a) / unsignedDivisor % 97
		gotUnsigned, runErr := unsignedVM.Run(uint64(a), uint64(b))
		if runErr != nil || uint32(gotUnsigned) != wantUnsigned {
			t.Fatalf("unsigned F(%d,%d) = %#x, %v; want %d", a, b, gotUnsigned, runErr, wantUnsigned)
		}

		signedA, signedB := int32(a), int32(b)
		if signedB == 0 {
			signedB = 1
		}
		wantSigned := signedA/signedB + signedA%signedB
		gotSigned, runErr := signedVM.Run(uint64(int64(int32(a))), uint64(int64(int32(b))))
		if runErr != nil || int32(gotSigned) != wantSigned {
			t.Fatalf("signed F(%d,%d) = %d, %v; want %d", int32(a), int32(b), int32(gotSigned), runErr, wantSigned)
		}
	}
}

func TestInt32ConversionsUseGoTruncationAndExtension(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func U(v uint64) uint64 { return uint64(uint32(v)) }
func S(v uint64) int64 { return int64(int32(v)) }
`, "U")
	if got := runExecutable(t, executable, 0xffffffff80000001); got != 0x80000001 {
		t.Fatalf("uint32 conversion = %#x", got)
	}
	signedExecutable := compileExecutable(t, `package main
func S(v uint64) int64 { return int64(int32(v)) }
`, "S")
	if got := runExecutable(t, signedExecutable, 0x80000001); int64(got) != -2147483647 {
		t.Fatalf("int32 sign extension = %d", int64(got))
	}
}

func TestFixedArraysDynamicIndexCompositeAndBounds(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func Sum(extra uint32) uint32 {
	a := [4]uint32{1, 2, 3: 4}
	a[2] += extra
	var sum uint32
	for i := uint32(0); i < uint32(4); i++ { sum += a[i] }
	return sum
}
`, "Sum")
	if got := runExecutable(t, executable, 10); uint32(got) != 17 {
		t.Fatalf("array sum = %d, want 17", uint32(got))
	}

	get := compileExecutable(t, `package main
func Get(i int32) uint64 { var a [2]uint64; a[0] = 7; a[1] = 9; return a[i] }
`, "Get")
	machine, err := vm.New(get.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	if got, runErr := machine.Run(1); runErr != nil || got != 9 {
		t.Fatalf("Get(1) = %d, %v", got, runErr)
	}
	for _, invalid := range []uint64{2, ^uint64(0)} {
		if _, runErr := machine.Run(invalid); !errors.Is(runErr, vm.ErrDivisionByZero) {
			t.Fatalf("Get(%#x) error = %v, want bounds trap", invalid, runErr)
		}
	}
}

func TestFixedArrayValueCopyIsIndependent(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func F() uint32 {
	a := [3]uint32{1, 2, 3}
	b := a
	b[1] = uint32(9)
	a = b
	b[1] = uint32(4)
	a = [3]uint32{a[2], a[1], a[0]}
	return a[1]
}
`, "F")
	if got := runExecutable(t, executable); uint32(got) != 9 {
		t.Fatalf("copied array element = %d, want 9", uint32(got))
	}
}

func TestInt32ArrayLoadStorePreservesSignedValue(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func F(v int32) int32 { a := [2]int32{-1, -2147483648}; a[0] = v; return a[0] + a[1] }
`, "F")
	if got := runExecutable(t, executable, uint64(int64(7))); int32(got) != -2147483641 {
		t.Fatalf("signed array result = %d", int32(got))
	}
}

func TestPointersUseSBPFVirtualAddresses(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func Add(p *uint32, amount uint32) uint32 { *p += amount; return *p }
`, "Add")
	machine, err := vm.New(executable.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	memory := make([]byte, 4)
	binary.LittleEndian.PutUint32(memory, 10)
	region := vm.WritableRegion(sbpf.MMInputStart+32, memory)
	result, err := machine.RunWithMemory([]vm.MemoryRegion{region}, sbpf.MMInputStart+32, 7)
	if err != nil || uint32(result) != 17 || binary.LittleEndian.Uint32(memory) != 17 {
		t.Fatalf("pointer update result=%d memory=%d err=%v", uint32(result), binary.LittleEndian.Uint32(memory), err)
	}
	_, err = machine.RunWithMemory([]vm.MemoryRegion{vm.ReadOnlyRegion(sbpf.MMInputStart+32, memory)}, sbpf.MMInputStart+32, 1)
	if !errors.Is(err, vm.ErrReadOnlyMemory) {
		t.Fatalf("read-only pointer store error = %v", err)
	}
}

func TestPointerNilComparison(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func IsNil(p *uint32) bool { return p == nil }
`, "IsNil")
	if got := runExecutable(t, executable, 0); got != 1 {
		t.Fatalf("IsNil(nil) = %d", got)
	}
	if got := runExecutable(t, executable, sbpf.MMInputStart); got != 0 {
		t.Fatalf("IsNil(input) = %d", got)
	}
}

func TestExplicitGuestMemoryIntrinsics(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func LoadUint8(address uint64) uint32
func LoadUint64(address uint64) uint64
func StoreUint32(address uint64, value uint32)
func Program(inputAddress uint64, instructionDataAddress uint64) uint64 {
	opcode := LoadUint8(instructionDataAddress)
	value := LoadUint64(inputAddress + uint64(8))
	StoreUint32(inputAddress + uint64(24), uint32(value) + opcode)
	return uint64(opcode)
}
`, "Program")
	machine, err := vm.New(executable.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	memory := make([]byte, 32)
	memory[3] = 9
	binary.LittleEndian.PutUint64(memory[8:], 40)
	result, err := machine.RunWithMemory(
		[]vm.MemoryRegion{vm.WritableRegion(sbpf.MMInputStart, memory)},
		sbpf.MMInputStart, sbpf.MMInputStart+3,
	)
	if err != nil || result != 9 || binary.LittleEndian.Uint32(memory[24:]) != 49 {
		t.Fatalf("intrinsic result=%d stored=%d err=%v", result, binary.LittleEndian.Uint32(memory[24:]), err)
	}
}

func TestAddressTakenLocalAndPointerCallStayInSBPFStack(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func Bump(p *uint32) { *p += uint32(1) }
func F(v uint32) uint32 {
	x := v
	p := &x
	Bump(p)
	*p += uint32(2)
	return x
}
`, "F")
	if got := runExecutable(t, executable, 39); uint32(got) != 42 {
		t.Fatalf("F(39) = %d, want 42", uint32(got))
	}
}

func FuzzCompiledUint32MatchesGo(f *testing.F) {
	executable, err := Compile("fuzz.go", []byte(`package main
func F(a uint32, b uint32) uint32 { if b == uint32(0) { b = uint32(1) }; return (a * uint32(31) + b) % b }
`), "F")
	if err != nil {
		f.Fatal(err)
	}
	machine, err := vm.New(executable.Instructions)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(uint32(0), uint32(0))
	f.Add(uint32(math.MaxUint32), uint32(17))
	f.Fuzz(func(t *testing.T, a, b uint32) {
		divisor := b
		if divisor == 0 {
			divisor = 1
		}
		want := (a*31 + divisor) % divisor
		got, runErr := machine.Run(uint64(a), uint64(b))
		if runErr != nil || uint32(got) != want {
			t.Fatalf("F(%d,%d) = %d, %v; want %d", a, b, uint32(got), runErr, want)
		}
	})
}

func FuzzCompiledInt32DivisionRemainderMatchesGo(f *testing.F) {
	executable, err := Compile("fuzz.go", []byte(`package main
func F(a int32, b int32) int32 { if b == int32(0) { b = int32(1) }; return a / b + a % b }
`), "F")
	if err != nil {
		f.Fatal(err)
	}
	machine, err := vm.New(executable.Instructions)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(int32(math.MinInt32), int32(-1))
	f.Add(int32(-21), int32(4))
	f.Fuzz(func(t *testing.T, a, b int32) {
		divisor := b
		if divisor == 0 {
			divisor = 1
		}
		want := a/divisor + a%divisor
		got, runErr := machine.Run(uint64(int64(a)), uint64(int64(b)))
		if runErr != nil || int32(got) != want {
			t.Fatalf("F(%d,%d) = %d, %v; want %d", a, b, int32(got), runErr, want)
		}
	})
}
