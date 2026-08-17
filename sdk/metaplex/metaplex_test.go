package metaplex

import (
	"encoding/binary"
	"testing"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/system"
)

func testPubkey(t *testing.T, seed byte) sdk.Pubkey {
	t.Helper()
	var raw [sdk.PubkeySize]byte
	for i := range raw {
		raw[i] = seed
	}
	key, err := sdk.PubkeyFromBytes(raw[:])
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestDeriveMetadataAddressMatchesManualSeeds(t *testing.T) {
	mint := testPubkey(t, 7)
	got, bump, err := DeriveMetadataAddress(mint)
	if err != nil {
		t.Fatal(err)
	}
	want, wantBump, err := sdk.FindProgramAddress([][]byte{[]byte("metadata"), ProgramID[:], mint[:]}, ProgramID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || bump != wantBump {
		t.Fatalf("derived %s/%d, want %s/%d", got, bump, want, wantBump)
	}
	if got.IsOnCurve() {
		t.Fatal("derived metadata PDA lies on the ed25519 curve")
	}
}

func TestCreateV1AccountsAndData(t *testing.T) {
	mint := testPubkey(t, 1)
	authority := testPubkey(t, 2)
	payer := testPubkey(t, 3)
	tokenProgram := testPubkey(t, 4) // stands in for token2022.ProgramID

	instruction, metadataAddress, err := CreateV1(mint, authority, payer, payer, tokenProgram, false, "WIWIW", "WIWIW", "https://example.com/m.json", 6, true)
	if err != nil {
		t.Fatal(err)
	}
	if instruction.ProgramID != ProgramID {
		t.Fatalf("program id = %s, want %s", instruction.ProgramID, ProgramID)
	}
	wantMetadata, _, err := DeriveMetadataAddress(mint)
	if err != nil {
		t.Fatal(err)
	}
	if metadataAddress != wantMetadata {
		t.Fatalf("returned metadata address %s, want %s", metadataAddress, wantMetadata)
	}

	wantAccounts := []sdk.AccountMeta{
		sdk.Writable(metadataAddress, false),
		sdk.Readonly(ProgramID, false),
		sdk.Writable(mint, false),
		sdk.Readonly(authority, true),
		sdk.Writable(payer, true),
		sdk.Readonly(payer, false),
		sdk.Readonly(system.ProgramID, false),
		sdk.Readonly(SysvarInstructionsID, false),
		sdk.Readonly(tokenProgram, false),
	}
	if len(instruction.Accounts) != len(wantAccounts) {
		t.Fatalf("accounts = %d, want %d", len(instruction.Accounts), len(wantAccounts))
	}
	for i, want := range wantAccounts {
		if instruction.Accounts[i] != want {
			t.Fatalf("account[%d] = %+v, want %+v", i, instruction.Accounts[i], want)
		}
	}

	data := instruction.Data
	if len(data) < 2 || data[0] != createDiscriminator || data[1] != 0 {
		t.Fatalf("discriminator/variant = %v, want [%d 0 ...]", data[:min(2, len(data))], createDiscriminator)
	}
	offset := 2
	name, offset := readTestBorshString(t, data, offset)
	symbol, offset := readTestBorshString(t, data, offset)
	uri, offset := readTestBorshString(t, data, offset)
	if name != "WIWIW" || symbol != "WIWIW" || uri != "https://example.com/m.json" {
		t.Fatalf("name/symbol/uri = %q/%q/%q", name, symbol, uri)
	}
	sellerFee := binary.LittleEndian.Uint16(data[offset : offset+2])
	offset += 2
	if sellerFee != 0 {
		t.Fatalf("seller_fee_basis_points = %d, want 0", sellerFee)
	}
	fields := []struct {
		name string
		want byte
	}{
		{"creators option", 0},
		{"primary_sale_happened", 0},
		{"is_mutable", 1},
		{"token_standard", 2}, // Fungible, decimals=6
		{"collection option", 0},
		{"uses option", 0},
		{"collection_details option", 0},
		{"rule_set option", 0},
	}
	for _, field := range fields {
		if data[offset] != field.want {
			t.Fatalf("%s byte = %d, want %d", field.name, data[offset], field.want)
		}
		offset++
	}
	if data[offset] != 1 || data[offset+1] != 6 {
		t.Fatalf("decimals option = %v, want [1 6] (Some(6))", data[offset:offset+2])
	}
	offset += 2
	if data[offset] != 0 { // print_supply: None
		t.Fatalf("print_supply option byte = %d, want 0 (None)", data[offset])
	}
	offset++
	if offset != len(data) {
		t.Fatalf("trailing bytes after decode: consumed %d of %d", offset, len(data))
	}
}

func TestCreateV1TokenStandardByDecimals(t *testing.T) {
	mint := testPubkey(t, 1)
	authority := testPubkey(t, 2)
	payer := testPubkey(t, 3)
	tokenProgram := testPubkey(t, 4)

	instruction, _, err := CreateV1(mint, authority, payer, payer, tokenProgram, false, "N", "S", "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	// token_standard sits right after: discriminator(1)+variant(1)+name(4+1)+symbol(4+1)+uri(4+0)
	// +seller_fee(2)+creators(1)+primary_sale(1)+is_mutable(1)
	offset := 2 + (4 + 1) + (4 + 1) + (4 + 0) + 2 + 1 + 1 + 1
	if instruction.Data[offset] != 1 {
		t.Fatalf("token_standard for decimals=0 = %d, want 1 (FungibleAsset)", instruction.Data[offset])
	}
}

func TestDeriveMasterEditionAddressMatchesManualSeeds(t *testing.T) {
	mint := testPubkey(t, 9)
	got, bump, err := DeriveMasterEditionAddress(mint)
	if err != nil {
		t.Fatal(err)
	}
	want, wantBump, err := sdk.FindProgramAddress([][]byte{[]byte("metadata"), ProgramID[:], mint[:], []byte("edition")}, ProgramID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || bump != wantBump {
		t.Fatalf("derived %s/%d, want %s/%d", got, bump, want, wantBump)
	}
	metadataAddress, _, err := DeriveMetadataAddress(mint)
	if err != nil {
		t.Fatal(err)
	}
	if got == metadataAddress {
		t.Fatal("master edition PDA must differ from the metadata PDA")
	}
}

func TestCreateNFTV1AccountsAndData(t *testing.T) {
	mint := testPubkey(t, 1)
	authority := testPubkey(t, 2)
	payer := testPubkey(t, 3)
	tokenProgram := testPubkey(t, 4) // stands in for token2022.ProgramID

	instruction, metadataAddress, masterEditionAddress, err := CreateNFTV1(mint, authority, payer, payer, tokenProgram, false, "WIWIW", "WIWIW", "https://example.com/m.json", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if instruction.ProgramID != ProgramID {
		t.Fatalf("program id = %s, want %s", instruction.ProgramID, ProgramID)
	}
	wantMetadata, _, err := DeriveMetadataAddress(mint)
	if err != nil {
		t.Fatal(err)
	}
	if metadataAddress != wantMetadata {
		t.Fatalf("returned metadata address %s, want %s", metadataAddress, wantMetadata)
	}
	wantMasterEdition, _, err := DeriveMasterEditionAddress(mint)
	if err != nil {
		t.Fatal(err)
	}
	if masterEditionAddress != wantMasterEdition {
		t.Fatalf("returned master edition address %s, want %s", masterEditionAddress, wantMasterEdition)
	}

	wantAccounts := []sdk.AccountMeta{
		sdk.Writable(metadataAddress, false),
		sdk.Writable(masterEditionAddress, false),
		sdk.Writable(mint, false),
		sdk.Readonly(authority, true),
		sdk.Writable(payer, true),
		sdk.Readonly(payer, false),
		sdk.Readonly(system.ProgramID, false),
		sdk.Readonly(SysvarInstructionsID, false),
		sdk.Readonly(tokenProgram, false),
	}
	if len(instruction.Accounts) != len(wantAccounts) {
		t.Fatalf("accounts = %d, want %d", len(instruction.Accounts), len(wantAccounts))
	}
	for i, want := range wantAccounts {
		if instruction.Accounts[i] != want {
			t.Fatalf("account[%d] = %+v, want %+v", i, instruction.Accounts[i], want)
		}
	}

	data := instruction.Data
	if len(data) < 2 || data[0] != createDiscriminator || data[1] != 0 {
		t.Fatalf("discriminator/variant = %v, want [%d 0 ...]", data[:min(2, len(data))], createDiscriminator)
	}
	offset := 2
	name, offset := readTestBorshString(t, data, offset)
	symbol, offset := readTestBorshString(t, data, offset)
	uri, offset := readTestBorshString(t, data, offset)
	if name != "WIWIW" || symbol != "WIWIW" || uri != "https://example.com/m.json" {
		t.Fatalf("name/symbol/uri = %q/%q/%q", name, symbol, uri)
	}
	sellerFee := binary.LittleEndian.Uint16(data[offset : offset+2])
	offset += 2
	if sellerFee != 0 {
		t.Fatalf("seller_fee_basis_points = %d, want 0", sellerFee)
	}
	fields := []struct {
		name string
		want byte
	}{
		{"creators option", 0},
		{"primary_sale_happened", 0},
		{"is_mutable", 0},
		{"token_standard", 0}, // NonFungible
		{"collection option", 0},
		{"uses option", 0},
		{"collection_details option", 0},
		{"rule_set option", 0},
	}
	for _, field := range fields {
		if data[offset] != field.want {
			t.Fatalf("%s byte = %d, want %d", field.name, data[offset], field.want)
		}
		offset++
	}
	if data[offset] != 1 || data[offset+1] != 0 {
		t.Fatalf("decimals option = %v, want [1 0] (Some(0))", data[offset:offset+2])
	}
	offset += 2
	if data[offset] != 1 || data[offset+1] != 0 {
		t.Fatalf("print_supply option = %v, want [1 0] (Some(PrintSupply::Zero))", data[offset:offset+2])
	}
	offset += 2
	if offset != len(data) {
		t.Fatalf("trailing bytes after decode: consumed %d of %d", offset, len(data))
	}
}

func TestCreateNFTV1WithRoyaltySetsVerifiedCreator(t *testing.T) {
	mint := testPubkey(t, 1)
	authority := testPubkey(t, 2)
	payer := testPubkey(t, 3)
	tokenProgram := testPubkey(t, 4)

	instruction, _, _, err := CreateNFTV1(mint, authority, payer, payer, tokenProgram, false, "N", "S", "https://example.com/m.json", false, 500)
	if err != nil {
		t.Fatal(err)
	}
	data := instruction.Data
	offset := 2
	_, offset = readTestBorshString(t, data, offset) // name
	_, offset = readTestBorshString(t, data, offset) // symbol
	_, offset = readTestBorshString(t, data, offset) // uri
	sellerFee := binary.LittleEndian.Uint16(data[offset : offset+2])
	offset += 2
	if sellerFee != 500 {
		t.Fatalf("seller_fee_basis_points = %d, want 500", sellerFee)
	}
	if data[offset] != 1 {
		t.Fatalf("creators option byte = %d, want 1 (Some)", data[offset])
	}
	offset++
	creatorCount := binary.LittleEndian.Uint32(data[offset : offset+4])
	offset += 4
	if creatorCount != 1 {
		t.Fatalf("creators length = %d, want 1", creatorCount)
	}
	gotAddress, err := sdk.PubkeyFromBytes(data[offset : offset+sdk.PubkeySize])
	if err != nil {
		t.Fatal(err)
	}
	if gotAddress != payer { // updateAuthority == payer in this call
		t.Fatalf("creator address = %s, want update authority %s", gotAddress, payer)
	}
	offset += sdk.PubkeySize
	if data[offset] != 1 {
		t.Fatalf("creator.verified = %d, want 1 (true)", data[offset])
	}
	offset++
	if data[offset] != 100 {
		t.Fatalf("creator.share = %d, want 100", data[offset])
	}
	offset++
	if data[offset] != 0 { // primary_sale_happened
		t.Fatalf("primary_sale_happened byte = %d, want 0", data[offset])
	}
}

func TestCreateNFTV1RejectsExcessiveRoyalty(t *testing.T) {
	mint := testPubkey(t, 1)
	authority := testPubkey(t, 2)
	payer := testPubkey(t, 3)
	tokenProgram := testPubkey(t, 4)

	if _, _, _, err := CreateNFTV1(mint, authority, payer, payer, tokenProgram, false, "N", "S", "u", false, 10001); err == nil {
		t.Fatal("seller fee basis points above 10000 accepted")
	}
}

func readTestBorshString(t *testing.T, data []byte, offset int) (string, int) {
	t.Helper()
	if offset+4 > len(data) {
		t.Fatalf("string length prefix out of range at offset %d", offset)
	}
	length := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
	offset += 4
	if offset+length > len(data) {
		t.Fatalf("string body out of range at offset %d length %d", offset, length)
	}
	return string(data[offset : offset+length]), offset + length
}
