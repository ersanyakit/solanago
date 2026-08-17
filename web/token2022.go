// token2022.go wires this server's wallet-signed transaction pattern
// (compile a message for an external fee payer, partial-sign only the
// locally-held ephemeral accounts, let the browser wallet complete and
// submit it — see deploy/wallet.go and svmtest.CompileTransactionMessage/
// PartialSignTransaction) to the *official*, already-deployed Token-2022
// program, via this repository's existing sdk/token2022 and
// sdk/associatedtoken instruction builders.
//
// This is architecturally different from the generic interact engine
// (interact_schema.go/interact_engine.go), which drives this repository's
// own compiled example programs from a declarative tag/account schema.
// Token-2022 is not one of those programs — there is nothing to build or
// deploy — and its associated-token-account address is a PDA, not a fresh
// signing keypair, which doesn't fit the interact engine's NewAccount
// model. A small dedicated handler set reusing the already-typed,
// already-tested sdk/token2022 builders directly is simpler and safer than
// forcing Token-2022's wire format through the generic byte encoder.
//
// Scope: base Token-2022 only (create mint, create/derive an associated
// token account, mint, transfer). No extensions (transfer fee,
// confidential transfer, metadata, ...) and no Metaplex metadata — see
// examples/token2022/cmd/token2022-init for the fuller CLI that supports
// those.
package web

import (
	"context"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/system"
	"github.com/ersanyakit/solanago/sdk/token2022"
	"github.com/ersanyakit/solanago/svmtest"
)

func fetchToken2022Mint(ctx context.Context, client svmtest.Client, address sdk.Pubkey) (token2022.Mint, error) {
	raw, err := fetchAccountData(ctx, client, address)
	if err != nil {
		return token2022.Mint{}, err
	}
	if len(raw) < token2022.MintSize {
		return token2022.Mint{}, token2022.ErrInvalidState
	}
	return token2022.DecodeMint(raw[:token2022.MintSize])
}

func fetchToken2022Account(ctx context.Context, client svmtest.Client, address sdk.Pubkey) (token2022.Account, error) {
	raw, err := fetchAccountData(ctx, client, address)
	if err != nil {
		return token2022.Account{}, err
	}
	if len(raw) < token2022.AccountSize {
		return token2022.Account{}, token2022.ErrInvalidState
	}
	return token2022.DecodeAccount(raw[:token2022.AccountSize])
}

// createMintInstructions returns the System::CreateAccount + InitializeMint2
// pair that creates a bare (no-extension) Token-2022 mint in one
// transaction, with feePayer as the mint authority. mint must sign (new
// account consent).
func createMintInstructions(payer, mint sdk.Pubkey, freezeAuthority token2022.OptionalPubkey, decimals uint8, rent uint64) []sdk.Instruction {
	create := system.CreateAccount(payer, mint, rent, uint64(token2022.MintSize), token2022.ProgramID)
	initialize := token2022.InitializeMint2(mint, payer, freezeAuthority, decimals)
	return []sdk.Instruction{create, initialize}
}
