// Command genprogram writes one generated program to a directory, along with a
// manifest describing how to run it.
//
// It exists so the differential harness -- which is orchestration, and reads
// better as a shell script -- can use the same generator the Go tests do,
// rather than a second implementation that would drift from it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cloudcompiler/cloudcc/internal/fuzz"
)

// manifest is what the harness needs to drive a program.
type manifest struct {
	Seed        int64       `json:"seed"`
	Language    string      `json:"language"`
	App         string      `json:"app"`
	Unit        string      `json:"unit"`
	EntryModule string      `json:"entry_module"`
	AppVar      string      `json:"app_var"`
	Store       string      `json:"store"`
	Scenario    []fuzz.Step `json:"scenario"`
}

func main() {
	seed := flag.Int64("seed", 1, "generator seed")
	out := flag.String("out", "", "directory to write the program into")
	language := flag.String("lang", "python", "language to generate: python or node")
	flag.Parse()

	if *out == "" {
		fail("-out is required")
	}

	var p *fuzz.Program
	switch *language {
	case "python":
		p = fuzz.Generate(*seed, fuzz.Options{Behavioural: true})
	case "node":
		p = fuzz.GenerateNode(*seed, fuzz.Options{Behavioural: true})
	default:
		fail("-lang must be python or node, not %q", *language)
	}

	for rel, content := range p.Files {
		abs := filepath.Join(*out, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			fail("%v", err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			fail("%v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(*out, "cloudcc.yaml"), []byte(p.Config), 0o644); err != nil {
		fail("%v", err)
	}

	data, err := json.MarshalIndent(manifest{
		Seed:        p.Seed,
		Language:    *language,
		App:         p.Name,
		Unit:        p.PrimaryUnit,
		EntryModule: p.EntryModule,
		AppVar:      p.AppVar,
		Store:       p.PrimaryStore,
		Scenario:    p.Scenario,
	}, "", "  ")
	if err != nil {
		fail("%v", err)
	}
	fmt.Println(string(data))
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "genprogram: "+format+"\n", args...)
	os.Exit(1)
}
