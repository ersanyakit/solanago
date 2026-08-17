package web

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"

	"github.com/gofiber/fiber/v3"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/system"
	"github.com/ersanyakit/solanago/svmtest"
)

func registerInteractRoutes(api fiber.Router) {
	api.Get("/examples/:id/schema", interactSchemaHandler())
	api.Post("/examples/:id/call/:instruction", interactCallHandler())
	api.Get("/examples/:id/read/:state", interactReadHandler())
}

func interactSchemaHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		schema, ok := InteractSchemas[c.Params("id")]
		if !ok {
			return c.JSON(ExampleInteractSchema{Instructions: []InstructionSpec{}, States: []StateLayoutSpec{}})
		}
		return c.JSON(schema)
	}
}

type interactCallRequest struct {
	ProgramID string            `json:"programId"`
	FeePayer  string            `json:"feePayer"`
	RPCURL    string            `json:"rpcUrl"`
	Accounts  map[string]string `json:"accounts"`
	Fields    map[string]string `json:"fields"`
}

func interactCallHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		schema, ok := InteractSchemas[c.Params("id")]
		if !ok {
			return c.Status(fiber.StatusNotFound).JSON(errorBody("unknown example id"))
		}
		var instruction *InstructionSpec
		for index := range schema.Instructions {
			if schema.Instructions[index].Name == c.Params("instruction") {
				instruction = &schema.Instructions[index]
				break
			}
		}
		if instruction == nil {
			return c.Status(fiber.StatusNotFound).JSON(errorBody("unknown instruction"))
		}
		var body interactCallRequest
		if err := c.Bind().Body(&body); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("malformed request body"))
		}
		programID, feePayer, rpcURL, err := parseProgramFeePayerRPC(body.ProgramID, body.FeePayer, body.RPCURL)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		client := svmtest.Client{URL: rpcURL}

		resolved, newSigners, createInstructions, generated, err := resolveAccounts(c.Context(), client, schema, *instruction, programID, feePayer, body.Accounts)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		data, err := encodeFields(instruction.Tag, instruction.Fields, body.Fields)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}

		instructions := append(createInstructions, sdk.Instruction{ProgramID: programID, Accounts: resolved, Data: data})
		blockhash, err := client.LatestBlockhash(c.Context())
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		message, keys, numRequired, err := svmtest.CompileTransactionMessage(feePayer, blockhash, instructions, true)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(errorBody(err.Error()))
		}
		tx := svmtest.PartialSignTransaction(message, keys, numRequired, newSigners)
		return c.JSON(fiber.Map{
			"tx":       base64.StdEncoding.EncodeToString(tx),
			"accounts": generated,
		})
	}
}

func interactReadHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		schema, ok := InteractSchemas[c.Params("id")]
		if !ok {
			return c.Status(fiber.StatusNotFound).JSON(errorBody("unknown example id"))
		}
		var layout *StateLayoutSpec
		for index := range schema.States {
			if schema.States[index].Name == c.Params("state") {
				layout = &schema.States[index]
				break
			}
		}
		if layout == nil {
			return c.Status(fiber.StatusNotFound).JSON(errorBody("unknown state layout"))
		}
		rpcURL, err := validateRPCURL(c.Query("rpcUrl"))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		address, err := sdk.ParsePubkey(c.Query("address"))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("address is not a valid Solana public key"))
		}
		client := svmtest.Client{URL: rpcURL}
		raw, err := fetchAccountData(c.Context(), client, address)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		decoded, err := decodeStateLayout(raw, *layout)
		if err != nil {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(errorBody(err.Error()))
		}
		return c.JSON(decoded)
	}
}

func fetchAccountData(ctx context.Context, client svmtest.Client, address sdk.Pubkey) ([]byte, error) {
	info, err := client.GetAccountInfo(ctx, address)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, errors.New("account not found")
	}
	return info.DataBytes()
}

