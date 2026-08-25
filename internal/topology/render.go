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

// ArchPNGFile is the architecture picture: the diagrams render, with the
// provider's own icons.
func ArchPNGFile(app string) string { return app + "-architecture.png" }

// ResourcePNGFile is graphviz's render of the exhaustive resource graph.
//
// It has its own name because it is a different picture, and one name for two
// pictures is a trap. When diagrams is not installed the -architecture.png
// simply does not exist, which is visible; if the fallback wrote to that name
// instead, the file would be there and would quietly be a dependency graph --
// and someone would review it believing they were looking at the architecture.
// This project refuses silent substitutions elsewhere (D9, D15) and a diagram
// is not an exception.
func ResourcePNGFile(app string) string { return app + "-resources.png" }

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

	// Diagram as code, in the notation the ecosystem already uses for this,
	// and the only one of the three that renders with the provider's own icons.
	// Written for the resource layer only: the capability layer has no icons to
	// draw with, and inventing some would claim something the program never
	// said.
	if opts.View == Resources {
		if err := afero.WriteFile(out, PythonFile, Python(p, opts), 0o644); err != nil {
			return result, err
		}
		result.Files = append(result.Files, PythonFile)
	}

	if outDir == "" {
		result.Notice = "no PNG rendered: the output is in memory"
		return result, nil
	}
	if opts.View == Resources {
		if notice := renderWithDiagrams(outDir); notice != "" {
			result.Notice = notice
		} else {
			result.Files = append(result.Files, ArchPNGFile(opts.App))
		}
		// Either way, graphviz still renders the exhaustive graph below, under
		// its own name.
	}

	dotBin, err := exec.LookPath("dot")
	if err != nil {
		if result.Notice != "" {
			return result, nil
		}
		result.Notice = "no PNG rendered: graphviz is not installed (brew install graphviz)"
		return result, nil
	}

	png := PNGFile(opts.App)
	if opts.View == Resources {
		png = ResourcePNGFile(opts.App)
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

// renderWithDiagrams runs the generated program, returning a notice when it
// could not.
//
// Only an interpreter that already has the package is used. Downloading thirty
// megabytes in the middle of a compile, to produce a picture nobody asked for
// on this run, is not a thing a compiler should do quietly -- so a missing
// package is a one-line note saying how to get the image, exactly as a missing
// graphviz has always been (D12).
func renderWithDiagrams(outDir string) string {
	python, err := exec.LookPath("python3")
	if err != nil {
		return "no icon diagram rendered: python3 is not installed"
	}
	if err := exec.Command(python, "-c", "import diagrams").Run(); err != nil {
		return "no icon diagram rendered: the diagrams package is not installed " +
			"(pip install diagrams); " + PythonFile + " was still written"
	}
	cmd := exec.Command(python, PythonFile)
	cmd.Dir = outDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Almost always a missing graphviz, which diagrams needs too.
		return fmt.Sprintf("no icon diagram rendered: %s failed (%v: %s)",
			PythonFile, err, trim(stderr.String()))
	}
	return ""
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
