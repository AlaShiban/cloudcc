package runtime_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The two runtimes implement one protocol, and a unit does not know what the
// unit it is calling was written in. That claim is only true while both sides
// spell the envelope the same way, and nothing about a mismatch would be
// visible until a Python unit called a Node one and got back a shape it read
// as "neither a result nor an error".
//
// Compared by reading the sources rather than by running them: this is a
// question about two constants, and the alternative is a test that needs both
// interpreters and an AWS SDK.
func TestBothRuntimesSpellTheEnvelopeTheSameWay(t *testing.T) {
	python := read(t, filepath.Join("py", "templates", "_cloudcc_runtime", "rpc.py"))
	node := read(t, filepath.Join("..", "lang", "node", "templates", "_cloudcc_runtime", "rpc.js"))

	for _, key := range []string{"CALL_KEY", "ERROR_KEY", "RESULT_KEY"} {
		pyValue := constantValue(t, python, key, `(?m)^`+key+`\s*=\s*"([^"]+)"`)
		jsValue := constantValue(t, node, key, `(?m)^export const `+key+`\s*=\s*"([^"]+)";`)
		if pyValue != jsValue {
			t.Errorf("%s is %q in Python and %q in Node; a call between units in "+
				"different languages would not be understood", key, pyValue, jsValue)
		}
	}

	// The keys must also differ from each other, or a result would be read as
	// an error.
	seen := map[string]string{}
	for _, key := range []string{"CALL_KEY", "ERROR_KEY", "RESULT_KEY"} {
		value := constantValue(t, python, key, `(?m)^`+key+`\s*=\s*"([^"]+)"`)
		if other, clash := seen[value]; clash {
			t.Errorf("%s and %s are both %q", key, other, value)
		}
		seen[value] = key
	}
}

// Both dispatchers must wrap what the function returned. A side that returned
// the value bare would put a scalar on the wire, and whether a scalar arrives
// quoted depends on the Lambda implementation rather than on the program.
func TestBothRuntimesWrapTheReturnedValue(t *testing.T) {
	python := read(t, filepath.Join("py", "templates", "_cloudcc_runtime", "rpc.py"))
	node := read(t, filepath.Join("..", "lang", "node", "templates", "_cloudcc_runtime", "rpc.js"))

	if !strings.Contains(python, "return {RESULT_KEY: result}") {
		t.Errorf("the Python dispatcher does not wrap its result:\n%s", python)
	}
	if !strings.Contains(node, "{ [RESULT_KEY]: await module[name](...args) }") {
		t.Errorf("the Node dispatcher does not wrap its result:\n%s", node)
	}
}

func read(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

func constantValue(t *testing.T, source, name, pattern string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(source)
	if m == nil {
		t.Fatalf("%s is not declared where this test expects it", name)
	}
	return m[1]
}
