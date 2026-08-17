package web

import (
	"github.com/gofiber/fiber/v3"

	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/svmtest"
)

// registerPhonebookRoutes wires the one piece of phonebook's surface the
// generic interact engine (interact_engine.go) intentionally doesn't cover:
// the repeated contact-list region inside a Phonebook account.
// StateLayoutSpec only supports fixed, non-repeating fields, so phonebook's
// "config" account is read generically via GET
// /api/examples/phonebook/read/config, but its contact list keeps this
// dedicated endpoint. init-config/init-phonebook/add-contact/withdraw are
// all now generic — see web/interact_schema.go's phonebookSchema.
func registerPhonebookRoutes(api fiber.Router) {
	api.Get("/phonebook/contacts", phonebookContactsHandler())
}

func phonebookContactsHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		rpcURL, err := validateRPCURL(c.Query("rpcUrl"))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}
		address, err := sdk.ParsePubkey(c.Query("phonebook"))
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("phonebook is not a valid Solana public key"))
		}
		client := svmtest.Client{URL: rpcURL}
		state, err := fetchPhonebookState(c.Context(), client, address)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		contacts := make([]fiber.Map, len(state.Contacts))
		for index, contact := range state.Contacts {
			contacts[index] = fiber.Map{"address": contact.Address.String(), "name": contact.Name}
		}
		return c.JSON(fiber.Map{
			"initialized": state.Initialized,
			"owner":       state.Owner.String(),
			"count":       state.Count,
			"contacts":    contacts,
		})
	}
}
