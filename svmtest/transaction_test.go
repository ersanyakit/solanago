package svmtest

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/ersany/go-solana/sdk"
)

func TestBuildLegacyTransactionOrdersAndSigns(t *testing.T) {
	payer := deterministicSigner(1)
	other := deterministicSigner(2)
	program := deterministicSigner(3).PublicKey
	readonly := deterministicSigner(4).PublicKey
	writable := deterministicSigner(5).PublicKey
	blockhash := deterministicSigner(6).PublicKey.String()
	instructions := []sdk.Instruction{{
		ProgramID: program,
		Accounts: []sdk.AccountMeta{
			sdk.Readonly(readonly, false),
			sdk.Writable(other.PublicKey, true),
			sdk.Writable(writable, false),
		},
		Data: []byte{1, 2, 3},
	}}
	transaction, err := BuildLegacyTransaction(payer, []Signer{other}, blockhash, instructions)
	if err != nil {
		t.Fatal(err)
	}
	// Two one-byte shortvec signatures followed by 2*64-byte signatures.
	if len(transaction) < 1+128+3 || transaction[0] != 2 {
		t.Fatalf("malformed transaction: %x", transaction)
	}
	message := transaction[129:]
	if message[0] != 2 || message[1] != 0 || message[2] != 2 {
		t.Fatalf("unexpected header %v", message[:3])
	}
	if !ed25519.Verify(payer.PublicKey[:], message, transaction[1:65]) {
		t.Fatal("payer signature does not verify")
	}
	if !ed25519.Verify(other.PublicKey[:], message, transaction[65:129]) {
		t.Fatal("secondary signature does not verify")
	}
}

func TestBuildVersionedTransactionUsesVersion0Prefix(t *testing.T) {
	payer := deterministicSigner(1)
	other := deterministicSigner(2)
	program := deterministicSigner(3).PublicKey
	blockhash := deterministicSigner(6).PublicKey.String()
	instructions := []sdk.Instruction{{
		ProgramID: program,
		Accounts: []sdk.AccountMeta{
			sdk.Readonly(other.PublicKey, true),
		},
	}}
	transaction, err := BuildVersionedTransaction(payer, []Signer{other}, blockhash, instructions)
	if err != nil {
		t.Fatal(err)
	}
	messageStart := 1 + 2*ed25519.SignatureSize
	message := transaction[messageStart:]
	if got, want := message[0], byte(0x80); got != want {
		t.Fatalf("version byte = %x, want %x", got, want)
	}
	// message header: requiredSignatures=2, readonlySigned=1, readonlyUnsigned=1
	if message[1] != 2 || message[2] != 1 || message[3] != 1 {
		t.Fatalf("unexpected message header %v", message[1:4])
	}
	if !ed25519.Verify(payer.PublicKey[:], message, transaction[1:65]) {
		t.Fatal("payer signature does not verify")
	}
	if !ed25519.Verify(other.PublicKey[:], message, transaction[65:129]) {
		t.Fatal("secondary signature does not verify")
	}
}

func TestBuildLegacyTransactionRequiresSigner(t *testing.T) {
	payer := deterministicSigner(1)
	missing := deterministicSigner(2)
	program := deterministicSigner(3).PublicKey
	_, err := BuildLegacyTransaction(payer, nil, deterministicSigner(4).PublicKey.String(), []sdk.Instruction{{
		ProgramID: program,
		Accounts:  []sdk.AccountMeta{sdk.Readonly(missing.PublicKey, true)},
	}})
	if err == nil {
		t.Fatal("missing signer accepted")
	}
}

func TestBuildLegacyTransactionAllows256KeysWhenHeaderCountsFit(t *testing.T) {
	payer := deterministicSigner(21)
	accounts := make([]sdk.AccountMeta, 254)
	for index := range accounts {
		accounts[index] = sdk.Readonly(indexedPubkey(uint32(1_000+index)), false)
	}
	program := indexedPubkey(999)
	transaction, err := BuildLegacyTransaction(payer, nil, indexedPubkey(2_000).String(), []sdk.Instruction{{
		ProgramID: program,
		Accounts:  accounts,
	}})
	if err != nil {
		t.Fatal(err)
	}
	// One signature shortvec byte and one 64-byte signature precede the
	// message. The message contains 1 payer + 254 metas + 1 program key.
	message := transaction[1+ed25519.SignatureSize:]
	if len(message) < 5 {
		t.Fatalf("short transaction: %d bytes", len(transaction))
	}
	if got := message[:3]; got[0] != 1 || got[1] != 0 || got[2] != 255 {
		t.Fatalf("legacy header = %v, want [1 0 255]", got)
	}
	if message[3] != 0x80 || message[4] != 0x02 {
		t.Fatalf("account-key shortvec = %x, want 8002", message[3:5])
	}
}

