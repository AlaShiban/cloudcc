package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// update regenerates the committed golden trees: go test ./internal/cli -update
var update = flag.Bool("update", false, "rewrite the golden output trees")

// examples are the applications compiled by the golden tests. petstore-multi
// is the load-bearing one: two units sharing a KV store, a static site, and a
// topic with a publisher and a subscriber.
var examples = []string{"petstore", "petstore-multi"}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd)) // internal/cli -> repo root
}

// compileExample runs the CLI exactly as a user would and returns the output
// directory.
func compileExample(t *testing.T, name, outDir string, extraArgs ...string) string {
	t.Helper()
	root := repoRoot(t)
	args := append([]string{filepath.Join(root, "examples", name), "-o", outDir}, extraArgs...)

	cmd := NewRootCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compiling %s failed: %v\nstderr:\n%s", name, err, stderr.String())
	}
	return outDir
}

// snapshot reads a directory tree into path -> content, skipping installed
// dependencies and build artefacts.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(abs string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, abs)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if rel == "node_modules" || rel == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		data, rerr := os.ReadFile(abs)
		if rerr != nil {
			return rerr
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGoldenOutput(t *testing.T) {
	for _, name := range examples {
		t.Run(name, func(t *testing.T) {
			got := snapshot(t, compileExample(t, name, t.TempDir()))
			goldenDir := filepath.Join("testdata", "golden", name)

			if *update {
				if err := os.RemoveAll(goldenDir); err != nil {
					t.Fatal(err)
				}
				for _, rel := range sortedKeys(got) {
					abs := filepath.Join(goldenDir, filepath.FromSlash(rel))
					if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(abs, []byte(got[rel]), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("wrote %d golden files to %s", len(got), goldenDir)
				return
			}

			if _, err := os.Stat(goldenDir); os.IsNotExist(err) {
				t.Fatalf("no golden tree at %s; run: go test ./internal/cli -update", goldenDir)
			}
			want := snapshot(t, goldenDir)

			for _, rel := range sortedKeys(want) {
				gotContent, ok := got[rel]
				if !ok {
					t.Errorf("missing from output: %s", rel)
					continue
				}
				if gotContent != want[rel] {
					t.Errorf("%s differs:\n%s", rel, firstDifference(want[rel], gotContent))
				}
			}
			for _, rel := range sortedKeys(got) {
				if _, ok := want[rel]; !ok {
					t.Errorf("unexpected file in output: %s", rel)
				}
			}
		})
	}
}

// TestCompileIsDeterministic compiles each example twice into different
// directories; the trees must be byte-identical (D18). Golden testing and the
// deploy fingerprint both depend on this.
func TestCompileIsDeterministic(t *testing.T) {
	for _, name := range examples {
		t.Run(name, func(t *testing.T) {
			first := snapshot(t, compileExample(t, name, t.TempDir()))
			second := snapshot(t, compileExample(t, name, t.TempDir()))

			if len(first) != len(second) {
				t.Fatalf("run one produced %d files, run two produced %d", len(first), len(second))
			}
			for _, rel := range sortedKeys(first) {
				if first[rel] != second[rel] {
					t.Errorf("%s is not reproducible:\n%s", rel, firstDifference(first[rel], second[rel]))
				}
			}
		})
	}
}

// TestMultiUnitSharesOneStore is the case Klotho 1 never exercised: two units
// wired to a single table, each with its own environment.
func TestMultiUnitSharesOneStore(t *testing.T) {
	out := compileExample(t, "petstore-multi", t.TempDir())
	index := readFile(t, filepath.Join(out, "index.ts"))

	if strings.Count(index, "new aws.dynamodb.Table(") != 1 {
		t.Errorf("expected exactly one DynamoDB table:\n%s", index)
	}
	for _, unit := range []string{"api", "worker"} {
		envBlock := blockAfter(index, "const "+unit+"Env")
		if !strings.Contains(envBlock, "CC_KV_PETSBYOWNER_TABLE") {
			t.Errorf("unit %q is not wired to the shared table:\n%s", unit, envBlock)
		}
	}
	// Each unit gets its own function and its own role.
	for _, want := range []string{
		`new aws.lambda.Function("api"`,
		`new aws.lambda.Function("worker"`,
		`new aws.iam.Role("api"`,
		`new aws.iam.Role("worker"`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("missing %q in the generated project", want)
		}
	}
}

// TestIAMIsLeastPrivilege pins the policy derivation: a unit may only reach
// the stores its code declares, scoped to those resources.
func TestIAMIsLeastPrivilege(t *testing.T) {
	out := compileExample(t, "petstore-multi", t.TempDir())
	index := readFile(t, filepath.Join(out, "index.ts"))

	apiPolicy := blockAfter(index, `new aws.iam.RolePolicy("api"`)
	workerPolicy := blockAfter(index, `new aws.iam.RolePolicy("worker"`)

	// Only the worker writes to the audit bucket, so only the worker may.
	if strings.Contains(apiPolicy, "petAuditBucket") {
		t.Errorf("the api unit was granted access to a bucket it never declares:\n%s", apiPolicy)
	}
	if !strings.Contains(workerPolicy, "petAuditBucket") {
		t.Errorf("the worker unit is missing access to the bucket it writes to:\n%s", workerPolicy)
	}
	// Only the api publishes, so only the api gets sns:Publish.
	if !strings.Contains(apiPolicy, "sns:Publish") {
		t.Errorf("the publisher is missing sns:Publish:\n%s", apiPolicy)
	}
	if strings.Contains(workerPolicy, "sns:Publish") {
		t.Errorf("a subscriber should not be granted sns:Publish:\n%s", workerPolicy)
	}
	// Both share the table, so both get table access, scoped to that table.
	for name, policy := range map[string]string{"api": apiPolicy, "worker": workerPolicy} {
		if !strings.Contains(policy, "dynamodb:GetItem") {
			t.Errorf("unit %q is missing table access:\n%s", name, policy)
		}
		if !strings.Contains(policy, "petsByOwnerTable.arn") {
			t.Errorf("unit %q's table grant is not scoped to the table ARN:\n%s", name, policy)
		}
		if strings.Contains(policy, `"*"`) {
			t.Errorf("unit %q has a wildcard resource grant:\n%s", name, policy)
		}
	}
}

// TestStaticAssetsNeverEnterAComputeBundle pins the ordering that makes
// static-units run before exec-units.
func TestStaticAssetsNeverEnterAComputeBundle(t *testing.T) {
	out := compileExample(t, "petstore-multi", t.TempDir())
	for _, unit := range []string{"api", "worker"} {
		if _, err := os.Stat(filepath.Join(out, unit, "public", "index.html")); err == nil {
			t.Errorf("a claimed static asset ended up inside the %q bundle", unit)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "static", "petstore-site", "index.html")); err != nil {
		t.Errorf("the static site was not written: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// blockAfter returns the text from marker up to the closing "});" line, which
// is enough to inspect a single generated resource.
func blockAfter(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i:]
	if j := strings.Index(rest, "\n});"); j >= 0 {
		return rest[:j+4]
	}
	if j := strings.Index(rest, "\n};"); j >= 0 {
		return rest[:j+3]
	}
	return rest
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// firstDifference renders the first differing line with a little context, so a
// golden failure points at the change instead of dumping two whole files.
func firstDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		w, g := "", ""
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w == g {
			continue
		}
		var b strings.Builder
		for j := max(0, i-3); j < i; j++ {
			b.WriteString("   " + wantLines[j] + "\n")
		}
		b.WriteString("  -" + w + "\n")
		b.WriteString("  +" + g + "\n")
		b.WriteString("  (line " + itoa(i+1) + "; run with -update to accept)")
		return b.String()
	}
	return "(files differ only in trailing content)"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
