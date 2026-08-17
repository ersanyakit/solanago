// phonebook.go decodes examples/phonebook's on-chain wire format directly
// (mirroring examples/phonebook/cmd/phonebook/main.go's offsets), since
// that command is package main and cannot be imported. examples/phonebook
// has no separate host library package the way erc1155/gospl/payable do.
//
// Writing instructions is now entirely handled by the generic interact
// engine (interact_engine.go + interact_schema.go's phonebookSchema) — this
// file is left with only what that engine can't express: the Phonebook
// account's repeated contact-list region (StateLayoutSpec only supports
// fixed, non-repeating fields).
//
// The signer-list bugs that originally made examples/phonebook/cmd/phonebook
// unusable (init-config/init-phonebook/add-contact asked signTransaction for
// private keys of accounts that had already signed their own creation in an
// earlier transaction) are fixed in that command directly.
package web

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/svmtest"
)

const (
	PhonebookConfigDataLen = uint64(74)
	PhonebookDataLen       = uint64(1315)
	PhonebookMaxContacts   = 20
	PhonebookMaxNameLen    = 32
	PhonebookEntrySize     = uint64(64)
	PhonebookEntriesOffset = uint64(35)
)

// PhonebookContact is one entry inside a Phonebook account.
type PhonebookContact struct {
	Address sdk.Pubkey `json:"address"`
	Name    string     `json:"name"`
}

// PhonebookState is examples/phonebook's 1315-byte Phonebook account.
type PhonebookState struct {
	Initialized bool               `json:"initialized"`
	Owner       sdk.Pubkey         `json:"owner"`
	Count       uint8              `json:"count"`
	Contacts    []PhonebookContact `json:"contacts"`
}

func decodePhonebookState(raw []byte) (PhonebookState, error) {
	if uint64(len(raw)) < PhonebookDataLen {
		return PhonebookState{}, fmt.Errorf("phonebook data is %d bytes, want at least %d", len(raw), PhonebookDataLen)
	}
	owner, err := sdk.PubkeyFromBytes(raw[2:34])
	if err != nil {
		return PhonebookState{}, err
	}
	count := raw[34]
	state := PhonebookState{Initialized: raw[0] == 1, Owner: owner, Count: count, Contacts: []PhonebookContact{}}
	limit := int(count)
	if limit > PhonebookMaxContacts {
		limit = PhonebookMaxContacts
	}
	for index := 0; index < limit; index++ {
		base := PhonebookEntriesOffset + uint64(index)*PhonebookEntrySize
		address, err := sdk.PubkeyFromBytes(raw[base : base+32])
		if err != nil {
			return PhonebookState{}, err
		}
		name := strings.TrimRight(string(raw[base+32:base+64]), "\x00")
		state.Contacts = append(state.Contacts, PhonebookContact{Address: address, Name: name})
	}
	return state, nil
}

func fetchPhonebookState(ctx context.Context, client svmtest.Client, address sdk.Pubkey) (PhonebookState, error) {
	info, err := client.GetAccountInfo(ctx, address)
	if err != nil {
		return PhonebookState{}, err
	}
	if info == nil {
		return PhonebookState{}, errors.New("phonebook account not found")
	}
	raw, err := info.DataBytes()
	if err != nil {
		return PhonebookState{}, err
	}
	return decodePhonebookState(raw)
}
