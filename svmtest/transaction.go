// Package svmtest provides transaction-level test tooling for programs built
// by go-solana. It talks to a real Agave validator over JSON-RPC; it is not a
// second in-process emulator.
package svmtest

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/ersanyakit/solanago/sdk"
)

var (
	ErrMissingSigner        = errors.New("svmtest: missing transaction signer")
	ErrInvalidSigner        = errors.New("svmtest: invalid transaction signer")
	ErrTooManyAccounts      = errors.New("svmtest: message exceeds 256 accounts")
	ErrLegacyHeaderOverflow = errors.New("svmtest: transaction header count exceeds 255")
	ErrInvalidBlockhash     = errors.New("svmtest: invalid recent blockhash")
	ErrInvalidTransaction   = errors.New("svmtest: malformed signed transaction")
)

const versionedTransactionV0 = byte(0x80)

// Signer is an Ed25519 keypair used by the transaction harness.
type Signer struct {
	PublicKey sdk.Pubkey
	Private   ed25519.PrivateKey
}

// NewSigner generates a new transaction signer using crypto/rand.
func NewSigner() (Signer, error) {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return Signer{}, err
	}
	return SignerFromPrivateKey(private)
}

// SignerFromPrivateKey accepts only the canonical Ed25519 seed||public-key
// representation used by Solana keypair files. Recomputing the complete key
// from the seed prevents a forged trailing public-key half from being trusted.
func SignerFromPrivateKey(private ed25519.PrivateKey) (Signer, error) {
	if len(private) != ed25519.PrivateKeySize {
		return Signer{}, ErrInvalidSigner
	}
	canonical := ed25519.NewKeyFromSeed(private[:ed25519.SeedSize])
	if !private.Equal(canonical) {
		return Signer{}, fmt.Errorf("%w: public key does not match seed", ErrInvalidSigner)
	}
	publicKey, err := sdk.PubkeyFromBytes(canonical[ed25519.SeedSize:])
	if err != nil {
		return Signer{}, fmt.Errorf("%w: %v", ErrInvalidSigner, err)
	}
	return Signer{PublicKey: publicKey, Private: append(ed25519.PrivateKey(nil), canonical...)}, nil
}

// ValidateSigner verifies both halves of a Solana Ed25519 keypair and its
// separately declared public key.
func ValidateSigner(signer Signer) error {
	canonical, err := SignerFromPrivateKey(signer.Private)
	if err != nil {
		return err
	}
	if signer.PublicKey != canonical.PublicKey {
		return fmt.Errorf("%w: declared public key does not match seed", ErrInvalidSigner)
	}
	return nil
}

// BuildLegacyTransaction compiles and signs a canonical legacy transaction.
// The fee payer is always the first writable signer. Privileges requested by
// duplicate metas are unioned before canonical account ordering.
func BuildLegacyTransaction(feePayer Signer, signers []Signer, recentBlockhash string, instructions []sdk.Instruction) ([]byte, error) {
	message, accountKeys, numRequiredSignatures, err := buildCanonicalTransactionPayload(feePayer, recentBlockhash, instructions, false)
	if err != nil {
		return nil, err
	}
	return signTransaction(message, accountKeys, numRequiredSignatures, feePayer, signers)
}

// BuildVersionedTransaction compiles and signs a canonical version 0 transaction.
// Address table lookups are intentionally empty; all accounts are included in the
// static account set, which is suitable for deterministic harness submissions.
func BuildVersionedTransaction(feePayer Signer, signers []Signer, recentBlockhash string, instructions []sdk.Instruction) ([]byte, error) {
	message, accountKeys, numRequiredSignatures, err := buildCanonicalTransactionPayload(feePayer, recentBlockhash, instructions, true)
	if err != nil {
		return nil, err
	}
	return signTransaction(message, accountKeys, numRequiredSignatures, feePayer, signers)
}

func buildCanonicalTransactionPayload(feePayer Signer, recentBlockhash string, instructions []sdk.Instruction, versioned bool) ([]byte, []sdk.Pubkey, int, error) {
	blockhash, err := sdk.ParsePubkey(recentBlockhash)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("%w: %v", ErrInvalidBlockhash, err)
	}
	if err := ValidateSigner(feePayer); err != nil {
		return nil, nil, 0, fmt.Errorf("%w: invalid fee payer keypair: %w", ErrMissingSigner, err)
	}

	type privileges struct {
		signer, writable bool
		order            int
	}
	privs := map[sdk.Pubkey]privileges{
		feePayer.PublicKey: {signer: true, writable: true, order: 0},
	}
	nextOrder := 1
	merge := func(key sdk.Pubkey, signer, writable bool) {
		current, exists := privs[key]
		if !exists {
			current.order = nextOrder
			nextOrder++
		}
		current.signer = current.signer || signer
		current.writable = current.writable || writable
		privs[key] = current
	}
	for _, instruction := range instructions {
		for _, meta := range instruction.Accounts {
			merge(meta.Pubkey, meta.IsSigner, meta.IsWritable)
		}
		merge(instruction.ProgramID, false, false)
	}

	keys := make([]sdk.Pubkey, 0, len(privs))
	keys = append(keys, feePayer.PublicKey)
	// Preserve encounter order inside each privilege class.
	for class := 0; class < 4; class++ {
		for order := 1; order < nextOrder; order++ {
			for key, privilege := range privs {
				if privilege.order != order || privilegeClass(privilege.signer, privilege.writable) != class {
					continue
				}
				keys = append(keys, key)
			}
		}
	}
	if len(keys) > 256 {
		return nil, nil, 0, ErrTooManyAccounts
	}
	indices := make(map[sdk.Pubkey]byte, len(keys))
	for index, key := range keys {
		indices[key] = byte(index)
	}

	numRequired := 0
	numReadonlySigned := 0
	numReadonlyUnsigned := 0
	for _, key := range keys {
		privilege := privs[key]
		if privilege.signer {
			numRequired++
			if !privilege.writable {
				numReadonlySigned++
			}
		} else if !privilege.writable {
			numReadonlyUnsigned++
		}
	}
	if err := validateLegacyHeaderCounts(numRequired, numReadonlySigned, numReadonlyUnsigned); err != nil {
		return nil, nil, 0, err
	}
	message := make([]byte, 0, 1+3+1+len(keys)*32+32+1+1+len(instructions))
	if versioned {
		message = append(message, versionedTransactionV0)
	}
	message = append(message, byte(numRequired), byte(numReadonlySigned), byte(numReadonlyUnsigned))
	message = appendShortVec(message, len(keys))
	for _, key := range keys {
		message = append(message, key[:]...)
	}
	message = append(message, blockhash[:]...)
	message = appendShortVec(message, len(instructions))
	for _, instruction := range instructions {
		message = append(message, indices[instruction.ProgramID])
		message = appendShortVec(message, len(instruction.Accounts))
		for _, meta := range instruction.Accounts {
			message = append(message, indices[meta.Pubkey])
		}
		message = appendShortVec(message, len(instruction.Data))
		message = append(message, instruction.Data...)
	}
	// Versioned transactions keep room for future extension through address table
	// lookups; this implementation uses static keys only.
	if versioned {
		message = append(message, 0x00) // zero address table lookups
	}
	return message, keys, numRequired, nil
}

