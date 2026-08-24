// Package diag accumulates compiler diagnostics.
//
// Diagnostics are accumulated rather than fatal (D20): a detection or
// validation pass that aborts on the first problem forces the user through one
// round-trip per mistake. Everything is reported in one go, capped, sorted by
// position, and formatted as file:line:col: severity: capability: message.
package diag

import (
	"fmt"
	"sort"
	"strings"
)

// Severity classifies a diagnostic. Warnings become errors under --strict.
type Severity int

const (
	SevWarning Severity = iota
	SevError
)

func (s Severity) String() string {
	if s == SevError {
		return "error"
	}
	return "warning"
}

// MaxDiagnostics caps accumulation so a pathological input cannot flood the
// terminal (D20).
const MaxDiagnostics = 50

// Position is a 1-based source location. Line 0 means "no position known".
type Position struct {
	File string
	Line int
	Col  int
}

func (p Position) String() string {
	if p.File == "" {
		return ""
	}
	if p.Line == 0 {
		return p.File
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

// Diagnostic is one accumulated compiler message.
type Diagnostic struct {
	Severity   Severity
	Pos        Position
	Capability string
	Message    string
}

// String renders the documented format: file:line:col: capability: message.
func (d Diagnostic) String() string {
	var b strings.Builder
	if p := d.Pos.String(); p != "" {
		b.WriteString(p)
		b.WriteString(": ")
	}
	b.WriteString(d.Severity.String())
	b.WriteString(": ")
	if d.Capability != "" {
		b.WriteString(d.Capability)
		b.WriteString(": ")
	}
	b.WriteString(d.Message)
	return b.String()
}

// Diagnostics is an accumulating, de-duplicating diagnostic sink.
type Diagnostics struct {
	items     []Diagnostic
	seen      map[string]bool
	Truncated bool
	// Strict promotes every warning to an error (--strict).
	Strict bool
}

// Add records a diagnostic, ignoring exact duplicates and stopping at
// MaxDiagnostics.
func (d *Diagnostics) Add(diag Diagnostic) {
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	key := diag.String()
	if d.seen[key] {
		return
	}
	if len(d.items) >= MaxDiagnostics {
		d.Truncated = true
		return
	}
	d.seen[key] = true
	d.items = append(d.items, diag)
}

// Errorf records an error diagnostic.
func (d *Diagnostics) Errorf(pos Position, capability, format string, args ...any) {
	d.Add(Diagnostic{SevError, pos, capability, fmt.Sprintf(format, args...)})
}

// Warnf records a warning, promoted to an error under --strict.
func (d *Diagnostics) Warnf(pos Position, capability, format string, args ...any) {
	sev := SevWarning
	if d.Strict {
		sev = SevError
	}
	d.Add(Diagnostic{sev, pos, capability, fmt.Sprintf(format, args...)})
}

// Items returns the diagnostics sorted by position then message, so output
// does not depend on plugin execution order.
func (d *Diagnostics) Items() []Diagnostic {
	out := append([]Diagnostic(nil), d.items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Pos.File != b.Pos.File {
			return a.Pos.File < b.Pos.File
		}
		if a.Pos.Line != b.Pos.Line {
			return a.Pos.Line < b.Pos.Line
		}
		if a.Pos.Col != b.Pos.Col {
			return a.Pos.Col < b.Pos.Col
		}
		return a.String() < b.String()
	})
	return out
}

// HasErrors reports whether any accumulated diagnostic is an error.
func (d *Diagnostics) HasErrors() bool {
	for _, it := range d.items {
		if it.Severity == SevError {
			return true
		}
	}
	return false
}

// ErrorCount returns the number of error-severity diagnostics.
func (d *Diagnostics) ErrorCount() int {
	n := 0
	for _, it := range d.items {
		if it.Severity == SevError {
			n++
		}
	}
	return n
}
