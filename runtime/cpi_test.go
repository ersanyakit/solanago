package runtime

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/ersanyakit/go-solana/sbpf"
	"github.com/ersanyakit/go-solana/sdk"
)

type cpiExecutorFunc func(CPIRequest) error

func (f cpiExecutorFunc) ExecuteCPI(request CPIRequest) error { return f(request) }

type cpiProgramPolicyFunc func(sdk.Pubkey, []byte) error

func (f cpiProgramPolicyFunc) ValidateCPIProgram(programID sdk.Pubkey, data []byte) error {
	return f(programID, data)
}

func testCPIInvoker(executor CPIExecutor) *CPIInvoker {
	return &CPIInvoker{
		Executor: executor,
		Policy:   cpiProgramPolicyFunc(func(sdk.Pubkey, []byte) error { return nil }),
	}
}

func TestCPIPrivilegeValidationAndPDAOnlySignerElevation(t *testing.T) {
	callerProgram := sequentialPubkey(1)
	calleeProgram := sequentialPubkey(90)
	pda, bump, err := sdk.FindProgramAddress([][]byte{[]byte("authority")}, callerProgram)
	if err != nil {
		t.Fatal(err)
	}
	context := NewContext(callerProgram, []Account{
		{Key: pda, Owner: callerProgram, IsWritable: true},
		{Key: sequentialPubkey(40), Owner: callerProgram},
		{Key: calleeProgram, Executable: true},
	}, nil)
	called := false
	invoker := testCPIInvoker(cpiExecutorFunc(func(request CPIRequest) error {
		called = true
		if len(request.SignerPubkeys) != 1 || request.SignerPubkeys[0] != pda || request.Accounts[0] != context.Accounts[0] {
			t.Fatalf("bad validated request: %#v", request)
		}
		return nil
	}))
	instruction := sdk.Instruction{
		ProgramID: calleeProgram,
		Accounts:  []sdk.AccountMeta{{Pubkey: pda, IsSigner: true}},
	}
	if err := invoker.Invoke(context, instruction, []*AccountView{context.Accounts[0]}, [][][]byte{{[]byte("authority"), []byte{bump}}}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("executor not called")
	}

	// The same signer request cannot be vouched for with an arbitrary pubkey;
	// without the actual PDA seeds it is a privilege escalation.
	called = false
	if err := invoker.Invoke(context, instruction, []*AccountView{context.Accounts[0]}, nil); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("expected signer escalation, got %v", err)
	}
	if called {
		t.Fatal("executor called after failed validation")
	}

	readonly := context.Accounts[1]
	instruction.Accounts = []sdk.AccountMeta{{Pubkey: readonly.Key(), IsWritable: true}}
	if err := invoker.Invoke(context, instruction, []*AccountView{readonly}, nil); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("expected writable escalation, got %v", err)
	}
}

func TestCPIIdentityExecutorBoundaryAndRollback(t *testing.T) {
	callerProgram := sequentialPubkey(1)
	calleeProgram := sequentialPubkey(80)
	accountKey := sequentialPubkey(33)
	context := NewContext(callerProgram, []Account{
		{Key: accountKey, Owner: callerProgram, Lamports: 77, Data: []byte{1, 2}, IsWritable: true},
		{Key: calleeProgram, Executable: true},
	}, nil)
	instruction := sdk.Instruction{ProgramID: calleeProgram, Accounts: []sdk.AccountMeta{{Pubkey: accountKey, IsWritable: true}}}

	clone := newAccountView(Account{Key: accountKey, Owner: callerProgram, IsWritable: true})
	invoker := testCPIInvoker(cpiExecutorFunc(func(CPIRequest) error { return nil }))
	if err := invoker.Invoke(context, instruction, []*AccountView{clone}, nil); !errors.Is(err, ErrInvalidAccountPointer) {
		t.Fatalf("identity clone accepted: %v", err)
	}

	backendError := errors.New("callee failed")
	invoker.Executor = cpiExecutorFunc(func(request CPIRequest) error {
		request.Accounts[0].setLamports(1)
		data, err := request.Accounts[0].WritableData(callerProgram)
		if err != nil {
			t.Fatal(err)
		}
		data[0] = 9
		return backendError
	})
	if err := invoker.Invoke(context, instruction, []*AccountView{context.Accounts[0]}, nil); !errors.Is(err, backendError) {
		t.Fatalf("wrong backend error: %v", err)
	}
	if context.Accounts[0].Lamports() != 77 || string(context.Accounts[0].Data()) != string([]byte{1, 2}) {
		t.Fatalf("failed CPI changes not rolled back: lamports=%d data=%v", context.Accounts[0].Lamports(), context.Accounts[0].Data())
	}

	if err := (&CPIInvoker{}).Invoke(context, instruction, []*AccountView{context.Accounts[0]}, nil); !errors.Is(err, ErrCPIExecutorUnavailable) {
		t.Fatalf("missing executor did not fail closed: %v", err)
	}
	if err := (&CPIInvoker{Executor: cpiExecutorFunc(func(CPIRequest) error { return nil })}).Invoke(context, instruction, []*AccountView{context.Accounts[0]}, nil); !errors.Is(err, ErrCPIPolicyUnavailable) {
		t.Fatalf("missing program policy did not fail closed: %v", err)
	}
	policyExecutorCalled := false
	denied := &CPIInvoker{
		Executor: cpiExecutorFunc(func(CPIRequest) error {
			policyExecutorCalled = true
			return nil
		}),
		Policy: cpiProgramPolicyFunc(func(sdk.Pubkey, []byte) error { return errors.New("forbidden by bank policy") }),
	}
	if err := denied.Invoke(context, instruction, []*AccountView{context.Accounts[0]}, nil); !errors.Is(err, ErrCPIProgramNotAllowed) {
		t.Fatalf("denied program policy error = %v", err)
	}
	if policyExecutorCalled {
		t.Fatal("executor called after program policy denial")
	}
}

