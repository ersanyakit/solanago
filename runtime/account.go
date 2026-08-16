package runtime

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"

	"github.com/ersany/go-solana/sdk"
)

var (
	ErrReadonlyAccountModified     = errors.New("runtime: read-only or executable account modified")
	ErrExternalAccountDataModified = errors.New("runtime: data or owner of externally owned account modified")
	ErrExternalLamportSpend        = errors.New("runtime: lamports debited from externally owned account")
	ErrUnbalancedLamports          = errors.New("runtime: instruction changed total lamports")
)

const (
	// MaxPermittedDataIncrease matches solana_account_info.
	MaxPermittedDataIncrease = 10 * 1024
	// MaxPermittedDataLength matches solana_system_interface (10 MiB).
	MaxPermittedDataLength = 10 * 1024 * 1024
)

// Account is an owned host-side input used to construct an ABIv1 invocation.
// Rent epoch is deliberately absent: current Agave masks it to u64::MAX in the
// program input and current SDK AccountInfo no longer exposes it semantically.
type Account struct {
	Key        sdk.Pubkey
	Owner      sdk.Pubkey
	Lamports   uint64
	Data       []byte
	IsSigner   bool
	IsWritable bool
	Executable bool
}

// InputAccount is either a unique account or a duplicate of an earlier slot.
// Exactly one of Account and DuplicateOf must be set. This models Agave's
// transaction-account identity explicitly instead of guessing from Go pointer
// identity or exposing Go heap addresses.
type InputAccount struct {
	Account     *Account
	DuplicateOf *uint8
}

// UniqueInputAccount constructs a unique serialized account record.
func UniqueInputAccount(account Account) InputAccount {
	copyAccount := account
	copyAccount.Data = append([]byte(nil), account.Data...)
	return InputAccount{Account: &copyAccount}
}

// DuplicateInputAccount constructs an alias of an earlier account slot.
func DuplicateInputAccount(index uint8) InputAccount {
	return InputAccount{DuplicateOf: &index}
}

// AccountView is a bounded view of one invocation account. Duplicate ABI
// slots share the same *AccountView, so lamports, owner, and data mutations
// have the aliasing behavior of SDK AccountInfo clones.
type AccountView struct {
	key         sdk.Pubkey
	owner       sdk.Pubkey
	lamports    uint64
	data        []byte
	dataLen     int
	originalLen int

	isSigner   bool
	isWritable bool
	executable bool

	baselineOwner    sdk.Pubkey
	baselineLamports uint64
	baselineDataLen  int
	baselineDataHash [32]byte

	raw             []byte
	ownerOffset     int
	lamportsOffset  int
	dataLenOffset   int
	ownerStorage    []byte
	lamportsStorage []byte
	dataLenStorage  []byte
	memory          *MemoryMap
	dataAddress     uint64
}

func newAccountView(account Account) *AccountView {
	capacity := len(account.Data) + MaxPermittedDataIncrease
	if capacity < len(account.Data) {
		capacity = len(account.Data)
	}
	data := make([]byte, capacity)
	copy(data, account.Data)
	view := &AccountView{
		key:         account.Key,
		owner:       account.Owner,
		lamports:    account.Lamports,
		data:        data,
		dataLen:     len(account.Data),
		originalLen: len(account.Data),
		isSigner:    account.IsSigner,
		isWritable:  account.IsWritable,
		executable:  account.Executable,
		ownerOffset: -1, lamportsOffset: -1, dataLenOffset: -1,
	}
	view.acceptBaseline()
	return view
}

// Key returns the account address by value.
func (a *AccountView) Key() sdk.Pubkey {
	if a == nil {
		return sdk.Pubkey{}
	}
	return a.key
}

// Owner returns the current owner by value.
func (a *AccountView) Owner() sdk.Pubkey {
	if a == nil {
		return sdk.Pubkey{}
	}
	if len(a.ownerStorage) == 32 {
		copy(a.owner[:], a.ownerStorage)
	} else if a.ownerOffset >= 0 {
		copy(a.owner[:], a.raw[a.ownerOffset:a.ownerOffset+32])
	}
	return a.owner
}

