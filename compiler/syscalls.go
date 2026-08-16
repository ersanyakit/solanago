package compiler

import (
	"fmt"
	"strings"

	"github.com/ersanyakit/go-solana/sbpf"
)

// SolanaSDKSyscallABIPin is the source revision used to define the bodyless
// syscall signatures below (define-syscall/src/definitions.rs).
const SolanaSDKSyscallABIPin = "7437469d1ab5bddbf665f3a1a69aefb422c33e36"

// syscallIntrinsic describes the deliberately small, source-pinned Solana
// syscall surface accepted as bodyless Go declarations. Pointer-shaped C ABI
// parameters are represented as uint64 sBPF virtual addresses, never Go or
// host pointers.
type syscallIntrinsic struct {
	Symbol     string
	ID         uint32
	Parameters [5]Type
	ParamCount uint8
	Result     Type
}

func newSyscallIntrinsic(symbol string, result Type, parameters ...Type) syscallIntrinsic {
	if len(parameters) > 5 {
		panic("compiler: syscall intrinsic exceeds the sBPF register ABI")
	}
	intrinsic := syscallIntrinsic{
		Symbol:     symbol,
		ID:         sbpf.HashSymbolName(symbol),
		ParamCount: uint8(len(parameters)),
		Result:     result,
	}
	copy(intrinsic.Parameters[:], parameters)
	return intrinsic
}

// These signatures mirror the register-level ABI declared by the pinned
// Solana SDK while making every C pointer an explicit uint64 guest address.
var syscallIntrinsics = map[string]syscallIntrinsic{
	"InvokeSignedC": newSyscallIntrinsic("sol_invoke_signed_c", TypeUint64,
		TypeUint64, TypeUint64, TypeUint64, TypeUint64, TypeUint64),
	"Log": newSyscallIntrinsic("sol_log_", TypeVoid,
		TypeUint64, TypeUint64),
	"Memcpy": newSyscallIntrinsic("sol_memcpy_", TypeVoid,
		TypeUint64, TypeUint64, TypeUint64),
	"Memmove": newSyscallIntrinsic("sol_memmove_", TypeVoid,
		TypeUint64, TypeUint64, TypeUint64),
	"Memset": newSyscallIntrinsic("sol_memset_", TypeVoid,
		TypeUint64, TypeUint8, TypeUint64),
	"Memcmp": newSyscallIntrinsic("sol_memcmp_", TypeVoid,
		TypeUint64, TypeUint64, TypeUint64, TypeUint64),
	"CreateProgramAddress": newSyscallIntrinsic("sol_create_program_address", TypeUint64,
		TypeUint64, TypeUint64, TypeUint64, TypeUint64),
	"TryFindProgramAddress": newSyscallIntrinsic("sol_try_find_program_address", TypeUint64,
		TypeUint64, TypeUint64, TypeUint64, TypeUint64, TypeUint64),
}

func init() {
	if err := validateSyscallIntrinsics(); err != nil {
		panic(err)
	}
}

func validateSyscallIntrinsics() error {
	return validateSyscallIntrinsicSet(syscallIntrinsics)
}

func validateSyscallIntrinsicSet(intrinsics map[string]syscallIntrinsic) error {
	bySymbol := make(map[string]string, len(intrinsics))
	byID := make(map[uint32]string, len(intrinsics))
	for goName, intrinsic := range intrinsics {
		if intrinsic.Symbol == "" || intrinsic.ID != sbpf.HashSymbolName(intrinsic.Symbol) {
			return fmt.Errorf("compiler: invalid Solana syscall intrinsic %s", goName)
		}
		if previous, exists := bySymbol[intrinsic.Symbol]; exists {
			return fmt.Errorf("compiler: Solana syscall symbol %s is registered by %s and %s", intrinsic.Symbol, previous, goName)
		}
		if previous, exists := byID[intrinsic.ID]; exists {
			return fmt.Errorf("compiler: Solana syscall hash collision 0x%08x between %s and %s", intrinsic.ID, previous, goName)
		}
		bySymbol[intrinsic.Symbol] = goName
		byID[intrinsic.ID] = goName
	}
	return nil
}

func lookupSyscallBySymbol(symbol string) (syscallIntrinsic, bool) {
	for _, intrinsic := range syscallIntrinsics {
		if intrinsic.Symbol == symbol {
			return intrinsic, true
		}
	}
	return syscallIntrinsic{}, false
}

func (intrinsic syscallIntrinsic) parameterTypes() []Type {
	return intrinsic.Parameters[:intrinsic.ParamCount]
}

func (intrinsic syscallIntrinsic) signature(name string) string {
	parameters := intrinsic.parameterTypes()
	parts := make([]string, len(parameters))
	for index, parameter := range parameters {
		parts[index] = parameter.String()
	}
	signature := fmt.Sprintf("func %s(%s)", name, strings.Join(parts, ", "))
	if intrinsic.Result != TypeVoid {
		signature += " " + intrinsic.Result.String()
	}
	return signature
}
