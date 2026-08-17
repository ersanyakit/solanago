package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var staticFiles embed.FS

// staticFS strips the "static" embed prefix so index.html/app.js/style.css
// are served at the site root.
func staticFS() (fs.FS, error) {
	return fs.Sub(staticFiles, "static")
}
