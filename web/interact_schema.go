// interact_schema.go is this UI's "ABI": a declarative description of every
// instruction and readable account state for each deployable example,
// interpreted generically by interact_handlers.go and rendered generically
// by the frontend — one form per instruction, one viewer per state layout,
// the same way a Remix-style IDE reads a contract's ABI instead of having
// bespoke code per contract. Every tag/account/offset below was read
// directly out of each example's guest source or existing host package
// (examples/gospl/instruction.go, examples/erc1155/instruction.go+codec.go,
// examples/payable/*.go, examples/*/testdata/program.go,
// examples/phonebook/cmd/phonebook/main.go) — nothing here is guessed.
package web

import "github.com/ersanyakit/solanago/sdk"

type FieldType string

const (
	FieldPubkey    FieldType = "pubkey" // 32 bytes
	FieldU8        FieldType = "u8"
	FieldU16       FieldType = "u16"
	FieldU32       FieldType = "u32"
	FieldU64       FieldType = "u64"
	FieldBool      FieldType = "bool"      // 1 byte, 0/1
	FieldString    FieldType = "string"    // fixed Len bytes, zero-padded UTF-8
	FieldVarString FieldType = "varstring" // u32 LE length prefix + raw bytes, no padding; must be the last field
)

// FieldSpec describes one instruction-data field, in wire order after the
// instruction's Tag.
type FieldSpec struct {
	Name string    `json:"name"`
	Type FieldType `json:"type"`
	Len  int       `json:"len,omitempty"` // required for FieldString
	Help string    `json:"help,omitempty"`
}

// AccountRoleSpec describes one account meta an instruction needs, and how
// this UI should obtain its pubkey:
//
//   - NewAccount: this backend generates a fresh ephemeral keypair, prepends
//     a System::CreateAccount sized Space and owned by the target program,
//     and signs that account's half of the combined transaction — the
//     frontend never sees or asks for this pubkey ahead of time, only shows
//     it in the result.
//   - Default "wallet": always the connected wallet; never asked for.
//   - Default "system": always the System Program; never asked for.
//   - DerivedFromAccount/Field/Layout: resolved server-side by decoding
//     another role's resolved account under the named StateLayoutSpec and
//     reading one pubkey field out of it (e.g. phonebook add-contact's
//     treasury account, read from the config account's own treasury
//     field) — never asked for.
//   - Otherwise: the frontend must collect an existing pubkey from the user.
type AccountRoleSpec struct {
	Name               string `json:"name"`
	Signer             bool   `json:"signer"`
	Writable           bool   `json:"writable"`
	NewAccount         bool   `json:"newAccount,omitempty"`
	Space              uint64 `json:"space,omitempty"`
	Default            string `json:"default,omitempty"`
	DerivedFromAccount string `json:"derivedFromAccount,omitempty"`
	DerivedFromLayout  string `json:"derivedFromLayout,omitempty"`
	DerivedFromField   string `json:"derivedFromField,omitempty"`
	Help               string `json:"help,omitempty"`
}

// InstructionSpec is one callable "method." Tag is nil for the one example
// (cpi) whose instruction data carries no discriminant at all.
type InstructionSpec struct {
	Name     string            `json:"name"`
	Tag      []byte            `json:"tag"`
	Accounts []AccountRoleSpec `json:"accounts"`
	Fields   []FieldSpec       `json:"fields"`
	Help     string            `json:"help,omitempty"`
}

// StateFieldSpec is one fixed-offset field inside an account's raw data,
// for read-only display — this UI's analogue of a Solidity public storage
// variable / view function.
type StateFieldSpec struct {
	Name   string    `json:"name"`
	Offset uint64    `json:"offset"`
	Type   FieldType `json:"type"`
	Len    int       `json:"len,omitempty"`
}

// StateLayoutSpec describes one fixed-size, fixed-shape account type. It
// intentionally does not support repeated/array regions (e.g. phonebook's
// contact list) — those keep their own dedicated endpoint; see
// phonebook.go's fetchPhonebookState.
type StateLayoutSpec struct {
	Name   string           `json:"name"`
	Size   uint64           `json:"size"`
	Fields []StateFieldSpec `json:"fields"`
}

// ExampleInteractSchema is one example's full "ABI."
type ExampleInteractSchema struct {
	Instructions []InstructionSpec `json:"instructions"`
	States       []StateLayoutSpec `json:"states"`
}

