package token2022

import (
	"encoding/binary"
	"math/bits"

	"github.com/ersany/go-solana/sdk"
)

const (
	// MaximumFeeBasisPoints is 100%, expressed in basis points.
	MaximumFeeBasisPoints = 10_000
	TransferFeeSize       = 18
	TransferFeeConfigSize = 108
	TransferFeeAmountSize = 8
)

// TransferFee is the fixed-width fee schedule stored in a transfer-fee mint
// extension. All integers use little-endian byte order on chain.
type TransferFee struct {
	Epoch       uint64
	MaximumFee  uint64
	BasisPoints uint16
}

// TransferFeeConfig is the 108-byte transfer-fee mint extension. Its
// authorities use Token-2022's MaybeNull<Address> representation: an all-zero
// address means None, rather than the four-byte COption tag used by base state.
type TransferFeeConfig struct {
	ConfigAuthority   OptionalPubkey
	WithdrawAuthority OptionalPubkey
	WithheldAmount    uint64
	Older             TransferFee
	Newer             TransferFee
}

// TransferFeeAmount is the account-side transfer-fee extension.
type TransferFeeAmount struct {
	WithheldAmount uint64
}

func EncodeTransferFee(value TransferFee) []byte {
	data := make([]byte, TransferFeeSize)
	binary.LittleEndian.PutUint64(data[0:8], value.Epoch)
	binary.LittleEndian.PutUint64(data[8:16], value.MaximumFee)
	binary.LittleEndian.PutUint16(data[16:18], value.BasisPoints)
	return data
}

func DecodeTransferFee(data []byte) (TransferFee, error) {
	if len(data) != TransferFeeSize {
		return TransferFee{}, ErrInvalidExtension
	}
	return TransferFee{
		Epoch:       binary.LittleEndian.Uint64(data[0:8]),
		MaximumFee:  binary.LittleEndian.Uint64(data[8:16]),
		BasisPoints: binary.LittleEndian.Uint16(data[16:18]),
	}, nil
}

func EncodeTransferFeeConfig(value TransferFeeConfig) ([]byte, error) {
	data := make([]byte, TransferFeeConfigSize)
	if err := putMaybeNullPubkey(data[0:32], value.ConfigAuthority); err != nil {
		return nil, err
	}
	if err := putMaybeNullPubkey(data[32:64], value.WithdrawAuthority); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(data[64:72], value.WithheldAmount)
	copy(data[72:90], EncodeTransferFee(value.Older))
	copy(data[90:108], EncodeTransferFee(value.Newer))
	return data, nil
}

func DecodeTransferFeeConfig(data []byte) (TransferFeeConfig, error) {
	if len(data) != TransferFeeConfigSize {
		return TransferFeeConfig{}, ErrInvalidExtension
	}
	older, err := DecodeTransferFee(data[72:90])
	if err != nil {
		return TransferFeeConfig{}, err
	}
	newer, err := DecodeTransferFee(data[90:108])
	if err != nil {
		return TransferFeeConfig{}, err
	}
	return TransferFeeConfig{
		ConfigAuthority:   decodeMaybeNullPubkey(data[0:32]),
		WithdrawAuthority: decodeMaybeNullPubkey(data[32:64]),
		WithheldAmount:    binary.LittleEndian.Uint64(data[64:72]),
		Older:             older,
		Newer:             newer,
	}, nil
}

func EncodeTransferFeeAmount(value TransferFeeAmount) []byte {
	data := make([]byte, TransferFeeAmountSize)
	binary.LittleEndian.PutUint64(data, value.WithheldAmount)
	return data
}

func DecodeTransferFeeAmount(data []byte) (TransferFeeAmount, error) {
	if len(data) != TransferFeeAmountSize {
		return TransferFeeAmount{}, ErrInvalidExtension
	}
	return TransferFeeAmount{WithheldAmount: binary.LittleEndian.Uint64(data)}, nil
}

// FeeForEpoch returns the schedule active at epoch.
func (value TransferFeeConfig) FeeForEpoch(epoch uint64) TransferFee {
	if epoch >= value.Newer.Epoch {
		return value.Newer
	}
	return value.Older
}

// CalculateFee mirrors the official Token-2022 ceiling division. The boolean
// is false only when an out-of-range basis-point value makes the u64 result
// overflow before the maximum-fee cap can be applied.
func (value TransferFee) CalculateFee(preFeeAmount uint64) (uint64, bool) {
	if value.BasisPoints == 0 || preFeeAmount == 0 {
		return 0, true
	}
	hi, lo := bits.Mul64(preFeeAmount, uint64(value.BasisPoints))
	if hi >= MaximumFeeBasisPoints {
		return 0, false
	}
	fee, remainder := bits.Div64(hi, lo, MaximumFeeBasisPoints)
	if remainder != 0 {
		fee++
	}
	if fee > value.MaximumFee {
		fee = value.MaximumFee
	}
	return fee, true
}

func putMaybeNullPubkey(destination []byte, value OptionalPubkey) error {
	if len(destination) != len(sdk.Pubkey{}) {
		return ErrInvalidExtension
	}
	if !value.Set {
		clear(destination)
		return nil
	}
	if value.Value == (sdk.Pubkey{}) {
		// MaybeNull cannot distinguish Some(zero) from None.
		return ErrInvalidExtension
	}
	copy(destination, value.Value[:])
	return nil
}

func decodeMaybeNullPubkey(data []byte) OptionalPubkey {
	var value sdk.Pubkey
	copy(value[:], data)
	if value == (sdk.Pubkey{}) {
		return OptionalPubkey{}
	}
	return SomePubkey(value)
}
