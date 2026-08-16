package compiler

import (
	"fmt"
	"os"
)

// CompileSource runs parse, subset/type checking, and IR lowering.
func CompileSource(filename string, source []byte) (*Program, error) {
	parsed, err := ParseFile(filename, source)
	if err != nil {
		return nil, err
	}
	checked, err := Check(parsed)
	if err != nil {
		return nil, err
	}
	return Lower(checked)
}

// CompileFile is the filesystem convenience API used by the CLI.
func CompileFile(filename string) (*Program, error) {
	source, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}
	return CompileSource(filename, source)
}
