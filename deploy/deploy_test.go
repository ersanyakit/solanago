package deploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sbpfelf "github.com/ersanyakit/go-solana/elf"
	"github.com/ersanyakit/go-solana/sbpf"
	"github.com/ersanyakit/go-solana/sdk"
	"github.com/ersanyakit/go-solana/sdk/loader"
	"github.com/ersanyakit/go-solana/svmtest"
)

func TestKeypairRoundTripAndNoOverwrite(t *testing.T) {
	signer, err := svmtest.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id.json")
	if err := SaveKeypair(path, signer, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("keypair permissions = %#o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var canonical []int
	if err := json.Unmarshal(raw, &canonical); err != nil || len(canonical) != ed25519.PrivateKeySize {
		t.Fatalf("keypair is not the canonical 64-number JSON array: %s (error %v)", raw, err)
	}
	for index, value := range canonical {
		if value != int(signer.Private[index]) {
			t.Fatalf("keypair byte %d = %d, want %d", index, value, signer.Private[index])
		}
	}
	loaded, err := LoadKeypair(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PublicKey != signer.PublicKey || !loaded.Private.Equal(signer.Private) {
		t.Fatal("keypair changed across round trip")
	}
	if err := SaveKeypair(path, signer, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite error = %v", err)
	}
}

func TestLoadKeypairAcceptsLegacyBase64Encoding(t *testing.T) {
	signer := newSigner(t)
	legacy, err := json.Marshal([]byte(signer.Private))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadKeypair(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PublicKey != signer.PublicKey || !loaded.Private.Equal(signer.Private) {
		t.Fatal("legacy base64 keypair changed while loading")
	}
}

func TestLoadKeypairRejectsMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("[1,2,3]"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadKeypair(path)
	if !errors.Is(err, ErrInvalidKeypair) {
		t.Fatalf("error = %v", err)
	}
	_ = ed25519.PrivateKeySize // compile-time documentation of required width
}

func TestKeypairOperationsRejectForgedPublicSuffix(t *testing.T) {
	forged := forgedSigner(t)
	path := filepath.Join(t.TempDir(), "forged.json")
	encoded, err := json.Marshal([]byte(forged.Private))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeypair(path); !errors.Is(err, ErrInvalidKeypair) {
		t.Fatalf("LoadKeypair error = %v, want ErrInvalidKeypair", err)
	}
	if err := SaveKeypair(filepath.Join(t.TempDir(), "saved.json"), forged, false); !errors.Is(err, ErrInvalidKeypair) {
		t.Fatalf("SaveKeypair error = %v, want ErrInvalidKeypair", err)
	}
}

func TestProgramRejectsEveryNonCanonicalSignerBeforeRPC(t *testing.T) {
	artifact := validELF(t)
	payer := newSigner(t)
	program := newSigner(t)
	authority := newSigner(t)
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"fee payer", func(config *Config) { config.FeePayer = forgedSigner(t) }},
		{"program", func(config *Config) { config.Program = forgedSigner(t) }},
		{"upgrade authority", func(config *Config) { config.UpgradeAuthority = forgedSigner(t) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &scriptedDeployClient{elf: artifact}
			config := Config{Client: client, FeePayer: payer, Program: program, UpgradeAuthority: authority}
			test.mutate(&config)
			_, err := Program(context.Background(), config, artifact)
			if !errors.Is(err, ErrInvalidKeypair) {
				t.Fatalf("Program error = %v, want ErrInvalidKeypair", err)
			}
			if client.getAccountCalls != 0 {
				t.Fatal("RPC was called before signer validation")
			}
		})
	}
}

func TestProgramRecordsAmbiguousCreateSignature(t *testing.T) {
	artifact := validELF(t)
	ambiguous := errors.New("confirmation outcome unknown")
	client := &scriptedDeployClient{elf: artifact, createSignature: "create-local-signature", createErr: ambiguous}
	result, err := Program(context.Background(), Config{
		Client: client, FeePayer: newSigner(t), Program: newSigner(t),
	}, artifact)
	if !errors.Is(err, ambiguous) {
		t.Fatalf("Program error = %v, want ambiguous create error", err)
	}
	if result == nil || len(result.SubmittedSignatures) != 1 || result.SubmittedSignatures[0] != client.createSignature {
		t.Fatalf("submitted journal = %#v", result)
	}
	if len(result.Signatures) != 0 {
		t.Fatalf("ambiguous create recorded as finalized: %#v", result.Signatures)
	}
}

func TestProgramRecordsAmbiguousFinalDeploySignature(t *testing.T) {
	artifact := validELF(t)
	ambiguous := errors.New("finalization outcome unknown")
	payer := newSigner(t)
	client := &scriptedDeployClient{
		elf: artifact, createSignature: "create-finalized", finalSignature: "deploy-local-signature", finalErr: ambiguous,
		authority: payer.PublicKey,
	}
	result, err := Program(context.Background(), Config{
		Client: client, FeePayer: payer, Program: newSigner(t),
	}, artifact)
	if !errors.Is(err, ambiguous) {
		t.Fatalf("Program error = %v, want ambiguous final deploy error", err)
	}
	if result == nil || !containsString(result.SubmittedSignatures, client.finalSignature) {
		t.Fatalf("submitted journal does not contain final signature: %#v", result)
	}
	if containsString(result.Signatures, client.finalSignature) {
		t.Fatalf("ambiguous final deploy recorded as finalized: %#v", result.Signatures)
	}
}

func TestProgramVerifiesFinalizedProgramDataBytes(t *testing.T) {
	artifact := validELF(t)
	payer := newSigner(t)
	program := newSigner(t)
	programData, err := loader.ProgramDataAddress(program.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedDeployClient{
		elf: artifact, createSignature: "create-finalized", finalSignature: "deploy-finalized",
		programID: program.PublicKey, programData: programData, authority: payer.PublicKey,
	}
	result, err := Program(context.Background(), Config{Client: client, FeePayer: payer, Program: program}, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Finalized || result.ProgramDataAddress != programData {
		t.Fatalf("result = %+v", result)
	}
}

func TestProgramRejectsCorruptFinalizedProgramData(t *testing.T) {
	artifact := validELF(t)
	payer := newSigner(t)
	program := newSigner(t)
	programData, err := loader.ProgramDataAddress(program.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedDeployClient{
		elf: artifact, createSignature: "create-finalized", finalSignature: "deploy-finalized",
		programID: program.PublicKey, programData: programData, authority: payer.PublicKey,
		corruptProgramData: true,
	}
	result, err := Program(context.Background(), Config{Client: client, FeePayer: payer, Program: program}, artifact)
	if err == nil || !strings.Contains(err.Error(), "ProgramData bytes") {
		t.Fatalf("Program error = %v, want finalized ProgramData mismatch", err)
	}
	if result.Finalized {
		t.Fatalf("corrupt ProgramData marked finalized: %+v", result)
	}
}

func TestResumeProgramRewritesFromZeroAndVerifiesFinalState(t *testing.T) {
	artifact := validELF(t)
	payer := newSigner(t)
	program := newSigner(t)
	buffer := newSigner(t).PublicKey
	programData, err := loader.ProgramDataAddress(program.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client := newResumeDeployClient(artifact, payer.PublicKey, program.PublicKey, programData, buffer)
	var stages []Stage
	result, err := ResumeProgram(context.Background(), Config{
		Client: client, FeePayer: payer, Program: program, ChunkSize: 17,
		Progress: func(stage Stage) { stages = append(stages, stage) },
	}, buffer, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Finalized || result.BufferAddress != buffer || result.ProgramDataAddress != programData {
		t.Fatalf("resume result = %+v", result)
	}
	if client.finalDeployCalls != 1 {
		t.Fatalf("final deploy calls = %d, want 1", client.finalDeployCalls)
	}
	wantWrites := (len(artifact) + 16) / 17
	if len(client.writeOffsets) != wantWrites {
		t.Fatalf("write offsets = %v, want %d writes", client.writeOffsets, wantWrites)
	}
	for index, offset := range client.writeOffsets {
		if want := index * 17; offset != want {
			t.Fatalf("write offset %d = %d, want %d", index, offset, want)
		}
	}
	if len(stages) != wantWrites+1 || stages[0].Kind != "write" || stages[0].Offset != 0 || stages[len(stages)-1].Kind != "deploy" {
		t.Fatalf("stages = %+v", stages)
	}
	if !bytes.Equal(client.bufferData[loader.BufferMetadataSize:], artifact) {
		t.Fatal("resume did not rewrite the complete buffer payload")
	}
}

func TestResumeProgramRejectsMalformedBufferBeforeWrites(t *testing.T) {
	artifact := validELF(t)
	payer := newSigner(t)
	program := newSigner(t)
	buffer := newSigner(t).PublicKey
	programData, err := loader.ProgramDataAddress(program.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	validData := canonicalBufferData(make([]byte, len(artifact)), payer.PublicKey)
	account := func(owner string, executable bool, data any) *svmtest.AccountInfo {
		return &svmtest.AccountInfo{Lamports: 10, Owner: owner, Executable: executable, Data: data}
	}
	encoded := func(data []byte) any {
		return []any{base64.StdEncoding.EncodeToString(data), "base64"}
	}
	tests := []struct {
		name    string
		account *svmtest.AccountInfo
		want    string
	}{
		{"missing", nil, "missing"},
		{"wrong owner", account(sdk.Pubkey{}.String(), false, encoded(validData)), "wrong owner"},
		{"executable", account(loader.ProgramID.String(), true, encoded(validData)), "must not be executable"},
		{"malformed encoded bytes", account(loader.ProgramID.String(), false, []any{"%%%", "base64"}), "decode loader buffer"},
		{"truncated", account(loader.ProgramID.String(), false, encoded(validData[:loader.BufferMetadataSize-1])), "truncated"},
		{"wrong tag", account(loader.ProgramID.String(), false, encoded(func() []byte {
			data := append([]byte(nil), validData...)
			binary.LittleEndian.PutUint32(data[:4], 3)
			return data
		}())), "not in Buffer state"},
		{"no authority", account(loader.ProgramID.String(), false, encoded(func() []byte {
			data := append([]byte(nil), validData...)
			data[4] = 0
			return data
		}())), "no upgrade authority"},
		{"short capacity", account(loader.ProgramID.String(), false, encoded(validData[:len(validData)-1])), "payload capacity"},
		{"long capacity", account(loader.ProgramID.String(), false, encoded(append(append([]byte(nil), validData...), 0))), "payload capacity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newResumeDeployClient(artifact, payer.PublicKey, program.PublicKey, programData, buffer)
			client.initialBufferAccount = test.account
			client.overrideInitialBuffer = true
			result, err := ResumeProgram(context.Background(), Config{Client: client, FeePayer: payer, Program: program}, buffer, artifact)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ResumeProgram error = %v, want %q", err, test.want)
			}
			if result == nil || result.BufferAddress != buffer {
				t.Fatalf("partial result = %+v", result)
			}
			if len(client.writeOffsets) != 0 || client.finalDeployCalls != 0 {
				t.Fatalf("invalid buffer caused transactions: writes=%v deploys=%d", client.writeOffsets, client.finalDeployCalls)
			}
		})
	}
}

func TestResumeProgramRejectsWrongBufferAuthority(t *testing.T) {
	artifact := validELF(t)
	payer := newSigner(t)
	program := newSigner(t)
	buffer := newSigner(t).PublicKey
	programData, err := loader.ProgramDataAddress(program.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client := newResumeDeployClient(artifact, newSigner(t).PublicKey, program.PublicKey, programData, buffer)
	_, err = ResumeProgram(context.Background(), Config{Client: client, FeePayer: payer, Program: program}, buffer, artifact)
	if err == nil || !strings.Contains(err.Error(), "upgrade authority does not match") {
		t.Fatalf("ResumeProgram error = %v, want authority mismatch", err)
	}
	if len(client.writeOffsets) != 0 || client.finalDeployCalls != 0 {
		t.Fatalf("wrong authority caused transactions: writes=%v deploys=%d", client.writeOffsets, client.finalDeployCalls)
	}
}

func TestUpgradeReplacesLiveProgramAndVerifiesFinalState(t *testing.T) {
	currentELF := validELF(t)
	newELF := validELF(t)
	payer := newSigner(t)
	authority := newSigner(t)
	program := newSigner(t).PublicKey
	client := newUpgradeDeployClient(currentELF, newELF, authority.PublicKey, program)
	var stages []Stage
	result, err := Upgrade(context.Background(), Config{
		Client: client, FeePayer: payer, UpgradeAuthority: authority, ChunkSize: 17,
		Progress: func(stage Stage) { stages = append(stages, stage) },
	}, program, newELF)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Finalized || result.ProgramID != program {
		t.Fatalf("upgrade result = %+v", result)
	}
	if stages[len(stages)-1].Kind != "upgrade" {
		t.Fatalf("stages = %+v", stages)
	}
	if !bytes.Equal(client.programAfterUpgrade, newELF) {
		t.Fatal("upgrade did not replace the finalized ProgramData bytes")
	}
	// The spill destination defaults to the fee payer when unset.
	if client.lastSpill != payer.PublicKey {
		t.Fatalf("spill address = %s, want fee payer %s", client.lastSpill, payer.PublicKey)
	}
}

func TestUpgradeRejectsMissingOrNonExecutableProgram(t *testing.T) {
	currentELF := validELF(t)
	newELF := validELF(t)
	authority := newSigner(t)
	program := newSigner(t).PublicKey
	for _, test := range []struct {
		name   string
		mutate func(*upgradeDeployClient)
	}{
		{"missing", func(c *upgradeDeployClient) { c.missingProgram = true }},
		{"not executable", func(c *upgradeDeployClient) { c.notExecutableProgram = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newUpgradeDeployClient(currentELF, newELF, authority.PublicKey, program)
			test.mutate(client)
			_, err := Upgrade(context.Background(), Config{
				Client: client, FeePayer: newSigner(t), UpgradeAuthority: authority,
			}, program, newELF)
			if !errors.Is(err, ErrProgramNotFound) {
				t.Fatalf("Upgrade error = %v, want ErrProgramNotFound", err)
			}
			if client.sendCalls != 0 {
				t.Fatal("transaction was submitted before existence check")
			}
		})
	}
}

func TestUpgradeRejectsAuthorityMismatchBeforeAnyTransaction(t *testing.T) {
	currentELF := validELF(t)
	newELF := validELF(t)
	onChainAuthority := newSigner(t)
	wrongAuthority := newSigner(t)
	program := newSigner(t).PublicKey
	client := newUpgradeDeployClient(currentELF, newELF, onChainAuthority.PublicKey, program)
	_, err := Upgrade(context.Background(), Config{
		Client: client, FeePayer: newSigner(t), UpgradeAuthority: wrongAuthority,
	}, program, newELF)
	if !errors.Is(err, ErrUpgradeAuthorityMismatch) {
		t.Fatalf("Upgrade error = %v, want ErrUpgradeAuthorityMismatch", err)
	}
	if client.sendCalls != 0 {
		t.Fatal("transaction was submitted before authority check")
	}
}

func TestUpgradeRejectsELFLargerThanAllocatedCapacity(t *testing.T) {
	currentELF := validELF(t)
	authority := newSigner(t)
	program := newSigner(t).PublicKey
	oversized := append(append([]byte(nil), currentELF...), 0, 0, 0, 0)
	client := newUpgradeDeployClient(currentELF, oversized, authority.PublicKey, program)
	_, err := Upgrade(context.Background(), Config{
		Client: client, FeePayer: newSigner(t), UpgradeAuthority: authority,
	}, program, oversized)
	if !errors.Is(err, ErrProgramTooLargeForUpgrade) {
		t.Fatalf("Upgrade error = %v, want ErrProgramTooLargeForUpgrade", err)
	}
	if client.sendCalls != 0 {
		t.Fatal("transaction was submitted before capacity check")
	}
}

func TestUpgradeRejectsCorruptFinalizedBuffer(t *testing.T) {
	currentELF := validELF(t)
	newELF := validELF(t)
	authority := newSigner(t)
	program := newSigner(t).PublicKey
	client := newUpgradeDeployClient(currentELF, newELF, authority.PublicKey, program)
	client.corruptFinalBuffer = true
	_, err := Upgrade(context.Background(), Config{
		Client: client, FeePayer: newSigner(t), UpgradeAuthority: authority,
	}, program, newELF)
	if err == nil || !strings.Contains(err.Error(), "do not match the strict ELF") {
		t.Fatalf("Upgrade error = %v, want finalized buffer mismatch", err)
	}
	if client.upgradeCalls != 0 {
		t.Fatal("upgrade instruction was submitted despite corrupt buffer")
	}
}

func TestUpgradeRecordsAmbiguousSignature(t *testing.T) {
	currentELF := validELF(t)
	newELF := validELF(t)
	authority := newSigner(t)
	program := newSigner(t).PublicKey
	client := newUpgradeDeployClient(currentELF, newELF, authority.PublicKey, program)
	ambiguous := errors.New("upgrade confirmation outcome unknown")
	client.upgradeSignature = "upgrade-local-signature"
	client.upgradeErr = ambiguous
	result, err := Upgrade(context.Background(), Config{
		Client: client, FeePayer: newSigner(t), UpgradeAuthority: authority,
	}, program, newELF)
	if !errors.Is(err, ambiguous) {
		t.Fatalf("Upgrade error = %v, want ambiguous upgrade error", err)
	}
	if !containsString(result.SubmittedSignatures, client.upgradeSignature) {
		t.Fatalf("submitted journal does not contain upgrade signature: %#v", result)
	}
	if containsString(result.Signatures, client.upgradeSignature) {
		t.Fatalf("ambiguous upgrade recorded as finalized: %#v", result.Signatures)
	}
}

type upgradeDeployClient struct {
	currentELF                           []byte
	authority, programID, programData    sdk.Pubkey
	maxDataLen                           int
	buffer                               sdk.Pubkey
	bufferData                           []byte
	missingProgram, notExecutableProgram bool
	corruptFinalBuffer                   bool
	programAfterUpgrade                  []byte
	lastSpill                            sdk.Pubkey
	writeOffsets                         []int
	sendCalls, upgradeCalls              int
	upgradeSignature                     string
	upgradeErr                           error
}

func newUpgradeDeployClient(currentELF, newELF []byte, authority, programID sdk.Pubkey) *upgradeDeployClient {
	programData, _ := loader.ProgramDataAddress(programID)
	return &upgradeDeployClient{
		currentELF: currentELF, authority: authority, programID: programID, programData: programData,
		maxDataLen:       len(currentELF),
		bufferData:       canonicalBufferData(make([]byte, len(newELF)), authority),
		upgradeSignature: "upgrade-finalized",
	}
}

func (client *upgradeDeployClient) GetAccountInfo(_ context.Context, address sdk.Pubkey) (*svmtest.AccountInfo, error) {
	switch address {
	case client.programID:
		if client.missingProgram {
			return nil, nil
		}
		data := make([]byte, loader.ProgramMetadataSize)
		binary.LittleEndian.PutUint32(data[:4], 2)
		copy(data[4:], client.programData[:])
		return encodedAccount(loader.ProgramID.String(), !client.notExecutableProgram, 1, data), nil
	case client.programData:
		programBytes := make([]byte, client.maxDataLen)
		if client.programAfterUpgrade != nil {
			copy(programBytes, client.programAfterUpgrade)
		} else {
			copy(programBytes, client.currentELF)
		}
		data := make([]byte, loader.ProgramDataMetadataSize+client.maxDataLen)
		binary.LittleEndian.PutUint32(data[:4], 3)
		binary.LittleEndian.PutUint64(data[4:12], 5)
		data[12] = 1
		copy(data[13:loader.ProgramDataMetadataSize], client.authority[:])
		copy(data[loader.ProgramDataMetadataSize:], programBytes)
		return encodedAccount(loader.ProgramID.String(), false, 1, data), nil
	default:
		if client.buffer == (sdk.Pubkey{}) {
			client.buffer = address
		}
		if address != client.buffer {
			return nil, fmt.Errorf("unexpected account lookup %s", address)
		}
		payload := client.bufferData
		if client.corruptFinalBuffer {
			payload = append([]byte(nil), payload...)
			payload[len(payload)-1] ^= 0xff
		}
		return encodedAccount(loader.ProgramID.String(), false, 10, payload), nil
	}
}

func (*upgradeDeployClient) GenesisHash(context.Context) (string, error) {
	return "upgrade-genesis", nil
}

func (*upgradeDeployClient) MinimumBalanceForRentExemption(context.Context, uint64) (uint64, error) {
	return 1, nil
}

func (*upgradeDeployClient) Balance(context.Context, sdk.Pubkey) (uint64, error) {
	return 1_000_000, nil
}

func (client *upgradeDeployClient) SendInstructions(_ context.Context, _ svmtest.Signer, _ []svmtest.Signer, instructions []sdk.Instruction) (string, error) {
	client.sendCalls++
	if len(instructions) == 2 {
		return "upgrade-create-buffer", nil
	}
	if len(instructions) != 1 || len(instructions[0].Data) != 4 || binary.LittleEndian.Uint32(instructions[0].Data) != 3 {
		return "", fmt.Errorf("unexpected upgrade instructions: %#v", instructions)
	}
	client.upgradeCalls++
	client.lastSpill = instructions[0].Accounts[3].Pubkey
	if client.upgradeErr != nil {
		return client.upgradeSignature, client.upgradeErr
	}
	client.programAfterUpgrade = append([]byte(nil), client.bufferData[loader.BufferMetadataSize:]...)
	return client.upgradeSignature, nil
}

func (client *upgradeDeployClient) SendInstructionsConfirmed(_ context.Context, _ svmtest.Signer, _ []svmtest.Signer, instructions []sdk.Instruction) (string, error) {
	if len(instructions) != 1 || len(instructions[0].Data) < 16 || binary.LittleEndian.Uint32(instructions[0].Data[:4]) != 1 {
		return "", fmt.Errorf("unexpected buffer write: %#v", instructions)
	}
	data := instructions[0].Data
	offset := int(binary.LittleEndian.Uint32(data[4:8]))
	length := int(binary.LittleEndian.Uint64(data[8:16]))
	if length != len(data)-16 || offset < 0 || offset+length > len(client.bufferData)-loader.BufferMetadataSize {
		return "", fmt.Errorf("invalid write offset=%d length=%d", offset, length)
	}
	copy(client.bufferData[loader.BufferMetadataSize+offset:], data[16:])
	client.writeOffsets = append(client.writeOffsets, offset)
	return fmt.Sprintf("upgrade-write-%d", len(client.writeOffsets)), nil
}

func (*upgradeDeployClient) WaitForFinalized(context.Context, string) error { return nil }

type resumeDeployClient struct {
	elf                   []byte
	authority, programID  sdk.Pubkey
	programData, buffer   sdk.Pubkey
	bufferData            []byte
	initialBufferAccount  *svmtest.AccountInfo
	overrideInitialBuffer bool
	initialBufferRead     bool
	writeOffsets          []int
	finalDeployCalls      int
}

func newResumeDeployClient(elf []byte, authority, programID, programData, buffer sdk.Pubkey) *resumeDeployClient {
	return &resumeDeployClient{
		elf: elf, authority: authority, programID: programID, programData: programData, buffer: buffer,
		bufferData: canonicalBufferData(make([]byte, len(elf)), authority),
	}
}

func (client *resumeDeployClient) GetAccountInfo(_ context.Context, address sdk.Pubkey) (*svmtest.AccountInfo, error) {
	switch address {
	case client.programID:
		if client.finalDeployCalls == 0 {
			return nil, nil
		}
		data := make([]byte, loader.ProgramMetadataSize)
		binary.LittleEndian.PutUint32(data[:4], 2)
		copy(data[4:], client.programData[:])
		return encodedAccount(loader.ProgramID.String(), true, 1, data), nil
	case client.buffer:
		if !client.initialBufferRead {
			client.initialBufferRead = true
			if client.overrideInitialBuffer {
				return client.initialBufferAccount, nil
			}
		}
		return encodedAccount(loader.ProgramID.String(), false, 10, client.bufferData), nil
	case client.programData:
		data := make([]byte, loader.ProgramDataMetadataSize+len(client.elf))
		binary.LittleEndian.PutUint32(data[:4], 3)
		binary.LittleEndian.PutUint64(data[4:12], 99)
		data[12] = 1
		copy(data[13:loader.ProgramDataMetadataSize], client.authority[:])
		copy(data[loader.ProgramDataMetadataSize:], client.elf)
		return encodedAccount(loader.ProgramID.String(), false, 1, data), nil
	default:
		return nil, fmt.Errorf("unexpected account lookup %s", address)
	}
}

func (*resumeDeployClient) GenesisHash(context.Context) (string, error) { return "resume-genesis", nil }

func (*resumeDeployClient) MinimumBalanceForRentExemption(context.Context, uint64) (uint64, error) {
	return 1, nil
}

func (*resumeDeployClient) Balance(context.Context, sdk.Pubkey) (uint64, error) {
	return 1_000_000, nil
}

func (client *resumeDeployClient) SendInstructions(_ context.Context, _ svmtest.Signer, _ []svmtest.Signer, instructions []sdk.Instruction) (string, error) {
	client.finalDeployCalls++
	if len(instructions) != 2 || len(instructions[1].Data) < 4 || binary.LittleEndian.Uint32(instructions[1].Data[:4]) != 2 {
		return "", fmt.Errorf("unexpected final deploy instructions: %#v", instructions)
	}
	return "resume-deploy-finalized", nil
}

func (client *resumeDeployClient) SendInstructionsConfirmed(_ context.Context, _ svmtest.Signer, _ []svmtest.Signer, instructions []sdk.Instruction) (string, error) {
	if len(instructions) != 1 || len(instructions[0].Data) < 16 || binary.LittleEndian.Uint32(instructions[0].Data[:4]) != 1 {
		return "", fmt.Errorf("unexpected buffer write: %#v", instructions)
	}
	data := instructions[0].Data
	offset := int(binary.LittleEndian.Uint32(data[4:8]))
	length := int(binary.LittleEndian.Uint64(data[8:16]))
	if length != len(data)-16 || offset < 0 || offset+length > len(client.elf) {
		return "", fmt.Errorf("invalid write offset=%d length=%d", offset, length)
	}
	client.writeOffsets = append(client.writeOffsets, offset)
	copy(client.bufferData[loader.BufferMetadataSize+offset:], data[16:])
	return fmt.Sprintf("resume-write-%d", len(client.writeOffsets)), nil
}

func (*resumeDeployClient) WaitForFinalized(context.Context, string) error { return nil }

func encodedAccount(owner string, executable bool, lamports uint64, data []byte) *svmtest.AccountInfo {
	return &svmtest.AccountInfo{
		Lamports: lamports, Owner: owner, Executable: executable,
		Data: []any{base64.StdEncoding.EncodeToString(data), "base64"},
	}
}

type scriptedDeployClient struct {
	elf                                    []byte
	createSignature, finalSignature        string
	createErr, finalErr                    error
	getAccountCalls, sendCalls, writeCalls int
	programID, programData, authority      sdk.Pubkey
	corruptProgramData                     bool
}

func (client *scriptedDeployClient) GetAccountInfo(_ context.Context, address sdk.Pubkey) (*svmtest.AccountInfo, error) {
	client.getAccountCalls++
	switch client.getAccountCalls {
	case 1:
		return nil, nil
	case 2:
		data := canonicalBufferData(client.elf, client.authority)
		return &svmtest.AccountInfo{
			Owner: loader.ProgramID.String(), Data: []any{base64.StdEncoding.EncodeToString(data), "base64"},
		}, nil
	case 3:
		data := make([]byte, loader.ProgramMetadataSize)
		binary.LittleEndian.PutUint32(data[:4], 2)
		copy(data[4:], client.programData[:])
		return &svmtest.AccountInfo{
			Owner: loader.ProgramID.String(), Executable: true,
			Data: []any{base64.StdEncoding.EncodeToString(data), "base64"},
		}, nil
	case 4:
		if address != client.programData {
			return nil, fmt.Errorf("unexpected ProgramData address %s", address)
		}
		data := make([]byte, loader.ProgramDataMetadataSize+len(client.elf))
		binary.LittleEndian.PutUint32(data[:4], 3)
		binary.LittleEndian.PutUint64(data[4:12], 99)
		data[12] = 1
		copy(data[13:loader.ProgramDataMetadataSize], client.authority[:])
		copy(data[loader.ProgramDataMetadataSize:], client.elf)
		if client.corruptProgramData {
			data[len(data)-1] ^= 0xff
		}
		return &svmtest.AccountInfo{
			Owner: loader.ProgramID.String(),
			Data:  []any{base64.StdEncoding.EncodeToString(data), "base64"},
		}, nil
	default:
		return nil, fmt.Errorf("unexpected account lookup %d for %s", client.getAccountCalls, address)
	}
}

func canonicalBufferData(payload []byte, authority sdk.Pubkey) []byte {
	data := make([]byte, loader.BufferMetadataSize+len(payload))
	binary.LittleEndian.PutUint32(data[:4], 1)
	data[4] = 1
	copy(data[5:loader.BufferMetadataSize], authority[:])
	copy(data[loader.BufferMetadataSize:], payload)
	return data
}

func (*scriptedDeployClient) GenesisHash(context.Context) (string, error) { return "test-genesis", nil }

func (*scriptedDeployClient) MinimumBalanceForRentExemption(context.Context, uint64) (uint64, error) {
	return 1, nil
}

func (*scriptedDeployClient) Balance(context.Context, sdk.Pubkey) (uint64, error) {
	return 1_000_000, nil
}

func (client *scriptedDeployClient) SendInstructions(_ context.Context, _ svmtest.Signer, _ []svmtest.Signer, _ []sdk.Instruction) (string, error) {
	client.sendCalls++
	if client.sendCalls == 1 {
		return client.createSignature, client.createErr
	}
	return client.finalSignature, client.finalErr
}

func (client *scriptedDeployClient) SendInstructionsConfirmed(_ context.Context, _ svmtest.Signer, _ []svmtest.Signer, _ []sdk.Instruction) (string, error) {
	client.writeCalls++
	return fmt.Sprintf("write-%d", client.writeCalls), nil
}

func (*scriptedDeployClient) WaitForFinalized(context.Context, string) error { return nil }

func validELF(t *testing.T) []byte {
	t.Helper()
	text, err := sbpf.Encode([]sbpf.Instruction{
		sbpf.ALUImm(sbpf.MOV64_IMM, sbpf.R0, 0),
		sbpf.Return(),
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := sbpfelf.BuildV3(text, 0)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func newSigner(t *testing.T) svmtest.Signer {
	t.Helper()
	signer, err := svmtest.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func forgedSigner(t *testing.T) svmtest.Signer {
	t.Helper()
	signer := newSigner(t)
	signer.Private = append(ed25519.PrivateKey(nil), signer.Private...)
	signer.Private[ed25519.SeedSize] ^= 0xff
	copy(signer.PublicKey[:], signer.Private[ed25519.SeedSize:])
	return signer
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
