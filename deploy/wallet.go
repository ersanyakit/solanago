package deploy

import (
	"context"
	"fmt"
	"math"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/loader"
	"github.com/ersanyakit/solanago/svmtest"
)

// PrepareCreateBufferTransaction returns a serialized versioned transaction
// that creates and initializes an upgradeable-loader buffer account sized
// for elfLength bytes, with feePayer as both the payer and the buffer's
// write authority. buffer signs (a new account must consent to being
// created); feePayer's signature slot is left zero-filled for an external
// wallet — e.g. a browser extension that never hands this process its
// private key — to complete with PartialSignTransaction's counterpart on
// the wallet side (Phantom's signTransaction/signAllTransactions).
//
// The buffer is funded with rent-exemption for ProgramDataMetadataSize (not
// BufferMetadataSize) plus elfLength lamports, matching Program's own
// self-signed path: DeployWithMaxDataLen later turns this same buffer's
// lamports into the larger ProgramData account's lamports, and (unlike
// Upgrade, which has a spill account to reconcile the difference) has none
// here, so under-funding it at creation would leave the finalized
// ProgramData account short of rent-exemption.
//
// This is the wallet-driven analogue of the fully self-signed path
// Program uses internally; it performs no RPC submission itself.
func PrepareCreateBufferTransaction(ctx context.Context, client svmtest.Client, feePayer sdk.Pubkey, buffer svmtest.Signer, elfLength int) ([]byte, error) {
	if elfLength < 0 {
		return nil, fmt.Errorf("deploy: elf length %d is negative", elfLength)
	}
	programDataRent, err := client.MinimumBalanceForRentExemption(ctx, uint64(loader.ProgramDataMetadataSize+elfLength))
	if err != nil {
		return nil, err
	}
	instructions, err := loader.CreateBuffer(feePayer, buffer.PublicKey, feePayer, programDataRent, elfLength)
	if err != nil {
		return nil, err
	}
	blockhash, err := client.LatestBlockhash(ctx)
	if err != nil {
		return nil, err
	}
	message, accountKeys, numRequiredSignatures, err := svmtest.CompileTransactionMessage(feePayer, blockhash, instructions, true)
	if err != nil {
		return nil, err
	}
	return svmtest.PartialSignTransaction(message, accountKeys, numRequiredSignatures, []svmtest.Signer{buffer}), nil
}

// PrepareDeployTransaction returns a serialized versioned transaction that
// creates the program account and deploys buffer's finalized contents as
// its code, with feePayer as both the payer and the program's upgrade
// authority. program signs; feePayer's signature slot is left zero-filled
// for an external wallet to complete, exactly like PrepareCreateBufferTransaction.
//
// The program account created here is always ProgramMetadataSize (36)
// bytes regardless of elfLength — the ProgramData account that actually
// holds elfLength bytes of code was already funded when its buffer was
// created (see PrepareCreateBufferTransaction).
//
// Callers must have already finished and finalized every buffer Write for
// elfLength bytes before submitting the transaction this returns — the
// loader instruction it wraps does not itself wait for that.
func PrepareDeployTransaction(ctx context.Context, client svmtest.Client, feePayer sdk.Pubkey, program svmtest.Signer, buffer sdk.Pubkey, elfLength int) ([]byte, error) {
	if elfLength < 0 {
		return nil, fmt.Errorf("deploy: elf length %d is negative", elfLength)
	}
	if uint64(elfLength) > math.MaxUint64-uint64(loader.ProgramDataMetadataSize) {
		return nil, loader.ErrTooLarge
	}
	programRent, err := client.MinimumBalanceForRentExemption(ctx, loader.ProgramMetadataSize)
	if err != nil {
		return nil, err
	}
	instructions, err := loader.DeployWithMaxDataLen(feePayer, program.PublicKey, buffer, feePayer, programRent, elfLength)
	if err != nil {
		return nil, err
	}
	blockhash, err := client.LatestBlockhash(ctx)
	if err != nil {
		return nil, err
	}
	message, accountKeys, numRequiredSignatures, err := svmtest.CompileTransactionMessage(feePayer, blockhash, instructions, true)
	if err != nil {
		return nil, err
	}
	return svmtest.PartialSignTransaction(message, accountKeys, numRequiredSignatures, []svmtest.Signer{program}), nil
}
