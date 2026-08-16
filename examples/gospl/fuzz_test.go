package gospl

import "testing"

func FuzzInstructionCodecIsCanonical(f *testing.F) {
	f.Add([]byte{})
	f.Add(EncodeInitializeMint(6, testKey(1)))
	f.Add(EncodeInitializeAccount(testKey(2)))
	f.Add(EncodeRevoke())
	amount, _ := EncodeAmountInstruction(InstructionTransfer, 42)
	f.Add(amount)

	f.Fuzz(func(t *testing.T, data []byte) {
		instruction, err := DecodeInstruction(data)
		if err != nil {
			return
		}
		var encoded []byte
		switch instruction.Kind {
		case InstructionInitializeMint:
			encoded = EncodeInitializeMint(instruction.Decimals, instruction.Authority)
		case InstructionInitializeAccount:
			encoded = EncodeInitializeAccount(instruction.Authority)
		case InstructionTransfer, InstructionMintTo, InstructionBurn, InstructionApprove:
			encoded, err = EncodeAmountInstruction(instruction.Kind, instruction.Amount)
		case InstructionRevoke:
			encoded = EncodeRevoke()
		case InstructionSetAuthority:
			encoded, err = EncodeSetAuthority(instruction.AuthorityType, instruction.NewAuthority)
		}
		if err != nil || string(encoded) != string(data) {
			t.Fatalf("accepted non-canonical encoding %x -> %x, %v", data, encoded, err)
		}
	})
}
