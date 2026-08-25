package fuzz_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/fuzz"
)

// NodeCorpusSeeds are the Node programs committed as a permanent regression
// corpus, chosen the same way the Python ones were: by sweeping and keeping the
// seeds that between them cover every generated shape.
//
// Each is reproducible: `go test ./internal/fuzz -run 'TestNodeCorpus/seed-N'`.
var NodeCorpusSeeds = []int64{
	1, 2, 3, 5, 7, 11, 13, 17, 19, 23,
	29, 31, 37, 41, 43, 47, 53, 59, 61, 67,
	// Added for the relational clients: 10 is the first seed writing a pg
	// Pool, 15 the first writing a Postgres URL, 45 the first writing a MySQL
	// one. TestNodeCorpusCoversEveryShape is what says whether this list is
	// still complete.
	10, 15, 45,
	// Added when the SDK's KVStore and FileStore were replaced by the AWS
	// SDK's own clients. Removing two client shapes moves every later draw
	// from the generator's rng, so seeds that used to cover `new Redis(`
	// stopped doing so -- caught by the coverage test rather than leaving a
	// detector path quietly untested.
	4, 8, 18,
}

// TestNodeCorpus is the Node half of the permanent corpus: fixed programs,
// compiled and checked against their own ground truth on every run.
//
// It shares the oracle with the Python corpus. That is the point of the
// generator producing one Program type for both languages -- what a correct
// compile must produce is a property of the program, not of the syntax it
// happens to be written in.
func TestNodeCorpus(t *testing.T) {
	for _, seed := range NodeCorpusSeeds {
		t.Run("seed-"+strconv.FormatInt(seed, 10), func(t *testing.T) {
			p := fuzz.GenerateNode(seed, fuzz.Options{})
			c := build(t, p)
			checkAgainstGroundTruth(t, p, c)
			checkBundles(t, p, c)
			checkRewrittenNode(t, p, c)
		})
	}
}

// TestNodeSweep runs a wider range on demand, the way new Python seeds were
// found.
//
//	CLOUDCC_FUZZ_SEEDS=200 go test ./internal/fuzz -run TestNodeSweep
func TestNodeSweep(t *testing.T) {
	count := 0
	if v := os.Getenv("CLOUDCC_FUZZ_SEEDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("CLOUDCC_FUZZ_SEEDS=%q is not a number", v)
		}
		count = n
	}
	if count == 0 {
		t.Skip("set CLOUDCC_FUZZ_SEEDS=N to sweep N seeds")
	}

	start := int64(1)
	if v := os.Getenv("CLOUDCC_FUZZ_START"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("CLOUDCC_FUZZ_START=%q is not a number", v)
		}
		start = n
	}

	for seed := start; seed < start+int64(count); seed++ {
		t.Run("seed-"+strconv.FormatInt(seed, 10), func(t *testing.T) {
			p := fuzz.GenerateNode(seed, fuzz.Options{})
			c := build(t, p)
			checkAgainstGroundTruth(t, p, c)
			checkBundles(t, p, c)
			checkRewrittenNode(t, p, c)
		})
	}
}

// TestNodeGenerationIsReproducible pins the property the corpus depends on: a
// seed names a program. The Python generator once quietly lost this by
// iterating a map, which consumed rng draws out of order.
func TestNodeGenerationIsReproducible(t *testing.T) {
	for _, seed := range []int64{1, 7, 42} {
		a := fuzz.GenerateNode(seed, fuzz.Options{})
		b := fuzz.GenerateNode(seed, fuzz.Options{})
		if len(a.Files) != len(b.Files) {
			t.Fatalf("seed %d produced %d files then %d", seed, len(a.Files), len(b.Files))
		}
		for rel, content := range a.Files {
			if b.Files[rel] != content {
				t.Errorf("seed %d is not reproducible at %s", seed, rel)
			}
		}
		if a.Config != b.Config {
			t.Errorf("seed %d produced a different config", seed)
		}
	}
}

// nodeClientShapes are the ways a persist() argument can be written in Node.
// The compiler reads the type of that expression to decide both what to
// provision and which client library to hand back, so a corpus that misses one
// is not testing the thing that decides what gets built.
var nodeClientShapes = []string{
	"new DynamoDBClient({})", "new DynamoDBClient({ region:", "new S3Client(",
	"new Secret(", "new Topic(",
	"new Pool(", "knex(", "new Redis(", "createClient(",
	"postgresql://", "mysql://",
}

// nodeImportShapes are the ways the SDK itself can be pulled in. Each is a
// distinct path through the detector's import resolution.
var nodeImportShapes = []string{
	`import { `, `import * as `, ` as store`,
	`= require("@cloudcompiler/sdk")`, `persist: store`,
}

func TestNodeCorpusCoversEveryShape(t *testing.T) {
	seen := map[string]bool{}
	for _, seed := range NodeCorpusSeeds {
		p := fuzz.GenerateNode(seed, fuzz.Options{})
		for _, content := range p.Files {
			for _, shape := range append(append([]string{}, nodeClientShapes...), nodeImportShapes...) {
				if strings.Contains(content, shape) {
					seen[shape] = true
				}
			}
		}
	}
	for _, shape := range append(append([]string{}, nodeClientShapes...), nodeImportShapes...) {
		if !seen[shape] {
			t.Errorf("no Node corpus seed generates %q; add a seed that does", shape)
		}
	}
}

// checkRewrittenNode asserts the compiler left behind valid JavaScript that no
// longer reaches for the SDK, which is not installed in a deployment bundle.
func checkRewrittenNode(t *testing.T, p *fuzz.Program, c compiled) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}

	for _, unit := range p.Expect.Units {
		unitDir := filepath.Join(c.outDir, unit)
		_ = filepath.Walk(unitDir, func(abs string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			ext := filepath.Ext(abs)
			if ext != ".js" && ext != ".cjs" && ext != ".ts" {
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
			rel, _ := filepath.Rel(c.outDir, abs)

			// A TypeScript file is not parseable as JavaScript, and esbuild is
			// what handles it at packaging time; syntax-checking it here would
			// need a compiler this test has no business owning.
			if ext != ".ts" {
				cmd := exec.Command(node, "--check", abs)
				if out, cerr := cmd.CombinedOutput(); cerr != nil {
					t.Errorf("the compiler produced invalid JavaScript at %s: %v\n%s\n--- rewritten ---\n%s\n%s",
						rel, cerr, out, src, render(p))
					return nil
				}
			}

			if strings.Contains(src, "@cloudcompiler/sdk") {
				t.Errorf("%s still imports the SDK, which is not installed in a bundle:\n%s\n%s",
					rel, src, render(p))
			}
			return nil
		})
	}
}