func TestBuildLegacyTransactionRejects256RequiredSignatures(t *testing.T) {
	payer := deterministicSigner(22)
	accounts := make([]sdk.AccountMeta, 255)
	for index := range accounts {
		accounts[index] = sdk.Readonly(indexedPubkey(uint32(3_000+index)), true)
	}
	// Reusing one signer key as the program id keeps the total at the legal
	// 256-key index boundary while making num_required_signatures equal 256.
	_, err := BuildLegacyTransaction(payer, nil, indexedPubkey(4_000).String(), []sdk.Instruction{{
		ProgramID: accounts[0].Pubkey,
		Accounts:  accounts,
	}})
	if !errors.Is(err, ErrLegacyHeaderOverflow) {
		t.Fatalf("error = %v, want ErrLegacyHeaderOverflow", err)
	}
}

func TestLegacyHeaderCountBoundaries(t *testing.T) {
	if err := validateLegacyHeaderCounts(255, 255, 255); err != nil {
		t.Fatalf("255 boundary rejected: %v", err)
	}
	for _, counts := range [][3]int{{256, 0, 0}, {0, 256, 0}, {0, 0, 256}, {-1, 0, 0}} {
		if err := validateLegacyHeaderCounts(counts[0], counts[1], counts[2]); !errors.Is(err, ErrLegacyHeaderOverflow) {
			t.Fatalf("counts %v error = %v, want ErrLegacyHeaderOverflow", counts, err)
		}
	}
}

func TestCanonicalSignerRejectsForgedPublicSuffix(t *testing.T) {
	forged := deterministicSigner(7)
	forged.Private = append(ed25519.PrivateKey(nil), forged.Private...)
	forged.Private[ed25519.SeedSize] ^= 0xff
	copy(forged.PublicKey[:], forged.Private[ed25519.SeedSize:])

	if err := ValidateSigner(forged); !errors.Is(err, ErrInvalidSigner) {
		t.Fatalf("ValidateSigner error = %v, want ErrInvalidSigner", err)
	}
	_, err := BuildLegacyTransaction(forged, nil, deterministicSigner(8).PublicKey.String(), nil)
	if !errors.Is(err, ErrInvalidSigner) {
		t.Fatalf("BuildLegacyTransaction error = %v, want ErrInvalidSigner", err)
	}
}

func TestTransactionSignature(t *testing.T) {
	transaction := append([]byte{1}, make([]byte, ed25519.SignatureSize)...)
	signature, err := TransactionSignature(transaction)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("1", ed25519.SignatureSize); signature != want {
		t.Fatalf("zero signature = %q, want %q", signature, want)
	}
	for _, malformed := range [][]byte{nil, {0}, {0x80}, {1, 2, 3}, append([]byte{2}, make([]byte, ed25519.SignatureSize)...)} {
		if _, err := TransactionSignature(malformed); !errors.Is(err, ErrInvalidTransaction) {
			t.Fatalf("TransactionSignature(%x) error = %v", malformed, err)
		}
	}
}

func TestEncodeBase58KnownVectors(t *testing.T) {
	tests := []struct {
		input []byte
		want  string
	}{
		{[]byte{0}, "1"},
		{[]byte{0x61}, "2g"},
		{[]byte{0x62, 0x62, 0x62}, "a3gV"},
		{[]byte{0x63, 0x63, 0x63}, "aPEr"},
	}
	for _, test := range tests {
		if got := encodeBase58(test.input); got != test.want {
			t.Fatalf("encodeBase58(%x) = %q, want %q", test.input, got, test.want)
		}
	}
}

func deterministicSigner(seedByte byte) Signer {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = seedByte
	}
	private := ed25519.NewKeyFromSeed(seed)
	signer, err := SignerFromPrivateKey(private)
	if err != nil {
		panic(err)
	}
	return signer
}

func indexedPubkey(index uint32) sdk.Pubkey {
	seed := make([]byte, ed25519.SeedSize)
	binary.LittleEndian.PutUint32(seed, index)
	private := ed25519.NewKeyFromSeed(seed)
	var key sdk.Pubkey
	copy(key[:], private[ed25519.SeedSize:])
	return key
}
