package sdk

import (
	"errors"
	"testing"
)

// Rust vectors: anza-xyz/solana-sdk commit
// 7437469d1ab5bddbf665f3a1a69aefb422c33e36, address/src/lib.rs.
// Cross-language vectors: anza-xyz/kit commit
// 851610f7d8755517f7f6870da360cd9109efe9cf, @solana/addresses 7.1.0.
func TestOfficialPDAVectors(t *testing.T) {
	tests := []struct {
		name      string
		programID string
		seeds     [][]byte
		want      string
		bump      uint8
	}{
		{"kit-no-seeds", "CZ3TbkgUYpDAJVEWpujQhDSgzNTeqbokrJmYa1j4HAZc", nil, "9tVtkyCGAHSDDBPwz7895aC3p2gJRjpu2v26o35FTUco", 255},
		{"kit-multiple-bumps", "EfTbwNBrSqSuCNBhWUHsBoBdSMWgRU1S47daqRNgW7aK", nil, "CKWT8KZ5GMzKpVRiAULWKPg1LiHt9U3NdAtbuTErHCTq", 251},
		{"kit-bytes", "FD3PDEvpQ9JXq8tv7FpJPyZrCjWkCnAaTju16gFPdpqP", [][]byte{{1, 2, 3}}, "9Tj3hpMWacDiZoBe94sjwJQ72zsUVvEQYsrqyy2CfHky", 255},
		{"kit-string", "EKaNRGA37uiGRyRPMap5EZg9cmbT5mt7KWrGwKwAQ3rK", [][]byte{[]byte("hello")}, "6V76gtKMCmVVjrx4sxR9uB868HtZbL3piKEmadC7rSgf", 255},
		{"kit-utf8", "A5dcVPLJsE2vbf7hkqqyYkYDK9UjUfNxuwGtWF2m2vEz", [][]byte{[]byte("🚀")}, "GYpAzW57Ex4Sw3rp4pq95QrjvtsDyqZsMhSZwqz3NMsE", 255},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			programID := MustParsePubkey(test.programID)
			got, bump, err := FindProgramAddress(test.seeds, programID)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != test.want || bump != test.bump {
				t.Fatalf("got (%s, %d), want (%s, %d)", got, bump, test.want, test.bump)
			}
			withBump := append(append([][]byte(nil), test.seeds...), []byte{bump})
			recreated, err := CreateProgramAddress(withBump, programID)
			if err != nil || recreated != got {
				t.Fatalf("recreate = (%s, %v), want (%s, nil)", recreated, err, got)
			}
		})
	}
}

func TestRustCreateProgramAddressVectors(t *testing.T) {
	programID := MustParsePubkey("BPFLoaderUpgradeab1e11111111111111111111111")
	publicKey := MustParsePubkey("SeedPubey1111111111111111111111111111111111")
	tests := []struct {
		seeds [][]byte
		want  string
	}{
		{[][]byte{nil, {1}}, "BwqrghZA2htAcqq8dzP1WDAhTXYTYWj7CHxF5j7TDBAe"},
		{[][]byte{[]byte("☉"), {0}}, "13yWmRpaTR4r5nAktwLqMpRNr28tnVUZw26rTvPSSB19"},
		{[][]byte{[]byte("Talking"), []byte("Squirrels")}, "2fnQrngrQT4SeLcdToJAD96phoEjNL2man2kfRLCASVk"},
		{[][]byte{publicKey[:], {1}}, "976ymqVnfE32QFe6NfGDctSvVa36LWnvYxhU6G2232YL"},
	}
	for _, test := range tests {
		got, err := CreateProgramAddress(test.seeds, programID)
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != test.want {
			t.Fatalf("got %s, want %s", got, test.want)
		}
	}
}

func TestKitCreateWithSeedVector(t *testing.T) {
	got, err := CreateWithSeed(
		MustParsePubkey("Bh1uUDP3ApWLeccVNHwyQKpnfGQbuE2UECbGA6M4jiZJ"),
		"seed",
		MustParsePubkey("FGrddpvjBUAG6VdV4fR8Q2hEZTHS6w4SEveVBgfwbfdm"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "HUKxCeXY6gZohFJFARbLE6L6C9wDEHz1SfK8ENM7QY7z" {
		t.Fatalf("got %s", got)
	}
}

func TestKitCurveVectors(t *testing.T) {
	onCurve := []string{
		"nick6zJc6HpW3kfBm4xS2dmbuVRyb5F3AnUvj5ymzR5",
		"11111111111111111111111111111111",
		"TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA",
	}
	offCurve := []string{
		"CCMCWh4FudPEmY6Q1AVi5o8mQMXkHYkJUmZfzRGdcJ9P",
		"2DRxyJDsDccGL6mb8PLMsKQTCU3C7xUq8aprz53VcW4k",
	}
	for _, text := range onCurve {
		if !MustParsePubkey(text).IsOnCurve() {
			t.Fatalf("%s: expected on curve", text)
		}
	}
	for _, text := range offCurve {
		if MustParsePubkey(text).IsOnCurve() {
			t.Fatalf("%s: expected off curve", text)
		}
	}
}

func TestPubkeyStrictParsingAndSeedLimits(t *testing.T) {
	for _, text := range []string{"", "0", "1", "111111111111111111111111111111111", "Tokenkeg0feZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"} {
		if _, err := ParsePubkey(text); !errors.Is(err, ErrInvalidPubkey) {
			t.Fatalf("ParsePubkey(%q) error = %v", text, err)
		}
	}
	tooLong := make([]byte, MaxSeedLength+1)
	if _, err := CreateProgramAddress([][]byte{tooLong}, Pubkey{}); !errors.Is(err, ErrMaxSeedLength) {
		t.Fatalf("oversize seed error = %v", err)
	}
	if _, _, err := FindProgramAddress(make([][]byte, MaxSeeds), Pubkey{}); !errors.Is(err, ErrTooManySeeds) {
		t.Fatalf("too many seeds error = %v", err)
	}
	if _, err := CreateWithSeed(Pubkey{}, string([]byte{0xff}), Pubkey{}); !errors.Is(err, ErrInvalidSeed) {
		t.Fatalf("invalid UTF-8 seed error = %v", err)
	}
}

func FuzzPubkeyTextRoundTrip(f *testing.F) {
	f.Add("11111111111111111111111111111111")
	f.Add("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA")
	f.Fuzz(func(t *testing.T, text string) {
		key, err := ParsePubkey(text)
		if err != nil {
			return
		}
		roundTrip, err := ParsePubkey(key.String())
		if err != nil || roundTrip != key {
			t.Fatalf("round trip = (%v, %v)", roundTrip, err)
		}
	})
}