// Lamports returns the current balance.
func (a *AccountView) Lamports() uint64 {
	if a == nil {
		return 0
	}
	if len(a.lamportsStorage) == 8 {
		a.lamports = binary.LittleEndian.Uint64(a.lamportsStorage)
	} else if a.lamportsOffset >= 0 {
		a.lamports = binary.LittleEndian.Uint64(a.raw[a.lamportsOffset : a.lamportsOffset+8])
	}
	return a.lamports
}

func (a *AccountView) IsSigner() bool   { return a != nil && a.isSigner }
func (a *AccountView) IsWritable() bool { return a != nil && a.isWritable }
func (a *AccountView) Executable() bool { return a != nil && a.executable }

// OriginalDataLen is the account length at invocation entry.
func (a *AccountView) OriginalDataLen() int {
	if a == nil {
		return 0
	}
	return a.originalLen
}

// DataLen returns the current logical length.
func (a *AccountView) DataLen() int {
	if a == nil {
		return 0
	}
	return a.dataLen
}

// Sync validates scalar fields that an SBF program may have changed directly
// through VM memory and refreshes the view before CPI or host-side commit.
func (a *AccountView) Sync() error {
	if a == nil {
		return ErrInvalidABI
	}
	if len(a.ownerStorage) == 32 {
		copy(a.owner[:], a.ownerStorage)
	} else if a.ownerOffset >= 0 {
		copy(a.owner[:], a.raw[a.ownerOffset:a.ownerOffset+32])
	}
	if len(a.lamportsStorage) == 8 {
		a.lamports = binary.LittleEndian.Uint64(a.lamportsStorage)
	} else if a.lamportsOffset >= 0 {
		a.lamports = binary.LittleEndian.Uint64(a.raw[a.lamportsOffset : a.lamportsOffset+8])
	}
	if len(a.dataLenStorage) == 8 || a.dataLenOffset >= 0 {
		storage := a.dataLenStorage
		if len(storage) != 8 {
			storage = a.raw[a.dataLenOffset : a.dataLenOffset+8]
		}
		length := binary.LittleEndian.Uint64(storage)
		if length > uint64(len(a.data)) || length > MaxPermittedDataLength || length > uint64(a.originalLen+MaxPermittedDataIncrease) {
			return BuiltinProgramError(ProgramErrorInvalidRealloc)
		}
		if a.memory != nil && length != uint64(a.dataLen) {
			if err := a.memory.ResizeRegion(a.dataAddress, length); err != nil {
				return BuiltinProgramError(ProgramErrorInvalidRealloc)
			}
		}
		a.dataLen = int(length)
	}
	return nil
}

// Data returns a copy. Mutating the returned slice never bypasses ownership or
// writable checks; use WritableData for an in-place view.
func (a *AccountView) Data() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.data[:a.dataLen]...)
}

// Snapshot returns an owned copy suitable for host-side commit assertions or
// deterministic test harnesses.
func (a *AccountView) Snapshot() (Account, error) {
	if err := a.Sync(); err != nil {
		return Account{}, err
	}
	return Account{
		Key: a.key, Owner: a.owner, Lamports: a.lamports,
		Data:     append([]byte(nil), a.data[:a.dataLen]...),
		IsSigner: a.isSigner, IsWritable: a.isWritable, Executable: a.executable,
	}, nil
}

// RequireSigner verifies the transaction-granted signer privilege.
func (a *AccountView) RequireSigner() error {
	if a == nil || !a.isSigner {
		return BuiltinProgramError(ProgramErrorMissingRequiredSignature)
	}
	return nil
}

// RequireWritable verifies the transaction-granted writable privilege.
func (a *AccountView) RequireWritable() error {
	if a == nil || !a.isWritable || a.executable {
		return BuiltinProgramError(ProgramErrorInvalidArgument)
	}
	return nil
}

