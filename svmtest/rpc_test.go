package svmtest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendInstructionsReturnsLocalSignatureOnAmbiguousSubmit(t *testing.T) {
	payer := deterministicSigner(11)
	blockhash := deterministicSigner(12).PublicKey.String()
	var submittedSignature string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		method, params := decodeRPCRequest(t, request)
		switch method {
		case "getLatestBlockhash":
			writeRPCResult(t, response, map[string]any{"value": map[string]any{"blockhash": blockhash}})
		case "sendTransaction":
			submittedSignature = signatureFromRPCTransaction(t, params)
			http.Error(response, "connection outcome is ambiguous", http.StatusServiceUnavailable)
		default:
			t.Fatalf("unexpected RPC method %q", method)
		}
	}))
	defer server.Close()

	signature, err := (Client{URL: server.URL}).SendInstructions(context.Background(), payer, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "submit outcome unknown") {
		t.Fatalf("error = %v, want ambiguous-submit error", err)
	}
	if submittedSignature == "" || signature != submittedSignature {
		t.Fatalf("returned signature = %q, locally submitted = %q", signature, submittedSignature)
	}
}

func TestSendInstructionsRejectsRPCSignatureMismatch(t *testing.T) {
	payer := deterministicSigner(13)
	blockhash := deterministicSigner(14).PublicKey.String()
	var submittedSignature string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		method, params := decodeRPCRequest(t, request)
		switch method {
		case "getLatestBlockhash":
			writeRPCResult(t, response, map[string]any{"value": map[string]any{"blockhash": blockhash}})
		case "sendTransaction":
			submittedSignature = signatureFromRPCTransaction(t, params)
			writeRPCResult(t, response, "not-the-locally-signed-transaction")
		default:
			t.Fatalf("unexpected RPC method %q", method)
		}
	}))
	defer server.Close()

	signature, err := (Client{URL: server.URL}).SendInstructions(context.Background(), payer, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("error = %v, want signature-mismatch error", err)
	}
	if submittedSignature == "" || signature != submittedSignature {
		t.Fatalf("returned signature = %q, locally submitted = %q", signature, submittedSignature)
	}
}

func TestSendInstructionsPreservesSignatureOnConfirmationTimeout(t *testing.T) {
	payer := deterministicSigner(15)
	blockhash := deterministicSigner(16).PublicKey.String()
	var submittedSignature string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		method, params := decodeRPCRequest(t, request)
		switch method {
		case "getLatestBlockhash":
			writeRPCResult(t, response, map[string]any{"value": map[string]any{"blockhash": blockhash}})
		case "sendTransaction":
			submittedSignature = signatureFromRPCTransaction(t, params)
			writeRPCResult(t, response, submittedSignature)
		case "getSignatureStatuses":
			writeRPCResult(t, response, map[string]any{"value": []any{nil}})
		default:
			t.Fatalf("unexpected RPC method %q", method)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	signature, err := (Client{URL: server.URL}).SendInstructions(ctx, payer, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if submittedSignature == "" || signature != submittedSignature {
		t.Fatalf("returned signature = %q, locally submitted = %q", signature, submittedSignature)
	}
}

func TestLatestBlockhashRetriesRateLimitedResponses(t *testing.T) {
	restoreRetryTuning(t)
	idempotentRetryBaseDelay = time.Millisecond
	blockhash := deterministicSigner(30).PublicKey.String()
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		method, _ := decodeRPCRequest(t, request)
		if method != "getLatestBlockhash" {
			t.Fatalf("unexpected RPC method %q", method)
		}
		attempts++
		if attempts < 3 {
			http.Error(response, "rate limited", http.StatusTooManyRequests)
			return
		}
		writeRPCResult(t, response, map[string]any{"value": map[string]any{"blockhash": blockhash}})
	}))
	defer server.Close()

	got, err := (Client{URL: server.URL}).LatestBlockhash(context.Background())
	if err != nil {
		t.Fatalf("LatestBlockhash() error = %v, want nil after retrying past 429s", err)
	}
	if got != blockhash {
		t.Fatalf("blockhash = %q, want %q", got, blockhash)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want exactly 3 (two 429s then success)", attempts)
	}
}

func TestLatestBlockhashDoesNotRetryNonRateLimitErrors(t *testing.T) {
	restoreRetryTuning(t)
	idempotentRetryBaseDelay = time.Millisecond
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		attempts++
		http.Error(response, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	if _, err := (Client{URL: server.URL}).LatestBlockhash(context.Background()); err == nil {
		t.Fatal("LatestBlockhash() error = nil, want the non-retryable HTTP 400 surfaced")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want exactly 1 (a non-429/5xx error must not be retried)", attempts)
	}
}

func TestLatestBlockhashGivesUpAfterMaxAttempts(t *testing.T) {
	restoreRetryTuning(t)
	idempotentRetryBaseDelay = time.Millisecond
	idempotentRetryAttempts = 3
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		attempts++
		http.Error(response, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	if _, err := (Client{URL: server.URL}).LatestBlockhash(context.Background()); err == nil {
		t.Fatal("LatestBlockhash() error = nil, want the persistent 429 surfaced once attempts are exhausted")
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want exactly idempotentRetryAttempts (3)", attempts)
	}
}

// restoreRetryTuning lets a test shrink the retry backoff/attempt knobs
// without leaking that override into other tests.
func restoreRetryTuning(t *testing.T) {
	t.Helper()
	attempts, base, max := idempotentRetryAttempts, idempotentRetryBaseDelay, idempotentRetryMaxDelay
	t.Cleanup(func() {
		idempotentRetryAttempts, idempotentRetryBaseDelay, idempotentRetryMaxDelay = attempts, base, max
	})
}

func decodeRPCRequest(t *testing.T, request *http.Request) (string, []json.RawMessage) {
	t.Helper()
	var payload struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Fatalf("decode RPC request: %v", err)
	}
	return payload.Method, payload.Params
}

func signatureFromRPCTransaction(t *testing.T, params []json.RawMessage) string {
	t.Helper()
	if len(params) == 0 {
		t.Fatal("sendTransaction has no encoded transaction")
	}
	var encoded string
	if err := json.Unmarshal(params[0], &encoded); err != nil {
		t.Fatalf("decode transaction parameter: %v", err)
	}
	transaction, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode transaction base64: %v", err)
	}
	signature, err := TransactionSignature(transaction)
	if err != nil {
		t.Fatalf("extract local transaction signature: %v", err)
	}
	return signature
}

func writeRPCResult(t *testing.T, response http.ResponseWriter, result any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result":  result,
	}); err != nil {
		t.Fatalf("encode RPC response: %v", err)
	}
}
