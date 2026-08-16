// Package metaplex builds the one instruction this repository needs from
// the Metaplex Token Metadata program: Create (V1). Most Solana wallets,
// explorers, and DEX frontends (Raydium's UI included) resolve a token's
// display name/symbol from this program's Metadata PDA rather than from
// Token-2022's newer, self-contained metadata extension — a mint that only
// carries the Token-2022 extension is invisible to them.
//
// Create (V1) is required rather than the older CreateMetadataAccountV3:
// the token-metadata program's dispatcher routes any instruction whose
// accounts include a Token-2022-owned account away from the legacy
// processor entirely, failing with MetadataError::InstructionNotSupported
// (custom program error 0x99) before CreateMetadataAccountV3 ever runs.
// Create is also the only path that accepts an explicit spl_token_program
// account, which is what tells the program this is a Token-2022 mint.
//
// Layout pin: mpl-token-metadata program (metaqbxxUerdq28cj1RbAWkYQm3ybzjb6a8bt518x1s),
// Create instruction discriminator 42, CreateArgs::V1 { AssetData, decimals, print_supply }.
package metaplex

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/system"
)

// ProgramID is the Metaplex Token Metadata program address, identical on
// every cluster.
var ProgramID = sdk.MustParsePubkey("metaqbxxUerdq28cj1RbAWkYQm3ybzjb6a8bt518x1s")

// SysvarInstructionsID is the Instructions sysvar Create requires for its
// CPI-depth introspection. It is unrelated to (and not a substitute for)
// the Rent sysvar used by other programs.
var SysvarInstructionsID = sdk.MustParsePubkey("Sysvar1nstructions1111111111111111111111111")

const createDiscriminator = 42

// MetadataAccountSize is the fixed size (MAX_METADATA_LEN, in the program's
// source) the program allocates for a Metadata PDA regardless of how short
// the supplied name/symbol/uri are. Callers use it only to pre-flight a
// payer's rent budget; passing it to a rent-exemption query yields the
// lamports the Create instruction transfers into the new Metadata account.
const MetadataAccountSize = 607

// CreateFeeSizeScalar and CreateFeeOffsetLamports reproduce the program's
// get_create_fee() formula: a rent-exemption query for this many bytes,
// plus this many flat lamports, gives the protocol fee Create charges (on
// top of the Metadata account's own rent) — currently a fixed 0.01 SOL by
// construction, though callers should still compute it via a live
// rent-exemption query rather than hard-coding the lamport total.
const (
	CreateFeeSizeScalar     = 1308
	CreateFeeOffsetLamports = 5440
)

var ErrStringTooLong = errors.New("metaplex: string exceeds Borsh u32 length")

// DeriveMetadataAddress computes the PDA mpl-token-metadata uses to store a
// mint's on-chain metadata. The address is identical regardless of which
// instruction (legacy or Create) wrote it.
func DeriveMetadataAddress(mint sdk.Pubkey) (sdk.Pubkey, uint8, error) {
	return sdk.FindProgramAddress([][]byte{[]byte("metadata"), ProgramID[:], mint[:]}, ProgramID)
}

// DeriveMasterEditionAddress computes the PDA mpl-token-metadata uses for a
// mint's Master Edition account — the account that, together with the
// Metadata account, marks a mint as a real, wallet-visible NonFungible
// token and permanently caps its supply. See CreateNFTV1.
func DeriveMasterEditionAddress(mint sdk.Pubkey) (sdk.Pubkey, uint8, error) {
	return sdk.FindProgramAddress([][]byte{[]byte("metadata"), ProgramID[:], mint[:], []byte("edition")}, ProgramID)
}

// CreateV1 builds a Create instruction for a fungible token (no master
// edition, no creators/collection/uses/rule set) on an already-initialized
// mint. tokenProgramID must be the program that owns mint (Token or
// Token-2022) — the program uses it both for its own CPI calls and, via the
// dispatcher's incompatible_accounts check, to decide whether to route the
// instruction through the Token-2022-aware "new" API at all. mintAuthority
// must sign; updateAuthority only needs to co-sign when
// updateAuthoritySigner is true (the common case, updateAuthority == payer,
// already signs as fee payer, so this is usually false).
func CreateV1(
	mint, mintAuthority, payer, updateAuthority, tokenProgramID sdk.Pubkey,
	updateAuthoritySigner bool,
	name, symbol, uri string,
	decimals uint8,
	isMutable bool,
) (sdk.Instruction, sdk.Pubkey, error) {
	metadataAddress, _, err := DeriveMetadataAddress(mint)
	if err != nil {
		return sdk.Instruction{}, sdk.Pubkey{}, err
	}

	data := []byte{createDiscriminator, 0} // CreateArgs::V1 tag
	data, err = appendBorshString(data, name)
	if err != nil {
		return sdk.Instruction{}, sdk.Pubkey{}, err
	}
	data, err = appendBorshString(data, symbol)
	if err != nil {
		return sdk.Instruction{}, sdk.Pubkey{}, err
	}
	data, err = appendBorshString(data, uri)
	if err != nil {
		return sdk.Instruction{}, sdk.Pubkey{}, err
	}
	data = binary.LittleEndian.AppendUint16(data, 0) // seller_fee_basis_points
	data = append(data, 0)                           // creators: None
	data = append(data, 0)                           // primary_sale_happened: false
	data = append(data, boolByte(isMutable))         // is_mutable
	data = append(data, tokenStandardByte(decimals)) // token_standard
	data = append(data, 0)                           // collection: None
	data = append(data, 0)                           // uses: None
	data = append(data, 0)                           // collection_details: None
	data = append(data, 0)                           // rule_set: None
	data = append(data, 1, decimals)                 // decimals: Some(decimals)
	data = append(data, 0)                           // print_supply: None

	instruction := sdk.Instruction{
		ProgramID: ProgramID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(metadataAddress, false),
			sdk.Readonly(ProgramID, false), // master_edition: None (placeholder)
			sdk.Writable(mint, false),      // mint already exists; no signature needed here
			sdk.Readonly(mintAuthority, true),
			sdk.Writable(payer, true),
			sdk.Readonly(updateAuthority, updateAuthoritySigner),
			sdk.Readonly(system.ProgramID, false),
			sdk.Readonly(SysvarInstructionsID, false),
			sdk.Readonly(tokenProgramID, false), // spl_token_program: Some(tokenProgramID)
		},
		Data: data,
	}
	return instruction, metadataAddress, nil
}