// RequireOwner verifies program ownership.
func (a *AccountView) RequireOwner(owner sdk.Pubkey) error {
	if a == nil || a.Owner() != owner {
		return BuiltinProgramError(ProgramErrorIncorrectProgramID)
	}
	return nil
}

// WritableData returns the live account-data region after writable and owner
// checks. The slice cannot grow; call ResizeData first.
func (a *AccountView) WritableData(programID sdk.Pubkey) ([]byte, error) {
	if err := a.RequireWritable(); err != nil {
		return nil, err
	}
	if err := a.RequireOwner(programID); err != nil {
		return nil, err
	}
	return a.data[:a.dataLen], nil
}

// ResizeData changes the logical length within the ABIv1 realloc region. New
// bytes are zeroed. Shrunk bytes are also cleared so regrowth is deterministic.
func (a *AccountView) ResizeData(programID sdk.Pubkey, newLength int) error {
	if err := a.Sync(); err != nil {
		return err
	}
	if err := a.RequireWritable(); err != nil {
		return err
	}
	if err := a.RequireOwner(programID); err != nil {
		return err
	}
	if newLength < 0 || newLength > MaxPermittedDataLength || newLength > a.originalLen+MaxPermittedDataIncrease || newLength > len(a.data) {
		return BuiltinProgramError(ProgramErrorInvalidRealloc)
	}
	if a.memory != nil {
		if err := a.memory.ResizeRegion(a.dataAddress, uint64(newLength)); err != nil {
			return BuiltinProgramError(ProgramErrorInvalidRealloc)
		}
	}
	if newLength != a.dataLen {
		start, end := newLength, a.dataLen
		if newLength > a.dataLen {
			start, end = a.dataLen, newLength
		}
		clear(a.data[start:end])
	}
	a.dataLen = newLength
	if len(a.dataLenStorage) == 8 {
		binary.LittleEndian.PutUint64(a.dataLenStorage, uint64(newLength))
	} else if a.dataLenOffset >= 0 {
		binary.LittleEndian.PutUint64(a.raw[a.dataLenOffset:a.dataLenOffset+8], uint64(newLength))
	}
	return nil
}

func (c *Context) attachMappedAccounts(memory *MemoryMap) {
	if c == nil || memory == nil {
		return
	}
	seen := make(map[*AccountView]struct{}, len(c.accountSlots))
	for index, account := range c.accountSlots {
		if account == nil || index >= len(c.regions) {
			continue
		}
		if _, exists := seen[account]; exists {
			continue
		}
		seen[account] = struct{}{}
		region := c.regions[index]
		if _, growable := memory.growableStorage(region.DataAddress, uint64(account.dataLen)); growable {
			account.memory = memory
			account.dataAddress = region.DataAddress
		}
	}
}

func (a *AccountView) setLamports(value uint64) {
	a.lamports = value
	if len(a.lamportsStorage) == 8 {
		binary.LittleEndian.PutUint64(a.lamportsStorage, value)
	} else if a.lamportsOffset >= 0 {
		binary.LittleEndian.PutUint64(a.raw[a.lamportsOffset:a.lamportsOffset+8], value)
	}
}

func (a *AccountView) setOwner(owner sdk.Pubkey) {
	a.owner = owner
	if len(a.ownerStorage) == 32 {
		copy(a.ownerStorage, owner[:])
	} else if a.ownerOffset >= 0 {
		copy(a.raw[a.ownerOffset:a.ownerOffset+32], owner[:])
	}
}

func (a *AccountView) acceptBaseline() {
	if a == nil {
		return
	}
	a.baselineOwner = a.owner
	a.baselineLamports = a.lamports
	a.baselineDataLen = a.dataLen
	a.baselineDataHash = sha256.Sum256(a.data[:a.dataLen])
}

// AccountRequirement declares validation for one positional account.
type AccountRequirement struct {
	Signer     bool
	Writable   bool
	Owner      *sdk.Pubkey
	MinDataLen int
}

