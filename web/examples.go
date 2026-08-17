// Package web serves the browser deploy UI: one tab per compiled example
// program in this repository, wired to a Phantom-signed BPF Upgradeable
// Loader v3 deploy flow (see deploy.PrepareCreateBufferTransaction and
// deploy.PrepareDeployTransaction). The backend never holds the user's
// wallet private key — it only prepares partially-signed transactions for
// the two ephemeral accounts it generates itself (the loader buffer and the
// new program account), and the browser completes and submits them.
package web

// Example describes one guest sBPFv3 program this server can build and
// prepare a wallet-signed deploy for. ID is the URL path segment and the
// frontend tab key.
type Example struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Sources     []string `json:"sources"`
}

// Examples is every guest program with a compiled Deploy tab. context and
// solana_noop are excluded (compiler smoke-test fixtures with no
// instruction set); token2022's clients talk to Solana's own pre-existing
// official Token-2022 program, not a program of this repository, so it has
// nothing of its own to deploy; spl20 is a native-Go-only reference oracle
// with no guest source to compile.
var Examples = []Example{
	{
		ID:          "hellogo",
		Name:        "hellogo",
		Description: "Minimal Go-to-sBPF entrypoint smoke test.",
		Sources:     []string{"examples/hellogo/testdata/program.go"},
	},
	{
		ID:          "name-storage",
		Name:        "name-storage",
		Description: "Fixed-size name/surname account storage.",
		Sources:     []string{"examples/name-storage/testdata/program.go"},
	},
	{
		ID:          "phonebook",
		Name:        "phonebook",
		Description: "Config-gated phonebook with a fee-collecting withdraw path.",
		Sources:     []string{"examples/phonebook/testdata/program.go"},
	},
	{
		ID:          "gospl",
		Name:        "gospl",
		Description: "Custom ERC-20-like token program (mint/transfer/burn/approve).",
		Sources:     []string{"examples/gospl/testdata/program.go"},
	},
	{
		ID:          "erc1155",
		Name:        "erc1155",
		Description: "Custom multi-token program with per-id balances and approve-for-all.",
		Sources:     []string{"examples/erc1155/testdata/accounts.go", "examples/erc1155/testdata/program.go"},
	},
	{
		ID:          "cpi",
		Name:        "cpi",
		Description: "System Program lamport transfer via sol_invoke_signed_c CPI.",
		Sources:     []string{"examples/cpi/testdata/program.go"},
	},
	{
		ID:          "payable",
		Name:        "payable",
		Description: "SOL vault: payable deposit (CPI), pull-payment withdraw, owner-only emergency withdraw.",
		Sources:     []string{"examples/payable/testdata/accounts.go", "examples/payable/testdata/program.go"},
	},
	{
		ID:          "erc20",
		Name:        "erc20",
		Description: "Solidity ERC-20 analogue: name/symbol/decimals/totalSupply, transfer/approve/transferFrom/allowance.",
		Sources:     []string{"examples/erc20/testdata/accounts.go", "examples/erc20/testdata/program.go"},
	},
}

// FindExample returns the registered example with the given ID.
func FindExample(id string) (Example, bool) {
	for _, example := range Examples {
		if example.ID == id {
			return example, true
		}
	}
	return Example{}, false
}