// CreateNFTV1 builds a Create instruction for a genuine, wallet-visible
// NonFungible token: a Metadata account plus a Master Edition account,
// created together in one instruction. tokenProgramID must be the program
// that owns mint (Token or Token-2022).
//
// Two on-chain preconditions, taken from mpl-token-metadata's own
// processor (programs/token-metadata/program/src/processor/edition/
// create_master_edition_v3.rs: "if mint.supply != 1 {
// EditionsMustHaveExactlyOneToken }"), are the caller's responsibility and
// are not checked client-side here:
//
//  1. mint must already have decimals == 0 and exactly one token minted to
//     some token account (via a prior MintTo) before this instruction is
//     submitted — Create does not mint tokens itself.
//  2. mintAuthority must still be the mint's current mint authority when
//     this instruction runs. This instruction transfers mint authority to
//     the derived Master Edition PDA as a side effect of creating it,
//     permanently capping supply at 1 — no separate "disable mint
//     authority" call is needed, or possible, afterward.
func CreateNFTV1(
	mint, mintAuthority, payer, updateAuthority, tokenProgramID sdk.Pubkey,
	updateAuthoritySigner bool,
	name, symbol, uri string,
	isMutable bool,
) (sdk.Instruction, sdk.Pubkey, sdk.Pubkey, error) {
	metadataAddress, _, err := DeriveMetadataAddress(mint)
	if err != nil {
		return sdk.Instruction{}, sdk.Pubkey{}, sdk.Pubkey{}, err
	}
	masterEditionAddress, _, err := DeriveMasterEditionAddress(mint)
	if err != nil {
		return sdk.Instruction{}, sdk.Pubkey{}, sdk.Pubkey{}, err
	}

	data := []byte{createDiscriminator, 0} // CreateArgs::V1 tag
	data, err = appendBorshString(data, name)
	if err != nil {
		return sdk.Instruction{}, sdk.Pubkey{}, sdk.Pubkey{}, err
	}
	data, err = appendBorshString(data, symbol)
	if err != nil {
		return sdk.Instruction{}, sdk.Pubkey{}, sdk.Pubkey{}, err
	}
	data, err = appendBorshString(data, uri)
	if err != nil {
		return sdk.Instruction{}, sdk.Pubkey{}, sdk.Pubkey{}, err
	}
	data = binary.LittleEndian.AppendUint16(data, 0) // seller_fee_basis_points
	data = append(data, 0)                           // creators: None
	data = append(data, 0)                           // primary_sale_happened: false
	data = append(data, boolByte(isMutable))         // is_mutable
	data = append(data, 0)                           // token_standard: NonFungible
	data = append(data, 0)                           // collection: None
	data = append(data, 0)                           // uses: None
	data = append(data, 0)                           // collection_details: None
	data = append(data, 0)                           // rule_set: None
	data = append(data, 1, 0)                        // decimals: Some(0)
	data = append(data, 1, 0)                        // print_supply: Some(PrintSupply::Zero)

	instruction := sdk.Instruction{
		ProgramID: ProgramID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(metadataAddress, false),
			sdk.Writable(masterEditionAddress, false),
			sdk.Writable(mint, false), // mint already exists; no signature needed here
			sdk.Readonly(mintAuthority, true),
			sdk.Writable(payer, true),
			sdk.Readonly(updateAuthority, updateAuthoritySigner),
			sdk.Readonly(system.ProgramID, false),
			sdk.Readonly(SysvarInstructionsID, false),
			sdk.Readonly(tokenProgramID, false), // spl_token_program: Some(tokenProgramID)
		},
		Data: data,
	}
	return instruction, metadataAddress, masterEditionAddress, nil
}

// tokenStandardByte follows the same convention spl-token CLIs use: a
// zero-decimal mint is classified FungibleAsset (1), anything else
// Fungible (2). NonFungible/edition/programmable standards are out of
// scope for this package.
func tokenStandardByte(decimals uint8) byte {
	if decimals == 0 {
		return 1 // TokenStandard::FungibleAsset
	}
	return 2 // TokenStandard::Fungible
}

func boolByte(value bool) byte {
	if value {
		return 1
	}
	return 0
}

func appendBorshString(dst []byte, value string) ([]byte, error) {
	if len(value) > math.MaxUint32 {
		return nil, ErrStringTooLong
	}
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(value)))
	return append(dst, value...), nil
}
