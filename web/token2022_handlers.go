package web

import (
	"encoding/base64"

	"github.com/gofiber/fiber/v3"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/sdk/associatedtoken"
	"github.com/ersanyakit/solanago/sdk/token2022"
	"github.com/ersanyakit/solanago/svmtest"
)

func registerToken2022Routes(api fiber.Router) {
	api.Post("/token2022/create-mint", token2022CreateMintHandler())
	api.Post("/token2022/create-ata", token2022CreateATAHandler())
	api.Post("/token2022/mint-to", token2022MintToHandler())
	api.Post("/token2022/transfer", token2022TransferHandler())
	api.Get("/token2022/mint", token2022ReadMintHandler())
	api.Get("/token2022/account", token2022ReadAccountHandler())
}

type token2022CreateMintRequest struct {
	FeePayer        string `json:"feePayer"`
	RPCURL          string `json:"rpcUrl"`
	Decimals        uint8  `json:"decimals"`
	FreezeAuthority string `json:"freezeAuthority"`
}

func token2022CreateMintHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		var body token2022CreateMintRequest
		if err := c.Bind().Body(&body); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("malformed request body"))
		}
		feePayer, err := sdk.ParsePubkey(body.FeePayer)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("feePayer is not a valid Solana public key"))
		}
		rpcURL, err := validateRPCURL(body.RPCURL)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		freezeAuthority := token2022.OptionalPubkey{}
		if body.FreezeAuthority != "" {
			key, err := sdk.ParsePubkey(body.FreezeAuthority)
			if err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(errorBody("freezeAuthority is not a valid Solana public key"))
			}
			freezeAuthority = token2022.SomePubkey(key)
		}

		mint, err := svmtest.NewSigner()
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(errorBody(err.Error()))
		}
		client := svmtest.Client{URL: rpcURL}
		rent, err := client.MinimumBalanceForRentExemption(c.Context(), uint64(token2022.MintSize))
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		blockhash, err := client.LatestBlockhash(c.Context())
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		instructions := createMintInstructions(feePayer, mint.PublicKey, freezeAuthority, body.Decimals, rent)
		message, keys, numRequired, err := svmtest.CompileTransactionMessage(feePayer, blockhash, instructions, true)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(errorBody(err.Error()))
		}
		tx := svmtest.PartialSignTransaction(message, keys, numRequired, []svmtest.Signer{mint})
		return c.JSON(fiber.Map{
			"tx":   base64.StdEncoding.EncodeToString(tx),
			"mint": mint.PublicKey.String(),
		})
	}
}

type token2022CreateATARequest struct {
	FeePayer string `json:"feePayer"`
	RPCURL   string `json:"rpcUrl"`
	Mint     string `json:"mint"`
	Owner    string `json:"owner"`
}

func token2022CreateATAHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		var body token2022CreateATARequest
		if err := c.Bind().Body(&body); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("malformed request body"))
		}
		feePayer, err := sdk.ParsePubkey(body.FeePayer)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("feePayer is not a valid Solana public key"))
		}
		rpcURL, err := validateRPCURL(body.RPCURL)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		mint, err := sdk.ParsePubkey(body.Mint)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("mint is not a valid Solana public key"))
		}
		owner := feePayer
		if body.Owner != "" {
			owner, err = sdk.ParsePubkey(body.Owner)
			if err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(errorBody("owner is not a valid Solana public key"))
			}
		}

		instruction, ata, err := associatedtoken.CreateIdempotent(feePayer, owner, mint, token2022.ProgramID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(errorBody(err.Error()))
		}
		client := svmtest.Client{URL: rpcURL}
		blockhash, err := client.LatestBlockhash(c.Context())
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		message, keys, numRequired, err := svmtest.CompileTransactionMessage(feePayer, blockhash, []sdk.Instruction{instruction}, true)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(errorBody(err.Error()))
		}
		tx := svmtest.PartialSignTransaction(message, keys, numRequired, nil)
		return c.JSON(fiber.Map{
			"tx":                     base64.StdEncoding.EncodeToString(tx),
			"associatedTokenAccount": ata.String(),
		})
	}
}

