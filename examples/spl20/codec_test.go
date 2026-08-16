package spl20

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func TestMintStateCodecRoundTrip(t *testing.T) {
	tests := []MintState{
		{
			Initialized:   true,
			Decimals:      9,
			Supply:        123_456_789,
			MintAuthority: OptionalPubkey{Set: true, Key: testPubkey(1)},
		},
		{
			Initialized: true,
			Decimals:    2,
			Supply:      42,
		},
	}
	for _, want := range tests {
		data := make([]byte, MintStateSize)
		if err := EncodeMintState(data, want); err != nil {
			t.Fatalf("EncodeMintState: %v", err)
		}
		got, err := DecodeMintState(data)
		if err != nil {
			t.Fatalf("DecodeMintState: %v", err)
		}
		if got != want {
			t.Fatalf("decoded mint = %+v, want %+v", got, want)
		}
	}
}

func TestTokenStateCodecRoundTrip(t *testing.T) {
	tests := []TokenAccountState{
		{
			Initialized:     true,
			Mint:            testPubkey(1),
			Owner:           testPubkey(2),
			Amount:          999,
			Delegate:        OptionalPubkey{Set: true, Key: testPubkey(3)},
			DelegatedAmount: 100,
		},
		{
			Initialized: true,
			Mint:        testPubkey(4),
			Owner:       testPubkey(5),
			Amount:      7,
		},
	}
	for _, want := range tests {
		data := make([]byte, TokenAccountStateSize)
		if err := EncodeTokenAccountState(data, want); err != nil {
			t.Fatalf("EncodeTokenAccountState: %v", err)
		}
		got, err := DecodeTokenAccountState(data)
		if err != nil {
			t.Fatalf("DecodeTokenAccountState: %v", err)
		}
		if got != want {
			t.Fatalf("decoded token = %+v, want %+v", got, want)
		}
	}
}

func TestStateCodecRecognizesZeroedUninitializedAccounts(t *testing.T) {
	mint, err := DecodeMintState(make([]byte, MintStateSize))
	if err != nil || mint.Initialized {
		t.Fatalf("zero mint decoded as %+v, %v", mint, err)
	}
	token, err := DecodeTokenAccountState(make([]byte, TokenAccountStateSize))
	if err != nil || token.Initialized {
		t.Fatalf("zero token decoded as %+v, %v", token, err)
	}
}

func TestStateCodecRejectsMalformedInput(t *testing.T) {
	validMint := make([]byte, MintStateSize)
	if err := EncodeMintState(validMint, MintState{Initialized: true, MintAuthority: OptionalPubkey{Set: true, Key: testPubkey(1)}}); err != nil {
		t.Fatal(err)
	}
	validToken := make([]byte, TokenAccountStateSize)
	if err := EncodeTokenAccountState(validToken, TokenAccountState{Initialized: true, Mint: testPubkey(2), Owner: testPubkey(3)}); err != nil {
		t.Fatal(err)
	}

	mintCases := [][]byte{
		make([]byte, MintStateSize-1),
		mutated(validMint, 0, 0xff),
		mutated(validMint, 1, 0xff),
		mutated(validMint, 3, 1),
		mutated(validMint, 1, flagAuthority),
	}
	unsetAuthorityWithKey := append([]byte(nil), validMint...)
	unsetAuthorityWithKey[1] = flagInitialized
	unsetAuthorityWithKey[16] = 1
	mintCases = append(mintCases, unsetAuthorityWithKey)
	for index, data := range mintCases {
		if _, err := DecodeMintState(data); !errors.Is(err, ErrInvalidState) {
			t.Errorf("mint malformed case %d error = %v, want %v", index, err, ErrInvalidState)
		}
	}

	tokenCases := [][]byte{
		make([]byte, TokenAccountStateSize+1),
		mutated(validToken, 0, 0xff),
		mutated(validToken, 1, 0xff),
		mutated(validToken, 2, 1),
		mutated(validToken, 1, 0),
	}
	unsetDelegateWithAllowance := append([]byte(nil), validToken...)
	unsetDelegateWithAllowance[16] = 1
	tokenCases = append(tokenCases, unsetDelegateWithAllowance)
	unsetDelegateWithKey := append([]byte(nil), validToken...)
	unsetDelegateWithKey[88] = 1
	tokenCases = append(tokenCases, unsetDelegateWithKey)
	for index, data := range tokenCases {
		if _, err := DecodeTokenAccountState(data); !errors.Is(err, ErrInvalidState) {
			t.Errorf("token malformed case %d error = %v, want %v", index, err, ErrInvalidState)
		}
	}

	if err := EncodeMintState(make([]byte, MintStateSize-1), MintState{Initialized: true}); !errors.Is(err, ErrInvalidState) {
		t.Errorf("short mint encode error = %v", err)
	}
	if err := EncodeMintState(make([]byte, MintStateSize), MintState{}); !errors.Is(err, ErrInvalidState) {
		t.Errorf("uninitialized mint encode error = %v", err)
	}
	if err := EncodeMintState(make([]byte, MintStateSize), MintState{
		Initialized:   true,
		MintAuthority: OptionalPubkey{Key: testPubkey(7)},
	}); !errors.Is(err, ErrInvalidState) {
		t.Errorf("unset mint authority with key encode error = %v", err)
	}
	if err := EncodeTokenAccountState(make([]byte, TokenAccountStateSize), TokenAccountState{Initialized: true, DelegatedAmount: 1}); !errors.Is(err, ErrInvalidState) {
		t.Errorf("delegate-less allowance encode error = %v", err)
	}
	if err := EncodeTokenAccountState(make([]byte, TokenAccountStateSize), TokenAccountState{
		Initialized: true,
		Delegate:    OptionalPubkey{Key: testPubkey(8)},
	}); !errors.Is(err, ErrInvalidState) {
		t.Errorf("unset delegate with key encode error = %v", err)
	}
}

