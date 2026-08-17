// Command gosbf-web serves the browser deploy UI: one tab per compiled
// example program, Phantom wallet connect, and a Phantom-signed BPF
// Upgradeable Loader v3 deploy flow. Run it from the repository root (or
// pass -repo):
//
//	go run ./cmd/gosbf-web
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/ersanyakit/solanago/web"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	repoRoot := flag.String("repo", ".", "repository root (must contain examples/)")
	flag.Parse()

	app := web.NewServer(web.Config{RepoRoot: *repoRoot})
	fmt.Printf("gosbf-web listening on %s (repo root %s)\n", *addr, *repoRoot)
	log.Fatal(app.Listen(*addr))
}
