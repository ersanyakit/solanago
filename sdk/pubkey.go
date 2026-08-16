// Package sdk contains the value types shared by the Solana-facing SDK
// packages. It intentionally contains no Go heap pointer or runtime account
// abstraction: Pubkey is exactly the 32 bytes used by the Solana ABI.
package sdk

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"unicode/utf8"
)

const (
	// PubkeySize is the byte width of a Solana public key.
	PubkeySize = 32
	// MaxSeedLength is the maximum byte length of one PDA seed.
	MaxSeedLength = 32
	// MaxSeeds is the maximum number of seed slices accepted by the runtime.
	MaxSeeds = 16
)

var (
	ErrInvalidPubkey    = errors.New("sdk: invalid pubkey")
	ErrMaxSeedLength    = errors.New("sdk: seed exceeds 32 bytes")
	ErrTooManySeeds     = errors.New("sdk: too many seeds")
	ErrInvalidSeeds     = errors.New("sdk: derived address is on the ed25519 curve")
	ErrInvalidSeed      = errors.New("sdk: seed is not valid UTF-8")
	ErrNoProgramAddress = errors.New("sdk: unable to find a viable program address bump")
	ErrIllegalOwner     = errors.New("sdk: owner ends with ProgramDerivedAddress")
	pdaMarker           = []byte("ProgramDerivedAddress")
	base58Alphabet      = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	base58Indexes       = makeBase58Indexes()
	edwardsPrime, _     = new(big.Int).SetString("57896044618658097711785492504343953926634992332820282019728792003956564819949", 10)
	edwardsD, _         = new(big.Int).SetString("37095705934669439343138083508754565189542113879843219016388785533085940283555", 10)
)

// Pubkey is Solana's canonical 32-byte account address representation.
type Pubkey [PubkeySize]byte

// PubkeyFromBytes constructs a Pubkey and rejects every non-32-byte input.
func PubkeyFromBytes(src []byte) (Pubkey, error) {
	if len(src) != PubkeySize {
		return Pubkey{}, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidPubkey, len(src), PubkeySize)
	}
	var key Pubkey
	copy(key[:], src)
	return key, nil
}

// ParsePubkey decodes a canonical 32-byte base58 Solana address.
func ParsePubkey(text string) (Pubkey, error) {
	if len(text) == 0 || len(text) > 44 {
		return Pubkey{}, ErrInvalidPubkey
	}

	// Digits are kept little-endian so multiplying by 58 never needs a
	// variable-width integer or a third-party base58 dependency.
	var decodedLE []byte
	leadingZeroes := 0
	for leadingZeroes < len(text) && text[leadingZeroes] == '1' {
		leadingZeroes++
	}
	for i := leadingZeroes; i < len(text); i++ {
		c := text[i]
		if int(c) >= len(base58Indexes) || base58Indexes[c] < 0 {
			return Pubkey{}, ErrInvalidPubkey
		}
		carry := int(base58Indexes[c])
		for j := 0; j < len(decodedLE); j++ {
			value := int(decodedLE[j])*58 + carry
			decodedLE[j] = byte(value)
			carry = value >> 8
		}
		for carry > 0 {
			decodedLE = append(decodedLE, byte(carry))
			carry >>= 8
		}
	}
	if leadingZeroes+len(decodedLE) != PubkeySize {
		return Pubkey{}, ErrInvalidPubkey
	}

	var key Pubkey
	for i := range decodedLE {
		key[PubkeySize-1-i] = decodedLE[i]
	}
	return key, nil
}

// MustParsePubkey is intended for package-level constants with reviewed
// literal addresses. It panics on malformed input.
func MustParsePubkey(text string) Pubkey {
	key, err := ParsePubkey(text)
	if err != nil {
		panic(err)
	}
	return key
}

// String returns the canonical base58 representation.
func (p Pubkey) String() string {
	leadingZeroes := 0
	for leadingZeroes < len(p) && p[leadingZeroes] == 0 {
		leadingZeroes++
	}
	var digitsLE []byte
	for _, b := range p[leadingZeroes:] {
		carry := int(b)
		for j := 0; j < len(digitsLE); j++ {
			value := int(digitsLE[j])*256 + carry
			digitsLE[j] = byte(value % 58)
			carry = value / 58
		}
		for carry > 0 {
			digitsLE = append(digitsLE, byte(carry%58))
			carry /= 58
		}
	}

	out := make([]byte, leadingZeroes+len(digitsLE))
	for i := 0; i < leadingZeroes; i++ {
		out[i] = '1'
	}
	for i := range digitsLE {
		out[len(out)-1-i] = base58Alphabet[digitsLE[i]]
	}
	return string(out)
}

