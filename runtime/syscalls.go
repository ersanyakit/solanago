package runtime

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	AgaveABIPin     = "12b5c7e4df705927b2f7f579f3aa606aa4bde1c0"
	SolanaSDKABIPin = "7437469d1ab5bddbf665f3a1a69aefb422c33e36"
)

var (
	ErrUnknownSyscall   = errors.New("runtime: unknown syscall")
	ErrSyscallCollision = errors.New("runtime: syscall hash collision")
)

// SBPFVersion identifies the instruction-set generation relevant to syscall
// linkage. v3 uses static hash-address calls; earlier generations resolve the
// same Murmur3 symbol ids dynamically.
type SBPFVersion uint8

const (
	SBPFV0 SBPFVersion = iota
	SBPFV1
	SBPFV2
	SBPFV3
)

// SyscallFeature is an Agave runtime feature gate, not a promise that this Go
// package implements the syscall body.
type SyscallFeature string

const (
	FeatureBlake3                SyscallFeature = "blake3_syscall_enabled"
	FeatureSHA512                SyscallFeature = "enable_sha512_syscall"
	FeatureCurve25519            SyscallFeature = "curve25519_syscall_enabled"
	FeatureBLS12381              SyscallFeature = "enable_bls12_381_syscall"
	FeatureDisableFeesSysvar     SyscallFeature = "disable_fees_sysvar"
	FeatureLastRestartSlot       SyscallFeature = "last_restart_slot_sysvar"
	FeaturePoseidon              SyscallFeature = "enable_poseidon_syscall"
	FeatureRemainingComputeUnits SyscallFeature = "remaining_compute_units_syscall_enabled"
	FeatureGetSysvar             SyscallFeature = "get_sysvar_syscall_enabled"
	FeatureGetEpochStake         SyscallFeature = "enable_get_epoch_stake_syscall"
	FeatureAltBN128              SyscallFeature = "enable_alt_bn128_syscall"
	FeatureBigModExp             SyscallFeature = "enable_big_mod_exp_syscall"
	FeatureAltBN128Compression   SyscallFeature = "enable_alt_bn128_compression_syscall"
	FeatureLegacyAllocFree       SyscallFeature = "legacy_alloc_free"
)

// AgaveFeatureSet explicitly selects the feature-gated entries. There is no
// implicit cluster default because activation is bank/slot dependent.
type AgaveFeatureSet struct {
	Blake3                  bool
	SHA512                  bool
	Curve25519              bool
	BLS12381                bool
	DisableFeesSysvar       bool
	LastRestartSlot         bool
	Poseidon                bool
	RemainingComputeUnits   bool
	GetSysvar               bool
	GetEpochStake           bool
	AltBN128                bool
	BigModExp               bool
	AltBN128Compression     bool
	LegacyAllocFree         bool
	DisableSBPFV0Execution  bool
	ReenableSBPFV0Execution bool
}

func (f AgaveFeatureSet) enabled(feature SyscallFeature) bool {
	switch feature {
	case "":
		return true
	case FeatureBlake3:
		return f.Blake3
	case FeatureSHA512:
		return f.SHA512
	case FeatureCurve25519:
		return f.Curve25519
	case FeatureBLS12381:
		return f.BLS12381
	case FeatureDisableFeesSysvar:
		return f.DisableFeesSysvar
	case FeatureLastRestartSlot:
		return f.LastRestartSlot
	case FeaturePoseidon:
		return f.Poseidon
	case FeatureRemainingComputeUnits:
		return f.RemainingComputeUnits
	case FeatureGetSysvar:
		return f.GetSysvar
	case FeatureGetEpochStake:
		return f.GetEpochStake
	case FeatureAltBN128:
		return f.AltBN128
	case FeatureBigModExp:
		return f.BigModExp
	case FeatureAltBN128Compression:
		return f.AltBN128Compression
	case FeatureLegacyAllocFree:
		return f.LegacyAllocFree
	default:
		return false
	}
}