func TestInstructionCodecRoundTrip(t *testing.T) {
	amountTransfer, _ := EncodeAmountInstruction(InstructionTransfer, 11)
	amountMint, _ := EncodeAmountInstruction(InstructionMintTo, 12)
	amountBurn, _ := EncodeAmountInstruction(InstructionBurn, 13)
	amountApprove, _ := EncodeAmountInstruction(InstructionApprove, 14)
	setMint, _ := EncodeSetAuthority(AuthorityMintTokens, OptionalPubkey{})
	setOwner, _ := EncodeSetAuthority(AuthorityAccountOwner, OptionalPubkey{Set: true, Key: testPubkey(9)})
	tests := []struct {
		data []byte
		want Instruction
	}{
		{EncodeInitializeMint(6, testPubkey(1)), Instruction{Kind: InstructionInitializeMint, Decimals: 6, Authority: testPubkey(1)}},
		{EncodeInitializeAccount(testPubkey(2)), Instruction{Kind: InstructionInitializeAccount, Authority: testPubkey(2)}},
		{amountTransfer, Instruction{Kind: InstructionTransfer, Amount: 11}},
		{amountMint, Instruction{Kind: InstructionMintTo, Amount: 12}},
		{amountBurn, Instruction{Kind: InstructionBurn, Amount: 13}},
		{amountApprove, Instruction{Kind: InstructionApprove, Amount: 14}},
		{EncodeRevoke(), Instruction{Kind: InstructionRevoke}},
		{setMint, Instruction{Kind: InstructionSetAuthority, AuthorityType: AuthorityMintTokens}},
		{setOwner, Instruction{Kind: InstructionSetAuthority, AuthorityType: AuthorityAccountOwner, NewAuthority: OptionalPubkey{Set: true, Key: testPubkey(9)}}},
	}

	for _, test := range tests {
		got, err := DecodeInstruction(test.data)
		if err != nil {
			t.Fatalf("DecodeInstruction(%x): %v", test.data, err)
		}
		if got != test.want {
			t.Fatalf("DecodeInstruction(%x) = %+v, want %+v", test.data, got, test.want)
		}
		encoded, err := encodeDecodedInstruction(got)
		if err != nil {
			t.Fatalf("encode decoded instruction: %v", err)
		}
		if !bytes.Equal(encoded, test.data) {
			t.Fatalf("instruction re-encode = %x, want %x", encoded, test.data)
		}
	}
}

