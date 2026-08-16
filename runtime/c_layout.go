package runtime

import (
	"encoding/binary"
	"fmt"

	"github.com/ersanyakit/solanago/sdk"
)

// Exact LP64 C/Rust-stable layouts consumed by current Agave CPI translation.
// Bool fields are uint8 plus explicit tail padding so Go layout changes cannot
// alter the guest representation.
//
// Source pins:
//   - anza-xyz/agave programs/sbf/c/inc/sol/{cpi,entrypoint,pubkey}.h
//   - anza-xyz/agave program-runtime/src/cpi.rs
//     at 12b5c7e4df705927b2f7f579f3aa606aa4bde1c0
//   - anza-xyz/solana-sdk stable-layout/src/{stable_instruction,stable_vec}.rs
//     at 7437469d1ab5bddbf665f3a1a69aefb422c33e36
const (
	CInstructionSize    = 40
	CAccountMetaSize    = 16
	CAccountInfoSize    = 56
	CSignerSeedSize     = 16
	CSignerSeedsSize    = 16
	RustStableVecSize   = 24
	RustInstructionSize = 80
	RustAccountMetaSize = 34
	MaxCPISigners       = 16
	MaxCPIAccountInfos  = 255
)

type CInstruction struct {
	ProgramIDAddress uint64
	AccountsAddress  uint64
	AccountsLength   uint64
	DataAddress      uint64
	DataLength       uint64
}

type CAccountMeta struct {
	PubkeyAddress uint64
	IsWritable    uint8
	IsSigner      uint8
	Padding       [6]byte
}

type CAccountInfo struct {
	KeyAddress      uint64
	LamportsAddress uint64
	DataLength      uint64
	DataAddress     uint64
	OwnerAddress    uint64
	RentEpoch       uint64
	IsSigner        uint8
	IsWritable      uint8
	Executable      uint8
	Padding         [5]byte
}

type CSignerSeed struct {
	Address uint64
	Length  uint64
}

type CSignerSeeds struct {
	Address uint64
	Length  uint64
}

type RustStableVec struct {
	Address  uint64
	Capacity uint64
	Length   uint64
}

type RustStableInstruction struct {
	Accounts  RustStableVec
	Data      RustStableVec
	ProgramID sdk.Pubkey
}

// CAccountInfoView is a translated C descriptor. It retains VM addresses so
// pointer-integrity checks can compare it with ABIv1 AccountRegion metadata.
type CAccountInfoView struct {
	CAccountInfo
	Key        sdk.Pubkey
	Owner      sdk.Pubkey
	Lamports   uint64
	Data       []byte
	IsSigner   bool
	IsWritable bool
	Executable bool
}

// AppendCInstruction emits the exact 40-byte C representation.
func AppendCInstruction(dst []byte, value CInstruction) []byte {
	dst = appendLayoutU64(dst, value.ProgramIDAddress)
	dst = appendLayoutU64(dst, value.AccountsAddress)
	dst = appendLayoutU64(dst, value.AccountsLength)
	dst = appendLayoutU64(dst, value.DataAddress)
	dst = appendLayoutU64(dst, value.DataLength)
	return dst
}

// AppendCAccountMeta emits the exact 16-byte C representation.
func AppendCAccountMeta(dst []byte, value CAccountMeta) []byte {
	dst = appendLayoutU64(dst, value.PubkeyAddress)
	dst = append(dst, value.IsWritable, value.IsSigner)
	dst = append(dst, value.Padding[:]...)
	return dst
}

// AppendCAccountInfo emits the exact 56-byte C representation.
func AppendCAccountInfo(dst []byte, value CAccountInfo) []byte {
	dst = appendLayoutU64(dst, value.KeyAddress)
	dst = appendLayoutU64(dst, value.LamportsAddress)
	dst = appendLayoutU64(dst, value.DataLength)
	dst = appendLayoutU64(dst, value.DataAddress)
	dst = appendLayoutU64(dst, value.OwnerAddress)
	dst = appendLayoutU64(dst, value.RentEpoch)
	dst = append(dst, value.IsSigner, value.IsWritable, value.Executable)
	dst = append(dst, value.Padding[:]...)
	return dst
}

