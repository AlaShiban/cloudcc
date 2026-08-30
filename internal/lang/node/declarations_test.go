package node

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The injected runtime is JavaScript, and the compiled copy of a TypeScript
// program is source someone reads, diffs and typechecks. Without a .d.ts beside
// each module, `tsc` on that tree reports every shim import as an implicit any
// -- an error, under strict, in code the user did not write and cannot fix.
//
// Two files describing one module is exactly the shape that drifts, which is
// what this test is for. It is the same argument as the Go/Python naming parity
// test next door: the drift is silent and it only shows up in someone else's
// project.

var exportedName = regexp.MustCompile(`(?m)^export\s+(?:declare\s+)?(?:async\s+)?(function|class|const|type)\s+([A-Za-z_$][\w$]*)`)

// exportsOf lists the *value* exports of a module or its declarations.
//
// A `type` export is skipped: it has no runtime counterpart, so a declaration
// file may add one and the .js it describes cannot. Comparing those would make
// naming a type an error.
func exportsOf(source string) []string {
	var out []string
	for _, m := range exportedName.FindAllStringSubmatch(source, -1) {
		if m[1] == "type" {
			continue
		}
		out = append(out, m[2])
	}
	sort.Strings(out)
	return out
}

func TestEveryRuntimeModuleHasDeclarations(t *testing.T) {
	files, err := runtimeFiles()
	if err != nil {
		t.Fatal(err)
	}

	modules := map[string]string{}  // name -> .js source
	declared := map[string]string{} // name -> .d.ts source
	for path, content := range files {
		base := strings.TrimPrefix(path, "_cloudcc_runtime/")
		switch {
		case strings.HasSuffix(base, ".d.ts"):
			declared[strings.TrimSuffix(base, ".d.ts")] = string(content)
		case strings.HasSuffix(base, ".js"):
			modules[strings.TrimSuffix(base, ".js")] = string(content)
		}
	}
	if len(modules) == 0 {
		t.Fatal("no runtime modules were embedded")
	}

	for _, name := range sortedNames(modules) {
		decl, ok := declared[name]
		if !ok {
			t.Errorf("%s.js has no %s.d.ts; a TypeScript program importing it gets an implicit any", name, name)
			continue
		}
		got, want := exportsOf(decl), exportsOf(modules[name])
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s declares %v but exports %v", name, got, want)
		}
	}

	for _, name := range sortedNames(declared) {
		if _, ok := modules[name]; !ok {
			t.Errorf("%s.d.ts describes a module that does not exist", name)
		}
	}
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
