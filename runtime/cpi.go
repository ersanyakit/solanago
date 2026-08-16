package runtime

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ersanyakit/go-solana/sdk"
)

var (
	ErrPrivilegeEscalation    = errors.New("runtime: CPI privilege escalation")
	ErrMissingCPIAccount      = errors.New("runtime: CPI references a missing caller account")
	ErrMissingCPIProgram      = errors.New("runtime: CPI program account is missing or not executable")
	ErrInvalidAccountPointer  = errors.New("runtime: CPI account info pointer does not match ABIv1 metadata")
	ErrCPIExecutorUnavailable = errors.New("runtime: no CPI executor configured")
	ErrCPIPolicyUnavailable   = errors.New("runtime: no CPI program policy configured")
	ErrCPIProgramNotAllowed   = errors.New("runtime: CPI program is not allowed")
)

// CPIRequest is passed only after limits, account identity, and privilege
// checks succeed. Accounts is ordered like Instruction.Accounts and duplicate
// metas intentionally contain the same *AccountView.
type CPIRequest struct {
	CallerProgramID sdk.Pubkey
	Instruction     sdk.Instruction
	Accounts        []*AccountView
	SignerPubkeys   []sdk.Pubkey
}

// CPIExecutor is the boundary to a real SVM or a deterministic test double.
// This package intentionally provides no default executor and therefore does
// not claim validator-equivalent CPI execution.
type CPIExecutor interface {
	ExecuteCPI(CPIRequest) error
}

// CPIProgramPolicy can deny loader/precompile or feature-specific invocation
// forms before an executor is entered. Real-SVM adapters should implement the
// current Agave check_authorized_program policy for their bank feature set.
type CPIProgramPolicy interface {
	ValidateCPIProgram(programID sdk.Pubkey, instructionData []byte) error
}

// CPIInvoker validates and atomically delegates an inner instruction.
type CPIInvoker struct {
	Executor CPIExecutor
	Policy   CPIProgramPolicy
}