// AppendCSignerSeed emits either SolSignerSeed or SolSignerSeeds (two u64s).
func AppendCSignerSeed(dst []byte, address, length uint64) []byte {
	dst = appendLayoutU64(dst, address)
	dst = appendLayoutU64(dst, length)
	return dst
}

// AppendRustStableInstruction emits StableInstruction's exact 80 bytes.
func AppendRustStableInstruction(dst []byte, value RustStableInstruction) []byte {
	dst = appendRustStableVec(dst, value.Accounts)
	dst = appendRustStableVec(dst, value.Data)
	dst = append(dst, value.ProgramID[:]...)
	return dst
}

func appendRustStableVec(dst []byte, value RustStableVec) []byte {
	dst = appendLayoutU64(dst, value.Address)
	dst = appendLayoutU64(dst, value.Capacity)
	dst = appendLayoutU64(dst, value.Length)
	return dst
}

func appendLayoutU64(dst []byte, value uint64) []byte {
	start := len(dst)
	dst = append(dst, make([]byte, 8)...)
	binary.LittleEndian.PutUint64(dst[start:], value)
	return dst
}

// TranslateCInstruction validates and copies one C SolInstruction from VM
// memory. Guest booleans must be canonical, as in Agave's MaybeUninit check.
func TranslateCInstruction(memory *MemoryMap, address uint64) (sdk.Instruction, error) {
	header, err := memory.Translate(address, CInstructionSize, AccessRead, 8)
	if err != nil {
		return sdk.Instruction{}, err
	}
	parsed := CInstruction{
		ProgramIDAddress: binary.LittleEndian.Uint64(header[0:8]),
		AccountsAddress:  binary.LittleEndian.Uint64(header[8:16]),
		AccountsLength:   binary.LittleEndian.Uint64(header[16:24]),
		DataAddress:      binary.LittleEndian.Uint64(header[24:32]),
		DataLength:       binary.LittleEndian.Uint64(header[32:40]),
	}
	if parsed.AccountsLength > MaxAccountsPerInstruction {
		return sdk.Instruction{}, fmt.Errorf("%w: got %d, maximum %d", ErrTooManyAccounts, parsed.AccountsLength, MaxAccountsPerInstruction)
	}
	if parsed.DataLength > MaxInstructionDataLen {
		return sdk.Instruction{}, fmt.Errorf("%w: got %d, maximum %d", ErrInstructionTooLarge, parsed.DataLength, MaxInstructionDataLen)
	}
	programBytes, err := memory.Translate(parsed.ProgramIDAddress, 32, AccessRead, 1)
	if err != nil {
		return sdk.Instruction{}, err
	}
	var programID sdk.Pubkey
	copy(programID[:], programBytes)
	metaBytes, err := translateArray(memory, parsed.AccountsAddress, parsed.AccountsLength, CAccountMetaSize, 8)
	if err != nil {
		return sdk.Instruction{}, err
	}
	accounts := make([]sdk.AccountMeta, int(parsed.AccountsLength))
	for index := range accounts {
		offset := index * CAccountMetaSize
		writable := metaBytes[offset+8]
		signer := metaBytes[offset+9]
		if writable > 1 || signer > 1 {
			return sdk.Instruction{}, fmt.Errorf("%w: account meta %d boolean", ErrInvalidABI, index)
		}
		pubkeyAddress := binary.LittleEndian.Uint64(metaBytes[offset : offset+8])
		pubkeyBytes, err := memory.Translate(pubkeyAddress, 32, AccessRead, 1)
		if err != nil {
			return sdk.Instruction{}, fmt.Errorf("account meta %d: %w", index, err)
		}
		copy(accounts[index].Pubkey[:], pubkeyBytes)
		accounts[index].IsWritable = writable == 1
		accounts[index].IsSigner = signer == 1
	}
	data, err := translateBytes(memory, parsed.DataAddress, parsed.DataLength, AccessRead)
	if err != nil {
		return sdk.Instruction{}, err
	}
	return sdk.Instruction{ProgramID: programID, Accounts: accounts, Data: append([]byte(nil), data...)}, nil
}

