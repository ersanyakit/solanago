// Package associatedtoken builds instructions and derives addresses for
// Solana's SPL Associated Token Account program. The address it derives is
// the deterministic account every wallet, explorer, and DEX (Raydium
// included) looks up by default for a given (owner, mint, token program)
// triple; a token balance sitting anywhere else is invisible to that lookup.
package associatedtoken

import (
	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/system"
)

// ProgramID is the SPL Associated Token Account program address, identical
// on every cluster.
var ProgramID = sdk.MustParsePubkey("ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL")

// Derive computes the associated token account address for owner+mint under
// the given token program (sdk/token's classic TokenkegQ... or
// sdk/token2022's TokenzQd...). It is the same PDA wallets and DEX frontends
// derive to locate a holder's balance for a mint.
func Derive(owner, mint, tokenProgramID sdk.Pubkey) (sdk.Pubkey, uint8, error) {
	return sdk.FindProgramAddress([][]byte{owner[:], tokenProgramID[:], mint[:]}, ProgramID)
}

// Create builds the "Create" instruction, which fails if the associated
// token account already exists.
func Create(payer, owner, mint, tokenProgramID sdk.Pubkey) (sdk.Instruction, sdk.Pubkey, error) {
	return build(payer, owner, mint, tokenProgramID, 0)
}

// CreateIdempotent builds the "CreateIdempotent" instruction, which succeeds
// as a no-op if the associated token account already exists. Prefer this
// over Create for setup flows that may be retried.
func CreateIdempotent(payer, owner, mint, tokenProgramID sdk.Pubkey) (sdk.Instruction, sdk.Pubkey, error) {
	return build(payer, owner, mint, tokenProgramID, 1)
}

func build(payer, owner, mint, tokenProgramID sdk.Pubkey, discriminator byte) (sdk.Instruction, sdk.Pubkey, error) {
	associatedAccount, _, err := Derive(owner, mint, tokenProgramID)
	if err != nil {
		return sdk.Instruction{}, sdk.Pubkey{}, err
	}
	instruction := sdk.Instruction{
		ProgramID: ProgramID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(payer, true),
			sdk.Writable(associatedAccount, false),
			sdk.Readonly(owner, false),
			sdk.Readonly(mint, false),
			sdk.Readonly(system.ProgramID, false),
			sdk.Readonly(tokenProgramID, false),
		},
		Data: []byte{discriminator},
	}
	return instruction, associatedAccount, nil
}
