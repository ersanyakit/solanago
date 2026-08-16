package sdk

// AccountMeta describes the privileges requested by one instruction account.
// Runtime privilege de-escalation and ownership checks belong to the ABI/CPI
// layer; this type is only the exact transaction-facing value model.
type AccountMeta struct {
	Pubkey     Pubkey
	IsSigner   bool
	IsWritable bool
}

// Writable returns a writable account meta.
func Writable(pubkey Pubkey, signer bool) AccountMeta {
	return AccountMeta{Pubkey: pubkey, IsSigner: signer, IsWritable: true}
}

// Readonly returns a read-only account meta.
func Readonly(pubkey Pubkey, signer bool) AccountMeta {
	return AccountMeta{Pubkey: pubkey, IsSigner: signer}
}

// Instruction is the canonical program id, ordered account metadata, and
// opaque instruction-data tuple submitted to the Solana runtime.
type Instruction struct {
	ProgramID Pubkey
	Accounts  []AccountMeta
	Data      []byte
}

// Clone returns a deep copy so a caller cannot mutate a cached builder result.
func (i Instruction) Clone() Instruction {
	clone := Instruction{ProgramID: i.ProgramID}
	clone.Accounts = append([]AccountMeta(nil), i.Accounts...)
	clone.Data = append([]byte(nil), i.Data...)
	return clone
}
