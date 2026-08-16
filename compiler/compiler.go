package compiler

import (
	"fmt"
	"os"
)

// CompileSource runs parse, subset/type checking, and IR lowering for one
// in-memory source file. It is a convenience wrapper around CompileSources
// for the common single-file case.
func CompileSource(filename string, source []byte) (*Program, error) {
	return CompileSources([]string{filename}, [][]byte{source})
}

// CompileSources runs parse, subset/type checking, and IR lowering across
// one or more in-memory source files compiled together as a single package.
// A function in one file may call a function defined in any other file in
// the set; imports of external packages remain rejected regardless of how
// many files are compiled together.
func CompileSources(filenames []string, sources [][]byte) (*Program, error) {
	parsed, err := ParseFiles(filenames, sources)
	if err != nil {
		return nil, err
	}
	checked, err := Check(parsed)
	if err != nil {
		return nil, err
	}
	return Lower(checked)
}

// CompileFile is the filesystem convenience API used by the CLI for the
// common single-file case.
func CompileFile(filename string) (*Program, error) {
	return CompileFiles([]string{filename})
}

// CompileFiles is the filesystem convenience API used by the CLI to compile
// several same-package source files together.
func CompileFiles(filenames []string) (*Program, error) {
	sources := make([][]byte, len(filenames))
	for index, filename := range filenames {
		source, err := os.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filename, err)
		}
		sources[index] = source
	}
	return CompileSources(filenames, sources)
}