// Invoke validates a CPI against the caller Context. signerSeeds are grouped
// by PDA signer; each group is passed to sdk.CreateProgramAddress using the
// caller program id. Arbitrary signer pubkeys cannot be injected.
func (invoker *CPIInvoker) Invoke(context *Context, instruction sdk.Instruction, accountInfos []*AccountView, signerSeeds [][][]byte) error {
	if context == nil {
		return ErrMissingCPIAccount
	}
	if err := context.SyncAccounts(); err != nil {
		return err
	}
	if err := context.ValidateProgramChanges(); err != nil {
		return err
	}
	if len(instruction.Accounts) > MaxAccountsPerInstruction {
		return fmt.Errorf("%w: got %d, maximum %d", ErrTooManyAccounts, len(instruction.Accounts), MaxAccountsPerInstruction)
	}
	if len(instruction.Data) > MaxInstructionDataLen {
		return fmt.Errorf("%w: got %d, maximum %d", ErrInstructionTooLarge, len(instruction.Data), MaxInstructionDataLen)
	}
	if len(accountInfos) > MaxCPIAccountInfos {
		return fmt.Errorf("%w: got %d account infos, maximum %d", ErrTooManyAccounts, len(accountInfos), MaxCPIAccountInfos)
	}
	if len(signerSeeds) > MaxCPISigners {
		return fmt.Errorf("runtime: too many CPI signers: %d > %d", len(signerSeeds), MaxCPISigners)
	}
	if invoker == nil || invoker.Executor == nil {
		return ErrCPIExecutorUnavailable
	}
	if invoker.Policy == nil {
		return ErrCPIPolicyUnavailable
	}
	if err := invoker.Policy.ValidateCPIProgram(instruction.ProgramID, instruction.Data); err != nil {
		return fmt.Errorf("%w: %v", ErrCPIProgramNotAllowed, err)
	}

	signers := make([]sdk.Pubkey, len(signerSeeds))
	for index, seeds := range signerSeeds {
		address, err := sdk.CreateProgramAddress(seeds, context.programID)
		if err != nil {
			if errors.Is(err, sdk.ErrMaxSeedLength) || errors.Is(err, sdk.ErrTooManySeeds) {
				return fmt.Errorf("signer %d: %w", index, BuiltinProgramError(ProgramErrorMaxSeedLengthExceeded))
			}
			return fmt.Errorf("signer %d: %w", index, BuiltinProgramError(ProgramErrorInvalidSeeds))
		}
		signers[index] = address
	}

	callerByKey := make(map[sdk.Pubkey]*AccountView, len(context.accountSlots))
	callerPrivileges := make(map[sdk.Pubkey]accountPrivileges, len(context.accountSlots))
	programExecutable := false
	for _, account := range context.accountSlots {
		if account == nil {
			continue
		}
		key := account.Key()
		if previous, exists := callerByKey[key]; exists && previous != account {
			// One transaction key denotes one account. Distinct views for the same
			// key would make identity/pointer validation ambiguous.
			return fmt.Errorf("%w: duplicate key has distinct views", ErrInvalidABI)
		}
		callerByKey[key] = account
		privileges := callerPrivileges[key]
		privileges.signer = privileges.signer || account.IsSigner()
		privileges.writable = privileges.writable || account.IsWritable()
		callerPrivileges[key] = privileges
		if key == instruction.ProgramID && account.Executable() {
			programExecutable = true
		}
	}
	if !programExecutable {
		return ErrMissingCPIProgram
	}

	providedByKey := make(map[sdk.Pubkey]*AccountView, len(accountInfos))
	for index, account := range accountInfos {
		if account == nil {
			return fmt.Errorf("account info %d: %w", index, ErrMissingCPIAccount)
		}
		key := account.Key()
		caller, ok := callerByKey[key]
		if !ok {
			return fmt.Errorf("account info %d (%s): %w", index, key.String(), ErrMissingCPIAccount)
		}
		if caller != account {
			return fmt.Errorf("account info %d (%s): %w", index, key.String(), ErrInvalidAccountPointer)
		}
		providedByKey[key] = account
	}

	requested := make(map[sdk.Pubkey]accountPrivileges, len(instruction.Accounts))
	for _, meta := range instruction.Accounts {
		privileges := requested[meta.Pubkey]
		privileges.signer = privileges.signer || meta.IsSigner
		privileges.writable = privileges.writable || meta.IsWritable
		requested[meta.Pubkey] = privileges
	}
	for key, requestedPrivileges := range requested {
		caller, ok := callerByKey[key]
		if !ok {
			return fmt.Errorf("%s: %w", key.String(), ErrMissingCPIAccount)
		}
		if _, ok := providedByKey[key]; !ok && !caller.Executable() {
			return fmt.Errorf("%s: %w", key.String(), ErrMissingCPIAccount)
		}
		callerPrivilege := callerPrivileges[key]
		if requestedPrivileges.writable && !callerPrivilege.writable {
			return fmt.Errorf("%s writable: %w", key.String(), ErrPrivilegeEscalation)
		}
		if requestedPrivileges.signer && !(callerPrivilege.signer || containsPubkey(signers, key)) {
			return fmt.Errorf("%s signer: %w", key.String(), ErrPrivilegeEscalation)
		}
	}

	orderedAccounts := make([]*AccountView, len(instruction.Accounts))
	for index, meta := range instruction.Accounts {
		orderedAccounts[index] = callerByKey[meta.Pubkey]
	}
	snapshots := snapshotAccounts(context.accountSlots)
	err := invoker.Executor.ExecuteCPI(CPIRequest{
		CallerProgramID: context.programID,
		Instruction:     instruction.Clone(),
		Accounts:        orderedAccounts,
		SignerPubkeys:   append([]sdk.Pubkey(nil), signers...),
	})
	if err != nil {
		restoreAccountSnapshots(snapshots)
		return err
	}
	if err := context.acceptAccountBaselines(); err != nil {
		restoreAccountSnapshots(snapshots)
		return err
	}
	return nil
}

