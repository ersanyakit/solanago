package compiler

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

// ParsedPackage contains the standard Go frontend representation for one or
// more source files compiled together as a single package. The custom IR
// produced by Lower does not retain this AST.
type ParsedPackage struct {
	Filenames []string
	FileSet   *token.FileSet
	Files     []*ast.File
}

// ParseFiles parses one or more Go source files of the same package against
// one shared token.FileSet, so diagnostics and cross-file identifier
// resolution report correct per-file positions. Each file may declare
// package-level functions that call functions defined in any other file in
// the set; imports of external packages remain rejected by Check regardless
// of how many files are compiled together.
func ParseFiles(filenames []string, sources [][]byte) (*ParsedPackage, error) {
	if len(filenames) == 0 {
		return nil, fmt.Errorf("parse: at least one source file is required")
	}
	if len(filenames) != len(sources) {
		return nil, fmt.Errorf("parse: %d filenames but %d sources", len(filenames), len(sources))
	}
	fileSet := token.NewFileSet()
	files := make([]*ast.File, 0, len(filenames))
	resolvedNames := make([]string, 0, len(filenames))
	for index, filename := range filenames {
		resolvedName := filename
		if resolvedName == "" {
			resolvedName = fmt.Sprintf("input%d.go", index)
		}
		file, err := parser.ParseFile(fileSet, resolvedName, sources[index], parser.AllErrors)
		if err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		if file == nil {
			return nil, fmt.Errorf("parse %s: parser returned no syntax tree", resolvedName)
		}
		resolvedNames = append(resolvedNames, resolvedName)
		files = append(files, file)
	}
	return &ParsedPackage{Filenames: resolvedNames, FileSet: fileSet, Files: files}, nil
}

// ParseFile parses exactly one Go source file. It is a convenience wrapper
// around ParseFiles for the common single-file case.
func ParseFile(filename string, source []byte) (*ParsedPackage, error) {
	if filename == "" {
		filename = "input.go"
	}
	return ParseFiles([]string{filename}, [][]byte{source})
}
