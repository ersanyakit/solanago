package deploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/loader"
	"github.com/ersanyakit/solanago/svmtest"
)

// fakeWalletRPC answers just enough JSON-RPC methods for
// PrepareCreateBufferTransaction/PrepareDeployTransaction: a fixed rent
// figure and a syntactically valid (pubkey-shaped) blockhash.
func fakeWalletRPC(t *testing.T, blockhash string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var result any
		switch body.Method {
		case "getMinimumBalanceForRentExemption":
			result = 1_000_000
		case "getLatestBlockhash":
			result = map[string]any{"value": map[string]any{"blockhash": blockhash}}
		default:
			t.Fatalf("unexpected RPC method %q", body.Method)
		}
		if err := json.NewEncoder(response).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result}); err != nil {
			t.Fatal(err)
		}
	}))
}

func TestPrepareCreateBufferTransactionLeavesFeePayerSlotZeroed(t *testing.T) {
	feePayer := newSigner(t)
	buffer := newSigner(t)
	server := fakeWalletRPC(t, newSigner(t).PublicKey.String())
	defer server.Close()

	tx, err := PrepareCreateBufferTransaction(context.Background(), svmtest.Client{URL: server.URL}, feePayer.PublicKey, buffer, 4096)
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeWalletTransaction(t, tx)
	if decoded.accountKeys[0] != feePayer.PublicKey {
		t.Fatalf("account 0 = %s, want fee payer %s", decoded.accountKeys[0], feePayer.PublicKey)
	}
	requireZeroSignature(t, decoded, feePayer.PublicKey)
	requireValidSignature(t, decoded, buffer.PublicKey)
	requireAccountPresent(t, decoded, loader.ProgramID)
}

func TestPrepareDeployTransactionLeavesFeePayerSlotZeroed(t *testing.T) {
	feePayer := newSigner(t)
	program := newSigner(t)
	buffer := newSigner(t)
	server := fakeWalletRPC(t, newSigner(t).PublicKey.String())
	defer server.Close()

	tx, err := PrepareDeployTransaction(context.Background(), svmtest.Client{URL: server.URL}, feePayer.PublicKey, program, buffer.PublicKey, 4096)
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodeWalletTransaction(t, tx)
	if decoded.accountKeys[0] != feePayer.PublicKey {
		t.Fatalf("account 0 = %s, want fee payer %s", decoded.accountKeys[0], feePayer.PublicKey)
	}
	requireZeroSignature(t, decoded, feePayer.PublicKey)
	requireValidSignature(t, decoded, program.PublicKey)
	requireAccountPresent(t, decoded, buffer.PublicKey)
}

func TestPrepareCreateBufferTransactionRejectsNegativeLength(t *testing.T) {
	server := fakeWalletRPC(t, newSigner(t).PublicKey.String())
	defer server.Close()
	_, err := PrepareCreateBufferTransaction(context.Background(), svmtest.Client{URL: server.URL}, newSigner(t).PublicKey, newSigner(t), -1)
	if err == nil {
		t.Fatal("want error for negative elf length")
	}
}

type decodedWalletTransaction struct {
	message               []byte
	accountKeys           []sdk.Pubkey
	numRequiredSignatures int
	signatures            [][]byte
}

func decodeWalletTransaction(t *testing.T, transaction []byte) decodedWalletTransaction {
	t.Helper()
	signatureCount, offset := readShortVec(t, transaction)
	signatures := make([][]byte, signatureCount)
	for index := range signatures {
		signatures[index] = transaction[offset : offset+ed25519.SignatureSize]
		offset += ed25519.SignatureSize
	}
	message := transaction[offset:]
	if len(message) == 0 || message[0] != 0x80 {
		t.Fatalf("message is not a versioned (v0) message: first byte = %#x", message[0])
	}
	numRequiredSignatures := int(message[1])
	if numRequiredSignatures != signatureCount {
		t.Fatalf("message header numRequiredSignatures = %d, signature slots = %d", numRequiredSignatures, signatureCount)
	}
	messageOffset := 4 // 0x80 prefix + 3 header bytes
	keyCount, keyOffset := readShortVecAt(t, message, messageOffset)
	messageOffset = keyOffset
	keys := make([]sdk.Pubkey, keyCount)
	for index := range keys {
		copy(keys[index][:], message[messageOffset:messageOffset+32])
		messageOffset += 32
	}
	return decodedWalletTransaction{
		message:               message,
		accountKeys:           keys,
		numRequiredSignatures: numRequiredSignatures,
		signatures:            signatures,
	}
}

func requireZeroSignature(t *testing.T, decoded decodedWalletTransaction, key sdk.Pubkey) {
	t.Helper()
	index := indexOfKey(t, decoded, key)
	if !bytes.Equal(decoded.signatures[index], make([]byte, ed25519.SignatureSize)) {
		t.Fatalf("signature slot for %s is not zero-filled", key)
	}
}

func requireValidSignature(t *testing.T, decoded decodedWalletTransaction, key sdk.Pubkey) {
	t.Helper()
	index := indexOfKey(t, decoded, key)
	if !ed25519.Verify(ed25519.PublicKey(key[:]), decoded.message, decoded.signatures[index]) {
		t.Fatalf("signature slot for %s does not verify against the message", key)
	}
}

func requireAccountPresent(t *testing.T, decoded decodedWalletTransaction, key sdk.Pubkey) {
	t.Helper()
	for _, candidate := range decoded.accountKeys {
		if candidate == key {
			return
		}
	}
	t.Fatalf("account %s not present in compiled message", key)
}

func indexOfKey(t *testing.T, decoded decodedWalletTransaction, key sdk.Pubkey) int {
	t.Helper()
	for index, candidate := range decoded.accountKeys[:decoded.numRequiredSignatures] {
		if candidate == key {
			return index
		}
	}
	t.Fatalf("%s is not among the %d required signers", key, decoded.numRequiredSignatures)
	return -1
}

func readShortVec(t *testing.T, data []byte) (int, int) {
	t.Helper()
	value, offset := readShortVecAt(t, data, 0)
	return value, offset
}

func readShortVecAt(t *testing.T, data []byte, start int) (int, int) {
	t.Helper()
	value := 0
	shift := uint(0)
	for index := start; index < len(data); index++ {
		current := data[index]
		value |= int(current&0x7f) << shift
		if current&0x80 == 0 {
			return value, index + 1
		}
		shift += 7
	}
	t.Fatal(fmt.Errorf("truncated short-vec length at offset %d", start))
	return 0, 0
}