// TranslateRustInstruction validates the Solana SDK StableInstruction and
// StableVec representations. It never interprets Go or host Rust Vec layout.
func TranslateRustInstruction(memory *MemoryMap, address uint64) (sdk.Instruction, error) {
	header, err := memory.Translate(address, RustInstructionSize, AccessRead, 8)
	if err != nil {
		return sdk.Instruction{}, err
	}
	accountsVec := decodeRustStableVec(header[0:24])
	dataVec := decodeRustStableVec(header[24:48])
	if accountsVec.Length > accountsVec.Capacity || dataVec.Length > dataVec.Capacity {
		return sdk.Instruction{}, fmt.Errorf("%w: StableVec length exceeds capacity", ErrInvalidABI)
	}
	if accountsVec.Length > MaxAccountsPerInstruction {
		return sdk.Instruction{}, fmt.Errorf("%w: got %d, maximum %d", ErrTooManyAccounts, accountsVec.Length, MaxAccountsPerInstruction)
	}
	if dataVec.Length > MaxInstructionDataLen {
		return sdk.Instruction{}, fmt.Errorf("%w: got %d, maximum %d", ErrInstructionTooLarge, dataVec.Length, MaxInstructionDataLen)
	}
	metaBytes, err := translateArray(memory, accountsVec.Address, accountsVec.Length, RustAccountMetaSize, 1)
	if err != nil {
		return sdk.Instruction{}, err
	}
	accounts := make([]sdk.AccountMeta, int(accountsVec.Length))
	for index := range accounts {
		offset := index * RustAccountMetaSize
		copy(accounts[index].Pubkey[:], metaBytes[offset:offset+32])
		signer, writable := metaBytes[offset+32], metaBytes[offset+33]
		if signer > 1 || writable > 1 {
			return sdk.Instruction{}, fmt.Errorf("%w: account meta %d boolean", ErrInvalidABI, index)
		}
		accounts[index].IsSigner = signer == 1
		accounts[index].IsWritable = writable == 1
	}
	data, err := translateBytes(memory, dataVec.Address, dataVec.Length, AccessRead)
	if err != nil {
		return sdk.Instruction{}, err
	}
	var programID sdk.Pubkey
	copy(programID[:], header[48:80])
	return sdk.Instruction{ProgramID: programID, Accounts: accounts, Data: append([]byte(nil), data...)}, nil
}

// TranslateCAccountInfos validates C SolAccountInfo descriptors and referenced
// memory. Account data is translated for reading here even when the callee
// requests a writable account: under direct mapping, a writable account owned
// by another program is deliberately read-only to the caller. CPI privilege
// checks are performed separately and the runtime grants the callee its own
// access when executing the inner instruction.
func TranslateCAccountInfos(memory *MemoryMap, address, length uint64) ([]CAccountInfoView, error) {
	if length > MaxCPIAccountInfos {
		return nil, fmt.Errorf("%w: got %d account infos, maximum %d", ErrTooManyAccounts, length, MaxCPIAccountInfos)
	}
	encoded, err := translateArray(memory, address, length, CAccountInfoSize, 8)
	if err != nil {
		return nil, err
	}
	result := make([]CAccountInfoView, int(length))
	for index := range result {
		offset := index * CAccountInfoSize
		info := CAccountInfo{
			KeyAddress:      binary.LittleEndian.Uint64(encoded[offset : offset+8]),
			LamportsAddress: binary.LittleEndian.Uint64(encoded[offset+8 : offset+16]),
			DataLength:      binary.LittleEndian.Uint64(encoded[offset+16 : offset+24]),
			DataAddress:     binary.LittleEndian.Uint64(encoded[offset+24 : offset+32]),
			OwnerAddress:    binary.LittleEndian.Uint64(encoded[offset+32 : offset+40]),
			RentEpoch:       binary.LittleEndian.Uint64(encoded[offset+40 : offset+48]),
			IsSigner:        encoded[offset+48], IsWritable: encoded[offset+49], Executable: encoded[offset+50],
		}
		if info.IsSigner > 1 || info.IsWritable > 1 || info.Executable > 1 {
			return nil, fmt.Errorf("%w: account info %d boolean", ErrInvalidABI, index)
		}
		keyBytes, err := memory.Translate(info.KeyAddress, 32, AccessRead, 1)
		if err != nil {
			return nil, fmt.Errorf("account info %d key: %w", index, err)
		}
		ownerBytes, err := memory.Translate(info.OwnerAddress, 32, AccessRead, 1)
		if err != nil {
			return nil, fmt.Errorf("account info %d owner: %w", index, err)
		}
		lamportsBytes, err := memory.Translate(info.LamportsAddress, 8, AccessRead, 8)
		if err != nil {
			return nil, fmt.Errorf("account info %d lamports: %w", index, err)
		}
		data, err := translateBytes(memory, info.DataAddress, info.DataLength, AccessRead)
		if err != nil {
			return nil, fmt.Errorf("account info %d data: %w", index, err)
		}
		result[index].CAccountInfo = info
		copy(result[index].Key[:], keyBytes)
		copy(result[index].Owner[:], ownerBytes)
		result[index].Lamports = binary.LittleEndian.Uint64(lamportsBytes)
		result[index].Data = data
		result[index].IsSigner = info.IsSigner == 1
		result[index].IsWritable = info.IsWritable == 1
		result[index].Executable = info.Executable == 1
	}
	return result, nil
}

