package sdkdetect_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/provider/aws"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
)

// examples/mega-app is a specification written as a program: every library
// category cloudcc should support, used the way it should be used, with the
// shim contract in the comments. Most of it does not compile yet.
//
// coverage.yaml is the machine-readable half, and this test is what stops it
// becoming a wish list nobody maintains. It checks the table against what the
// compiler actually knows in both directions: a row claiming support must
// really resolve, and a row claiming a library is unsupported must really not
// -- so the day someone implements one, this test fails until the row moves.
const megaApp = "../../examples/mega-app"

type coverage struct {
	Libraries []coverageRow `yaml:"libraries"`
}

type coverageRow struct {
	Library      string   `yaml:"library"`
	SDKProvided  bool     `yaml:"sdk_provided"`
	Category     string   `yaml:"category"`
	Constructors []string `yaml:"constructors"`
	Capability   string   `yaml:"capability"`
	Type         string   `yaml:"type"`
	TypeFromURL  bool     `yaml:"type_from_url"`
	NeedsModule  bool     `yaml:"needs_module_resolution"`
	Status       string   `yaml:"status"`
	Note         string   `yaml:"note"`
}

// label names a row in a failure message. Library is not unique -- redis-py
// appears twice, once for Valkey -- so the constructors disambiguate.
func (r coverageRow) label() string {
	name := r.Library
	if name == "" {
		name = "sdk"
	}
	if len(r.Constructors) > 0 {
		return name + " (" + strings.Join(r.Constructors, ", ") + ")"
	}
	return name + " (" + r.Category + ")"
}

func loadCoverage(t *testing.T) coverage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(megaApp, "coverage.yaml"))
	if err != nil {
		t.Fatalf("reading the mega-app coverage table: %v", err)
	}
	var c coverage
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("parsing coverage.yaml: %v", err)
	}
	if len(c.Libraries) == 0 {
		t.Fatal("coverage.yaml lists no libraries")
	}
	return c
}

var validStatus = map[string]bool{
	"supported": true, // resolves today
	"proposed":  true, // should resolve, does not yet
	"rejected":  true, // should be refused with an explanation
	"none":      true, // nothing declared: runtime configuration or a lint
}

func TestCoverageStatusesAreKnown(t *testing.T) {
	for _, row := range loadCoverage(t).Libraries {
		if !validStatus[row.Status] {
			t.Errorf("%s: unknown status %q", row.label(), row.Status)
		}
		if row.Note == "" && row.Status != "supported" {
			// A supported row explains itself by working. Everything else is a
			// claim about what should happen, and a claim with no reasoning is
			// not reviewable.
			t.Errorf("%s: status %q needs a note saying what should happen and why",
				row.label(), row.Status)
		}
	}
}

// A row claiming support must actually resolve, to the capability and the
// library identifier it names. The library matters as much as the capability:
// mysqlclient and PyMySQL are the same capability and returning one where the
// other was asked for silently stops exception handlers from catching.
func TestSupportedRowsResolve(t *testing.T) {
	for _, row := range loadCoverage(t).Libraries {
		if row.Status != "supported" {
			continue
		}
		for _, ctor := range row.Constructors {
			client, ok := sdkdetect.LookupClient("python", ctor)
			if !ok {
				t.Errorf("%s: coverage.yaml says supported, but %q resolves to nothing",
					row.label(), ctor)
				continue
			}
			if client.Capability != row.Capability {
				t.Errorf("%s: %q resolves to capability %q, table says %q",
					row.label(), ctor, client.Capability, row.Capability)
			}
			if client.Library != row.Library {
				t.Errorf("%s: %q resolves to library %q, table says %q",
					row.label(), ctor, client.Library, row.Library)
			}
			if row.SDKProvided && client.Library != "" {
				t.Errorf("%s: an SDK-provided client should have no library identifier, got %q",
					row.label(), client.Library)
			}
			// A library that serves several engines leaves Type empty and lets
			// the URL decide, which the table marks with type_from_url.
			if !row.TypeFromURL && client.Type != row.Type {
				t.Errorf("%s: %q resolves to type %q, table says %q",
					row.label(), ctor, client.Type, row.Type)
			}
		}
		if config.IsKind(row.Capability) && row.Type != "" {
			if got := aws.Support(row.Capability, row.Type); got != aws.Supported {
				t.Errorf("%s: the provider does not support %s/%s (level %d)",
					row.label(), row.Capability, row.Type, got)
			}
		}
	}
}