// InvokeSignedC translates the official C syscall representation, validates
// every AccountInfo pointer against the caller's ABIv1 region metadata, then
// delegates through Invoke.
func (invoker *CPIInvoker) InvokeSignedC(
	context *Context,
	memory *MemoryMap,
	instructionAddress uint64,
	accountInfosAddress uint64,
	accountInfosLength uint64,
	signerSeedsAddress uint64,
	signerSeedsLength uint64,
) error {
	if context == nil {
		return ErrMissingCPIAccount
	}
	if err := context.SyncAccounts(); err != nil {
		return err
	}
	if accountInfosLength > MaxCPIAccountInfos {
		return fmt.Errorf("%w: got %d account infos, maximum %d", ErrTooManyAccounts, accountInfosLength, MaxCPIAccountInfos)
	}
	if accountInfosLength > ^uint64(0)/CAccountInfoSize {
		return fmt.Errorf("%w: invalid account info array length", ErrInvalidAccountPointer)
	}
	accountInfosEnd := accountInfosAddress + accountInfosLength*CAccountInfoSize
	if accountInfosEnd < accountInfosAddress || accountInfosEnd >= MMInputStart {
		return fmt.Errorf("%w: account info array must be outside program input", ErrInvalidAccountPointer)
	}
	for index := uint64(0); index < accountInfosLength; index++ {
		// Agave translates SolAccountInfo::data_len mutably so it can synchronize
		// post-CPI reallocations back to the caller.
		if _, err := memory.Translate(accountInfosAddress+index*CAccountInfoSize+16, 8, AccessWrite, 8); err != nil {
			return fmt.Errorf("account info %d data_len: %w", index, err)
		}
	}
	instruction, err := TranslateCInstruction(memory, instructionAddress)
	if err != nil {
		return err
	}
	translatedInfos, err := TranslateCAccountInfos(memory, accountInfosAddress, accountInfosLength)
	if err != nil {
		return err
	}
	seeds, err := TranslateCSignerSeeds(memory, signerSeedsAddress, signerSeedsLength)
	if err != nil {
		return err
	}
	regionsByKey := make(map[sdk.Pubkey]AccountRegion, len(context.accountSlots))
	accountsByKey := make(map[sdk.Pubkey]*AccountView, len(context.accountSlots))
	for index, account := range context.accountSlots {
		if account == nil || index >= len(context.regions) {
			continue
		}
		if _, exists := regionsByKey[account.Key()]; !exists {
			regionsByKey[account.Key()] = context.regions[index]
			accountsByKey[account.Key()] = account
		}
	}
	accountInfos := make([]*AccountView, len(translatedInfos))
	for index, info := range translatedInfos {
		account, ok := accountsByKey[info.Key]
		if !ok {
			return fmt.Errorf("account info %d: %w", index, ErrMissingCPIAccount)
		}
		region := regionsByKey[info.Key]
		if info.KeyAddress != region.KeyAddress || info.OwnerAddress != region.OwnerAddress || info.LamportsAddress != region.LamportsAddress || info.DataAddress != region.DataAddress {
			return fmt.Errorf("account info %d: %w", index, ErrInvalidAccountPointer)
		}
		if info.DataLength != uint64(account.DataLen()) || info.IsSigner != account.IsSigner() || info.IsWritable != account.IsWritable() || info.Executable != account.Executable() {
			return fmt.Errorf("account info %d: %w", index, ErrInvalidABI)
		}
		accountInfos[index] = account
	}
	if err := invoker.Invoke(context, instruction, accountInfos, seeds); err != nil {
		return err
	}
	// Agave writes a callee realloc back to SolAccountInfo::data_len so the
	// caller observes the new slice length after a successful CPI. The
	// descriptor fields were translated with write access above, so this mirrors
	// update_caller_account() without exposing any host pointer.
	for index, account := range accountInfos {
		lengthStorage, err := memory.Translate(accountInfosAddress+uint64(index)*CAccountInfoSize+16, 8, AccessWrite, 8)
		if err != nil {
			return fmt.Errorf("account info %d data_len: %w", index, err)
		}
		binary.LittleEndian.PutUint64(lengthStorage, uint64(account.DataLen()))
	}
	return nil
}

