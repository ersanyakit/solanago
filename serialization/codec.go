// Package serialization provides small, explicit deterministic codecs for
// program instruction and account-state layouts. It deliberately avoids
// reflection and Go's native binary representation: every field width, byte
// order, length prefix, and optional value is chosen by the caller.
package serialization

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

var (
	ErrInvalidBool     = errors.New("serialization: non-canonical boolean")
	ErrInvalidOption   = errors.New("serialization: non-canonical option tag")
	ErrLengthExceeded  = errors.New("serialization: length exceeds limit")
	ErrTrailingData    = errors.New("serialization: trailing data")
	ErrUnexpectedEnd   = errors.New("serialization: unexpected end of input")
	ErrInvalidArgument = errors.New("serialization: invalid argument")
)

// Marshaler writes one canonical representation to an Encoder.
type Marshaler interface {
	EncodeTo(*Encoder) error
}

// Unmarshaler reads one canonical representation from a Decoder.
type Unmarshaler interface {
	DecodeFrom(*Decoder) error
}

// Encoder appends explicitly-sized fields to a deterministic byte stream.
// Its zero value is ready for use.
type Encoder struct {
	buf []byte
	err error
}

// NewEncoder creates an encoder with an optional capacity hint.
func NewEncoder(capacity int) *Encoder {
	if capacity < 0 {
		capacity = 0
	}
	return &Encoder{buf: make([]byte, 0, capacity)}
}

// Err returns the first encoding error.
func (e *Encoder) Err() error {
	if e == nil {
		return ErrInvalidArgument
	}
	return e.err
}

// Bytes returns a copy of the encoded bytes.
func (e *Encoder) Bytes() []byte {
	if e == nil {
		return nil
	}
	return append([]byte(nil), e.buf...)
}

// U8 writes an unsigned byte.
func (e *Encoder) U8(value uint8) {
	if e == nil || e.err != nil {
		return
	}
	e.buf = append(e.buf, value)
}

// Bool writes a canonical boolean tag (zero or one).
func (e *Encoder) Bool(value bool) {
	if value {
		e.U8(1)
		return
	}
	e.U8(0)
}

// U16 writes a little-endian uint16.
func (e *Encoder) U16(value uint16) {
	if e == nil || e.err != nil {
		return
	}
	start := len(e.buf)
	e.buf = append(e.buf, make([]byte, 2)...)
	binary.LittleEndian.PutUint16(e.buf[start:], value)
}

// U32 writes a little-endian uint32.
func (e *Encoder) U32(value uint32) {
	if e == nil || e.err != nil {
		return
	}
	start := len(e.buf)
	e.buf = append(e.buf, make([]byte, 4)...)
	binary.LittleEndian.PutUint32(e.buf[start:], value)
}

// I32 writes a two's-complement little-endian int32.
func (e *Encoder) I32(value int32) { e.U32(uint32(value)) }

// U64 writes a little-endian uint64.
func (e *Encoder) U64(value uint64) {
	if e == nil || e.err != nil {
		return
	}
	start := len(e.buf)
	e.buf = append(e.buf, make([]byte, 8)...)
	binary.LittleEndian.PutUint64(e.buf[start:], value)
}

// I64 writes a two's-complement little-endian int64.
func (e *Encoder) I64(value int64) { e.U64(uint64(value)) }

// Fixed writes bytes without a prefix. The caller defines the field width.
func (e *Encoder) Fixed(value []byte) {
	if e == nil || e.err != nil {
		return
	}
	e.buf = append(e.buf, value...)
}

// BytesU32 writes a uint32 byte-length followed by the bytes. max is an
// application limit and must fit uint32; zero permits only an empty value.
func (e *Encoder) BytesU32(value []byte, max uint32) {
	if e == nil || e.err != nil {
		return
	}
	if uint64(len(value)) > uint64(max) || uint64(len(value)) > math.MaxUint32 {
		e.err = fmt.Errorf("%w: got %d, maximum %d", ErrLengthExceeded, len(value), max)
		return
	}
	e.U32(uint32(len(value)))
	e.Fixed(value)
}

// Option writes a canonical zero/one tag followed by the payload only when
// present. The callback is not invoked for None.
func (e *Encoder) Option(present bool, encode func(*Encoder) error) {
	if e == nil || e.err != nil {
		return
	}
	e.Bool(present)
	if !present {
		return
	}
	if encode == nil {
		e.err = ErrInvalidArgument
		return
	}
	if err := encode(e); err != nil {
		e.err = err
	}
}

