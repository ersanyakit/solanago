package serialization

import "testing"

func FuzzDeterministicCodec(f *testing.F) {
	f.Add(uint8(1), uint32(2), int32(-3), uint64(4), true, []byte("seed"))
	f.Fuzz(func(t *testing.T, tag uint8, counter uint32, delta int32, amount uint64, enabled bool, memo []byte) {
		if len(memo) > 16 {
			memo = memo[:16]
		}
		input := fixture{Tag: tag, Counter: counter, Delta: delta, Amount: amount, Enabled: enabled, Memo: append([]byte(nil), memo...)}
		encoded, err := Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var output fixture
		if err := Unmarshal(encoded, &output); err != nil {
			t.Fatal(err)
		}
		if output.Tag != input.Tag || output.Counter != input.Counter || output.Delta != input.Delta || output.Amount != input.Amount || output.Enabled != input.Enabled || string(output.Memo) != string(input.Memo) {
			t.Fatalf("round trip mismatch: got %#v want %#v", output, input)
		}
	})
}

func FuzzDecoderNeverPanics(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4})
	f.Fuzz(func(t *testing.T, data []byte) {
		var output fixture
		_ = Unmarshal(data, &output)
	})
}
