package fuzz_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/fuzz"
)

// CorpusSeeds are the programs committed as a permanent regression corpus.
// They were chosen by sweeping and keeping the seeds that between them cover
// every generated shape: all four import styles, all three layouts, one to
// three units, every store kind, topics with a publisher and a subscriber,
// static units, embedded assets and secret configuration.
//
// Each one is reproducible: `go test ./internal/fuzz -run 'TestCorpus/seed-N'`.
var CorpusSeeds = []int64{
	1, 2, 3, 5, 7, 11, 13, 17, 19, 23,
	29, 31, 37, 41, 43, 47, 53, 59, 61, 67,
}

// TestCorpus is the permanent one: twenty fixed programs, compiled and checked
// against their own ground truth on every run.
func TestCorpus(t *testing.T) {
	for _, seed := range CorpusSeeds {
		t.Run("seed-"+strconv.FormatInt(seed, 10), func(t *testing.T) {
			p := fuzz.Generate(seed, fuzz.Options{})
			c := build(t, p)
			checkAgainstGroundTruth(t, p, c)
			checkBundles(t, p, c)
			checkRewrittenPython(t, p, c)
		})
	}
}

// TestSweep runs a wider range on demand. It is how new seeds are found; the
// interesting ones get promoted into CorpusSeeds.
//
//	CLOUDCC_FUZZ_SEEDS=500 go test ./internal/fuzz -run TestSweep
func TestSweep(t *testing.T) {
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
			p := fuzz.Generate(seed, fuzz.Options{})
			c := build(t, p)
			checkAgainstGroundTruth(t, p, c)
			checkBundles(t, p, c)
			checkRewrittenPython(t, p, c)
		})
	}
}

// TestGenerationIsReproducible pins the property the whole approach rests on:
// a seed always produces the same program, so a failure can always be
// reproduced from the seed alone.
func TestGenerationIsReproducible(t *testing.T) {
	for _, seed := range []int64{1, 7, 42, 1000} {
		first := fuzz.Generate(seed, fuzz.Options{})
		second := fuzz.Generate(seed, fuzz.Options{})

		if len(first.Files) != len(second.Files) {
			t.Fatalf("seed %d produced %d files then %d", seed, len(first.Files), len(second.Files))
		}
		for path, content := range first.Files {
			if second.Files[path] != content {
				t.Errorf("seed %d is not reproducible at %s", seed, path)
			}
		}
		if first.Config != second.Config {
			t.Errorf("seed %d produced a different configuration on the second run", seed)
		}
	}
}

// TestCompilingIsDeterministic checks the compiler's own determinism against
// generated input rather than the two hand-written examples, which is a much
// wider net.
func TestCompilingIsDeterministic(t *testing.T) {
	for _, seed := range CorpusSeeds[:5] {
		p := fuzz.Generate(seed, fuzz.Options{})
		first := build(t, p)
		second := build(t, p)

		firstFiles := snapshotDir(t, first.outDir)
		secondFiles := snapshotDir(t, second.outDir)

		for rel, content := range firstFiles {
			other, ok := secondFiles[rel]
			if !ok {
				t.Errorf("seed %d: %s appeared in one compile but not the other", seed, rel)
				continue
			}
			if content != other {
				t.Errorf("seed %d: %s is not reproducible across compiles", seed, rel)
			}
		}
		if len(firstFiles) != len(secondFiles) {
			t.Errorf("seed %d produced %d files then %d", seed, len(firstFiles), len(secondFiles))
		}
	}
}

func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	walk(t, dir, dir, out)
	return out
}

func walk(t *testing.T, root, dir string, out map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		full := dir + "/" + e.Name()
		if e.IsDir() {
			walk(t, root, full, out)
			continue
		}
		// The rendered PNG depends on the local graphviz version, not on the
		// compiler, so it is not part of what determinism means here.
		if len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".png" {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		out[full[len(root):]] = string(data)
	}
}
