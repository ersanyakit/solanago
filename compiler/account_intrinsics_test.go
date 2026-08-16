package compiler

import (
	"strings"
	"testing"

	"github.com/ersanyakit/solanago/sbpf"
	"github.com/ersanyakit/solanago/vm"
)

const allAccountFieldDeclarations = `
func AccountIsSigner(record uint64) bool
func AccountIsWritable(record uint64) bool
func AccountIsExecutable(record uint64) bool
func AccountKeyAddress(record uint64) uint64
func AccountOwnerAddress(record uint64) uint64
func AccountLamportsAddress(record uint64) uint64
func AccountDataLen(record uint64) uint64
func AccountDataAddress(record uint64) uint64
`

// TestAccountFieldIntrinsicsLowerToConstAddAndLoad asserts each intrinsic
// lowers to exactly the same const+add(+load) instruction shape a
// hand-written LoadX(record+uint64(N)) expression already produces — these
// are sugar over that pattern, not a new execution mechanism.
func TestAccountFieldIntrinsicsLowerToConstAddAndLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		call       string
		resultType string
		wantOffset uint64
		wantLoad   bool
		wantMemory MemoryType
	}{
		{"AccountIsSigner", "AccountIsSigner(r)", "bool", 1, true, MemoryBool},
		{"AccountIsWritable", "AccountIsWritable(r)", "bool", 2, true, MemoryBool},
		{"AccountIsExecutable", "AccountIsExecutable(r)", "bool", 3, true, MemoryBool},
		{"AccountKeyAddress", "AccountKeyAddress(r)", "uint64", 8, false, MemoryInvalid},
		{"AccountOwnerAddress", "AccountOwnerAddress(r)", "uint64", 40, false, MemoryInvalid},
		{"AccountLamportsAddress", "AccountLamportsAddress(r)", "uint64", 72, false, MemoryInvalid},
		{"AccountDataLen", "AccountDataLen(r)", "uint64", 80, true, MemoryUint64},
		{"AccountDataAddress", "AccountDataAddress(r)", "uint64", 88, false, MemoryInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := "package p\n" + allAccountFieldDeclarations + `
func Program(r uint64) ` + test.resultType + ` {
	return ` + test.call + `
}
`
			program, err := CompileSource(test.name+".go", []byte(source))
			if err != nil {
				t.Fatalf("CompileSource: %v", err)
			}
			function, ok := program.Function("Program")
			if !ok {
				t.Fatal("Program function missing")
			}
			var instructions []Instruction
			for _, block := range function.Blocks {
				instructions = append(instructions, block.Instructions...)
			}
			// Expect exactly: const offset, add record+offset, [load].
			var constIndex, addIndex = -1, -1
			for index, instruction := range instructions {
				if instruction.Op == OpConst && instruction.Imm == test.wantOffset {
					constIndex = index
				}
				if instruction.Op == OpAdd && constIndex >= 0 && instruction.Y == instructions[constIndex].Dest {
					addIndex = index
					break
				}
			}
			if constIndex < 0 || addIndex < 0 {
				t.Fatalf("%s did not lower to const(%d)+add: %+v", test.name, test.wantOffset, instructions)
			}
			if test.wantLoad {
				found := false
				for _, instruction := range instructions {
					if instruction.Op == OpLoad && instruction.X == instructions[addIndex].Dest && instruction.Memory == test.wantMemory {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("%s did not load Memory=%v from the computed address: %+v", test.name, test.wantMemory, instructions)
				}
			} else {
				for _, instruction := range instructions {
					if instruction.Op == OpLoad && instruction.X == instructions[addIndex].Dest {
						t.Fatalf("%s (address intrinsic) unexpectedly loaded from the computed address: %+v", test.name, instructions)
					}
				}
			}
		})
	}
}

func TestAccountFieldIntrinsicsRejectSpoofAndWrongSignature(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, declaration, want string
	}{
		{
			name:        "body spoof",
			declaration: "func AccountIsSigner(record uint64) bool { return true }",
			want:        "reserved and must be a bodyless declaration",
		},
		{
			name:        "wrong parameter type",
			declaration: "func AccountIsSigner(record uint32) bool",
			want:        "must have signature func(record uint64) bool",
		},
		{
			name:        "wrong result for value intrinsic",
			declaration: "func AccountDataLen(record uint64) uint32",
			want:        "must have signature func(record uint64) uint64",
		},
		{
			name:        "wrong result for address intrinsic",
			declaration: "func AccountKeyAddress(record uint64) bool",
			want:        "must have signature func(record uint64) uint64",
		},
		{
			name:        "missing body and not an intrinsic",
			declaration: "func NotAnIntrinsic(record uint64) uint64",
			want:        "only explicit guest-memory, Solana syscall, and account-field intrinsics may omit a body",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := []byte("package p\n" + test.declaration + "\nfunc Program() {}\n")
			_, err := CompileSource(test.name+".go", source)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

// TestAccountFieldIntrinsicsMatchManualOffsetArithmetic differentially
// verifies AccountIsSigner against the exact hand-written expression it
// replaces (LoadUint8(record+uint64(1)) != uint32(0), the pattern used by
// examples/cpi and examples/gospl today) over the same VM-mapped memory.
func TestAccountFieldIntrinsicsMatchManualOffsetArithmetic(t *testing.T) {
	t.Parallel()
	executable := compileExecutable(t, `package p
func LoadUint8(address uint64) uint32
func AccountIsSigner(record uint64) bool
func Intrinsic(record uint64) uint32 {
	if AccountIsSigner(record) {
		return uint32(1)
	}
	return uint32(0)
}
func Manual(record uint64) uint32 {
	if LoadUint8(record+uint64(1)) != uint32(0) {
		return uint32(1)
	}
	return uint32(0)
}
`, "Intrinsic")
	manualExecutable := compileExecutable(t, `package p
func LoadUint8(address uint64) uint32
func Manual(record uint64) uint32 {
	if LoadUint8(record+uint64(1)) != uint32(0) {
		return uint32(1)
	}
	return uint32(0)
}
`, "Manual")

	for _, isSigner := range []byte{0, 1} {
		memoryA := make([]byte, 4)
		memoryA[1] = isSigner
		machineA, err := vm.New(executable.Instructions)
		if err != nil {
			t.Fatal(err)
		}
		gotIntrinsic, err := machineA.RunWithMemory(
			[]vm.MemoryRegion{vm.WritableRegion(sbpf.MMInputStart, memoryA)},
			sbpf.MMInputStart,
		)
		if err != nil {
			t.Fatalf("intrinsic run: %v", err)
		}

		memoryB := make([]byte, 4)
		memoryB[1] = isSigner
		machineB, err := vm.New(manualExecutable.Instructions)
		if err != nil {
			t.Fatal(err)
		}
		gotManual, err := machineB.RunWithMemory(
			[]vm.MemoryRegion{vm.WritableRegion(sbpf.MMInputStart, memoryB)},
			sbpf.MMInputStart,
		)
		if err != nil {
			t.Fatalf("manual run: %v", err)
		}
		if gotIntrinsic != gotManual {
			t.Fatalf("is_signer=%d: intrinsic=%d, manual=%d differ", isSigner, gotIntrinsic, gotManual)
		}
		wantResult := uint64(0)
		if isSigner != 0 {
			wantResult = 1
		}
		if gotIntrinsic != wantResult {
			t.Fatalf("is_signer=%d: result=%d, want %d", isSigner, gotIntrinsic, wantResult)
		}
	}
}
