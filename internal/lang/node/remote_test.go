package node

import (
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/source"
)

func remoteFns(t *testing.T, src string) map[string]bool {
	t.Helper()
	files := source.NewSet("/tmp")
	f := &source.File{Path: "pricing.js", Content: []byte(src)}
	if err := (Frontend{}).Parse(f); err != nil {
		t.Fatal(err)
	}
	files.Add(f)
	t.Cleanup(func() { f.SetContent(nil) })

	out := map[string]bool{}
	fns, supported := (Frontend{}).RemoteFunctions(files, "pricing.js")
	if !supported {
		t.Fatal("Node should be able to take part in a remote call")
	}
	for _, fn := range fns {
		out[fn.Name] = fn.Async
	}
	return out
}

// Both spellings of an exported function, and the async flag, which is what
// the compiler refuses a call on.
func TestNodeRemoteFunctionsAndTheirAsyncness(t *testing.T) {
	got := remoteFns(t, `
export async function quoteBasket(items) { return items; }
export function priceOf(sku) { return 1; }
export const reserve = async (id) => ({ id });
export const describe = (id) => id;
function notExported() {}
export async function _private() {}
`)

	want := map[string]bool{
		"quoteBasket": true,
		"priceOf":     false,
		"reserve":     true,
		"describe":    false,
	}
	if len(got) != len(want) {
		t.Fatalf("functions = %v, want %v", got, want)
	}
	for name, async := range want {
		if got[name] != async {
			t.Errorf("%s: async = %v, want %v", name, got[name], async)
		}
	}
	// Not exported, and private, are both absent -- a unit's interface is what
	// it exports, minus what it marks private.
	for _, absent := range []string{"notExported", "_private"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s should not be callable over the wire", absent)
		}
	}
}
