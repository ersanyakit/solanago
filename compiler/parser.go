package compiler

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

// ParsedFile contains the standard Go frontend representation. The custom IR
// produced by Lower does not retain this AST.
type ParsedFile struct {
	Filename string
	FileSet  *token.FileSet
	AST      *ast.File
}

// ParseFile parses exactly one Go source file. The MVP intentionally compiles a
// single, self-contained file so imports and cross-file declarations cannot be
// accepted accidentally.
func ParseFile(filename string, source []byte) (*ParsedFile, error) {
	if filename == "" {
		filename = "input.go"
	}
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, filename, source, parser.AllErrors)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if file == nil {
		return nil, fmt.Errorf("parse %s: parser returned no syntax tree", filename)
	}
	return &ParsedFile{Filename: filename, FileSet: fileSet, AST: file}, nil
}