// InteractSchemas holds every example with a wired-up "Use it" surface.
// Examples absent from this map (currently none of the seven Deploy tabs)
// get no Methods panel.
var InteractSchemas = map[string]ExampleInteractSchema{
	"hellogo":      tokenProgramSchema(),
	"gospl":        tokenProgramSchema(),
	"erc1155":      erc1155Schema(),
	"name-storage": nameStorageSchema(),
	"cpi":          cpiSchema(),
	"payable":      payableSchema(),
	"phonebook":    phonebookSchema(),
}

func pubkeyField(name string) FieldSpec { return FieldSpec{Name: name, Type: FieldPubkey} }
func u64Field(name string) FieldSpec    { return FieldSpec{Name: name, Type: FieldU64} }

// tokenProgramSchema covers both hellogo and gospl: their guest sources are
// byte-identical (see examples/hellogo/testdata/program.go's own comment),
// and examples/hellogo's host package (instruction.go/types.go/codec.go)
// mirrors examples/gospl's exactly, differing only in error-message text.
func tokenProgramSchema() ExampleInteractSchema {
	authorityRole := AccountRoleSpec{Name: "authority", Signer: true, Writable: false, Default: "wallet"}
	return ExampleInteractSchema{
		Instructions: []InstructionSpec{
			{
				Name: "InitializeMint",
				Tag:  []byte{0},
				Accounts: []AccountRoleSpec{
					{Name: "mint", Signer: true, Writable: true, NewAccount: true, Space: 48},
				},
				Fields: []FieldSpec{
					{Name: "decimals", Type: FieldU8},
					pubkeyField("mintAuthority"),
				},
				Help: "Creates a new mint with a revocable mint authority.",
			},
			{
				Name: "InitializeAccount",
				Tag:  []byte{1},
				Accounts: []AccountRoleSpec{
					{Name: "account", Signer: true, Writable: true, NewAccount: true, Space: 120},
					{Name: "mint", Signer: false, Writable: false},
				},
				Fields: []FieldSpec{pubkeyField("owner")},
				Help:   "Creates a new token account for mint, owned by owner.",
			},
			{
				Name: "Transfer",
				Tag:  []byte{2},
				Accounts: []AccountRoleSpec{
					{Name: "source", Signer: false, Writable: true},
					{Name: "destination", Signer: false, Writable: true},
					authorityRole,
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "Moves amount from source to destination. authority is the source's owner or delegate.",
			},
			{
				Name: "MintTo",
				Tag:  []byte{3},
				Accounts: []AccountRoleSpec{
					{Name: "mint", Signer: false, Writable: true},
					{Name: "destination", Signer: false, Writable: true},
					authorityRole,
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "Mints amount new tokens into destination. authority must be mint's mint authority.",
			},
			{
				Name: "Burn",
				Tag:  []byte{4},
				Accounts: []AccountRoleSpec{
					{Name: "source", Signer: false, Writable: true},
					{Name: "mint", Signer: false, Writable: true},
					authorityRole,
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "Burns amount tokens from source.",
			},
			{
				Name: "Approve",
				Tag:  []byte{5},
				Accounts: []AccountRoleSpec{
					{Name: "source", Signer: false, Writable: true},
					{Name: "owner", Signer: true, Writable: false, Default: "wallet"},
					{Name: "delegate", Signer: false, Writable: false},
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "Authorizes delegate to spend up to amount from source.",
			},
			{
				Name: "Revoke",
				Tag:  []byte{6},
				Accounts: []AccountRoleSpec{
					{Name: "source", Signer: false, Writable: true},
					{Name: "owner", Signer: true, Writable: false, Default: "wallet"},
				},
				Help: "Clears source's delegate and delegated amount.",
			},
			{
				Name: "SetAuthority",
				Tag:  []byte{7},
				Accounts: []AccountRoleSpec{
					{Name: "target", Signer: false, Writable: true},
					{Name: "currentAuthority", Signer: true, Writable: false, Default: "wallet"},
				},
				Fields: []FieldSpec{
					{Name: "authorityType", Type: FieldU8, Help: "0 = mint authority (target must be a mint), 1 = account owner (target must be a token account)"},
					{Name: "newAuthoritySet", Type: FieldBool, Help: "0 to permanently disable the authority, 1 to set newAuthority"},
					pubkeyField("newAuthority"),
				},
				Help: "Changes or permanently disables target's mint/owner authority.",
			},
		},
		States: []StateLayoutSpec{
			{
				Name: "mint",
				Size: 48,
				Fields: []StateFieldSpec{
					{Name: "initialized", Offset: 1, Type: FieldBool},
					{Name: "decimals", Offset: 2, Type: FieldU8},
					{Name: "supply", Offset: 8, Type: FieldU64},
					{Name: "mintAuthority", Offset: 16, Type: FieldPubkey},
				},
			},
			{
				Name: "tokenAccount",
				Size: 120,
				Fields: []StateFieldSpec{
					{Name: "initialized", Offset: 1, Type: FieldBool},
					{Name: "amount", Offset: 8, Type: FieldU64},
					{Name: "delegatedAmount", Offset: 16, Type: FieldU64},
					{Name: "mint", Offset: 24, Type: FieldPubkey},
					{Name: "owner", Offset: 56, Type: FieldPubkey},
					{Name: "delegate", Offset: 88, Type: FieldPubkey},
				},
			},
		},
	}
}

func erc1155Schema() ExampleInteractSchema {
	return ExampleInteractSchema{
		Instructions: []InstructionSpec{
			{
				Name: "InitializeCollection",
				Tag:  []byte{0},
				Accounts: []AccountRoleSpec{
					{Name: "collection", Signer: true, Writable: true, NewAccount: true, Space: 41},
				},
				Fields: []FieldSpec{pubkeyField("authority")},
				Help:   "Creates a new collection (\"contract instance\").",
			},
			{
				Name: "CreateTokenType",
				Tag:  []byte{1},
				Accounts: []AccountRoleSpec{
					{Name: "tokenType", Signer: true, Writable: true, NewAccount: true, Space: 117},
					{Name: "collection", Signer: false, Writable: true},
					{Name: "authority", Signer: true, Writable: false, Default: "wallet"},
				},
				Fields: []FieldSpec{{Name: "uri", Type: FieldVarString, Help: "max 64 bytes"}},
				Help:   "Defines a new token id under collection, assigned collection's next_id.",
			},
			{
				Name: "InitializeBalance",
				Tag:  []byte{2},
				Accounts: []AccountRoleSpec{
					{Name: "balance", Signer: true, Writable: true, NewAccount: true, Space: 81},
					{Name: "tokenType", Signer: false, Writable: false},
				},
				Fields: []FieldSpec{pubkeyField("owner")},
				Help:   "Creates a zeroed balance for (tokenType.id, owner).",
			},
			{
				Name: "MintTo",
				Tag:  []byte{3},
				Accounts: []AccountRoleSpec{
					{Name: "collection", Signer: false, Writable: false},
					{Name: "tokenType", Signer: false, Writable: true},
					{Name: "balance", Signer: false, Writable: true},
					{Name: "authority", Signer: true, Writable: false, Default: "wallet"},
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "Increases tokenType's supply and balance's amount.",
			},
			{
				Name: "Burn",
				Tag:  []byte{4},
				Accounts: []AccountRoleSpec{
					{Name: "tokenType", Signer: false, Writable: true},
					{Name: "balance", Signer: false, Writable: true},
					{Name: "owner", Signer: true, Writable: false, Default: "wallet"},
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "Decreases tokenType's supply and balance's amount.",
			},
			{
				Name: "Transfer",
				Tag:  []byte{5},
				Accounts: []AccountRoleSpec{
					{Name: "source", Signer: false, Writable: true},
					{Name: "destination", Signer: false, Writable: true},
					{Name: "owner", Signer: true, Writable: false, Default: "wallet"},
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "Moves amount from source to destination (same tokenType/id).",
			},
			{
				Name: "InitializeApproval",
				Tag:  []byte{6},
				Accounts: []AccountRoleSpec{
					{Name: "approval", Signer: true, Writable: true, NewAccount: true, Space: 98},
					{Name: "owner", Signer: true, Writable: false, Default: "wallet"},
					{Name: "operator", Signer: false, Writable: false},
				},
				Fields: []FieldSpec{pubkeyField("collection"), {Name: "approved", Type: FieldBool}},
				Help:   "Creates a new approve-for-all record for (owner, operator).",
			},
			{
				Name: "SetApproval",
				Tag:  []byte{7},
				Accounts: []AccountRoleSpec{
					{Name: "approval", Signer: false, Writable: true},
					{Name: "owner", Signer: true, Writable: false, Default: "wallet"},
				},
				Fields: []FieldSpec{{Name: "approved", Type: FieldBool}},
				Help:   "Flips an existing approval's approved flag.",
			},
			{
				Name: "TransferFrom",
				Tag:  []byte{8},
				Accounts: []AccountRoleSpec{
					{Name: "source", Signer: false, Writable: true},
					{Name: "destination", Signer: false, Writable: true},
					{Name: "approval", Signer: false, Writable: false},
					{Name: "operator", Signer: true, Writable: false, Default: "wallet"},
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "Moves amount from source to destination on behalf of source's owner, authorized by approval.",
			},
		},
		States: []StateLayoutSpec{
			{
				Name: "collection",
				Size: 41,
				Fields: []StateFieldSpec{
					{Name: "authority", Offset: 1, Type: FieldPubkey},
					{Name: "nextId", Offset: 33, Type: FieldU64},
				},
			},
			{
				Name: "tokenType",
				Size: 117,
				Fields: []StateFieldSpec{
					{Name: "collection", Offset: 1, Type: FieldPubkey},
					{Name: "id", Offset: 33, Type: FieldU64},
					{Name: "supply", Offset: 41, Type: FieldU64},
					{Name: "uri", Offset: 49, Type: FieldVarString},
				},
			},
			{
				Name: "balance",
				Size: 81,
				Fields: []StateFieldSpec{
					{Name: "collection", Offset: 1, Type: FieldPubkey},
					{Name: "id", Offset: 33, Type: FieldU64},
					{Name: "owner", Offset: 41, Type: FieldPubkey},
					{Name: "amount", Offset: 73, Type: FieldU64},
				},
			},
			{
				Name: "approval",
				Size: 98,
				Fields: []StateFieldSpec{
					{Name: "collection", Offset: 1, Type: FieldPubkey},
					{Name: "owner", Offset: 33, Type: FieldPubkey},
					{Name: "operator", Offset: 65, Type: FieldPubkey},
					{Name: "approved", Offset: 97, Type: FieldBool},
				},
			},
		},
	}
}

func nameStorageSchema() ExampleInteractSchema {
	return ExampleInteractSchema{
		Instructions: []InstructionSpec{
			{
				Name: "RecordName",
				Tag:  []byte{1},
				Accounts: []AccountRoleSpec{
					{Name: "storage", Signer: true, Writable: true, NewAccount: true, Space: 65},
				},
				Fields: []FieldSpec{
					{Name: "name", Type: FieldString, Len: 32},
					{Name: "surname", Type: FieldString, Len: 32},
				},
				Help: "Creates storage and writes name/surname into it.",
			},
		},
		States: []StateLayoutSpec{
			{
				Name: "storage",
				Size: 65,
				Fields: []StateFieldSpec{
					{Name: "initialized", Offset: 0, Type: FieldBool},
					{Name: "name", Offset: 1, Type: FieldString, Len: 32},
					{Name: "surname", Offset: 33, Type: FieldString, Len: 32},
				},
			},
		},
	}
}

func cpiSchema() ExampleInteractSchema {
	return ExampleInteractSchema{
		Instructions: []InstructionSpec{
			{
				Name: "Transfer",
				Tag:  nil,
				Accounts: []AccountRoleSpec{
					{Name: "source", Signer: true, Writable: true, Default: "wallet"},
					{Name: "destination", Signer: false, Writable: true},
					{Name: "systemProgram", Signer: false, Writable: false, Default: "system"},
				},
				Fields: []FieldSpec{u64Field("lamports")},
				Help:   "Asks the program to CPI a System Program transfer from source (your wallet) to destination.",
			},
		},
	}
}

func payableSchema() ExampleInteractSchema {
	return ExampleInteractSchema{
		Instructions: []InstructionSpec{
			{
				Name: "InitializeVault",
				Tag:  []byte{0},
				Accounts: []AccountRoleSpec{
					{Name: "vault", Signer: true, Writable: true, NewAccount: true, Space: 48},
				},
				Fields: []FieldSpec{pubkeyField("authority")},
				Help:   "Creates an empty vault ledger. authority is the only signer EmergencyWithdraw will accept.",
			},
			{
				Name: "InitializeDepositAccount",
				Tag:  []byte{1},
				Accounts: []AccountRoleSpec{
					{Name: "depositAccount", Signer: true, Writable: true, NewAccount: true, Space: 80},
					{Name: "vault", Signer: false, Writable: false},
				},
				Fields: []FieldSpec{pubkeyField("depositor")},
				Help:   "Binds a fresh deposit account to (vault, depositor).",
			},
			{
				Name: "Deposit",
				Tag:  []byte{2},
				Accounts: []AccountRoleSpec{
					{Name: "vault", Signer: false, Writable: true},
					{Name: "depositAccount", Signer: false, Writable: true},
					{Name: "depositor", Signer: true, Writable: true, Default: "wallet"},
					{Name: "systemProgram", Signer: false, Writable: false, Default: "system"},
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "Moves amount lamports from you into vault via CPI, credits depositAccount's balance.",
			},
			{
				Name: "Withdraw",
				Tag:  []byte{3},
				Accounts: []AccountRoleSpec{
					{Name: "vault", Signer: false, Writable: true},
					{Name: "depositAccount", Signer: false, Writable: true},
					{Name: "depositor", Signer: true, Writable: false, Default: "wallet"},
					{Name: "recipient", Signer: false, Writable: true},
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "Moves amount lamports from vault to recipient, debits depositAccount's balance.",
			},
			{
				Name: "EmergencyWithdraw",
				Tag:  []byte{4},
				Accounts: []AccountRoleSpec{
					{Name: "vault", Signer: false, Writable: true},
					{Name: "authority", Signer: true, Writable: false, Default: "wallet"},
					{Name: "recipient", Signer: false, Writable: true},
				},
				Fields: []FieldSpec{u64Field("amount")},
				Help:   "onlyOwner rescue: moves amount lamports from vault to recipient. authority must match vault's Authority.",
			},
		},
		States: []StateLayoutSpec{
			{
				Name: "vault",
				Size: 48,
				Fields: []StateFieldSpec{
					{Name: "initialized", Offset: 1, Type: FieldBool},
					{Name: "totalDeposited", Offset: 8, Type: FieldU64},
					{Name: "authority", Offset: 16, Type: FieldPubkey},
				},
			},
			{
				Name: "depositAccount",
				Size: 80,
				Fields: []StateFieldSpec{
					{Name: "initialized", Offset: 1, Type: FieldBool},
					{Name: "balance", Offset: 8, Type: FieldU64},
					{Name: "vault", Offset: 16, Type: FieldPubkey},
					{Name: "depositor", Offset: 48, Type: FieldPubkey},
				},
			},
		},
	}
}

func phonebookSchema() ExampleInteractSchema {
	return ExampleInteractSchema{
		Instructions: []InstructionSpec{
			{
				Name: "InitConfig",
				Tag:  []byte{1},
				Accounts: []AccountRoleSpec{
					{Name: "config", Signer: false, Writable: true, NewAccount: true, Space: PhonebookConfigDataLen},
					{Name: "admin", Signer: true, Writable: true, Default: "wallet"},
					{Name: "treasury", Signer: false, Writable: false, Default: "wallet"},
				},
				Fields: []FieldSpec{u64Field("feeLamports")},
				Help:   "Creates the Config account. Leave treasury as your wallet unless you want fees sent elsewhere.",
			},
			{
				Name: "InitPhonebook",
				Tag:  []byte{2},
				Accounts: []AccountRoleSpec{
					{Name: "phonebook", Signer: false, Writable: true, NewAccount: true, Space: PhonebookDataLen},
					{Name: "owner", Signer: true, Writable: true, Default: "wallet"},
				},
				Help: "Creates the Phonebook account, owned by you.",
			},
			{
				Name: "AddContact",
				Tag:  []byte{3},
				Accounts: []AccountRoleSpec{
					{Name: "phonebook", Signer: false, Writable: true},
					{Name: "owner", Signer: true, Writable: true, Default: "wallet"},
					{Name: "config", Signer: false, Writable: false},
					{Name: "treasury", Signer: false, Writable: true, DerivedFromAccount: "config", DerivedFromLayout: "config", DerivedFromField: "treasury"},
					{Name: "systemProgram", Signer: false, Writable: false, Default: "system"},
				},
				Fields: []FieldSpec{pubkeyField("address"), {Name: "name", Type: FieldString, Len: 32}},
				Help:   "Adds or updates a contact. Charges config's feeLamports to treasury.",
			},
			{
				Name: "Withdraw",
				Tag:  []byte{4},
				Accounts: []AccountRoleSpec{
					{Name: "config", Signer: false, Writable: false},
					{Name: "admin", Signer: true, Writable: true, Default: "wallet"},
					{Name: "destination", Signer: false, Writable: true},
					{Name: "systemProgram", Signer: false, Writable: false, Default: "system"},
				},
				Fields: []FieldSpec{u64Field("amountLamports")},
				Help:   "Admin-only: withdraws collected fees (treasury must equal admin). 0 = full balance.",
			},
		},
		States: []StateLayoutSpec{
			{
				Name: "config",
				Size: PhonebookConfigDataLen,
				Fields: []StateFieldSpec{
					{Name: "initialized", Offset: 0, Type: FieldBool},
					{Name: "admin", Offset: 2, Type: FieldPubkey},
					{Name: "treasury", Offset: 34, Type: FieldPubkey},
					{Name: "feeLamports", Offset: 66, Type: FieldU64},
				},
			},
		},
	}
}

// zeroPubkey is the well-known System Program address (32 zero bytes).
var zeroPubkey sdk.Pubkey