// MemoryMap returns the canonical ABIv1 mapping. Contiguous input is one
// region; account-data direct mapping preserves Agave's split regions and
// fixed realloc reservations.
func (c *Context) MemoryMap() (*MemoryMap, error) {
	if c == nil {
		return nil, ErrInvalidMemoryRegion
	}
	if c.memory != nil {
		return c.memory, nil
	}
	if len(c.mappedSources) > 0 {
		regions := make([]MemoryRegion, 0, len(c.mappedSources))
		for _, source := range c.mappedSources {
			mapped := source.snapshot()
			regions = append(regions, MemoryRegion{
				VMStart: mapped.VMStart, Data: mapped.Data, Writable: mapped.Writable,
				ReservedLength: mapped.ReservedLength, Growable: mapped.Growable, Name: mapped.Name,
			})
		}
		memory, err := NewMemoryMap(regions...)
		if err != nil {
			return nil, err
		}
		c.memory = memory
		c.attachMappedAccounts(memory)
		return memory, nil
	}
	if len(c.raw) == 0 {
		return nil, ErrInvalidMemoryRegion
	}
	return NewMemoryMap(MemoryRegion{VMStart: MMInputStart, Data: c.raw, Writable: true, Name: "program input"})
}

type accountPrivileges struct {
	signer   bool
	writable bool
}

func containsPubkey(keys []sdk.Pubkey, wanted sdk.Pubkey) bool {
	for _, key := range keys {
		if key == wanted {
			return true
		}
	}
	return false
}

type accountSnapshot struct {
	account  *AccountView
	owner    sdk.Pubkey
	lamports uint64
	data     []byte
	dataLen  int
}

func snapshotAccounts(accounts []*AccountView) []accountSnapshot {
	seen := make(map[*AccountView]struct{}, len(accounts))
	result := make([]accountSnapshot, 0, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		if _, exists := seen[account]; exists {
			continue
		}
		seen[account] = struct{}{}
		result = append(result, accountSnapshot{
			account: account, owner: account.owner, lamports: account.lamports,
			data: append([]byte(nil), account.data...), dataLen: account.dataLen,
		})
	}
	return result
}

func restoreAccountSnapshots(snapshots []accountSnapshot) {
	for _, snapshot := range snapshots {
		snapshot.account.setOwner(snapshot.owner)
		snapshot.account.setLamports(snapshot.lamports)
		copy(snapshot.account.data, snapshot.data)
		snapshot.account.dataLen = snapshot.dataLen
		if snapshot.account.memory != nil {
			_ = snapshot.account.memory.ResizeRegion(snapshot.account.dataAddress, uint64(snapshot.dataLen))
			copy(snapshot.account.data, snapshot.data)
		}
		if len(snapshot.account.dataLenStorage) == 8 || snapshot.account.dataLenOffset >= 0 {
			// ResizeData cannot be used because restore must not re-run privilege
			// checks after an executor failure.
			putAccountDataLength(snapshot.account, snapshot.dataLen)
		}
	}
}

func putAccountDataLength(account *AccountView, length int) {
	if len(account.dataLenStorage) == 8 {
		for byteIndex := 0; byteIndex < 8; byteIndex++ {
			account.dataLenStorage[byteIndex] = byte(uint64(length) >> (8 * byteIndex))
		}
		return
	}
	for byteIndex := 0; byteIndex < 8; byteIndex++ {
		account.raw[account.dataLenOffset+byteIndex] = byte(uint64(length) >> (8 * byteIndex))
	}
}
