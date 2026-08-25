package py

import (
	"strings"
	"testing"
)

// The runtime templates are embedded wholesale, so anything that ends up in
// that directory travels into every compiled bundle. Running one of the
// modules in place -- a test loading it by path, someone poking at it in a
// shell -- leaves a __pycache__ beside it, and a .pyc in a bundle is stale by
// definition and built for whichever interpreter happened to write it.
//
// This happened: a test added for the logging shim loaded it by path and the
// stray .pyc turned up in the golden trees. The test now suppresses bytecode,
// and this makes the compiler refuse to ship any that appears anyway.
func TestNoCompiledBytecodeReachesABundle(t *testing.T) {
	files, err := RuntimeFiles()
	if err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if strings.HasSuffix(name, ".pyc") || strings.Contains(name, "__pycache__") {
			t.Errorf("%s would be shipped into every bundle", name)
		}
	}
}

func TestBytecodePathsAreRecognised(t *testing.T) {
	for path, want := range map[string]bool{
		"templates/_cloudcc_runtime/logs.py":                          false,
		"templates/_cloudcc_runtime/__pycache__/logs.cpython-312.pyc": true,
		"templates/_cloudcc_runtime/kv.pyc":                           true,
		"templates/_cloudcc_runtime/pubsub.py":                        false,
	} {
		if got := isBytecode(path); got != want {
			t.Errorf("isBytecode(%q) = %v, want %v", path, got, want)
		}
	}
}