type token2022MintToRequest struct {
	FeePayer     string `json:"feePayer"`
	RPCURL       string `json:"rpcUrl"`
	Mint         string `json:"mint"`
	TokenAccount string `json:"tokenAccount"`
	Amount       uint64 `json:"amount"`
}

func token2022MintToHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		var body token2022MintToRequest
		if err := c.Bind().Body(&body); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("malformed request body"))
		}
		feePayer, err := sdk.ParsePubkey(body.FeePayer)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("feePayer is not a valid Solana public key"))
		}
		rpcURL, err := validateRPCURL(body.RPCURL)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		mint, err := sdk.ParsePubkey(body.Mint)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("mint is not a valid Solana public key"))
		}
		tokenAccount, err := sdk.ParsePubkey(body.TokenAccount)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("tokenAccount is not a valid Solana public key"))
		}
		instruction, err := token2022.MintTo(mint, tokenAccount, feePayer, nil, body.Amount)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		client := svmtest.Client{URL: rpcURL}
		blockhash, err := client.LatestBlockhash(c.Context())
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		message, keys, numRequired, err := svmtest.CompileTransactionMessage(feePayer, blockhash, []sdk.Instruction{instruction}, true)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(errorBody(err.Error()))
		}
		tx := svmtest.PartialSignTransaction(message, keys, numRequired, nil)
		return c.JSON(fiber.Map{"tx": base64.StdEncoding.EncodeToString(tx)})
	}
}

type token2022TransferRequest struct {
	FeePayer    string `json:"feePayer"`
	RPCURL      string `json:"rpcUrl"`
	Mint        string `json:"mint"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Amount      uint64 `json:"amount"`
	Decimals    uint8  `json:"decimals"`
}

func token2022TransferHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		var body token2022TransferRequest
		if err := c.Bind().Body(&body); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("malformed request body"))
		}
		feePayer, err := sdk.ParsePubkey(body.FeePayer)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("feePayer is not a valid Solana public key"))
		}
		rpcURL, err := validateRPCURL(body.RPCURL)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		mint, err := sdk.ParsePubkey(body.Mint)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("mint is not a valid Solana public key"))
		}
		source, err := sdk.ParsePubkey(body.Source)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("source is not a valid Solana public key"))
		}
		destination, err := sdk.ParsePubkey(body.Destination)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("destination is not a valid Solana public key"))
		}
		instruction, err := token2022.TransferChecked(source, mint, destination, feePayer, nil, body.Amount, body.Decimals)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		client := svmtest.Client{URL: rpcURL}
		blockhash, err := client.LatestBlockhash(c.Context())
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		message, keys, numRequired, err := svmtest.CompileTransactionMessage(feePayer, blockhash, []sdk.Instruction{instruction}, true)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(errorBody(err.Error()))
		}
		tx := svmtest.PartialSignTransaction(message, keys, numRequired, nil)
		return c.JSON(fiber.Map{"tx": base64.StdEncoding.EncodeToString(tx)})
	}
}

func token2022ReadMintHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		rpcURL, err := validateRPCURL(c.Query("rpcUrl"))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		address, err := sdk.ParsePubkey(c.Query("address"))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("address is not a valid Solana public key"))
		}
		client := svmtest.Client{URL: rpcURL}
		mint, err := fetchToken2022Mint(c.Context(), client, address)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		return c.JSON(fiber.Map{
			"initialized":     mint.Initialized,
			"decimals":        mint.Decimals,
			"supply":          mint.Supply,
			"mintAuthority":   formatOptionalPubkeyJSON(mint.MintAuthority),
			"freezeAuthority": formatOptionalPubkeyJSON(mint.FreezeAuthority),
		})
	}
}

func token2022ReadAccountHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		rpcURL, err := validateRPCURL(c.Query("rpcUrl"))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		address, err := sdk.ParsePubkey(c.Query("address"))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("address is not a valid Solana public key"))
		}
		client := svmtest.Client{URL: rpcURL}
		account, err := fetchToken2022Account(c.Context(), client, address)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		return c.JSON(fiber.Map{
			"mint":   account.Mint.String(),
			"owner":  account.Owner.String(),
			"amount": account.Amount,
			"state":  account.State,
		})
	}
}

func formatOptionalPubkeyJSON(value token2022.OptionalPubkey) any {
	if !value.Set {
		return nil
	}
	return value.Value.String()
}