func TestCPIRollsBackPostExecutorSynchronizationFailure(t *testing.T) {
	callerProgram := sequentialPubkey(1)
	calleeProgram := sequentialPubkey(80)
	accountKey := sequentialPubkey(33)
	serialized, err := SerializeInputV1(callerProgram, []InputAccount{
		UniqueInputAccount(Account{Key: accountKey, Owner: callerProgram, Lamports: 77, Data: []byte{1, 2}, IsWritable: true}),
		UniqueInputAccount(Account{Key: calleeProgram, Executable: true}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := ParseInputV1(serialized.Buffer)
	if err != nil {
		t.Fatal(err)
	}
	account := context.Accounts[0]
	instruction := sdk.Instruction{ProgramID: calleeProgram, Accounts: []sdk.AccountMeta{{Pubkey: accountKey, IsWritable: true}}}
	invoker := testCPIInvoker(cpiExecutorFunc(func(CPIRequest) error {
		binary.LittleEndian.PutUint64(account.dataLenStorage, uint64(len(account.data)+1))
		account.setLamports(1)
		return nil
	}))
	if err := invoker.Invoke(context, instruction, []*AccountView{account}, nil); !isProgramErrorKind(err, ProgramErrorInvalidRealloc) {
		t.Fatalf("invalid post-CPI realloc accepted: %v", err)
	}
	if account.Lamports() != 77 || account.DataLen() != 2 || binary.LittleEndian.Uint64(account.dataLenStorage) != 2 {
		t.Fatalf("post-executor validation failure was not atomic: lamports=%d len=%d stored=%d", account.Lamports(), account.DataLen(), binary.LittleEndian.Uint64(account.dataLenStorage))
	}
}

func TestCPIRollsBackMappedLengthStorageAndRegion(t *testing.T) {
	callerProgram := sequentialPubkey(1)
	calleeProgram := sequentialPubkey(80)
	accountKey := sequentialPubkey(33)
	serialized, err := SerializeInputV1WithOptions(callerProgram, []InputAccount{
		UniqueInputAccount(Account{Key: accountKey, Owner: callerProgram, Lamports: 77, Data: []byte{1, 2}, IsWritable: true}),
		UniqueInputAccount(Account{Key: calleeProgram, Executable: true}),
	}, nil, SerializeOptions{AccountDataDirectMapping: true})
	if err != nil {
		t.Fatal(err)
	}
	context, err := ParseMappedInputV1(serialized, ParseOptions{RejectNonCanonicalBools: true})
	if err != nil {
		t.Fatal(err)
	}
	account := context.Accounts[0]
	if account.dataLenOffset != -1 || len(account.dataLenStorage) != 8 {
		t.Fatalf("test requires mapped length storage: offset=%d storage=%d", account.dataLenOffset, len(account.dataLenStorage))
	}
	instruction := sdk.Instruction{ProgramID: calleeProgram, Accounts: []sdk.AccountMeta{{Pubkey: accountKey, IsWritable: true}}}
	backendError := errors.New("mapped callee failed")
	invoker := testCPIInvoker(cpiExecutorFunc(func(CPIRequest) error {
		if err := account.ResizeData(callerProgram, 5); err != nil {
			t.Fatal(err)
		}
		return backendError
	}))
	if err := invoker.Invoke(context, instruction, []*AccountView{account}, nil); !errors.Is(err, backendError) {
		t.Fatalf("mapped executor error = %v", err)
	}
	assertMappedLengthRestored(t, context, serialized.AccountRegions[0], account, 2)

	invoker = testCPIInvoker(cpiExecutorFunc(func(CPIRequest) error {
		binary.LittleEndian.PutUint64(account.dataLenStorage, uint64(len(account.data)+1))
		return nil
	}))
	if err := invoker.Invoke(context, instruction, []*AccountView{account}, nil); !isProgramErrorKind(err, ProgramErrorInvalidRealloc) {
		t.Fatalf("mapped invalid post-CPI realloc accepted: %v", err)
	}
	assertMappedLengthRestored(t, context, serialized.AccountRegions[0], account, 2)
}

func assertMappedLengthRestored(t *testing.T, context *Context, region AccountRegion, account *AccountView, want int) {
	t.Helper()
	if account.DataLen() != want || binary.LittleEndian.Uint64(account.dataLenStorage) != uint64(want) {
		t.Fatalf("mapped length not restored: view=%d storage=%d want=%d", account.DataLen(), binary.LittleEndian.Uint64(account.dataLenStorage), want)
	}
	memory, err := context.MemoryMap()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Translate(region.DataAddress, uint64(want+1), AccessRead, 1); !errors.Is(err, ErrAccessViolation) {
		t.Fatalf("mapped region length was not restored to %d: %v", want, err)
	}
}

func TestInvokeSignedCCurrentPointerRestrictionsAndDirectInput(t *testing.T) {
	callerProgram := sequentialPubkey(1)
	calleeProgram := sequentialPubkey(80)
	targetKey := sequentialPubkey(33)
	serialized, err := SerializeInputV1WithOptions(callerProgram, []InputAccount{
		// The target is writable for CPI but owned by the callee, so Agave's
		// direct mapping exposes its data read-only to the caller. Forwarding it
		// must still reach the callee; writable privilege is validated separately.
		UniqueInputAccount(Account{Key: targetKey, Owner: calleeProgram, Lamports: 7, Data: []byte{1, 2}, IsWritable: true}),
		UniqueInputAccount(Account{Key: calleeProgram, Owner: sequentialPubkey(120), Executable: true}),
	}, nil, SerializeOptions{AccountDataDirectMapping: true})
	if err != nil {
		t.Fatal(err)
	}
	context, err := serialized.MappedContext()
	if err != nil {
		t.Fatal(err)
	}
	inputMemory, err := context.MemoryMap()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inputMemory.Translate(serialized.AccountRegions[0].DataAddress, 1, AccessWrite, 1); !errors.Is(err, ErrAccessViolation) {
		t.Fatalf("external-owner account data unexpectedly writable to caller: %v", err)
	}

	heapBase := sbpf.MMHeapStart + 0x1000
	heap := make([]byte, 256)
	copy(heap[40:72], calleeProgram[:])
	copy(heap[88:120], targetKey[:])
	heap[120] = 9
	copy(heap[:40], AppendCInstruction(nil, CInstruction{
		ProgramIDAddress: heapBase + 40,
		AccountsAddress:  heapBase + 72,
		AccountsLength:   1,
		DataAddress:      heapBase + 120,
		DataLength:       1,
	}))
	copy(heap[72:88], AppendCAccountMeta(nil, CAccountMeta{PubkeyAddress: heapBase + 88, IsWritable: 1}))
	targetRegion := serialized.AccountRegions[0]
	copy(heap[128:184], AppendCAccountInfo(nil, CAccountInfo{
		KeyAddress: targetRegion.KeyAddress, LamportsAddress: targetRegion.LamportsAddress,
		DataLength: 2, DataAddress: targetRegion.DataAddress, OwnerAddress: targetRegion.OwnerAddress,
		RentEpoch: math.MaxUint64, IsWritable: 1,
	}))
	regions := inputMemory.Regions()
	regions = append(regions, MemoryRegion{VMStart: heapBase, Data: heap, Writable: true, Name: "C CPI arguments"})
	memory, err := NewMemoryMap(regions...)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	invoker := testCPIInvoker(cpiExecutorFunc(func(request CPIRequest) error {
		called = true
		if request.Instruction.ProgramID != calleeProgram || !bytes.Equal(request.Instruction.Data, []byte{9}) || len(request.Accounts) != 1 || request.Accounts[0].Key() != targetKey {
			t.Fatalf("unexpected C CPI request: %#v", request)
		}
		return nil
	}))
	if err := invoker.InvokeSignedC(context, memory, heapBase, heapBase+128, 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("CPI executor was not called")
	}
	if err := invoker.InvokeSignedC(context, memory, heapBase, MMInputStart, 0, 0, 0); !errors.Is(err, ErrInvalidAccountPointer) {
		t.Fatalf("account-info array inside input was accepted: %v", err)
	}

	readOnlyRegions := inputMemory.Regions()
	readOnlyRegions = append(readOnlyRegions, MemoryRegion{VMStart: heapBase, Data: heap, Name: "read-only C CPI arguments"})
	readOnlyMemory, err := NewMemoryMap(readOnlyRegions...)
	if err != nil {
		t.Fatal(err)
	}
	if err := invoker.InvokeSignedC(context, readOnlyMemory, heapBase, heapBase+128, 1, 0, 0); !errors.Is(err, ErrAccessViolation) {
		t.Fatalf("read-only SolAccountInfo.data_len accepted: %v", err)
	}
}

func TestInvokeSignedCPropagatesReallocatedDataLength(t *testing.T) {
	callerProgram := sequentialPubkey(1)
	calleeProgram := sequentialPubkey(80)
	targetKey := sequentialPubkey(33)
	serialized, err := SerializeInputV1(callerProgram, []InputAccount{
		UniqueInputAccount(Account{Key: targetKey, Owner: calleeProgram, Data: []byte{1, 2}, IsWritable: true}),
		UniqueInputAccount(Account{Key: calleeProgram, Executable: true}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := ParseInputV1(serialized.Buffer)
	if err != nil {
		t.Fatal(err)
	}
	inputMemory, err := context.MemoryMap()
	if err != nil {
		t.Fatal(err)
	}
	heapBase := sbpf.MMHeapStart + 0x2000
	heap := make([]byte, 256)
	copy(heap[40:72], calleeProgram[:])
	copy(heap[88:120], targetKey[:])
	copy(heap[:40], AppendCInstruction(nil, CInstruction{
		ProgramIDAddress: heapBase + 40,
		AccountsAddress:  heapBase + 72,
		AccountsLength:   1,
	}))
	copy(heap[72:88], AppendCAccountMeta(nil, CAccountMeta{PubkeyAddress: heapBase + 88, IsWritable: 1}))
	targetRegion := serialized.AccountRegions[0]
	copy(heap[128:184], AppendCAccountInfo(nil, CAccountInfo{
		KeyAddress: targetRegion.KeyAddress, LamportsAddress: targetRegion.LamportsAddress,
		DataLength: 2, DataAddress: targetRegion.DataAddress, OwnerAddress: targetRegion.OwnerAddress,
		RentEpoch: math.MaxUint64, IsWritable: 1,
	}))
	regions := inputMemory.Regions()
	regions = append(regions, MemoryRegion{VMStart: heapBase, Data: heap, Writable: true, Name: "C CPI arguments"})
	memory, err := NewMemoryMap(regions...)
	if err != nil {
		t.Fatal(err)
	}
	invoker := testCPIInvoker(cpiExecutorFunc(func(request CPIRequest) error {
		return request.Accounts[0].ResizeData(calleeProgram, 5)
	}))
	if err := invoker.InvokeSignedC(context, memory, heapBase, heapBase+128, 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint64(heap[144:152]); got != 5 {
		t.Fatalf("SolAccountInfo.data_len = %d, want 5", got)
	}
	if context.Accounts[0].DataLen() != 5 {
		t.Fatalf("account data length = %d, want 5", context.Accounts[0].DataLen())
	}
}

func TestExecuteEntrypointStatusMapping(t *testing.T) {
	programID := sequentialPubkey(1)
	serialized, err := SerializeInputV1(programID, nil, []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if got := ExecuteEntrypoint(append([]byte(nil), serialized.Buffer...), func(context *Context) error {
		if context.ProgramID != programID {
			t.Fatal("wrong program id")
		}
		return CustomProgramError(7)
	}); got != 7 {
		t.Fatalf("custom status %d", got)
	}
	if got := ExecuteEntrypoint([]byte{1}, func(*Context) error { return nil }); got != BuiltinProgramError(ProgramErrorInvalidArgument).ReturnCode() {
		t.Fatalf("malformed input status %#x", got)
	}
	entrypoint := NewEntrypoint(func(*Context) error { return nil })
	if got := entrypoint(append([]byte(nil), serialized.Buffer...)); got != ProgramSuccess {
		t.Fatalf("generated wrapper status %#x", got)
	}

	withAccount, err := SerializeInputV1(programID, []InputAccount{
		UniqueInputAccount(Account{Key: sequentialPubkey(2), Owner: programID, Data: []byte{1}, IsWritable: true}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), withAccount.Buffer...)
	// Entry parsing always populates SDK's original_data_len padding field.
	binary.LittleEndian.PutUint32(original[12:16], 1)
	if got := ExecuteEntrypoint(withAccount.Buffer, func(context *Context) error {
		data, err := context.Accounts[0].WritableData(programID)
		if err != nil {
			return err
		}
		data[0] = 9
		return CustomProgramError(8)
	}); got != 8 {
		t.Fatalf("custom rollback status %d", got)
	}
	if string(withAccount.Buffer) != string(original) {
		t.Fatal("failed entrypoint did not roll back account memory")
	}
}