// Context is the decoded program invocation. Accounts retains ABI order and
// duplicate slots intentionally alias one AccountView.
type Context struct {
	ProgramID       sdk.Pubkey
	Accounts        []*AccountView
	InstructionData []byte
	programID       sdk.Pubkey
	accountSlots    []*AccountView
	regions         []AccountRegion
	raw             []byte
	mappedSources   []mappedRegionSource
	memory          *MemoryMap
}

// NewContext creates a native bounded context without serializing. It is for
// deterministic unit models; real on-chain entry uses ParseInputV1.
func NewContext(programID sdk.Pubkey, accounts []Account, instructionData []byte) *Context {
	views := make([]*AccountView, len(accounts))
	for i := range accounts {
		views[i] = newAccountView(accounts[i])
	}
	return &Context{
		ProgramID: programID, Accounts: append([]*AccountView(nil), views...), InstructionData: append([]byte(nil), instructionData...),
		programID: programID, accountSlots: views,
	}
}

// ProgramAddress returns the immutable program id used for security checks.
func (c *Context) ProgramAddress() sdk.Pubkey {
	if c == nil {
		return sdk.Pubkey{}
	}
	return c.programID
}

// AccountCount returns the immutable ABI slot count.
func (c *Context) AccountCount() int {
	if c == nil {
		return 0
	}
	return len(c.accountSlots)
}

// AccountAt returns one canonical ABI slot without exposing the internal slot
// table for replacement. Duplicate indices return the same *AccountView.
func (c *Context) AccountAt(index int) (*AccountView, error) {
	if c == nil || index < 0 || index >= len(c.accountSlots) || c.accountSlots[index] == nil {
		return nil, BuiltinProgramError(ProgramErrorNotEnoughAccountKeys)
	}
	return c.accountSlots[index], nil
}

// RequireAccount retrieves and validates a positional account.
func (c *Context) RequireAccount(index int, requirement AccountRequirement) (*AccountView, error) {
	account, err := c.AccountAt(index)
	if err != nil {
		return nil, err
	}
	if err := account.Sync(); err != nil {
		return nil, err
	}
	if requirement.Signer {
		if err := account.RequireSigner(); err != nil {
			return nil, err
		}
	}
	if requirement.Writable {
		if err := account.RequireWritable(); err != nil {
			return nil, err
		}
	}
	if requirement.Owner != nil {
		if err := account.RequireOwner(*requirement.Owner); err != nil {
			return nil, err
		}
	}
	if requirement.MinDataLen < 0 {
		return nil, fmt.Errorf("negative minimum data length")
	}
	if account.dataLen < requirement.MinDataLen {
		return nil, BuiltinProgramError(ProgramErrorAccountDataTooSmall)
	}
	return account, nil
}

// TransferLamports applies Solana's core debit boundary: both accounts must be
// writable and only an account owned by the current program may be debited.
func (c *Context) TransferLamports(from, to int, amount uint64) error {
	source, err := c.RequireAccount(from, AccountRequirement{Writable: true})
	if err != nil {
		return err
	}
	destination, err := c.RequireAccount(to, AccountRequirement{Writable: true})
	if err != nil {
		return err
	}
	if source.Owner() != c.programID {
		return BuiltinProgramError(ProgramErrorIllegalOwner)
	}
	if source.Lamports() < amount {
		return BuiltinProgramError(ProgramErrorInsufficientFunds)
	}
	if destination.Lamports() > math.MaxUint64-amount {
		return BuiltinProgramError(ProgramErrorArithmeticOverflow)
	}
	source.setLamports(source.Lamports() - amount)
	destination.setLamports(destination.Lamports() + amount)
	return nil
}