// The other direction, and the one that makes this table self-maintaining: a
// row that is not `supported` must not resolve. Implementing a library without
// moving its row fails here, which is the reminder to record what it became.
func TestUnsupportedRowsDoNotResolve(t *testing.T) {
	rows := loadCoverage(t).Libraries
	for _, row := range rows {
		if row.Status == "supported" {
			continue
		}
		for _, ctor := range row.Constructors {
			client, ok := sdkdetect.LookupClient("python", ctor)
			if !ok {
				continue
			}
			if client.Library == row.Library {
				t.Errorf("%s: coverage.yaml says %q, but %q now resolves to %s/%s -- "+
					"move the row to `supported` and record what it became",
					row.label(), row.Status, ctor, client.Capability, client.Library)
				continue
			}
			// It resolved to something else, which is the misclassification an
			// ambiguous constructor name causes: a SQLModel engine detected as
			// a SQLAlchemy one. Tolerated only where the table says so, and
			// only where some other row really does own that name.
			if !row.NeedsModule {
				t.Errorf("%s: %q resolves to %s, a different library. Either this "+
					"row needs needs_module_resolution: true, or the constructor "+
					"is wrong", row.label(), ctor, client.Library)
				continue
			}
			if !ownedBy(rows, ctor, client.Library) {
				t.Errorf("%s: %q resolves to library %q, which no row in the table "+
					"claims", row.label(), ctor, client.Library)
			}
		}
	}
}

// ownedBy reports whether some supported row claims this constructor for this
// library, so a documented misclassification points at a documented owner.
func ownedBy(rows []coverageRow, ctor, library string) bool {
	for _, row := range rows {
		if row.Status != "supported" || row.Library != library {
			continue
		}
		for _, c := range row.Constructors {
			if c == ctor {
				return true
			}
		}
	}
	return false
}

// Detection matches the final attribute of a call, so two libraries spelling a
// constructor the same way are indistinguishable -- `create_engine` is both
// SQLAlchemy's and SQLModel's, and `connect` belongs to PyMySQL, mysqlclient
// and sqlite3 at once. That ambiguity is fine as long as it is written down:
// resolving it means following the import, not just reading the name.
//
// This test exists so a new ambiguity cannot be added silently.
func TestAmbiguousConstructorsAreDeclaredAmbiguous(t *testing.T) {
	rows := loadCoverage(t).Libraries
	byConstructor := map[string][]coverageRow{}
	for _, row := range rows {
		for _, ctor := range row.Constructors {
			byConstructor[ctor] = append(byConstructor[ctor], row)
		}
	}

	names := make([]string, 0, len(byConstructor))
	for ctor := range byConstructor {
		names = append(names, ctor)
	}
	sort.Strings(names)

	for _, ctor := range names {
		sharing := byConstructor[ctor]
		if len(sharing) < 2 {
			continue
		}
		for _, row := range sharing {
			if !row.NeedsModule {
				t.Errorf("%s: %q is claimed by %d libraries, so this row needs "+
					"needs_module_resolution: true", row.label(), ctor, len(sharing))
			}
		}
	}
}

// Nothing the compiler supports may be missing from the table: a client that
// resolves but is undocumented is a capability nobody can discover.
func TestEveryKnownClientIsInTheTable(t *testing.T) {
	documented := map[string]bool{}
	for _, row := range loadCoverage(t).Libraries {
		if row.Status != "supported" {
			continue
		}
		for _, ctor := range row.Constructors {
			documented[ctor] = true
		}
	}
	for _, ctor := range sdkdetect.KnownClients("python") {
		if !documented[ctor] {
			t.Errorf("%q is a supported client but has no row in "+
				"examples/mega-app/coverage.yaml", ctor)
		}
	}
}

// The example is a program before it is a document, and a specification that
// does not parse is not one people will trust. Skipped where python3 is not
// installed rather than silently passing.
func TestTheMegaAppIsValidPython(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}

	var files []string
	err = filepath.Walk(megaApp, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(p, ".py") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("examples/mega-app has no Python files")
	}
	sort.Strings(files)

	// compile() rather than import: the point is that the syntax is valid, and
	// importing would need every library in requirements.txt installed.
	const script = `
import sys
for path in sys.argv[1:]:
    with open(path, "rb") as fh:
        compile(fh.read(), path, "exec")
`
	out, err := exec.Command(python, append([]string{"-c", script}, files...)...).CombinedOutput()
	if err != nil {
		t.Errorf("examples/mega-app does not parse:\n%s", out)
	}
}