func signTransaction(message []byte, accountKeys []sdk.Pubkey, numRequiredSignatures int, feePayer Signer, signers []Signer) ([]byte, error) {
	available := make(map[sdk.Pubkey]ed25519.PrivateKey, len(signers)+1)
	available[feePayer.PublicKey] = feePayer.Private
	for _, signer := range signers {
		if err := ValidateSigner(signer); err != nil {
			return nil, err
		}
		available[signer.PublicKey] = signer.Private
	}

	transaction := appendShortVec(nil, numRequiredSignatures)
	for _, key := range accountKeys[:numRequiredSignatures] {
		private, ok := available[key]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrMissingSigner, key)
		}
		transaction = append(transaction, ed25519.Sign(private, message)...)
	}
	transaction = append(transaction, message...)
	return transaction, nil
}

func validateLegacyHeaderCounts(numRequired, numReadonlySigned, numReadonlyUnsigned int) error {
	for name, count := range map[string]int{
		"required signatures": numRequired,
		"readonly signed":     numReadonlySigned,
		"readonly unsigned":   numReadonlyUnsigned,
	} {
		if count < 0 || count > 255 {
			return fmt.Errorf("%w: %s=%d", ErrLegacyHeaderOverflow, name, count)
		}
	}
	return nil
}

// EncodeTransactionBase64 returns the JSON-RPC wire representation.
func EncodeTransactionBase64(transaction []byte) string {
	return base64.StdEncoding.EncodeToString(transaction)
}

// TransactionSignature returns the canonical base58 first signature from a
// signed transaction. The first signature is deterministic before RPC
// submission and is therefore the recovery identifier for ambiguous submits.
func TransactionSignature(transaction []byte) (string, error) {
	count, offset, err := decodeShortVec(transaction)
	if err != nil || count < 1 || offset > len(transaction) || count > (len(transaction)-offset)/ed25519.SignatureSize {
		return "", ErrInvalidTransaction
	}
	return encodeBase58(transaction[offset : offset+ed25519.SignatureSize]), nil
}

func privilegeClass(signer, writable bool) int {
	switch {
	case signer && writable:
		return 0
	case signer:
		return 1
	case writable:
		return 2
	default:
		return 3
	}
}

func appendShortVec(dst []byte, value int) []byte {
	for {
		current := byte(value & 0x7f)
		value >>= 7
		if value == 0 {
			return append(dst, current)
		}
		dst = append(dst, current|0x80)
	}
}

func decodeShortVec(encoded []byte) (int, int, error) {
	value := 0
	shift := uint(0)
	for index, current := range encoded {
		if index >= 5 || shift >= 32 {
			return 0, 0, ErrInvalidTransaction
		}
		value |= int(current&0x7f) << shift
		if current&0x80 == 0 {
			if index > 0 && current == 0 {
				return 0, 0, ErrInvalidTransaction
			}
			return value, index + 1, nil
		}
		shift += 7
	}
	return 0, 0, ErrInvalidTransaction
}

func encodeBase58(value []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	leadingZeroes := 0
	for leadingZeroes < len(value) && value[leadingZeroes] == 0 {
		leadingZeroes++
	}
	var digitsLE []byte
	for _, current := range value[leadingZeroes:] {
		carry := int(current)
		for index := range digitsLE {
			combined := int(digitsLE[index])*256 + carry
			digitsLE[index] = byte(combined % 58)
			carry = combined / 58
		}
		for carry > 0 {
			digitsLE = append(digitsLE, byte(carry%58))
			carry /= 58
		}
	}
	encoded := make([]byte, leadingZeroes+len(digitsLE))
	for index := 0; index < leadingZeroes; index++ {
		encoded[index] = alphabet[0]
	}
	for index := range digitsLE {
		encoded[len(encoded)-1-index] = alphabet[digitsLE[index]]
	}
	return string(encoded)
}