// MarshalText implements encoding.TextMarshaler.
func (p Pubkey) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (p *Pubkey) UnmarshalText(text []byte) error {
	if p == nil {
		return ErrInvalidPubkey
	}
	key, err := ParsePubkey(string(text))
	if err != nil {
		return err
	}
	*p = key
	return nil
}

// IsOnCurve reports whether the compressed Edwards-Y bytes decompress to an
// ed25519 point. This matches the predicate used by Solana PDA derivation; it
// is not a signature or public-key validation API.
func (p Pubkey) IsOnCurve() bool { return compressedEdwardsYIsOnCurve(p) }

// CreateProgramAddress derives a PDA from already-finalized seeds. Callers
// normally use FindProgramAddress and retain its bump.
func CreateProgramAddress(seeds [][]byte, programID Pubkey) (Pubkey, error) {
	if len(seeds) > MaxSeeds {
		return Pubkey{}, ErrTooManySeeds
	}
	h := sha256.New()
	for _, seed := range seeds {
		if len(seed) > MaxSeedLength {
			return Pubkey{}, ErrMaxSeedLength
		}
		_, _ = h.Write(seed)
	}
	_, _ = h.Write(programID[:])
	_, _ = h.Write(pdaMarker)
	sum := h.Sum(nil)
	key, _ := PubkeyFromBytes(sum)
	if key.IsOnCurve() {
		return Pubkey{}, ErrInvalidSeeds
	}
	return key, nil
}

// FindProgramAddress searches bump seeds 255 down through 0, matching the
// current Rust SDK and JavaScript Kit implementations. The caller-provided
// seeds are limited to 15 because the bump is the 16th seed.
func FindProgramAddress(seeds [][]byte, programID Pubkey) (Pubkey, uint8, error) {
	if len(seeds) >= MaxSeeds {
		return Pubkey{}, 0, ErrTooManySeeds
	}
	for _, seed := range seeds {
		if len(seed) > MaxSeedLength {
			return Pubkey{}, 0, ErrMaxSeedLength
		}
	}
	for bump := 255; bump >= 0; bump-- {
		bumpSeed := []byte{byte(bump)}
		withBump := make([][]byte, 0, len(seeds)+1)
		withBump = append(withBump, seeds...)
		withBump = append(withBump, bumpSeed)
		address, err := CreateProgramAddress(withBump, programID)
		if err == nil {
			return address, uint8(bump), nil
		}
		if !errors.Is(err, ErrInvalidSeeds) {
			return Pubkey{}, 0, err
		}
	}
	return Pubkey{}, 0, ErrNoProgramAddress
}

// CreateWithSeed derives the signer-backed System Program address
// SHA256(base || seed || owner). It is distinct from a PDA.
func CreateWithSeed(base Pubkey, seed string, owner Pubkey) (Pubkey, error) {
	if !utf8.ValidString(seed) {
		return Pubkey{}, ErrInvalidSeed
	}
	if len(seed) > MaxSeedLength {
		return Pubkey{}, ErrMaxSeedLength
	}
	if len(owner) >= len(pdaMarker) && string(owner[len(owner)-len(pdaMarker):]) == string(pdaMarker) {
		return Pubkey{}, ErrIllegalOwner
	}
	h := sha256.New()
	_, _ = h.Write(base[:])
	_, _ = h.Write([]byte(seed))
	_, _ = h.Write(owner[:])
	return PubkeyFromBytes(h.Sum(nil))
}

func compressedEdwardsYIsOnCurve(compressed Pubkey) bool {
	sign := compressed[31]&0x80 != 0
	yLE := compressed
	yLE[31] &= 0x7f
	yBE := make([]byte, len(yLE))
	for i := range yLE {
		yBE[len(yLE)-1-i] = yLE[i]
	}
	y := new(big.Int).SetBytes(yBE)

	y2 := new(big.Int).Mul(y, y)
	y2.Mod(y2, edwardsPrime)
	u := new(big.Int).Sub(y2, big.NewInt(1))
	u.Mod(u, edwardsPrime)
	v := new(big.Int).Mul(edwardsD, y2)
	v.Add(v, big.NewInt(1))
	v.Mod(v, edwardsPrime)
	vInverse := new(big.Int).ModInverse(v, edwardsPrime)
	if vInverse == nil {
		return false
	}
	x2 := new(big.Int).Mul(u, vInverse)
	x2.Mod(x2, edwardsPrime)
	x := new(big.Int).ModSqrt(x2, edwardsPrime)
	if x == nil {
		return false
	}
	return x.Sign() != 0 || !sign
}

func makeBase58Indexes() [256]int16 {
	var indexes [256]int16
	for i := range indexes {
		indexes[i] = -1
	}
	for i := range base58Alphabet {
		indexes[base58Alphabet[i]] = int16(i)
	}
	return indexes
}
