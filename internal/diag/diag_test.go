package diag

import (
	"strings"
	"testing"
)

func TestFormat(t *testing.T) {
	d := Diagnostic{
		Severity:   SevError,
		Pos:        Position{File: "app.py", Line: 4, Col: 8},
		Capability: "persist_kv",
		Message:    "id must be a string literal",
	}
	want := "app.py:4:8: error: persist_kv: id must be a string literal"
	if got := d.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestFormatWithoutPosition(t *testing.T) {
	d := Diagnostic{Severity: SevWarning, Capability: "expose", Message: "no routes found"}
	if got, want := d.String(), "warning: expose: no routes found"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStrictPromotesWarnings(t *testing.T) {
	var d Diagnostics
	d.Warnf(Position{File: "a.py", Line: 1, Col: 1}, "expose", "APIRouter is not detected")
	if d.HasErrors() {
		t.Error("a warning should not be an error without --strict")
	}

	var s Diagnostics
	s.Strict = true
	s.Warnf(Position{File: "a.py", Line: 1, Col: 1}, "expose", "APIRouter is not detected")
	if !s.HasErrors() {
		t.Error("--strict should promote warnings to errors")
	}
}

func TestDeduplicates(t *testing.T) {
	var d Diagnostics
	for i := 0; i < 5; i++ {
		d.Errorf(Position{File: "a.py", Line: 1, Col: 1}, "persist_kv", "boom")
	}
	if got := len(d.Items()); got != 1 {
		t.Errorf("got %d diagnostics, want 1 after de-duplication", got)
	}
}

func TestCapsAtMax(t *testing.T) {
	var d Diagnostics
	for i := 0; i < MaxDiagnostics*2; i++ {
		d.Errorf(Position{File: "a.py", Line: i + 1, Col: 1}, "persist_kv", "boom %d", i)
	}
	if got := len(d.Items()); got != MaxDiagnostics {
		t.Errorf("got %d diagnostics, want the cap of %d", got, MaxDiagnostics)
	}
	if !d.Truncated {
		t.Error("Truncated should be set once the cap is hit")
	}
}

func TestItemsSortedByPosition(t *testing.T) {
	var d Diagnostics
	d.Errorf(Position{File: "b.py", Line: 1, Col: 1}, "x", "third")
	d.Errorf(Position{File: "a.py", Line: 9, Col: 1}, "x", "second")
	d.Errorf(Position{File: "a.py", Line: 2, Col: 5}, "x", "first")

	var got []string
	for _, it := range d.Items() {
		got = append(got, it.Message)
	}
	want := "first second third"
	if strings.Join(got, " ") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
}

func TestErrorCount(t *testing.T) {
	var d Diagnostics
	d.Warnf(Position{File: "a.py", Line: 1, Col: 1}, "x", "warn")
	d.Errorf(Position{File: "a.py", Line: 2, Col: 1}, "x", "err")
	if got := d.ErrorCount(); got != 1 {
		t.Errorf("ErrorCount = %d, want 1", got)
	}
}
