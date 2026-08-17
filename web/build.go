package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ersanyakit/solanago/compiler"
	sbpfelf "github.com/ersanyakit/solanago/elf"
)

// maxBuildHistory bounds how many past builds each example keeps in memory
// (and therefore how many artifact byte slices it holds onto) — a local
// dev server clicking Build in a loop should never grow unbounded.
const maxBuildHistory = 20

// ErrBuildNotFound is returned when a requested build ID doesn't exist for
// an example (never built, or evicted past maxBuildHistory).
var ErrBuildNotFound = errors.New("web: build not found")

// Builder compiles an Example's guest source straight to a strict sBPFv3
// ELF in-process — the same compiler.CompileFiles -> GenerateSolanaEntrypoint
// -> sbpfelf.BuildV3 chain `go-solana build -target solana` shells out to.
// Every call to Build records a new, distinct BuildRecord — clicking Build
// repeatedly is expected and each click is kept in that example's history,
// selectable and deployable independently — but recompiling is skipped
// whenever the source hasn't changed since the last compile, reusing those
// bytes for the new record instead.
type Builder struct {
	RepoRoot string

	mu        sync.Mutex
	lastBytes map[string]cachedBytes   // example ID -> last compiled artifact, keyed by source hash
	history   map[string][]BuildRecord // example ID -> records, oldest first
	artifacts map[string][]byte        // build ID -> artifact bytes
	counters  map[string]int           // example ID -> next build sequence number
}

type cachedBytes struct {
	sourceSHA256 string
	bytes        []byte
}

// BuildRecord identifies one build of one example, independent of whether
// its bytes were freshly compiled or reused from an unchanged source.
type BuildRecord struct {
	ID           string    `json:"id"`
	SHA256       string    `json:"sha256"`
	SourceSHA256 string    `json:"sourceSha256"`
	SizeBytes    int       `json:"sizeBytes"`
	BuiltAt      time.Time `json:"builtAt"`
}

func NewBuilder(repoRoot string) *Builder {
	return &Builder{
		RepoRoot:  repoRoot,
		lastBytes: make(map[string]cachedBytes),
		history:   make(map[string][]BuildRecord),
		artifacts: make(map[string][]byte),
		counters:  make(map[string]int),
	}
}

// Build compiles example (or reuses the last compiled bytes if its source
// is unchanged) and always appends a new BuildRecord to that example's
// history.
func (b *Builder) Build(example Example) (BuildRecord, error) {
	paths := make([]string, len(example.Sources))
	sourceHash := sha256.New()
	for index, relative := range example.Sources {
		absolute := filepath.Join(b.RepoRoot, relative)
		paths[index] = absolute
		content, err := os.ReadFile(absolute)
		if err != nil {
			return BuildRecord{}, fmt.Errorf("read %s: %w", relative, err)
		}
		sourceHash.Write(content)
		sourceHash.Write([]byte{0}) // file-boundary separator
	}
	sourceSHA256 := hex.EncodeToString(sourceHash.Sum(nil))

	b.mu.Lock()
	cached, ok := b.lastBytes[example.ID]
	b.mu.Unlock()

	artifact := cached.bytes
	if !ok || cached.sourceSHA256 != sourceSHA256 {
		program, err := compiler.CompileFiles(paths)
		if err != nil {
			return BuildRecord{}, fmt.Errorf("compile %s: %w", example.ID, err)
		}
		executable, err := compiler.GenerateSolanaEntrypoint(program, "")
		if err != nil {
			return BuildRecord{}, fmt.Errorf("generate Solana entrypoint for %s: %w", example.ID, err)
		}
		artifact, err = sbpfelf.BuildV3(executable.Bytecode, 0)
		if err != nil {
			return BuildRecord{}, fmt.Errorf("build sBPFv3 ELF for %s: %w", example.ID, err)
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastBytes[example.ID] = cachedBytes{sourceSHA256: sourceSHA256, bytes: artifact}
	b.counters[example.ID]++
	record := BuildRecord{
		ID:           example.ID + "-" + strconv.Itoa(b.counters[example.ID]),
		SHA256:       artifactSHA256(artifact),
		SourceSHA256: sourceSHA256,
		SizeBytes:    len(artifact),
		BuiltAt:      time.Now(),
	}
	b.artifacts[record.ID] = artifact
	history := append(b.history[example.ID], record)
	if len(history) > maxBuildHistory {
		delete(b.artifacts, history[0].ID)
		history = history[len(history)-maxBuildHistory:]
	}
	b.history[example.ID] = history
	return record, nil
}

// History returns exampleID's build records, oldest first.
func (b *Builder) History(exampleID string) []BuildRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]BuildRecord(nil), b.history[exampleID]...)
}

// Artifact returns buildID's bytes, or the most recent build's bytes if
// buildID is empty. It never triggers a compile — callers that want to
// guarantee a build exists should call Build first.
func (b *Builder) Artifact(exampleID, buildID string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if buildID == "" {
		records := b.history[exampleID]
		if len(records) == 0 {
			return nil, ErrBuildNotFound
		}
		buildID = records[len(records)-1].ID
	}
	bytes, ok := b.artifacts[buildID]
	if !ok {
		return nil, ErrBuildNotFound
	}
	return bytes, nil
}

func artifactSHA256(artifact []byte) string {
	sum := sha256.Sum256(artifact)
	return hex.EncodeToString(sum[:])
}
