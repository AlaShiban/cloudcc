package topology

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/spf13/afero"
)

// File names, relative to the output root.
//
// Two diagrams are written for every application, because they answer two
// different questions. topology.* is what the program declared: a handful of
// capability nodes, which is the picture to reason about. architecture.* is
// what it compiled to: every resource that will exist in the account, which is
// the picture to review before a deploy and the one to hand to somebody asking
// what this costs.
const (
	MermaidFile = "topology.mmd"
	DOTFile     = "topology.dot"

	ArchMermaidFile = "architecture.mmd"
	ArchDOTFile     = "architecture.dot"
)

// Files returns the diagram names for a view.
func (v View) Files() (mermaid, dot string) {
	if v == Resources {
		return ArchMermaidFile, ArchDOTFile
	}
	return MermaidFile, DOTFile
}

// Name is how a view is spelled on the command line.
func (v View) Name() string {
	if v == Resources {
		return "architecture"
	}
	return "topology"
}

// PNGFile is the rendered image's name for an application's declared topology.
func PNGFile(app string) string { return app + ".png" }

// ArchPNGFile is the rendered image of what it compiled to. A separate name so
// the two never overwrite each other.
func ArchPNGFile(app string) string { return app + "-architecture.png" }

// Result reports what was written.
type Result struct {
	Files []string
	// Notice explains why the PNG was skipped, empty when one was written.
	Notice string
}

// Write renders the topology into out. Mermaid and DOT are always written; the
// PNG is attempted only when graphviz is installed, and its absence is a
// notice rather than a failure (D12).
//
// outDir is the real filesystem path backing out, or "" when out is in-memory,
// in which case the PNG step is skipped.
func Write(p *ir.Program, out afero.Fs, outDir string, opts Options) (Result, error) {
	var result Result
	mermaidFile, dotFile := opts.View.Files()

	mmd := Mermaid(p, opts)
	if err := afero.WriteFile(out, mermaidFile, mmd, 0o644); err != nil {
		return result, err
	}
	result.Files = append(result.Files, mermaidFile)

	dot := DOT(p, opts)
	if err := afero.WriteFile(out, dotFile, dot, 0o644); err != nil {
		return result, err
	}
	result.Files = append(result.Files, dotFile)

	if outDir == "" {
		result.Notice = "no PNG rendered: the output is in memory"
		return result, nil
	}
	dotBin, err := exec.LookPath("dot")
	if err != nil {
		result.Notice = "no PNG rendered: graphviz is not installed (brew install graphviz)"
		return result, nil
	}

	png := PNGFile(opts.App)
	if opts.View == Resources {
		png = ArchPNGFile(opts.App)
	}
	cmd := exec.Command(dotBin, "-Tpng", "-o", filepath.Join(outDir, png))
	cmd.Stdin = bytes.NewReader(dot)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// A broken graphviz is still not a reason to fail a compile.
		result.Notice = fmt.Sprintf("no PNG rendered: graphviz failed (%v: %s)", err, trim(stderr.String()))
		return result, nil
	}
	if _, err := os.Stat(filepath.Join(outDir, png)); err != nil {
		result.Notice = "no PNG rendered: graphviz produced no output"
		return result, nil
	}
	result.Files = append(result.Files, png)
	return result, nil
}

func trim(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
