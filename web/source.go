// source.go serves read-only source file content for the file tree's
// "click a file to view it" behavior. It only ever serves paths from an
// explicit allowlist (every Example's Sources, plus the handful of
// sdk/token2022 files the tree also shows) — never an arbitrary
// caller-supplied path — so this can't become a directory-traversal or
// general file-disclosure endpoint.
package web

import (
	"os"
	"path/filepath"

	"github.com/gofiber/fiber/v3"
)

// extraViewableSources lists files outside Examples' own Sources that the
// file tree also displays (see web/static/app.js's buildFileTree).
var extraViewableSources = []string{
	"sdk/token2022/token2022.go",
	"sdk/token2022/instruction.go",
	"sdk/token2022/state.go",
	"sdk/token2022/extension.go",
}

func allowedSourcePaths() map[string]bool {
	allowed := make(map[string]bool)
	for _, example := range Examples {
		for _, path := range example.Sources {
			allowed[path] = true
		}
	}
	for _, path := range extraViewableSources {
		allowed[path] = true
	}
	return allowed
}

func sourceHandler(repoRoot string) fiber.Handler {
	allowed := allowedSourcePaths()
	return func(c fiber.Ctx) error {
		path := c.Query("path")
		if !allowed[path] {
			return c.Status(fiber.StatusNotFound).JSON(errorBody("unknown source path"))
		}
		content, err := os.ReadFile(filepath.Join(repoRoot, path))
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(errorBody(err.Error()))
		}
		c.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
		return c.Send(content)
	}
}
