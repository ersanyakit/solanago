package compiler

import (
	"bytes"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/ersany/go-solana/sbpf"
	"github.com/ersany/go-solana/vm"
)

func compileExecutable(t *testing.T, source, entry string) *Executable {
	t.Helper()
	executable, err := Compile("test.go", []byte(source), entry)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return executable
}

func runExecutable(t *testing.T, executable *Executable, arguments ...uint64) uint64 {
	t.Helper()
	machine, err := vm.New(executable.Instructions)
	if err != nil {
		t.Fatalf("vm.New: %v", err)
	}
	result, err := machine.Run(arguments...)
	if err != nil {
		t.Fatalf("VM Run: %v", err)
	}
	return result
}

func TestGenerateAddOfficialGolden(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func Add(a uint64, b uint64) uint64 { return a + b }
`, "Add")
	wantInstructions := []sbpf.Instruction{
		sbpf.ALUReg(sbpf.MOV64_REG, sbpf.R0, sbpf.R1),
		sbpf.ALUReg(sbpf.ADD64_REG, sbpf.R0, sbpf.R2),
		sbpf.Return(),
	}
	if !reflect.DeepEqual(executable.Instructions, wantInstructions) {
		t.Fatalf("instructions\n got: %#v\nwant: %#v", executable.Instructions, wantInstructions)
	}
	wantBytes := []byte{
		0xbf, 0x10, 0, 0, 0, 0, 0, 0,
		0x0f, 0x20, 0, 0, 0, 0, 0, 0,
		0x95, 0x00, 0, 0, 0, 0, 0, 0,
	}
	if !bytes.Equal(executable.Bytecode, wantBytes) {
		t.Fatalf("bytecode\n got: % x\nwant: % x", executable.Bytecode, wantBytes)
	}
	if got := runExecutable(t, executable, 10, 20); got != 30 {
		t.Fatalf("Add(10, 20) = %d, want 30", got)
	}
}

func TestBackendControlFlowCallsAndSpills(t *testing.T) {
	t.Parallel()
	source := `package main
func Step(v uint64) uint64 { return v + 1 }
func Sum(n uint64) uint64 {
	var total uint64 = 0
	var i uint64 = 0
	for i < n {
		if i != 3 { total = total + Step(i) }
		i = i + 1
	}
	return total
}`
	executable := compileExecutable(t, source, "Sum")
	if got := runExecutable(t, executable, 10); got != 51 {
		t.Fatalf("Sum(10) = %d, want 51", got)
	}
	if _, included := executable.Functions["Step"]; !included {
		t.Fatal("reachable Step function was not emitted")
	}
}

func TestBackendEliminatesUnreachableFunctions(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func Add(a uint64, b uint64) uint64 { return a + b }
func Unused() uint64 { return 99 }
`, "Add")
	if _, included := executable.Functions["Unused"]; included {
		t.Fatal("unreachable function was emitted")
	}
	if got := len(executable.Bytecode); got != 3*sbpf.InstructionSize {
		t.Fatalf("bytecode length = %d, want 24", got)
	}
}

func TestBackendDifferentialUint64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		goFn   func(uint64, uint64) uint64
	}{
		{"add", "return a + b", func(a, b uint64) uint64 { return a + b }},
		{"sub", "return a - b", func(a, b uint64) uint64 { return a - b }},
		{"mul", "return a * b", func(a, b uint64) uint64 { return a * b }},
		{"div", "return a / b", func(a, b uint64) uint64 { return a / b }},
		{"branch", "if a >= b { return a - b }; return b - a", func(a, b uint64) uint64 {
			if a >= b {
				return a - b
			}
			return b - a
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			executable := compileExecutable(t, "package main\nfunc F(a uint64, b uint64) uint64 { "+test.source+" }\n", "F")
			machine, err := vm.New(executable.Instructions)
			if err != nil {
				t.Fatal(err)
			}
			inputs := [][2]uint64{{0, 1}, {1, 1}, {10, 20}, {math.MaxUint64, 1}, {1 << 63, 3}}
			generator := rand.New(rand.NewPCG(1, 2))
			for range 250 {
				inputs = append(inputs, [2]uint64{generator.Uint64(), generator.Uint64()})
			}
			for _, input := range inputs {
				a, b := input[0], input[1]
				if test.name == "div" && b == 0 {
					b = 1
				}
				got, runErr := machine.Run(a, b)
				if runErr != nil {
					t.Fatalf("F(%d,%d): %v", a, b, runErr)
				}
				if want := test.goFn(a, b); got != want {
					t.Fatalf("F(%d,%d) = %d, native Go = %d", a, b, got, want)
				}
			}
		})
	}
}

func TestBackendDifferentialInt64Division(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package main
func Divide(a int64, b int64) int64 { return a / b }
`, "Divide")
	machine, err := vm.New(executable.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	tests := [][2]int64{{-21, 4}, {21, -4}, {-21, -4}, {21, 4}, {math.MinInt64, -1}, {math.MinInt64, 1}}
	for _, test := range tests {
		got, runErr := machine.Run(uint64(test[0]), uint64(test[1]))
		if runErr != nil {
			t.Fatalf("Divide(%d,%d): %v", test[0], test[1], runErr)
		}
		if want := test[0] / test[1]; int64(got) != want {
			t.Fatalf("Divide(%d,%d) = %d, native Go = %d", test[0], test[1], int64(got), want)
		}
	}
	_, err = machine.Run(10, 0)
	if !errors.Is(err, vm.ErrDivisionByZero) {
		t.Fatalf("division by zero error = %v", err)
	}
}

func FuzzCompiledAddMatchesGo(f *testing.F) {
	executable, err := Compile("fuzz.go", []byte(`package main
func Add(a uint64, b uint64) uint64 { return a + b }
`), "Add")
	if err != nil {
		f.Fatal(err)
	}
	machine, err := vm.New(executable.Instructions)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(uint64(10), uint64(20))
	f.Add(^uint64(0), uint64(1))
	f.Fuzz(func(t *testing.T, a, b uint64) {
		got, runErr := machine.Run(a, b)
		if runErr != nil {
			t.Fatal(runErr)
		}
		if want := a + b; got != want {
			t.Fatalf("Add(%d,%d) = %d, native Go = %d", a, b, got, want)
		}
	})
}
