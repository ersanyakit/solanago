package web

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/static"

	"github.com/ersanyakit/solanago/deploy"
	"github.com/ersanyakit/solanago/sdk"
	"github.com/ersanyakit/solanago/svmtest"
)

func parseProgramFeePayerRPC(programText, feePayerText, rpcText string) (programID, feePayer sdk.Pubkey, rpcURL string, err error) {
	programID, err = sdk.ParsePubkey(programText)
	if err != nil {
		return sdk.Pubkey{}, sdk.Pubkey{}, "", errors.New("programId is not a valid Solana public key")
	}
	feePayer, err = sdk.ParsePubkey(feePayerText)
	if err != nil {
		return sdk.Pubkey{}, sdk.Pubkey{}, "", errors.New("feePayer is not a valid Solana public key")
	}
	rpcURL, err = validateRPCURL(rpcText)
	if err != nil {
		return sdk.Pubkey{}, sdk.Pubkey{}, "", err
	}
	return programID, feePayer, rpcURL, nil
}

// Config configures NewServer.
type Config struct {
	// RepoRoot is the repository checkout Examples' Sources paths are
	// resolved against.
	RepoRoot string
}

// NewServer builds the Fiber app serving the example registry, the
// in-process builder, and the wallet-signed deploy session endpoints, plus
// the embedded static frontend.
func NewServer(cfg Config) *fiber.App {
	app := fiber.New()
	builder := NewBuilder(cfg.RepoRoot)
	sessions := newSessionStore()

	api := app.Group("/api")
	api.Get("/examples", listExamplesHandler())
	api.Post("/examples/:id/build", buildExampleHandler(builder))
	api.Get("/examples/:id/builds", buildHistoryHandler(builder))
	api.Get("/examples/:id/artifact", artifactHandler(builder))
	api.Post("/deploy/session", createSessionHandler(builder, sessions))
	api.Post("/deploy/session/:id/create-buffer-tx", createBufferTxHandler(sessions))
	api.Post("/deploy/session/:id/deploy-tx", deployTxHandler(sessions))
	registerInteractRoutes(api)
	registerPhonebookRoutes(api)
	registerToken2022Routes(api)
	api.Get("/source", sourceHandler(cfg.RepoRoot))

	sub, err := staticFS()
	if err != nil {
		panic(err)
	}
	app.Get("/*", static.New("", static.Config{FS: sub}))
	return app
}

func listExamplesHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		return c.JSON(Examples)
	}
}

// buildExampleHandler compiles example and always appends a new build to
// its history — clicking Build repeatedly is expected, not an error, and
// every click is independently selectable/deployable afterward (see
// buildHistoryHandler and createSessionHandler's buildId).
func buildExampleHandler(builder *Builder) fiber.Handler {
	return func(c fiber.Ctx) error {
		example, ok := FindExample(c.Params("id"))
		if !ok {
			return c.Status(fiber.StatusNotFound).JSON(errorBody("unknown example id"))
		}
		record, err := builder.Build(example)
		if err != nil {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(errorBody(err.Error()))
		}
		return c.JSON(record)
	}
}

func buildHistoryHandler(builder *Builder) fiber.Handler {
	return func(c fiber.Ctx) error {
		if _, ok := FindExample(c.Params("id")); !ok {
			return c.Status(fiber.StatusNotFound).JSON(errorBody("unknown example id"))
		}
		return c.JSON(builder.History(c.Params("id")))
	}
}

// artifactHandler serves one build's bytes — the one named by ?buildId, or
// the most recent build if omitted. It never compiles on the caller's
// behalf; a 404 here means POST .../build hasn't been called yet.
func artifactHandler(builder *Builder) fiber.Handler {
	return func(c fiber.Ctx) error {
		example, ok := FindExample(c.Params("id"))
		if !ok {
			return c.Status(fiber.StatusNotFound).JSON(errorBody("unknown example id"))
		}
		artifact, err := builder.Artifact(example.ID, c.Query("buildId"))
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(errorBody("no matching build; call build first"))
		}
		c.Set(fiber.HeaderContentType, "application/octet-stream")
		return c.Send(artifact)
	}
}

type createSessionRequest struct {
	ExampleID string `json:"exampleId"`
	FeePayer  string `json:"feePayer"`
	RPCURL    string `json:"rpcUrl"`
	// BuildID selects a specific prior build (from GET .../builds) to
	// deploy. Empty means "build fresh now," matching the old behavior.
	BuildID string `json:"buildId"`
}

func createSessionHandler(builder *Builder, sessions *sessionStore) fiber.Handler {
	return func(c fiber.Ctx) error {
		var body createSessionRequest
		if err := c.Bind().Body(&body); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("malformed request body"))
		}
		example, ok := FindExample(body.ExampleID)
		if !ok {
			return c.Status(fiber.StatusNotFound).JSON(errorBody("unknown example id"))
		}
		feePayer, err := sdk.ParsePubkey(body.FeePayer)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody("feePayer is not a valid Solana public key"))
		}
		rpcURL, err := validateRPCURL(body.RPCURL)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(errorBody(err.Error()))
		}

		var buildID string
		var artifactLen int
		if body.BuildID != "" {
			artifact, err := builder.Artifact(example.ID, body.BuildID)
			if err != nil {
				return c.Status(fiber.StatusNotFound).JSON(errorBody("unknown buildId; build it again first"))
			}
			buildID, artifactLen = body.BuildID, len(artifact)
		} else {
			record, err := builder.Build(example)
			if err != nil {
				return c.Status(fiber.StatusUnprocessableEntity).JSON(errorBody(err.Error()))
			}
			buildID, artifactLen = record.ID, record.SizeBytes
		}

		session, err := sessions.create(example.ID, feePayer, rpcURL, artifactLen, buildID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(errorBody(err.Error()))
		}
		return c.JSON(fiber.Map{
			"sessionId": session.id,
			"programId": session.program.PublicKey.String(),
			"bufferId":  session.buffer.PublicKey.String(),
			"elfLength": session.elfLength,
			"buildId":   session.buildID,
		})
	}
}

func createBufferTxHandler(sessions *sessionStore) fiber.Handler {
	return func(c fiber.Ctx) error {
		session, err := sessions.get(c.Params("id"))
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(errorBody(err.Error()))
		}
		client := svmtest.Client{URL: session.rpcURL}
		tx, err := deploy.PrepareCreateBufferTransaction(c.Context(), client, session.feePayer, session.buffer, session.elfLength)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		return c.JSON(fiber.Map{"tx": base64.StdEncoding.EncodeToString(tx)})
	}
}

func deployTxHandler(sessions *sessionStore) fiber.Handler {
	return func(c fiber.Ctx) error {
		session, err := sessions.get(c.Params("id"))
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(errorBody(err.Error()))
		}
		client := svmtest.Client{URL: session.rpcURL}
		tx, err := deploy.PrepareDeployTransaction(c.Context(), client, session.feePayer, session.program, session.buffer.PublicKey, session.elfLength)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(errorBody(err.Error()))
		}
		return c.JSON(fiber.Map{"tx": base64.StdEncoding.EncodeToString(tx)})
	}
}

func errorBody(message string) fiber.Map {
	return fiber.Map{"error": message}
}

// validateRPCURL requires an absolute http(s) URL so a session can't be
// coerced into aiming a server-side RPC call at an arbitrary scheme/host
// class this backend never intended to speak to (e.g. file:// or a bare
// path). It intentionally still allows any http(s) host, including
// loopback, since a local test-validator is a supported target.
func validateRPCURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("rpcUrl must be an absolute http:// or https:// URL")
	}
	return trimmed, nil
}