func TestInstructionCodecRejectsMalformedInput(t *testing.T) {
	validSetOwner, _ := EncodeSetAuthority(AuthorityAccountOwner, OptionalPubkey{Set: true, Key: testPubkey(1)})
	tests := [][]byte{
		nil,
		{0xff},
		{byte(InstructionInitializeMint)},
		append(EncodeInitializeAccount(testPubkey(1)), 0),
		{byte(InstructionTransfer)},
		{byte(InstructionRevoke), 0},
		mutated(validSetOwner, 1, 0xff),
		mutated(validSetOwner, 2, 2),
	}
	unsetWithKey := make([]byte, 35)
	unsetWithKey[0] = byte(InstructionSetAuthority)
	unsetWithKey[1] = byte(AuthorityMintTokens)
	unsetWithKey[3] = 1
	tests = append(tests, unsetWithKey)
	for index, data := range tests {
		if _, err := DecodeInstruction(data); !errors.Is(err, ErrInvalidInstruction) {
			t.Errorf("malformed instruction case %d error = %v, want %v", index, err, ErrInvalidInstruction)
		}
	}

	if _, err := EncodeAmountInstruction(InstructionInitializeMint, 1); !errors.Is(err, ErrInvalidInstruction) {
		t.Errorf("invalid amount instruction error = %v", err)
	}
	if _, err := EncodeSetAuthority(AuthorityType(99), OptionalPubkey{}); !errors.Is(err, ErrInvalidInstruction) {
		t.Errorf("invalid authority kind error = %v", err)
	}
	if _, err := EncodeSetAuthority(AuthorityAccountOwner, OptionalPubkey{}); !errors.Is(err, ErrInvalidInstruction) {
		t.Errorf("unset account owner error = %v", err)
	}
	if _, err := EncodeSetAuthority(AuthorityMintTokens, OptionalPubkey{Key: testPubkey(2)}); !errors.Is(err, ErrInvalidInstruction) {
		t.Errorf("unset mint authority with key error = %v", err)
	}
}

func encodeDecodedInstruction(instruction Instruction) ([]byte, error) {
	switch instruction.Kind {
	case InstructionInitializeMint:
		return EncodeInitializeMint(instruction.Decimals, instruction.Authority), nil
	case InstructionInitializeAccount:
		return EncodeInitializeAccount(instruction.Authority), nil
	case InstructionTransfer, InstructionMintTo, InstructionBurn, InstructionApprove:
		return EncodeAmountInstruction(instruction.Kind, instruction.Amount)
	case InstructionRevoke:
		return EncodeRevoke(), nil
	case InstructionSetAuthority:
		return EncodeSetAuthority(instruction.AuthorityType, instruction.NewAuthority)
	default:
		return nil, ErrInvalidInstruction
	}
}

func mutated(data []byte, index int, value byte) []byte {
	copyOfData := append([]byte(nil), data...)
	copyOfData[index] = value
	return copyOfData
}

func TestProgramErrorCodesAreStableAndDistinct(t *testing.T) {
	errorsByCode := []ProgramError{
		ErrInvalidInstruction,
		ErrMissingAccount,
		ErrInvalidProgramOwner,
		ErrAccountReadOnly,
		ErrMissingSignature,
		ErrInvalidAuthority,
		ErrInvalidState,
		ErrAlreadyInitialized,
		ErrUninitialized,
		ErrMintMismatch,
		ErrInsufficientFunds,
		ErrInsufficientAllowance,
		ErrArithmeticOverflow,
		ErrSameAccount,
		ErrAuthorityDisabled,
	}
	seen := make(map[ProgramError]struct{}, len(errorsByCode))
	for _, programError := range errorsByCode {
		if _, exists := seen[programError]; exists {
			t.Fatalf("duplicate program error code %d", programError)
		}
		seen[programError] = struct{}{}
		if programError.Error() == "" {
			t.Fatalf("empty message for program error code %d", programError)
		}
	}
	if reflect.TypeOf(ErrInvalidInstruction).Kind().String() != "uint32" {
		t.Fatalf("ProgramError underlying kind changed: %v", reflect.TypeOf(ErrInvalidInstruction).Kind())
	}
}