// TranslateCSignerSeeds copies and bounds the nested C signer-seed arrays.
func TranslateCSignerSeeds(memory *MemoryMap, address, length uint64) ([][][]byte, error) {
	if length > MaxCPISigners {
		return nil, fmt.Errorf("runtime: too many CPI signers: %d > %d", length, MaxCPISigners)
	}
	outer, err := translateArray(memory, address, length, CSignerSeedsSize, 8)
	if err != nil {
		return nil, err
	}
	result := make([][][]byte, int(length))
	for signerIndex := range result {
		offset := signerIndex * CSignerSeedsSize
		seedsAddress := binary.LittleEndian.Uint64(outer[offset : offset+8])
		seedsLength := binary.LittleEndian.Uint64(outer[offset+8 : offset+16])
		if seedsLength > sdk.MaxSeeds {
			return nil, fmt.Errorf("signer %d: %w", signerIndex, BuiltinProgramError(ProgramErrorMaxSeedLengthExceeded))
		}
		seedHeaders, err := translateArray(memory, seedsAddress, seedsLength, CSignerSeedSize, 8)
		if err != nil {
			return nil, fmt.Errorf("signer %d: %w", signerIndex, err)
		}
		result[signerIndex] = make([][]byte, int(seedsLength))
		for seedIndex := range result[signerIndex] {
			seedOffset := seedIndex * CSignerSeedSize
			seedAddress := binary.LittleEndian.Uint64(seedHeaders[seedOffset : seedOffset+8])
			seedLength := binary.LittleEndian.Uint64(seedHeaders[seedOffset+8 : seedOffset+16])
			if seedLength > sdk.MaxSeedLength {
				return nil, fmt.Errorf("signer %d seed %d: %w", signerIndex, seedIndex, BuiltinProgramError(ProgramErrorMaxSeedLengthExceeded))
			}
			seed, err := memory.Translate(seedAddress, seedLength, AccessRead, 1)
			if err != nil {
				return nil, fmt.Errorf("signer %d seed %d: %w", signerIndex, seedIndex, err)
			}
			result[signerIndex][seedIndex] = append([]byte(nil), seed...)
		}
	}
	return result, nil
}

func decodeRustStableVec(data []byte) RustStableVec {
	return RustStableVec{
		Address:  binary.LittleEndian.Uint64(data[0:8]),
		Capacity: binary.LittleEndian.Uint64(data[8:16]),
		Length:   binary.LittleEndian.Uint64(data[16:24]),
	}
}

func translateArray(memory *MemoryMap, address, count uint64, elementSize int, alignment uint64) ([]byte, error) {
	if count > ^uint64(0)/uint64(elementSize) {
		return nil, ErrInvalidLength
	}
	if count == 0 {
		return []byte{}, nil
	}
	return memory.Translate(address, count*uint64(elementSize), AccessRead, alignment)
}

func translateBytes(memory *MemoryMap, address, length uint64, access AccessType) ([]byte, error) {
	if length == 0 {
		return []byte{}, nil
	}
	return memory.Translate(address, length, access, 1)
}