// resolveAccounts turns an InstructionSpec's account roles into concrete
// AccountMetas, generating one System::CreateAccount instruction (and one
// ephemeral local signer) per NewAccount role, and resolving Default/
// DerivedFrom roles without asking the caller for them. It returns the
// resolved metas in the instruction's declared order, every locally-held
// signer PartialSignTransaction should sign with, any CreateAccount
// instructions to prepend, and a name->pubkey map of every account this
// call resolved on the caller's behalf (for the response, so the frontend
// can show/reuse generated addresses).
func resolveAccounts(ctx context.Context, client svmtest.Client, schema ExampleInteractSchema, instruction InstructionSpec, programID, feePayer sdk.Pubkey, provided map[string]string) ([]sdk.AccountMeta, []svmtest.Signer, []sdk.Instruction, map[string]string, error) {
	metas := make([]sdk.AccountMeta, 0, len(instruction.Accounts))
	var newSigners []svmtest.Signer
	var createInstructions []sdk.Instruction
	resolvedByName := make(map[string]sdk.Pubkey, len(instruction.Accounts))
	generated := make(map[string]string, len(instruction.Accounts))

	for _, role := range instruction.Accounts {
		var key sdk.Pubkey
		switch {
		case role.NewAccount:
			signer, err := svmtest.NewSigner()
			if err != nil {
				return nil, nil, nil, nil, err
			}
			rent, err := client.MinimumBalanceForRentExemption(ctx, role.Space)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("rent for %s: %w", role.Name, err)
			}
			createInstructions = append(createInstructions, system.CreateAccount(feePayer, signer.PublicKey, rent, role.Space, programID))
			newSigners = append(newSigners, signer)
			key = signer.PublicKey
			generated[role.Name] = key.String()
		case role.Default == "wallet":
			key = feePayer
		case role.Default == "system":
			key = zeroPubkey
		case role.DerivedFromAccount != "":
			source, ok := resolvedByName[role.DerivedFromAccount]
			if !ok {
				return nil, nil, nil, nil, fmt.Errorf("account %s must be resolved before %s can derive from it", role.DerivedFromAccount, role.Name)
			}
			layout, ok := findStateLayout(schema, role.DerivedFromLayout)
			if !ok {
				return nil, nil, nil, nil, fmt.Errorf("unknown state layout %q for %s", role.DerivedFromLayout, role.Name)
			}
			raw, err := fetchAccountData(ctx, client, source)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("read %s to derive %s: %w", role.DerivedFromAccount, role.Name, err)
			}
			decoded, err := decodeStateLayout(raw, layout)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("decode %s to derive %s: %w", role.DerivedFromAccount, role.Name, err)
			}
			text, ok := decoded[role.DerivedFromField].(string)
			if !ok {
				return nil, nil, nil, nil, fmt.Errorf("field %q not found on %s", role.DerivedFromField, role.DerivedFromLayout)
			}
			key, err = sdk.ParsePubkey(text)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("derived %s is not a valid pubkey: %w", role.Name, err)
			}
		default:
			text, ok := provided[role.Name]
			if !ok || text == "" {
				return nil, nil, nil, nil, fmt.Errorf("missing required account %q", role.Name)
			}
			var err error
			key, err = sdk.ParsePubkey(text)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("account %q is not a valid pubkey: %w", role.Name, err)
			}
		}
		resolvedByName[role.Name] = key
		if role.Writable {
			metas = append(metas, sdk.Writable(key, role.Signer))
		} else {
			metas = append(metas, sdk.Readonly(key, role.Signer))
		}
	}
	return metas, newSigners, createInstructions, generated, nil
}

func findStateLayout(schema ExampleInteractSchema, name string) (StateLayoutSpec, bool) {
	for _, layout := range schema.States {
		if layout.Name == name {
			return layout, true
		}
	}
	return StateLayoutSpec{}, false
}

