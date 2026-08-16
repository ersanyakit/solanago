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
