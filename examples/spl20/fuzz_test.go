package spl20

import (
	"bytes"
	"testing"
)

func FuzzDecodeInstructionNeverPanics(f *testing.F) {
	validAmount, _ := EncodeAmountInstruction(InstructionTransfer, 42)
	validSetAuthority, _ := EncodeSetAuthority(AuthorityMintTokens, OptionalPubkey{Set: true, Key: testPubkey(1)})
	for _, seed := range [][]byte{
		nil,
		{0xff},
		EncodeInitializeMint(9, testPubkey(2)),
		validAmount,
		validSetAuthority,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		instruction, err := DecodeInstruction(data)
		if err != nil {
			return
		}
		encoded, err := encodeDecodedInstruction(instruction)
		if err != nil {
			t.Fatalf("successfully decoded instruction cannot be encoded: %+v: %v", instruction, err)
		}
		if !bytes.Equal(encoded, data) {
			t.Fatalf("non-canonical successful decode: input=%x encoded=%x", data, encoded)
		}
	})
}

func FuzzDecodeMintStateNeverPanics(f *testing.F) {
	valid := make([]byte, MintStateSize)
	if err := EncodeMintState(valid, MintState{
		Initialized:   true,
		Decimals:      9,
		Supply:        123,
		MintAuthority: OptionalPubkey{Set: true, Key: testPubkey(1)},
	}); err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{nil, make([]byte, MintStateSize), valid} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		state, err := DecodeMintState(data)
		if err != nil {
			return
		}
		if !state.Initialized {
			if !allZero(data) {
				t.Fatalf("nonzero data decoded as uninitialized mint: %x", data)
			}
			return
		}
		encoded := make([]byte, MintStateSize)
		if err := EncodeMintState(encoded, state); err != nil {
			t.Fatalf("successfully decoded mint cannot be encoded: %+v: %v", state, err)
		}
		if !bytes.Equal(encoded, data) {
			t.Fatalf("non-canonical successful mint decode: input=%x encoded=%x", data, encoded)
		}
	})
}

func FuzzDecodeTokenStateNeverPanics(f *testing.F) {
	valid := make([]byte, TokenAccountStateSize)
	if err := EncodeTokenAccountState(valid, TokenAccountState{
		Initialized:     true,
		Mint:            testPubkey(1),
		Owner:           testPubkey(2),
		Amount:          100,
		Delegate:        OptionalPubkey{Set: true, Key: testPubkey(3)},
		DelegatedAmount: 50,
	}); err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{nil, make([]byte, TokenAccountStateSize), valid} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		state, err := DecodeTokenAccountState(data)
		if err != nil {
			return
		}
		if !state.Initialized {
			if !allZero(data) {
				t.Fatalf("nonzero data decoded as uninitialized token account: %x", data)
			}
			return
		}
		encoded := make([]byte, TokenAccountStateSize)
		if err := EncodeTokenAccountState(encoded, state); err != nil {
			t.Fatalf("successfully decoded token state cannot be encoded: %+v: %v", state, err)
		}
		if !bytes.Equal(encoded, data) {
			t.Fatalf("non-canonical successful token decode: input=%x encoded=%x", data, encoded)
		}
	})
}
