package sdkdetect

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/source"
)

func detect(t *testing.T, src string) ([]Hint, *diag.Diagnostics) {
	t.Helper()
	f := &source.File{Path: "app.py", Content: []byte(src)}
	if err := f.ParsePython(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.SetContent(nil) })
	var d diag.Diagnostics
	return Detect(f, &d), &d
}

func TestDetectModuleAlias(t *testing.T) {
	hints, d := detect(t, `
import cloudcompiler as cloudcc
pets = cloudcc.persist_kv("petsByOwner")
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 1 {
		t.Fatalf("got %d hints, want 1: %v", len(hints), hints)
	}
	h := hints[0]
	if h.Func != FnPersistKV || h.Capability != "persist_kv" {
		t.Errorf("func/capability = %q/%q", h.Func, h.Capability)
	}
	if h.ID() != "petsByOwner" {
		t.Errorf("id = %q", h.ID())
	}
	if h.Receives != "pets" {
		t.Errorf("Receives = %q, want pets", h.Receives)
	}
	if h.Enclosing != "" {
		t.Errorf("Enclosing = %q, want module level", h.Enclosing)
	}
}

func TestDetectPlainImport(t *testing.T) {
	hints, _ := detect(t, "import cloudcompiler\nx = cloudcompiler.persist_fs(\"blobs\")\n")
	if len(hints) != 1 || hints[0].Func != FnPersistFS || hints[0].ID() != "blobs" {
		t.Fatalf("hints = %v", hints)
	}
}

func TestDetectFromImport(t *testing.T) {
	hints, _ := detect(t, "from cloudcompiler import persist_kv\npets = persist_kv(\"a\")\n")
	if len(hints) != 1 || hints[0].ID() != "a" {
		t.Fatalf("hints = %v", hints)
	}
}

func TestDetectFromImportAliased(t *testing.T) {
	hints, _ := detect(t, "from cloudcompiler import persist_kv as pkv\npets = pkv(\"a\")\n")
	if len(hints) != 1 || hints[0].Func != FnPersistKV || hints[0].ID() != "a" {
		t.Fatalf("hints = %v", hints)
	}
}

func TestUnrelatedCallsIgnored(t *testing.T) {
	hints, d := detect(t, `
import cloudcompiler as cloudcc
import other
other.persist_kv("not-ours")
cloudcc.not_a_capability("x")
print("hello")
pets = cloudcc.persist_kv("ours")
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 1 || hints[0].ID() != "ours" {
		t.Fatalf("hints = %v", hints)
	}
}

func TestNoSDKImportMeansNoHints(t *testing.T) {
	hints, _ := detect(t, "persist_kv(\"x\")\n")
	if len(hints) != 0 {
		t.Fatalf("hints = %v", hints)
	}
}

func TestExposeRecordsAppExpression(t *testing.T) {
	hints, d := detect(t, `
from fastapi import FastAPI
import cloudcompiler as cloudcc
app = FastAPI()
cloudcc.expose(app, id="pet-api")
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 1 {
		t.Fatalf("hints = %v", hints)
	}
	if got := hints[0].Str("app"); got != "app" {
		t.Errorf("app = %q, want app", got)
	}
	if got := hints[0].Str("id"); got != "pet-api" {
		t.Errorf("id = %q", got)
	}
}

func TestConfigValueBoolAndDefault(t *testing.T) {
	hints, d := detect(t, `
import cloudcompiler as cloudcc
k = cloudcc.config_value("api_key", default="none", secret=True)
lvl = cloudcc.config_value("log_level", "info")
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if len(hints) != 2 {
		t.Fatalf("hints = %v", hints)
	}
	if !hints[0].Bool("secret") || hints[0].Str("default") != "none" {
		t.Errorf("hint 0 = %+v", hints[0].Args)
	}
	if hints[1].Bool("secret") || hints[1].Str("default") != "info" {
		t.Errorf("hint 1 = %+v", hints[1].Args)
	}
}

func TestPersistORMModelList(t *testing.T) {
	hints, d := detect(t, `
import cloudcompiler as cloudcc
db = cloudcc.persist_orm("maindb", models=["Pet", "Owner"])
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if got, want := hints[0].StrList("models"), []string{"Pet", "Owner"}; !reflect.DeepEqual(got, want) {
		t.Errorf("models = %v, want %v", got, want)
	}
}

func TestPersistORMAcceptsModelClassNames(t *testing.T) {
	hints, d := detect(t, `
import cloudcompiler as cloudcc
db = cloudcc.persist_orm("maindb", models=[Pet, Owner])
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	if got, want := hints[0].StrList("models"), []string{"Pet", "Owner"}; !reflect.DeepEqual(got, want) {
		t.Errorf("models = %v, want %v", got, want)
	}
}

func TestNonLiteralArgumentIsAPreciseError(t *testing.T) {
	_, d := detect(t, `
import cloudcompiler as cloudcc
name = "petsByOwner"
pets = cloudcc.persist_kv(name)
`)
	items := d.Items()
	if len(items) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %v", len(items), items)
	}
	got := items[0].String()
	// The offending argument starts at line 4, column 27.
	if !strings.HasPrefix(got, "app.py:4:27: error: persist_kv:") {
		t.Errorf("diagnostic = %q", got)
	}
	if !strings.Contains(got, "string literal") || !strings.Contains(got, "the variable name") {
		t.Errorf("diagnostic should name the problem and the argument: %q", got)
	}
}

func TestFStringRejected(t *testing.T) {
	_, d := detect(t, "import cloudcompiler as cloudcc\nx = cloudcc.persist_kv(f\"pets-{env}\")\n")
	items := d.Items()
	if len(items) != 1 || !strings.Contains(items[0].Message, "f-string") {
		t.Fatalf("diagnostics = %v", items)
	}
}

func TestUnknownKeywordRejected(t *testing.T) {
	_, d := detect(t, "import cloudcompiler as cloudcc\nx = cloudcc.persist_kv(id=\"a\", nope=1)\n")
	items := d.Items()
	if len(items) != 1 || !strings.Contains(items[0].Message, "no parameter") {
		t.Fatalf("diagnostics = %v", items)
	}
}

func TestMissingRequiredArgRejected(t *testing.T) {
	_, d := detect(t, "import cloudcompiler as cloudcc\nx = cloudcc.persist_kv()\n")
	items := d.Items()
	if len(items) != 1 || !strings.Contains(items[0].Message, "requires the id argument") {
		t.Fatalf("diagnostics = %v", items)
	}
}

func TestTooManyPositionalArgsRejected(t *testing.T) {
	_, d := detect(t, "import cloudcompiler as cloudcc\nx = cloudcc.persist_kv(\"a\", \"b\")\n")
	items := d.Items()
	if len(items) != 1 || !strings.Contains(items[0].Message, "at most 1") {
		t.Fatalf("diagnostics = %v", items)
	}
}

func TestStaticUnitArgs(t *testing.T) {
	hints, d := detect(t, `
import cloudcompiler as cloudcc
cloudcc.static_unit("site", static_files="./public/**/*", shared_files="./public/shared/*")
`)
	if d.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", d.Items())
	}
	h := hints[0]
	if h.Str("static_files") != "./public/**/*" || h.Str("shared_files") != "./public/shared/*" {
		t.Errorf("args = %+v", h.Args)
	}
}

func TestEnclosingFunctionRecorded(t *testing.T) {
	hints, _ := detect(t, `
import cloudcompiler as cloudcc
def handler():
    t = cloudcc.pubsub_topic("events")
`)
	if len(hints) != 1 || hints[0].Enclosing != "handler" {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestSpanCoversTheWholeCall(t *testing.T) {
	src := "import cloudcompiler as cloudcc\npets = cloudcc.persist_kv(\"petsByOwner\")\n"
	hints, _ := detect(t, src)
	h := hints[0]
	if got, want := src[h.Span[0]:h.Span[1]], `cloudcc.persist_kv("petsByOwner")`; got != want {
		t.Errorf("span text = %q, want %q", got, want)
	}
}

func TestHintsSortedByOffset(t *testing.T) {
	hints, _ := detect(t, `
import cloudcompiler as cloudcc
a = cloudcc.persist_kv("a")
b = cloudcc.persist_fs("b")
c = cloudcc.pubsub_topic("c")
`)
	var ids []string
	for _, h := range hints {
		ids = append(ids, h.ID())
	}
	if !reflect.DeepEqual(ids, []string{"a", "b", "c"}) {
		t.Errorf("ids = %v", ids)
	}
}

func TestEscapeSequencesDecoded(t *testing.T) {
	hints, _ := detect(t, "import cloudcompiler as cloudcc\ncloudcc.static_unit(\"s\", static_files=\"./a\\tb/*\")\n")
	if got := hints[0].Str("static_files"); got != "./a\tb/*" {
		t.Errorf("static_files = %q", got)
	}
}

func TestResolveImportsRecordsAlias(t *testing.T) {
	f := &source.File{Path: "a.py", Content: []byte("import cloudcompiler as cloudcc\n")}
	if err := f.ParsePython(); err != nil {
		t.Fatal(err)
	}
	imp := ResolveImports(f)
	if imp.Alias != "cloudcc" || !imp.Modules["cloudcc"] {
		t.Errorf("imports = %+v", imp)
	}
}

func TestSignaturesCoverEveryFunctionName(t *testing.T) {
	want := []string{
		FnConfigValue, FnEmbedAssets, FnExecutionUnit, FnExpose, FnPersistFS,
		FnPersistKV, FnPersistORM, FnPersistRedis, FnPersistSecret,
		FnPubSubTopic, FnStaticUnit,
	}
	if got := FunctionNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("FunctionNames() = %v, want %v", got, want)
	}
}
