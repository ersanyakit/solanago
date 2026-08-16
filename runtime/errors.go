package runtime

import "fmt"

// ProgramErrorKind identifies Solana's built-in program error variants.
// Numeric values are shifted into the upper 32 bits at the ABI boundary.
//
// Source pin: anza-xyz/solana-sdk program-error/src/lib.rs at
// 7437469d1ab5bddbf665f3a1a69aefb422c33e36 (2026-08-16 snapshot).
type ProgramErrorKind uint32

const (
	ProgramErrorCustomZero ProgramErrorKind = iota + 1
	ProgramErrorInvalidArgument
	ProgramErrorInvalidInstructionData
	ProgramErrorInvalidAccountData
	ProgramErrorAccountDataTooSmall
	ProgramErrorInsufficientFunds
	ProgramErrorIncorrectProgramID
	ProgramErrorMissingRequiredSignature
	ProgramErrorAccountAlreadyInitialized
	ProgramErrorUninitializedAccount
	ProgramErrorNotEnoughAccountKeys
	ProgramErrorAccountBorrowFailed
	ProgramErrorMaxSeedLengthExceeded
	ProgramErrorInvalidSeeds
	ProgramErrorBorshIO
	ProgramErrorAccountNotRentExempt
	ProgramErrorUnsupportedSysvar
	ProgramErrorIllegalOwner
	ProgramErrorMaxAccountsDataAllocationsExceeded
	ProgramErrorInvalidRealloc
	ProgramErrorMaxInstructionTraceLengthExceeded
	ProgramErrorBuiltinProgramsMustConsumeComputeUnits
	ProgramErrorInvalidAccountOwner
	ProgramErrorArithmeticOverflow
	ProgramErrorImmutable
	ProgramErrorIncorrectAuthority
)

const (
	programErrorBuiltinShift = 32
	ProgramSuccess           = uint64(0)
)

// ProgramError is a typed built-in or custom program failure. CustomCode is
// used only when Custom is true. Custom(0) maps to the dedicated CUSTOM_ZERO
// built-in value exactly as the Rust SDK does.
type ProgramError struct {
	Kind       ProgramErrorKind
	CustomCode uint32
	Custom     bool
}

// CustomProgramError constructs a stable program-defined error.
func CustomProgramError(code uint32) ProgramError {
	return ProgramError{CustomCode: code, Custom: true}
}

// BuiltinProgramError constructs a Solana built-in program error.
func BuiltinProgramError(kind ProgramErrorKind) ProgramError {
	return ProgramError{Kind: kind}
}

func (e ProgramError) Error() string {
	if e.Custom {
		return fmt.Sprintf("custom program error: %#x", e.CustomCode)
	}
	if name, ok := programErrorNames[e.Kind]; ok {
		return name
	}
	return fmt.Sprintf("unknown program error kind %d", e.Kind)
}

// ReturnCode maps an error to the u64 returned by an SBF entrypoint.
func (e ProgramError) ReturnCode() uint64 {
	if e.Custom {
		if e.CustomCode == 0 {
			return uint64(ProgramErrorCustomZero) << programErrorBuiltinShift
		}
		return uint64(e.CustomCode)
	}
	return uint64(e.Kind) << programErrorBuiltinShift
}

// ProgramErrorFromReturnCode performs the Solana SDK's inverse mapping. A
// non-built-in value becomes a custom error using its low 32 bits.
func ProgramErrorFromReturnCode(code uint64) (ProgramError, bool) {
	if code == ProgramSuccess {
		return ProgramError{}, false
	}
	if code == uint64(ProgramErrorCustomZero)<<programErrorBuiltinShift {
		return CustomProgramError(0), true
	}
	if code&0xffffffff == 0 {
		kind := ProgramErrorKind(code >> programErrorBuiltinShift)
		if _, ok := programErrorNames[kind]; ok && kind != ProgramErrorCustomZero {
			return BuiltinProgramError(kind), true
		}
	}
	return CustomProgramError(uint32(code)), true
}

