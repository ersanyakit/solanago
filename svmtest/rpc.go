package svmtest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ersany/go-solana/sdk"
)

// RPCError is a JSON-RPC error returned by Agave.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("Solana RPC %d: %s", e.Code, e.Message)
}

// Client is the minimal fail-closed JSON-RPC client used by the real-runtime
// harness. A submitted transaction is never automatically resent.
type Client struct {
	URL  string
	HTTP *http.Client
}

// AccountInfo is the finalized account metadata needed by deploy verification.
type AccountInfo struct {
	Lamports   uint64 `json:"lamports"`
	Owner      string `json:"owner"`
	Executable bool   `json:"executable"`
	Data       any    `json:"data"`
}

// DataBytes decodes the canonical [base64, "base64"] account-data response.
// It rejects alternate/ambiguous encodings instead of guessing.
func (a AccountInfo) DataBytes() ([]byte, error) {
	encoded, ok := a.Data.([]any)
	if !ok || len(encoded) != 2 {
		return nil, errors.New("Solana RPC account data is not a two-element encoded tuple")
	}
	payload, payloadOK := encoded[0].(string)
	encoding, encodingOK := encoded[1].(string)
	if !payloadOK || !encodingOK || encoding != "base64" {
		return nil, errors.New("Solana RPC account data is not canonical base64")
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode Solana RPC account base64: %w", err)
	}
	return decoded, nil
}

func (c Client) call(ctx context.Context, method string, params any, result any) error {
	requestBody, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", 1, method, params})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(requestBody))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Solana RPC HTTP %s: %s", response.Status, string(body))
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode Solana RPC response: %w", err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if result == nil {
		return nil
	}
	if len(envelope.Result) == 0 {
		return errors.New("Solana RPC response has neither result nor error")
	}
	return json.Unmarshal(envelope.Result, result)
}

// Health checks whether the validator RPC is ready.
func (c Client) Health(ctx context.Context) error {
	var result string
	if err := c.call(ctx, "getHealth", []any{}, &result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("validator health is %q", result)
	}
	return nil
}

// GenesisHash identifies the connected cluster.
func (c Client) GenesisHash(ctx context.Context) (string, error) {
	var result string
	err := c.call(ctx, "getGenesisHash", []any{}, &result)
	return result, err
}

// MinimumBalanceForRentExemption queries the runtime rent calculation.
func (c Client) MinimumBalanceForRentExemption(ctx context.Context, size uint64) (uint64, error) {
	var result uint64
	err := c.call(ctx, "getMinimumBalanceForRentExemption", []any{size, map[string]any{"commitment": "finalized"}}, &result)
	return result, err
}

// Balance returns the finalized lamport balance.
func (c Client) Balance(ctx context.Context, address sdk.Pubkey) (uint64, error) {
	var result struct {
		Value uint64 `json:"value"`
	}
	err := c.call(ctx, "getBalance", []any{address.String(), map[string]any{"commitment": "finalized"}}, &result)
	return result.Value, err
}

// GetAccountInfo returns nil when the account does not exist at finalized
// commitment.
func (c Client) GetAccountInfo(ctx context.Context, address sdk.Pubkey) (*AccountInfo, error) {
	var result struct {
		Value *AccountInfo `json:"value"`
	}
	err := c.call(ctx, "getAccountInfo", []any{address.String(), map[string]any{"commitment": "finalized", "encoding": "base64"}}, &result)
	return result.Value, err
}

// RequestAirdrop requests test-validator/devnet/testnet funding exactly once
// and waits for that returned signature to finalize. Callers decide whether
// the connected cluster permits airdrops; this method never retries a request.
func (c Client) RequestAirdrop(ctx context.Context, address sdk.Pubkey, lamports uint64) (string, error) {
	var signature string
	if err := c.call(ctx, "requestAirdrop", []any{
		address.String(), lamports, map[string]any{"commitment": "finalized"},
	}, &signature); err != nil {
		return "", fmt.Errorf("airdrop outcome unknown; request was not resent: %w", err)
	}
	if signature == "" {
		return "", errors.New("requestAirdrop returned an empty signature")
	}
	return signature, c.WaitForFinalized(ctx, signature)
}

