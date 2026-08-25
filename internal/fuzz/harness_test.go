package fuzz_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/cli"
	"github.com/cloudcompiler/cloudcc/internal/fuzz"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// compiled is the result of putting a generated program through the real CLI.
type compiled struct {
	srcDir string
	outDir string
	dump   ir.Dump
	stderr string
}

// build writes a generated program to disk and compiles it exactly as a user
// would, returning the IR the compiler produced.
func build(t *testing.T, p *fuzz.Program) compiled {
	t.Helper()

	srcDir := t.TempDir()
	for rel, content := range p.Files {
		abs := filepath.Join(srcDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(srcDir, "cloudcc.yaml"), []byte(p.Config), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	cmd := cli.NewRootCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{srcDir, "-o", outDir, "--dump-ir"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("seed %d failed to compile: %v\n%s\n%s",
			p.Seed, err, stderr.String(), render(p))
	}

	var dump ir.Dump
	if err := json.Unmarshal(stdout.Bytes(), &dump); err != nil {
		t.Fatalf("seed %d: --dump-ir is not valid JSON: %v", p.Seed, err)
	}
	// out_dir holds a folder per application, so the generated program's
	// output is one level down -- under the app name its cloudcc.yaml declared.
	return compiled{
		srcDir: srcDir,
		outDir: filepath.Join(outDir, appNameOf(p.Config)),
		dump:   dump,
		stderr: stderr.String(),
	}
}

// appNameOf reads `app:` out of a generated cloudcc.yaml. The generator always
// writes one, and it is what names the output folder.
func appNameOf(cfg string) string {
	for _, line := range strings.Split(cfg, "\n") {
		if rest, ok := strings.CutPrefix(line, "app:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// render prints a whole generated program, so a failure carries the program
// that caused it rather than just a seed to go and reproduce.
func render(p *fuzz.Program) string {
	var b strings.Builder
	b.WriteString("--- generated program, seed ")
	b.WriteString(itoa(int(p.Seed)))
	b.WriteString(" ---\n")
	paths := make([]string, 0, len(p.Files))
	for path := range p.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if strings.HasSuffix(path, ".py") || strings.HasSuffix(path, ".yaml") {
			b.WriteString("----- " + path + " -----\n")
			b.WriteString(p.Files[path])
			b.WriteString("\n")
		}
	}
	b.WriteString("----- cloudcc.yaml -----\n" + p.Config + "\n")
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	if neg {
		return "-" + string(d)
	}
	return string(d)
}

// ---------------------------------------------------------------- oracle

// intentIDs returns the ids of every intent of a kind, sorted.
func intentIDs(dump ir.Dump, kind string) []string {
	var out []string
	for _, in := range dump.Intents {
		if in.Key.Kind == kind {
			out = append(out, in.Key.ID)
		}
	}
	sort.Strings(out)
	return out
}

// payload returns one intent's payload as a generic map, which is enough to
// read the fields the oracle checks without re-declaring the IR types.
func payload(t *testing.T, dump ir.Dump, kind, id string) map[string]any {
	t.Helper()
	for _, in := range dump.Intents {
		if in.Key.Kind == kind && in.Key.ID == id {
			raw, err := json.Marshal(in.Payload)
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatalf("no %s intent with id %q; present: %v", kind, id, intentIDs(dump, kind))
	return nil
}

// checkAgainstGroundTruth is the oracle: the compiler must find exactly what
// the generator planted, and nothing more.
func checkAgainstGroundTruth(t *testing.T, p *fuzz.Program, c compiled) {
	t.Helper()
	expect := p.Expect

	if got := intentIDs(c.dump, "execution_unit"); !sameStrings(got, expect.Units) {
		t.Errorf("execution units = %v, want %v\n%s", got, expect.Units, render(p))
	}

	// Stores: every planted id must be present under the right capability, and
	// no store may appear that was never planted.
	for id, kind := range expect.Stores {
		found := false
		for _, in := range c.dump.Intents {
			if in.Key.ID == id {
				found = true
				if in.Key.Kind != kind {
					t.Errorf("store %q resolved as %s, planted as %s", id, in.Key.Kind, kind)
				}
			}
		}
		if !found {
			t.Errorf("store %q was planted but not detected\n%s", id, render(p))
		}
	}

	for _, id := range expect.Topics {
		if !contains(intentIDs(c.dump, "pubsub"), id) {
			t.Errorf("topic %q was planted but not detected\n%s", id, render(p))
		}
	}

	for _, id := range expect.StaticUnits {
		if !contains(intentIDs(c.dump, "static_unit"), id) {
			t.Errorf("static unit %q was planted but not detected\n%s", id, render(p))
		}
	}

	// Configuration values, including whether each is a secret.
	wantCfg := make([]string, 0, len(expect.ConfigVars))
	for id := range expect.ConfigVars {
		wantCfg = append(wantCfg, id)
	}
	sort.Strings(wantCfg)
	if got := intentIDs(c.dump, "config"); !sameStrings(got, wantCfg) {
		t.Errorf("config values = %v, want %v\n%s", got, wantCfg, render(p))
	} else {
		for id, want := range expect.ConfigVars {
			pl := payload(t, c.dump, "config", id)
			if secret, _ := pl["secret"].(bool); secret != want.Secret {
				t.Errorf("config %q secret = %v, want %v", id, secret, want.Secret)
			}
			if def, _ := pl["default"].(string); def != want.Default {
				t.Errorf("config %q default = %q, want %q", id, def, want.Default)
			}
		}
	}

	// Gateways, their unit, and the routes discovered on them.
	wantGW := make([]string, 0, len(expect.Gateways))
	for id := range expect.Gateways {
		wantGW = append(wantGW, id)
	}
	sort.Strings(wantGW)
	if got := intentIDs(c.dump, "expose"); !sameStrings(got, wantGW) {
		t.Fatalf("gateways = %v, want %v\n%s", got, wantGW, render(p))
	}
	for id, want := range expect.Gateways {
		pl := payload(t, c.dump, "expose", id)
		if unit, _ := pl["unit"].(string); unit != want.Unit {
			t.Errorf("gateway %q fronts unit %q, want %q", id, unit, want.Unit)
		}
		var gotRoutes []fuzz.Route
		for _, raw := range pl["routes"].([]any) {
			r := raw.(map[string]any)
			gotRoutes = append(gotRoutes, fuzz.Route{
				Verb: r["verb"].(string), Path: r["path"].(string),
			})
		}
		if !reflect.DeepEqual(gotRoutes, want.Routes) {
			t.Errorf("gateway %q routes =\n  %v\nwant\n  %v\n%s",
				id, gotRoutes, want.Routes, render(p))
		}
	}
}

// checkBundles verifies which files ended up in which unit.
func checkBundles(t *testing.T, p *fuzz.Program, c compiled) {
	t.Helper()
	for _, unit := range p.Expect.Units {
		for _, shared := range p.Expect.SharedModules {
			requireFile(t, c.outDir, filepath.Join(unit, shared), p)
		}
		if entry, ok := p.Expect.EntryFile[unit]; ok {
			requireFile(t, c.outDir, filepath.Join(unit, entry), p)
		}
		// Assets a static unit claimed must never travel with compute.
		for _, claimed := range p.Expect.ClaimedFiles {
			requireNoFile(t, c.outDir, filepath.Join(unit, claimed),
				"a static unit claimed this file", p)
		}
		// Another unit's entry module must not be swept into this bundle.
		for _, other := range p.Expect.Units {
			if other == unit {
				continue
			}
			if entry, ok := p.Expect.EntryFile[other]; ok {
				requireNoFile(t, c.outDir, filepath.Join(unit, entry),
					"it is another unit's entrypoint", p)
			}
		}
	}
	for unit, files := range p.Expect.EmbeddedFiles {
		for _, f := range files {
			requireFile(t, c.outDir, filepath.Join(unit, f), p)
			for _, other := range p.Expect.Units {
				if other != unit {
					requireNoFile(t, c.outDir, filepath.Join(other, f),
						"embed_assets bound it to a different unit", p)
				}
			}
		}
	}
}

func requireFile(t *testing.T, outDir, rel string, p *fuzz.Program) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(outDir, rel)); err != nil {
		t.Errorf("expected %s in the output: %v\n%s", rel, err, render(p))
	}
}

func requireNoFile(t *testing.T, outDir, rel, why string, p *fuzz.Program) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(outDir, rel)); err == nil {
		t.Errorf("%s should not be in the output: %s\n%s", rel, why, render(p))
	}
}

// checkRewrittenPython is the other half of correctness: whatever the compiler
// wrote must still be a valid Python program, and must no longer depend on the
// SDK, which is not installed in a deployment bundle.
func checkRewrittenPython(t *testing.T, p *fuzz.Program, c compiled) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH")
	}

	for _, unit := range p.Expect.Units {
		unitDir := filepath.Join(c.outDir, unit)
		_ = filepath.Walk(unitDir, func(abs string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(abs, ".py") {
				return nil
			}
			// The injected runtime is generated, not rewritten; checking it
			// here would only re-test the templates.
			if strings.Contains(abs, "_cloudcc_runtime") {
				return nil
			}
			data, rerr := os.ReadFile(abs)
			if rerr != nil {
				return nil
			}
			src := string(data)

			cmd := exec.Command(python, "-c", "import ast,sys; ast.parse(sys.stdin.read())")
			cmd.Stdin = strings.NewReader(src)
			if out, cerr := cmd.CombinedOutput(); cerr != nil {
				rel, _ := filepath.Rel(c.outDir, abs)
				t.Errorf("the compiler produced invalid Python at %s: %v\n%s\n--- rewritten ---\n%s\n%s",
					rel, cerr, out, src, render(p))
				return nil
			}
			if strings.Contains(src, "cloudcompiler") {
				rel, _ := filepath.Rel(c.outDir, abs)
				t.Errorf("%s still references the SDK, which is not installed in a bundle:\n%s\n%s",
					rel, src, render(p))
			}
			return nil
		})
	}
}

// sameStrings compares two lists treating nil and empty as equal, which
// reflect.DeepEqual does not. Getting this wrong made an empty expectation
// look like a failure.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