// SyscallDescriptor is a versioned registry entry. ID is the Murmur3-x86-32
// hash used by solana-sbpf and the generated v3 C headers.
type SyscallDescriptor struct {
	Name       string
	ID         uint32
	Feature    SyscallFeature
	DisabledBy SyscallFeature
	MinVersion SBPFVersion
	MaxVersion SBPFVersion
}

// SyscallRegistry is an immutable lookup table for one pinned Agave source and
// explicit feature set. It describes linkage only; syscall execution requires
// separately registered handlers or a real SVM adapter.
type SyscallRegistry struct {
	AgaveCommit string
	features    AgaveFeatureSet
	byName      map[string]SyscallDescriptor
	byID        map[uint32]SyscallDescriptor
}

// NewAgaveSyscallRegistry builds the exact registry declared by current
// Agave's syscalls/src/lib.rs at AgaveABIPin.
func NewAgaveSyscallRegistry(features AgaveFeatureSet) (*SyscallRegistry, error) {
	registry := &SyscallRegistry{
		AgaveCommit: AgaveABIPin,
		features:    features,
		byName:      make(map[string]SyscallDescriptor),
		byID:        make(map[uint32]SyscallDescriptor),
	}
	minVersion := SBPFV0
	if features.DisableSBPFV0Execution && !features.ReenableSBPFV0Execution {
		minVersion = SBPFV3
	}
	for _, declaration := range agaveSyscalls {
		if !features.enabled(declaration.feature) || (declaration.disabledBy != "" && features.enabled(declaration.disabledBy)) {
			continue
		}
		descriptor := SyscallDescriptor{
			Name: declaration.name, ID: HashSyscallName(declaration.name), Feature: declaration.feature,
			DisabledBy: declaration.disabledBy, MinVersion: minVersion, MaxVersion: SBPFV3,
		}
		if previous, exists := registry.byID[descriptor.ID]; exists {
			return nil, fmt.Errorf("%w: %q and %q -> %#x", ErrSyscallCollision, previous.Name, descriptor.Name, descriptor.ID)
		}
		registry.byName[descriptor.Name] = descriptor
		registry.byID[descriptor.ID] = descriptor
	}
	return registry, nil
}

// LookupName returns a feature-enabled syscall valid for version.
func (r *SyscallRegistry) LookupName(name string, version SBPFVersion) (SyscallDescriptor, error) {
	if r == nil {
		return SyscallDescriptor{}, ErrUnknownSyscall
	}
	descriptor, ok := r.byName[name]
	if !ok || version < descriptor.MinVersion || version > descriptor.MaxVersion {
		return SyscallDescriptor{}, fmt.Errorf("%w: %s for sBPF v%d", ErrUnknownSyscall, name, version)
	}
	return descriptor, nil
}

// LookupID returns a feature-enabled syscall valid for version.
func (r *SyscallRegistry) LookupID(id uint32, version SBPFVersion) (SyscallDescriptor, error) {
	if r == nil {
		return SyscallDescriptor{}, ErrUnknownSyscall
	}
	descriptor, ok := r.byID[id]
	if !ok || version < descriptor.MinVersion || version > descriptor.MaxVersion {
		return SyscallDescriptor{}, fmt.Errorf("%w: %#x for sBPF v%d", ErrUnknownSyscall, id, version)
	}
	return descriptor, nil
}

// Entries returns a deterministic declaration-order-independent snapshot.
func (r *SyscallRegistry) Entries() []SyscallDescriptor {
	if r == nil {
		return nil
	}
	entries := make([]SyscallDescriptor, 0, len(r.byName))
	for _, declaration := range agaveSyscalls {
		if descriptor, ok := r.byName[declaration.name]; ok {
			entries = append(entries, descriptor)
		}
	}
	return entries
}