// SendInstructions signs and submits exactly once, then polls the signature
// status until confirmed/finalized or ctx expires. An ambiguous submit error
// is returned to the caller and is never retried automatically.
func (c Client) SendInstructions(ctx context.Context, feePayer Signer, signers []Signer, instructions []sdk.Instruction) (string, error) {
	return c.sendInstructionsUntil(ctx, feePayer, signers, instructions, "finalized")
}

// SendInstructionsConfirmed submits exactly once and returns after Agave has
// confirmed the signature. It is intended for ordered transaction pipelines
// that subsequently call WaitForFinalized on the last dependent signature and
// verify finalized state. It must not be used as final settlement evidence.
func (c Client) SendInstructionsConfirmed(ctx context.Context, feePayer Signer, signers []Signer, instructions []sdk.Instruction) (string, error) {
	return c.sendInstructionsUntil(ctx, feePayer, signers, instructions, "confirmed")
}

func (c Client) sendInstructionsUntil(ctx context.Context, feePayer Signer, signers []Signer, instructions []sdk.Instruction, commitment string) (string, error) {
	var latest struct {
		Value struct {
			Blockhash string `json:"blockhash"`
		} `json:"value"`
	}
	if err := c.call(ctx, "getLatestBlockhash", []any{map[string]any{"commitment": "finalized"}}, &latest); err != nil {
		return "", err
	}
	transaction, err := BuildVersionedTransaction(feePayer, signers, latest.Value.Blockhash, instructions)
	if err != nil {
		return "", err
	}
	localSignature, err := TransactionSignature(transaction)
	if err != nil {
		return "", err
	}
	var rpcSignature string
	if err := c.call(ctx, "sendTransaction", []any{
		EncodeTransactionBase64(transaction),
		map[string]any{"encoding": "base64", "preflightCommitment": "confirmed", "skipPreflight": false},
	}, &rpcSignature); err != nil {
		return localSignature, fmt.Errorf("submit outcome unknown for transaction %s; transaction was not resent: %w", localSignature, err)
	}
	if rpcSignature == "" {
		return localSignature, fmt.Errorf("sendTransaction returned an empty signature; local transaction signature is %s", localSignature)
	}
	if rpcSignature != localSignature {
		return localSignature, fmt.Errorf("sendTransaction signature mismatch: RPC returned %s, local transaction signature is %s", rpcSignature, localSignature)
	}
	return localSignature, c.waitForCommitment(ctx, localSignature, commitment)
}

// WaitForFinalized waits for an already-submitted signature. It never submits
// or resubmits a transaction.
func (c Client) WaitForFinalized(ctx context.Context, signature string) error {
	if signature == "" {
		return errors.New("cannot wait for an empty transaction signature")
	}
	return c.waitForCommitment(ctx, signature, "finalized")
}

func (c Client) waitForCommitment(ctx context.Context, signature, commitment string) error {
	if commitment != "confirmed" && commitment != "finalized" {
		return fmt.Errorf("unsupported transaction commitment %q", commitment)
	}
	// Public Solana RPC endpoints cap calls to a single RPC method. Polling at
	// 200 ms exceeds Testnet's documented per-method limit and can keep a valid
	// submitted transaction permanently hidden behind HTTP 429 responses. One
	// second remains responsive while leaving ample headroom for deployment
	// traffic and other finalized-state checks.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var statuses struct {
			Value []struct {
				Err                json.RawMessage `json:"err"`
				ConfirmationStatus string          `json:"confirmationStatus"`
			} `json:"value"`
		}
		if err := c.call(ctx, "getSignatureStatuses", []any{[]string{signature}, map[string]any{"searchTransactionHistory": true}}, &statuses); err == nil && len(statuses.Value) == 1 {
			status := statuses.Value[0]
			if len(status.Err) != 0 && string(status.Err) != "null" {
				return fmt.Errorf("transaction %s failed: %s", signature, status.Err)
			}
			if status.ConfirmationStatus == "finalized" || commitment == "confirmed" && status.ConfirmationStatus == "confirmed" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("transaction %s confirmation unknown: %w", signature, ctx.Err())
		case <-ticker.C:
		}
	}
}
