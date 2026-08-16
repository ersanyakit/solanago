package runtime

import (
	"errors"
	"testing"
)

func TestOfficialStaticSyscallHashes(t *testing.T) {
	// Cross-language goldens from Agave's generated sBPFv3 C headers at
	// commit 12b5c7e4df705927b2f7f579f3aa606aa4bde1c0.
	want := map[string]uint32{
		"sol_invoke_signed_c":          2720767109,
		"sol_create_program_address":   2474062396,
		"sol_try_find_program_address": 1213221432,
		"sol_log_pubkey":               2129692874,
	}
	for name, id := range want {
		if got := HashSyscallName(name); got != id {
			t.Fatalf("%s hash: got %d want %d", name, got, id)
		}
	}
}

func TestVersionedFeatureGatedSyscallRegistry(t *testing.T) {
	registry, err := NewAgaveSyscallRegistry(AgaveFeatureSet{})
	if err != nil {
		t.Fatal(err)
	}
	if registry.AgaveCommit != AgaveABIPin {
		t.Fatalf("wrong source pin %q", registry.AgaveCommit)
	}
	invoke, err := registry.LookupName("sol_invoke_signed_c", SBPFV3)
	if err != nil || invoke.ID != 2720767109 {
		t.Fatalf("invoke lookup: %#v %v", invoke, err)
	}
	if byID, err := registry.LookupID(invoke.ID, SBPFV0); err != nil || byID.Name != invoke.Name {
		t.Fatalf("dynamic lookup: %#v %v", byID, err)
	}
	if _, err := registry.LookupName("sol_blake3", SBPFV3); !errors.Is(err, ErrUnknownSyscall) {
		t.Fatalf("disabled syscall visible: %v", err)
	}
	if _, err := registry.LookupName("sol_get_fees_sysvar", SBPFV2); err != nil {
		t.Fatalf("fees syscall should remain enabled until disable_fees_sysvar: %v", err)
	}

	registry, err = NewAgaveSyscallRegistry(AgaveFeatureSet{Blake3: true, DisableFeesSysvar: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LookupName("sol_blake3", SBPFV3); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LookupName("sol_get_fees_sysvar", SBPFV2); !errors.Is(err, ErrUnknownSyscall) {
		t.Fatalf("disabled fees syscall visible: %v", err)
	}
	if len(registry.Entries()) == 0 {
		t.Fatal("empty registry")
	}

	registry, err = NewAgaveSyscallRegistry(AgaveFeatureSet{DisableSBPFV0Execution: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LookupName("sol_log_", SBPFV2); !errors.Is(err, ErrUnknownSyscall) {
		t.Fatalf("disabled pre-v3 linkage visible: %v", err)
	}
	if _, err := registry.LookupName("sol_log_", SBPFV3); err != nil {
		t.Fatal(err)
	}
	registry, err = NewAgaveSyscallRegistry(AgaveFeatureSet{DisableSBPFV0Execution: true, ReenableSBPFV0Execution: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LookupName("sol_log_", SBPFV0); err != nil {
		t.Fatalf("reenabled v0 linkage missing: %v", err)
	}
}
