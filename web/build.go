package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ersanyakit/solanago/compiler"
	sbpfelf "github.com/ersanyakit/solanago/elf"
)

// Builder compiles an Example's guest source straight to a strict sBPFv3
// ELF in-process — the same compiler.CompileFiles -> GenerateSolanaEntrypoint
// -> sbpfelf.BuildV3 chain `go-solana build -target solana` shells out to —
// and caches the result keyed by the source's own content hash, so editing
// a testdata/program.go and rebuilding never serves a stale artifact.
type Builder struct {
	RepoRoot string

	mu    sync.Mutex
	cache map[string]buildResult // example ID -> last build
}

type buildResult struct {
	sourceSHA256 string
	artifact     []byte
}

// Artifact is a built program's bytes plus its content-addressed identity.
type Artifact struct {
	Bytes        []byte
	SHA256       string
	SourceSHA256 string
}

func NewBuilder(repoRoot string) *Builder {
	return &Builder{RepoRoot: repoRoot, cache: make(map[string]buildResult)}
}

// Build compiles example, reusing a cached artifact whenever its source
// files' combined hash is unchanged since the last build.
func (b *Builder) Build(example Example) (Artifact, error) {
	paths := make([]string, len(example.Sources))
	sourceHash := sha256.New()
	for index, relative := range example.Sources {
		absolute := filepath.Join(b.RepoRoot, relative)
		paths[index] = absolute
		content, err := os.ReadFile(absolute)
		if err != nil {
			return Artifact{}, fmt.Errorf("read %s: %w", relative, err)
		}
		sourceHash.Write(content)
		sourceHash.Write([]byte{0}) // file-boundary separator
	}
	sourceSHA256 := hex.EncodeToString(sourceHash.Sum(nil))

	b.mu.Lock()
	cached, ok := b.cache[example.ID]
	b.mu.Unlock()
	if ok && cached.sourceSHA256 == sourceSHA256 {
		return Artifact{Bytes: cached.artifact, SHA256: artifactSHA256(cached.artifact), SourceSHA256: sourceSHA256}, nil
	}

	program, err := compiler.CompileFiles(paths)
	if err != nil {
		return Artifact{}, fmt.Errorf("compile %s: %w", example.ID, err)
	}
	executable, err := compiler.GenerateSolanaEntrypoint(program, "")
	if err != nil {
		return Artifact{}, fmt.Errorf("generate Solana entrypoint for %s: %w", example.ID, err)
	}
	artifact, err := sbpfelf.BuildV3(executable.Bytecode, 0)
	if err != nil {
		return Artifact{}, fmt.Errorf("build sBPFv3 ELF for %s: %w", example.ID, err)
	}

	b.mu.Lock()
	b.cache[example.ID] = buildResult{sourceSHA256: sourceSHA256, artifact: artifact}
	b.mu.Unlock()
	return Artifact{Bytes: artifact, SHA256: artifactSHA256(artifact), SourceSHA256: sourceSHA256}, nil
}

func artifactSHA256(artifact []byte) string {
	sum := sha256.Sum256(artifact)
	return hex.EncodeToString(sum[:])
}
