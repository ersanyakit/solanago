package serialization

import (
	"errors"
	"reflect"
	"testing"
)

type fixture struct {
	Tag     uint8
	Counter uint32
	Delta   int32
	Amount  uint64
	Enabled bool
	Memo    []byte
}

func (f fixture) EncodeTo(e *Encoder) error {
	e.U8(f.Tag)
	e.U32(f.Counter)
	e.I32(f.Delta)
	e.U64(f.Amount)
	e.Bool(f.Enabled)
	e.BytesU32(f.Memo, 16)
	return e.Err()
}

func (f *fixture) DecodeFrom(d *Decoder) error {
	f.Tag = d.U8()
	f.Counter = d.U32()
	f.Delta = d.I32()
	f.Amount = d.U64()
	f.Enabled = d.Bool()
	f.Memo = d.BytesU32(16)
	return d.Err()
}

func TestDeterministicCodecGolden(t *testing.T) {
	want := []byte{
		0x7f,
		0x04, 0x03, 0x02, 0x01,
		0xfe, 0xff, 0xff, 0xff,
		0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
		0x01,
		0x03, 0x00, 0x00, 0x00, 's', 'b', 'f',
	}
	input := fixture{Tag: 0x7f, Counter: 0x01020304, Delta: -2, Amount: 0x0102030405060708, Enabled: true, Memo: []byte("sbf")}
	got, err := Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("golden mismatch\n got: %x\nwant: %x", got, want)
	}

	var decoded fixture
	if err := Unmarshal(want, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, input) {
		t.Fatalf("round trip mismatch: got %#v want %#v", decoded, input)
	}
}

func TestDecoderRejectsNonCanonicalAndTrailing(t *testing.T) {
	d := NewDecoder([]byte{2})
	_ = d.Bool()
	if !errors.Is(d.Err(), ErrInvalidBool) {
		t.Fatalf("expected invalid bool, got %v", d.Err())
	}

	d = NewDecoder([]byte{2})
	_ = d.Option(func(*Decoder) error { return nil })
	if !errors.Is(d.Err(), ErrInvalidOption) {
		t.Fatalf("expected invalid option, got %v", d.Err())
	}

	var decoded fixture
	encoded, err := Marshal(fixture{})
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, 0)
	if err := Unmarshal(encoded, &decoded); !errors.Is(err, ErrTrailingData) {
		t.Fatalf("expected trailing data, got %v", err)
	}
}

func TestLengthLimitsAndTruncation(t *testing.T) {
	e := NewEncoder(0)
	e.BytesU32([]byte{1, 2}, 1)
	if !errors.Is(e.Err(), ErrLengthExceeded) {
		t.Fatalf("expected length error, got %v", e.Err())
	}

	d := NewDecoder([]byte{4, 0, 0, 0, 1})
	_ = d.BytesU32(4)
	if !errors.Is(d.Err(), ErrUnexpectedEnd) {
		t.Fatalf("expected truncation, got %v", d.Err())
	}
}
