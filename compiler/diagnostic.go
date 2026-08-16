package compiler

import (
	"fmt"
	"strings"
)

// SourcePosition is deliberately independent of go/ast. It can be retained in
// the IR and consumed by later compiler stages without importing the frontend.
type SourcePosition struct {
	Filename string
	Line     int
	Column   int
}

func (p SourcePosition) String() string {
	switch {
	case p.Filename != "" && p.Line > 0:
		return fmt.Sprintf("%s:%d:%d", p.Filename, p.Line, p.Column)
	case p.Filename != "":
		return p.Filename
	case p.Line > 0:
		return fmt.Sprintf("%d:%d", p.Line, p.Column)
	default:
		return ""
	}
}

// Diagnostic is one actionable compiler error.
type Diagnostic struct {
	Phase    string
	Position SourcePosition
	Message  string
}

func (d Diagnostic) String() string {
	prefix := d.Phase
	if pos := d.Position.String(); pos != "" {
		if prefix != "" {
			prefix += " "
		}
		prefix += pos
	}
	if prefix == "" {
		return d.Message
	}
	return prefix + ": " + d.Message
}

// DiagnosticError reports all errors discovered in one compiler phase.
// Callers can use errors.As to inspect the individual diagnostics.
type DiagnosticError struct {
	Diagnostics []Diagnostic
}

func (e *DiagnosticError) Error() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return "compiler error"
	}
	parts := make([]string, len(e.Diagnostics))
	for i, diagnostic := range e.Diagnostics {
		parts[i] = diagnostic.String()
	}
	return strings.Join(parts, "\n")
}

func diagnosticsError(diagnostics []Diagnostic) error {
	if len(diagnostics) == 0 {
		return nil
	}
	return &DiagnosticError{Diagnostics: diagnostics}
}
