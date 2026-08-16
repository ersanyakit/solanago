package erc1155

import "encoding/binary"

const (
	collectionTag = byte(1)
	tokenTypeTag  = byte(2)
	balanceTag    = byte(3)
	approvalTag   = byte(4)
)

// DecodeCollectionState verifies and decodes the canonical 41-byte layout.
// An all-zero buffer (the state of a freshly created, uninitialized
// account) decodes to a zero-value CollectionState with Initialized=false.
func DecodeCollectionState(data []byte) (CollectionState, error) {
	if len(data) != CollectionStateSize {
		return CollectionState{}, ErrInvalidStateEncoding
	}
	if allZero(data) {
		return CollectionState{}, nil
	}
	if data[0] != collectionTag {
		return CollectionState{}, ErrInvalidStateEncoding
	}
	state := CollectionState{Initialized: true}
	copy(state.Authority[:], data[1:33])
	state.NextID = binary.LittleEndian.Uint64(data[33:41])
	return state, nil
}

// EncodeCollectionState writes a canonical initialized collection into
// exactly CollectionStateSize bytes.
func EncodeCollectionState(data []byte, state CollectionState) error {
	if len(data) != CollectionStateSize || !state.Initialized {
		return ErrInvalidStateEncoding
	}
	clear(data)
	data[0] = collectionTag
	copy(data[1:33], state.Authority[:])
	binary.LittleEndian.PutUint64(data[33:41], state.NextID)
	return nil
}

// DecodeTokenTypeState verifies and decodes the canonical 117-byte layout.
func DecodeTokenTypeState(data []byte) (TokenTypeState, error) {
	if len(data) != TokenTypeStateSize {
		return TokenTypeState{}, ErrInvalidStateEncoding
	}
	if allZero(data) {
		return TokenTypeState{}, nil
	}
	if data[0] != tokenTypeTag {
		return TokenTypeState{}, ErrInvalidStateEncoding
	}
	state := TokenTypeState{Initialized: true}
	copy(state.Collection[:], data[1:33])
	state.ID = binary.LittleEndian.Uint64(data[33:41])
	state.Supply = binary.LittleEndian.Uint64(data[41:49])
	uriLen := binary.LittleEndian.Uint32(data[49:53])
	if uriLen > MaxURILength {
		return TokenTypeState{}, ErrInvalidStateEncoding
	}
	if !allZero(data[53+uriLen : 53+MaxURILength]) {
		return TokenTypeState{}, ErrInvalidStateEncoding
	}
	state.URI = string(data[53 : 53+uriLen])
	return state, nil
}

// EncodeTokenTypeState writes a canonical initialized token type into
// exactly TokenTypeStateSize bytes.
func EncodeTokenTypeState(data []byte, state TokenTypeState) error {
	if len(data) != TokenTypeStateSize || !state.Initialized {
		return ErrInvalidStateEncoding
	}
	if len(state.URI) > MaxURILength {
		return ErrURITooLong
	}
	clear(data)
	data[0] = tokenTypeTag
	copy(data[1:33], state.Collection[:])
	binary.LittleEndian.PutUint64(data[33:41], state.ID)
	binary.LittleEndian.PutUint64(data[41:49], state.Supply)
	binary.LittleEndian.PutUint32(data[49:53], uint32(len(state.URI)))
	copy(data[53:53+len(state.URI)], state.URI)
	return nil
}

// DecodeBalanceState verifies and decodes the canonical 81-byte layout.
func DecodeBalanceState(data []byte) (BalanceState, error) {
	if len(data) != BalanceStateSize {
		return BalanceState{}, ErrInvalidStateEncoding
	}
	if allZero(data) {
		return BalanceState{}, nil
	}
	if data[0] != balanceTag {
		return BalanceState{}, ErrInvalidStateEncoding
	}
	state := BalanceState{Initialized: true}
	copy(state.Collection[:], data[1:33])
	state.ID = binary.LittleEndian.Uint64(data[33:41])
	copy(state.Owner[:], data[41:73])
	state.Amount = binary.LittleEndian.Uint64(data[73:81])
	return state, nil
}

// EncodeBalanceState writes a canonical initialized balance into exactly
// BalanceStateSize bytes.
func EncodeBalanceState(data []byte, state BalanceState) error {
	if len(data) != BalanceStateSize || !state.Initialized {
		return ErrInvalidStateEncoding
	}
	clear(data)
	data[0] = balanceTag
	copy(data[1:33], state.Collection[:])
	binary.LittleEndian.PutUint64(data[33:41], state.ID)
	copy(data[41:73], state.Owner[:])
	binary.LittleEndian.PutUint64(data[73:81], state.Amount)
	return nil
}

// DecodeApprovalState verifies and decodes the canonical 98-byte layout.
func DecodeApprovalState(data []byte) (ApprovalState, error) {
	if len(data) != ApprovalStateSize {
		return ApprovalState{}, ErrInvalidStateEncoding
	}
	if allZero(data) {
		return ApprovalState{}, nil
	}
	if data[0] != approvalTag {
		return ApprovalState{}, ErrInvalidStateEncoding
	}
	state := ApprovalState{Initialized: true}
	copy(state.Collection[:], data[1:33])
	copy(state.Owner[:], data[33:65])
	copy(state.Operator[:], data[65:97])
	state.Approved = data[97] != 0
	if data[97] > 1 {
		return ApprovalState{}, ErrInvalidStateEncoding
	}
	return state, nil
}

// EncodeApprovalState writes a canonical initialized approval into exactly
// ApprovalStateSize bytes.
func EncodeApprovalState(data []byte, state ApprovalState) error {
	if len(data) != ApprovalStateSize || !state.Initialized {
		return ErrInvalidStateEncoding
	}
	clear(data)
	data[0] = approvalTag
	copy(data[1:33], state.Collection[:])
	copy(data[33:65], state.Owner[:])
	copy(data[65:97], state.Operator[:])
	if state.Approved {
		data[97] = 1
	}
	return nil
}

func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}