// HashSyscallName implements solana-sbpf's Murmur3-x86-32 symbol hash with a
// zero seed. Official generated headers at AgaveABIPin contain cross-language
// golden ids asserted by tests in this package.
func HashSyscallName(name string) uint32 {
	data := []byte(name)
	const c1 uint32 = 0xcc9e2d51
	const c2 uint32 = 0x1b873593
	var hash uint32
	blocks := len(data) / 4
	for block := 0; block < blocks; block++ {
		value := binary.LittleEndian.Uint32(data[block*4 : block*4+4])
		value *= c1
		value = value<<15 | value>>17
		value *= c2
		hash ^= value
		hash = hash<<13 | hash>>19
		hash = hash*5 + 0xe6546b64
	}
	var tail uint32
	switch len(data) & 3 {
	case 3:
		tail ^= uint32(data[blocks*4+2]) << 16
		fallthrough
	case 2:
		tail ^= uint32(data[blocks*4+1]) << 8
		fallthrough
	case 1:
		tail ^= uint32(data[blocks*4])
		tail *= c1
		tail = tail<<15 | tail>>17
		tail *= c2
		hash ^= tail
	}
	hash ^= uint32(len(data))
	hash ^= hash >> 16
	hash *= 0x85ebca6b
	hash ^= hash >> 13
	hash *= 0xc2b2ae35
	hash ^= hash >> 16
	return hash
}

type syscallDeclaration struct {
	name       string
	feature    SyscallFeature
	disabledBy SyscallFeature
}

var agaveSyscalls = []syscallDeclaration{
	{name: "abort"},
	{name: "sol_panic_"},
	{name: "sol_log_"},
	{name: "sol_log_64_"},
	{name: "sol_log_pubkey"},
	{name: "sol_log_compute_units_"},
	{name: "sol_create_program_address"},
	{name: "sol_try_find_program_address"},
	{name: "sol_sha256"},
	{name: "sol_keccak256"},
	{name: "sol_secp256k1_recover"},
	{name: "sol_blake3", feature: FeatureBlake3},
	{name: "sol_sha512", feature: FeatureSHA512},
	{name: "sol_curve_validate_point", feature: FeatureCurve25519},
	{name: "sol_curve_group_op", feature: FeatureCurve25519},
	{name: "sol_curve_multiscalar_mul", feature: FeatureCurve25519},
	{name: "sol_curve_decompress", feature: FeatureBLS12381},
	{name: "sol_curve_pairing_map", feature: FeatureBLS12381},
	{name: "sol_get_clock_sysvar"},
	{name: "sol_get_epoch_schedule_sysvar"},
	{name: "sol_get_fees_sysvar", disabledBy: FeatureDisableFeesSysvar},
	{name: "sol_get_rent_sysvar"},
	{name: "sol_get_last_restart_slot", feature: FeatureLastRestartSlot},
	{name: "sol_get_epoch_rewards_sysvar"},
	{name: "sol_memcpy_"},
	{name: "sol_memmove_"},
	{name: "sol_memset_"},
	{name: "sol_memcmp_"},
	{name: "sol_get_processed_sibling_instruction"},
	{name: "sol_get_stack_height"},
	{name: "sol_set_return_data"},
	{name: "sol_get_return_data"},
	{name: "sol_invoke_signed_c"},
	{name: "sol_invoke_signed_rust"},
	{name: "sol_alloc_free_", feature: FeatureLegacyAllocFree},
	{name: "sol_alt_bn128_group_op", feature: FeatureAltBN128},
	{name: "sol_big_mod_exp", feature: FeatureBigModExp},
	{name: "sol_poseidon", feature: FeaturePoseidon},
	{name: "sol_remaining_compute_units", feature: FeatureRemainingComputeUnits},
	{name: "sol_alt_bn128_compression", feature: FeatureAltBN128Compression},
	{name: "sol_get_sysvar", feature: FeatureGetSysvar},
	{name: "sol_get_epoch_stake", feature: FeatureGetEpochStake},
	{name: "sol_log_data"},
}