// Marshal invokes a value's explicit codec and returns its canonical bytes.
func Marshal(value Marshaler) ([]byte, error) {
	if value == nil {
		return nil, ErrInvalidArgument
	}
	encoder := NewEncoder(0)
	if err := value.EncodeTo(encoder); err != nil {
		return nil, err
	}
	if err := encoder.Err(); err != nil {
		return nil, err
	}
	return encoder.Bytes(), nil
}

// Decoder reads an immutable deterministic byte stream. It never advances
// past malformed input and retains the first error.
type Decoder struct {
	data   []byte
	offset int
	err    error
}

// NewDecoder creates a decoder over data. The input is not mutated.
func NewDecoder(data []byte) *Decoder { return &Decoder{data: data} }

// Err returns the first decoding error.
func (d *Decoder) Err() error {
	if d == nil {
		return ErrInvalidArgument
	}
	return d.err
}

// Offset is the number of bytes consumed.
func (d *Decoder) Offset() int {
	if d == nil {
		return 0
	}
	return d.offset
}

// Remaining is the number of unread bytes.
func (d *Decoder) Remaining() int {
	if d == nil || d.offset > len(d.data) {
		return 0
	}
	return len(d.data) - d.offset
}

func (d *Decoder) take(size int) []byte {
	if d == nil || d.err != nil {
		return nil
	}
	if size < 0 || size > len(d.data)-d.offset {
		d.err = fmt.Errorf("%w at offset %d: need %d, have %d", ErrUnexpectedEnd, d.offset, size, len(d.data)-d.offset)
		return nil
	}
	start := d.offset
	d.offset += size
	return d.data[start:d.offset]
}

// U8 reads one byte.
func (d *Decoder) U8() uint8 {
	value := d.take(1)
	if value == nil {
		return 0
	}
	return value[0]
}

// Bool reads only canonical zero/one boolean tags.
func (d *Decoder) Bool() bool {
	value := d.U8()
	if d == nil || d.err != nil {
		return false
	}
	switch value {
	case 0:
		return false
	case 1:
		return true
	default:
		d.err = fmt.Errorf("%w at offset %d: %d", ErrInvalidBool, d.offset-1, value)
		return false
	}
}

// U16 reads a little-endian uint16.
func (d *Decoder) U16() uint16 {
	value := d.take(2)
	if value == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(value)
}

// U32 reads a little-endian uint32.
func (d *Decoder) U32() uint32 {
	value := d.take(4)
	if value == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(value)
}

// I32 reads a two's-complement little-endian int32.
func (d *Decoder) I32() int32 { return int32(d.U32()) }

// U64 reads a little-endian uint64.
func (d *Decoder) U64() uint64 {
	value := d.take(8)
	if value == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(value)
}

// I64 reads a two's-complement little-endian int64.
func (d *Decoder) I64() int64 { return int64(d.U64()) }

// Fixed reads exactly size bytes and returns a copy.
func (d *Decoder) Fixed(size int) []byte {
	value := d.take(size)
	return append([]byte(nil), value...)
}

// BytesU32 reads a uint32 byte-length and rejects values above max.
func (d *Decoder) BytesU32(max uint32) []byte {
	size := d.U32()
	if d == nil || d.err != nil {
		return nil
	}
	if size > max {
		d.err = fmt.Errorf("%w at offset %d: got %d, maximum %d", ErrLengthExceeded, d.offset-4, size, max)
		return nil
	}
	return d.Fixed(int(size))
}

// Option reads a canonical zero/one tag and invokes decode for Some.
func (d *Decoder) Option(decode func(*Decoder) error) bool {
	tag := d.U8()
	if d == nil || d.err != nil {
		return false
	}
	switch tag {
	case 0:
		return false
	case 1:
		if decode == nil {
			d.err = ErrInvalidArgument
			return false
		}
		if err := decode(d); err != nil {
			d.err = err
		}
		return d.err == nil
	default:
		d.err = fmt.Errorf("%w at offset %d: %d", ErrInvalidOption, d.offset-1, tag)
		return false
	}
}

// Finish requires the decoder to be error-free and fully consumed.
func (d *Decoder) Finish() error {
	if err := d.Err(); err != nil {
		return err
	}
	if d.Remaining() != 0 {
		return fmt.Errorf("%w: %d bytes", ErrTrailingData, d.Remaining())
	}
	return nil
}

// Unmarshal invokes an explicit decoder and rejects trailing bytes.
func Unmarshal(data []byte, value Unmarshaler) error {
	if value == nil {
		return ErrInvalidArgument
	}
	decoder := NewDecoder(data)
	if err := value.DecodeFrom(decoder); err != nil {
		return err
	}
	return decoder.Finish()
}