var programErrorNames = map[ProgramErrorKind]string{
	ProgramErrorCustomZero:                             "custom program error zero",
	ProgramErrorInvalidArgument:                        "invalid argument",
	ProgramErrorInvalidInstructionData:                 "invalid instruction data",
	ProgramErrorInvalidAccountData:                     "invalid account data",
	ProgramErrorAccountDataTooSmall:                    "account data too small",
	ProgramErrorInsufficientFunds:                      "insufficient funds",
	ProgramErrorIncorrectProgramID:                     "incorrect program id",
	ProgramErrorMissingRequiredSignature:               "missing required signature",
	ProgramErrorAccountAlreadyInitialized:              "account already initialized",
	ProgramErrorUninitializedAccount:                   "uninitialized account",
	ProgramErrorNotEnoughAccountKeys:                   "not enough account keys",
	ProgramErrorAccountBorrowFailed:                    "account borrow failed",
	ProgramErrorMaxSeedLengthExceeded:                  "maximum seed length exceeded",
	ProgramErrorInvalidSeeds:                           "invalid seeds",
	ProgramErrorBorshIO:                                "borsh io error",
	ProgramErrorAccountNotRentExempt:                   "account not rent exempt",
	ProgramErrorUnsupportedSysvar:                      "unsupported sysvar",
	ProgramErrorIllegalOwner:                           "illegal owner",
	ProgramErrorMaxAccountsDataAllocationsExceeded:     "maximum accounts data allocations exceeded",
	ProgramErrorInvalidRealloc:                         "invalid account data reallocation",
	ProgramErrorMaxInstructionTraceLengthExceeded:      "maximum instruction trace length exceeded",
	ProgramErrorBuiltinProgramsMustConsumeComputeUnits: "builtin programs must consume compute units",
	ProgramErrorInvalidAccountOwner:                    "invalid account owner",
	ProgramErrorArithmeticOverflow:                     "arithmetic overflow",
	ProgramErrorImmutable:                              "immutable account",
	ProgramErrorIncorrectAuthority:                     "incorrect authority",
}

// Processor is the stable Go-side program entrypoint signature.
type Processor func(*Context) error

// Entrypoint is the generated-wrapper-friendly host signature. The compiler's
// ELF wrapper can delegate its input memory to the same parsing/status logic.
type Entrypoint func([]byte) uint64

// NewEntrypoint binds a processor to canonical ABIv1 parsing.
func NewEntrypoint(processor Processor) Entrypoint {
	return NewEntrypointWithOptions(processor, ParseOptions{})
}

// NewEntrypointWithOptions binds explicit ABIv1 feature gates, including the
// SIMD-0449 direct account-pointer appendix.
func NewEntrypointWithOptions(processor Processor, options ParseOptions) Entrypoint {
	return func(input []byte) uint64 {
		return ExecuteEntrypointWithOptions(input, processor, options)
	}
}

// ExecuteEntrypoint parses current aligned ABIv1 input, calls processor, and
// returns the exact Solana u64 status representation. Unknown Go errors are
// mapped fail-closed to InvalidArgument; panics are intentionally not caught.
func ExecuteEntrypoint(input []byte, processor Processor) uint64 {
	return ExecuteEntrypointWithOptions(input, processor, ParseOptions{})
}

// ExecuteEntrypointWithOptions is ExecuteEntrypoint with explicit ABI gates.
func ExecuteEntrypointWithOptions(input []byte, processor Processor, options ParseOptions) uint64 {
	if processor == nil {
		return BuiltinProgramError(ProgramErrorInvalidArgument).ReturnCode()
	}
	context, err := ParseInputV1WithOptions(input, options)
	if err != nil {
		return BuiltinProgramError(ProgramErrorInvalidArgument).ReturnCode()
	}
	snapshots := snapshotAccounts(context.accountSlots)
	if err := processor(context); err != nil {
		restoreAccountSnapshots(snapshots)
		switch typed := err.(type) {
		case ProgramError:
			return typed.ReturnCode()
		case *ProgramError:
			if typed != nil {
				return typed.ReturnCode()
			}
		}
		return BuiltinProgramError(ProgramErrorInvalidArgument).ReturnCode()
	}
	if err := context.ValidateProgramChanges(); err != nil {
		restoreAccountSnapshots(snapshots)
		if typed, ok := err.(ProgramError); ok {
			return typed.ReturnCode()
		}
		return BuiltinProgramError(ProgramErrorInvalidArgument).ReturnCode()
	}
	return ProgramSuccess
}
