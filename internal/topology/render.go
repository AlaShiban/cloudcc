package topology

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cloudcompiler/cc/internal/ir"
	"github.com/spf13/afero"
)

// File names, relative to the output root.
const (
	MermaidFile = "topology.mmd"
	DOTFile     = "topology.dot"
)

// PNGFile is the rendered image's name for an application.
func PNGFile(app string) string { return app + ".png" }

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

	mmd := Mermaid(p, opts)
	if err := afero.WriteFile(out, MermaidFile, mmd, 0o644); err != nil {
		return result, err
	}
	result.Files = append(result.Files, MermaidFile)

	dot := DOT(p, opts)
	if err := afero.WriteFile(out, DOTFile, dot, 0o644); err != nil {
		return result, err
	}
	result.Files = append(result.Files, DOTFile)

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