// AssignOwner changes ownership only for writable accounts currently owned by
// this program whose data is entirely zero, matching the runtime boundary that
// prevents assigning opaque live state to another program.
func (c *Context) AssignOwner(index int, owner sdk.Pubkey) error {
	account, err := c.RequireAccount(index, AccountRequirement{Writable: true, Owner: &c.programID})
	if err != nil {
		return err
	}
	for _, value := range account.data[:account.dataLen] {
		if value != 0 {
			return BuiltinProgramError(ProgramErrorInvalidAccountData)
		}
	}
	account.setOwner(owner)
	return nil
}

// RawInput returns a copy of the mutated ABIv1 buffer. A native context has no
// raw input and returns nil.
func (c *Context) RawInput() []byte {
	if c == nil {
		return nil
	}
	return append([]byte(nil), c.raw...)
}

// AccountRegions returns a copy of the parser's exact VM region metadata.
func (c *Context) AccountRegions() []AccountRegion {
	if c == nil {
		return nil
	}
	return append([]AccountRegion(nil), c.regions...)
}

// SyncAccounts refreshes every unique account view from direct VM memory and
// validates realloc lengths before CPI or commit.
func (c *Context) SyncAccounts() error {
	if c == nil {
		return ErrInvalidABI
	}
	seen := make(map[*AccountView]struct{}, len(c.accountSlots))
	for _, account := range c.accountSlots {
		if account == nil {
			return ErrInvalidABI
		}
		if _, exists := seen[account]; exists {
			continue
		}
		seen[account] = struct{}{}
		if err := account.Sync(); err != nil {
			return err
		}
	}
	return nil
}

// ValidateProgramChanges enforces the caller-side account privilege boundary
// after direct VM writes: read-only/executable accounts are immutable; a
// program cannot alter another owner's data/owner or debit its lamports; and
// total lamports across unique instruction accounts must be conserved.
func (c *Context) ValidateProgramChanges() error {
	if err := c.SyncAccounts(); err != nil {
		return err
	}
	seen := make(map[*AccountView]struct{}, len(c.accountSlots))
	var baselineLow, baselineHigh uint64
	var currentLow, currentHigh uint64
	for _, account := range c.accountSlots {
		if _, exists := seen[account]; exists {
			continue
		}
		seen[account] = struct{}{}
		currentHash := sha256.Sum256(account.data[:account.dataLen])
		ownerChanged := account.owner != account.baselineOwner
		dataChanged := account.dataLen != account.baselineDataLen || currentHash != account.baselineDataHash
		lamportsChanged := account.lamports != account.baselineLamports
		if (!account.isWritable || account.executable) && (ownerChanged || dataChanged || lamportsChanged) {
			return fmt.Errorf("%s: %w", account.key.String(), ErrReadonlyAccountModified)
		}
		if account.baselineOwner != c.programID {
			if ownerChanged || dataChanged {
				return fmt.Errorf("%s: %w", account.key.String(), ErrExternalAccountDataModified)
			}
			if account.lamports < account.baselineLamports {
				return fmt.Errorf("%s: %w", account.key.String(), ErrExternalLamportSpend)
			}
		} else if ownerChanged {
			for _, value := range account.data[:account.dataLen] {
				if value != 0 {
					return BuiltinProgramError(ProgramErrorInvalidAccountData)
				}
			}
		}
		var carry uint64
		baselineLow, carry = bits.Add64(baselineLow, account.baselineLamports, 0)
		baselineHigh, _ = bits.Add64(baselineHigh, 0, carry)
		currentLow, carry = bits.Add64(currentLow, account.lamports, 0)
		currentHigh, _ = bits.Add64(currentHigh, 0, carry)
	}
	if baselineLow != currentLow || baselineHigh != currentHigh {
		return ErrUnbalancedLamports
	}
	return nil
}

func (c *Context) acceptAccountBaselines() error {
	if err := c.SyncAccounts(); err != nil {
		return err
	}
	seen := make(map[*AccountView]struct{}, len(c.accountSlots))
	for _, account := range c.accountSlots {
		if _, exists := seen[account]; exists {
			continue
		}
		seen[account] = struct{}{}
		account.acceptBaseline()
	}
	return nil
}
