package node

import (
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/source"
)

// rewrittenAt compiles one source file at a given path within its unit and
// returns what the rewriter left behind.
func rewrittenAt(t *testing.T, path, src string) string {
	t.Helper()
	f := &source.File{Path: path, Content: []byte(src)}
	if err := (Frontend{}).Parse(f); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.SetContent(nil) })

	var d diag.Diagnostics
	hints := detectHints(f, &d)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if err := (Frontend{}).Rewrite(f, hints); err != nil {
		t.Fatal(err)
	}
	return string(f.Content)
}

func rewritten(t *testing.T, src string) string {
	t.Helper()
	return rewrittenAt(t, "app.js", src)
}

func TestPersistIsRewrittenToARuntimeConnect(t *testing.T) {
	got := rewritten(t, "import { persist, KVStore } from \"@cloudcompiler/sdk\";\n"+
		"const pets = persist(new KVStore(), { id: \"petsByOwner\" });\n")

	if !strings.Contains(got, `_cloudccKv.connect("petsByOwner")`) {
		t.Errorf("the call was not rewritten:\n%s", got)
	}
	if !strings.Contains(got, `import * as _cloudccKv from "./_cloudcc_runtime/kv.js"`) {
		t.Errorf("the shim import was not injected:\n%s", got)
	}
}

// The SDK is not installed in a deployment bundle, so an import of it left
// behind kills the unit on its first load. That holds whether or not the module
// used the import.
func TestTheSDKImportIsAlwaysRemoved(t *testing.T) {
	for name, src := range map[string]string{
		"used":   "import { persist, KVStore } from \"@cloudcompiler/sdk\";\nconst p = persist(new KVStore(), { id: \"a\" });\n",
		"unused": "import { persist } from \"@cloudcompiler/sdk\";\n\nexport const VALUE = 1;\n",
		"require": "const { persist, KVStore } = require(\"@cloudcompiler/sdk\");\n" +
			"const p = persist(new KVStore(), { id: \"a\" });\n",
	} {
		t.Run(name, func(t *testing.T) {
			got := rewrittenAt(t, "app.js", src)
			if strings.Contains(got, "@cloudcompiler/sdk") {
				t.Errorf("the SDK import survived:\n%s", got)
			}
		})
	}
}

// A JavaScript import resolves relative to the importing file, so a module in a
// subdirectory has to reach up to find the injected runtime at the unit root.
// Emitting "./_cloudcc_runtime/..." from every file compiled cleanly and then
// failed at bundling time -- esbuild could not resolve it -- which the program
// generator caught by laying a program out under src/.
func TestRuntimeImportsReachUpFromSubdirectories(t *testing.T) {
	for path, want := range map[string]string{
		"server.js":       `"./_cloudcc_runtime/kv.js"`,
		"src/store.js":    `"../_cloudcc_runtime/kv.js"`,
		"src/a/b/deep.js": `"../../../_cloudcc_runtime/kv.js"`,
	} {
		t.Run(path, func(t *testing.T) {
			src := "import { persist, KVStore } from \"@cloudcompiler/sdk\";\n" +
				"const pets = persist(new KVStore(), { id: \"pets\" });\n"
			got := rewrittenAt(t, path, src)
			if !strings.Contains(got, want) {
				t.Errorf("a file at %s should import the runtime as %s:\n%s", path, want, got)
			}
		})
	}
}

// The same reach-up applies to CommonJS, which resolves require() the same way.
func TestRequireAlsoReachesUp(t *testing.T) {
	got := rewrittenAt(t, "src/store.cjs",
		"const { persist, KVStore } = require(\"@cloudcompiler/sdk\");\n"+
			"const pets = persist(new KVStore(), { id: \"pets\" });\n")
	if !strings.Contains(got, `require("../_cloudcc_runtime/kv.js")`) {
		t.Errorf("a CommonJS module in a subdirectory should reach up:\n%s", got)
	}
}

// The library decides which module a store is wired to, because two Redis
// clients are not interchangeable.
func TestTheClientLibraryPicksTheShimModule(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"ioredis": {
			"import Redis from \"ioredis\";\nimport { persist } from \"@cloudcompiler/sdk\";\n" +
				"const c = persist(new Redis(), { id: \"cache\" });\n",
			"_cloudcc_runtime/redis_ioredis.js",
		},
		"node-redis": {
			"import { createClient } from \"redis\";\nimport { persist } from \"@cloudcompiler/sdk\";\n" +
				"const c = persist(createClient(), { id: \"cache\" });\n",
			"_cloudcc_runtime/redis_node.js",
		},
		"pg": {
			"import { Pool } from \"pg\";\nimport { persist } from \"@cloudcompiler/sdk\";\n" +
				"const d = persist(new Pool({ connectionString: \"postgres://x/y\" }), { id: \"db\" });\n",
			"_cloudcc_runtime/orm_pg.js",
		},
		"knex": {
			"import knex from \"knex\";\nimport { persist } from \"@cloudcompiler/sdk\";\n" +
				"const d = persist(knex({ client: \"pg\" }), { id: \"db\" });\n",
			"_cloudcc_runtime/orm_knex.js",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := rewrittenAt(t, "app.js", tc.src)
			if !strings.Contains(got, tc.want) {
				t.Errorf("expected the unit to be wired to %s:\n%s", tc.want, got)
			}
		})
	}
}
