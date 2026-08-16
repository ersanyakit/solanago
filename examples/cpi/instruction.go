// Package cpi contains the host-side instruction builder for the Go CPI
// example program in testdata. The on-chain program performs one System
// Program transfer through sol_invoke_signed_c.
package cpi

import (
	"encoding/binary"

	"github.com/ersany/go-solana/sdk"
	"github.com/ersany/go-solana/sdk/system"
)

const TransferDataSize = 8

// Transfer asks programID to invoke the System Program and move lamports from
// source to destination. source must sign the outer transaction.
func Transfer(programID, source, destination sdk.Pubkey, lamports uint64) sdk.Instruction {
	data := make([]byte, TransferDataSize)
	binary.LittleEndian.PutUint64(data, lamports)
	return sdk.Instruction{
		ProgramID: programID,
		Accounts: []sdk.AccountMeta{
			sdk.Writable(source, true),
			sdk.Writable(destination, false),
			sdk.Readonly(system.ProgramID, false),
		},
		Data: data,
	}
}