// encodeFields packs tag followed by each field's value (from values,
// keyed by FieldSpec.Name) in order, using the same little-endian /
// zero-padded conventions every example's own instruction.go already uses.
func encodeFields(tag []byte, fields []FieldSpec, values map[string]string) ([]byte, error) {
	data := append([]byte(nil), tag...)
	for _, field := range fields {
		raw, ok := values[field.Name]
		if !ok {
			return nil, fmt.Errorf("missing required field %q", field.Name)
		}
		switch field.Type {
		case FieldPubkey:
			key, err := sdk.ParsePubkey(raw)
			if err != nil {
				return nil, fmt.Errorf("field %q is not a valid pubkey: %w", field.Name, err)
			}
			data = append(data, key[:]...)
		case FieldU8:
			value, err := strconv.ParseUint(raw, 10, 8)
			if err != nil {
				return nil, fmt.Errorf("field %q is not a valid u8: %w", field.Name, err)
			}
			data = append(data, byte(value))
		case FieldU16:
			value, err := strconv.ParseUint(raw, 10, 16)
			if err != nil {
				return nil, fmt.Errorf("field %q is not a valid u16: %w", field.Name, err)
			}
			data = binary.LittleEndian.AppendUint16(data, uint16(value))
		case FieldU32:
			value, err := strconv.ParseUint(raw, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("field %q is not a valid u32: %w", field.Name, err)
			}
			data = binary.LittleEndian.AppendUint32(data, uint32(value))
		case FieldU64:
			value, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("field %q is not a valid u64: %w", field.Name, err)
			}
			data = binary.LittleEndian.AppendUint64(data, value)
		case FieldBool:
			if raw == "true" || raw == "1" {
				data = append(data, 1)
			} else {
				data = append(data, 0)
			}
		case FieldString:
			encoded := []byte(raw)
			if len(encoded) > field.Len {
				return nil, fmt.Errorf("field %q is %d bytes, want at most %d", field.Name, len(encoded), field.Len)
			}
			padded := make([]byte, field.Len)
			copy(padded, encoded)
			data = append(data, padded...)
		case FieldVarString:
			encoded := []byte(raw)
			data = binary.LittleEndian.AppendUint32(data, uint32(len(encoded)))
			data = append(data, encoded...)
		default:
			return nil, fmt.Errorf("field %q has unknown type %q", field.Name, field.Type)
		}
	}
	return data, nil
}

// decodeStateLayout reads every field out of raw at its fixed offset. It
// does not otherwise validate raw against the account's own tag/flag
// conventions (each example's Decode*State does that on the Go side); this
// is a display-only reader.
func decodeStateLayout(raw []byte, layout StateLayoutSpec) (map[string]any, error) {
	if uint64(len(raw)) < layout.Size {
		return nil, fmt.Errorf("account data is %d bytes, want at least %d for %s", len(raw), layout.Size, layout.Name)
	}
	result := make(map[string]any, len(layout.Fields))
	for _, field := range layout.Fields {
		switch field.Type {
		case FieldPubkey:
			key, err := sdk.PubkeyFromBytes(raw[field.Offset : field.Offset+32])
			if err != nil {
				return nil, err
			}
			result[field.Name] = key.String()
		case FieldU8:
			result[field.Name] = raw[field.Offset]
		case FieldU16:
			result[field.Name] = binary.LittleEndian.Uint16(raw[field.Offset : field.Offset+2])
		case FieldU32:
			result[field.Name] = binary.LittleEndian.Uint32(raw[field.Offset : field.Offset+4])
		case FieldU64:
			result[field.Name] = binary.LittleEndian.Uint64(raw[field.Offset : field.Offset+8])
		case FieldBool:
			result[field.Name] = raw[field.Offset] != 0
		case FieldString:
			end := field.Offset + uint64(field.Len)
			result[field.Name] = trimTrailingZeros(raw[field.Offset:end])
		case FieldVarString:
			length := binary.LittleEndian.Uint32(raw[field.Offset : field.Offset+4])
			start := field.Offset + 4
			end := start + uint64(length)
			if end > uint64(len(raw)) {
				return nil, fmt.Errorf("field %q length %d overruns account data", field.Name, length)
			}
			result[field.Name] = string(raw[start:end])
		default:
			return nil, fmt.Errorf("field %q has unknown type %q", field.Name, field.Type)
		}
	}
	return result, nil
}

func trimTrailingZeros(raw []byte) string {
	end := len(raw)
	for end > 0 && raw[end-1] == 0 {
		end--
	}
	return string(raw[:end])
}
