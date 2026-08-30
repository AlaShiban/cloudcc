package aws

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The compiler writes environment bindings and the injected Python shims read
// them. Two implementations of one naming scheme drift, so these tests compare
// them directly rather than trusting that both were updated together.

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(wd))) // internal/provider/aws -> root
}

func shimSource(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "internal", "runtime", "py", "templates", "_cloudcc_runtime", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestShimsReadTheNamesTheCompilerWrites checks that every environment binding
// the resolver emits appears, in its Python spelling, in the shim that
// consumes it.
func TestShimsReadTheNamesTheCompilerWrites(t *testing.T) {
	cases := []struct {
		shim     string
		goName   string
		pyFormat string
	}{
		{"kv.py", EnvKVTable("x"), "CLOUDCC_KV_%s_TABLE"},
		{"fs.py", EnvFSBucket("x"), "CLOUDCC_FS_%s_BUCKET"},
		{"secret.py", EnvSecretARN("x"), "CLOUDCC_SECRET_%s_ARN"},
		{"orm.py", EnvORMURL("x"), "CLOUDCC_ORM_%s_URL"},
		{"orm.py", EnvORMSecretARN("x"), "CLOUDCC_ORM_%s_SECRET_ARN"},
		{"redis_.py", EnvRedisEndpoint("x"), "CLOUDCC_REDIS_%s_ENDPOINT"},
		{"redis_.py", EnvRedisPort("x"), "CLOUDCC_REDIS_%s_PORT"},
		{"redis_.py", EnvRedisTLS("x"), "CLOUDCC_REDIS_%s_TLS"},
		{"pubsub.py", EnvTopicARN("x"), "CLOUDCC_TOPIC_%s_ARN"},
		{"pubsub.py", EnvTopicURL("x"), "CLOUDCC_TOPIC_%s_URL"},
		{"pubsub.py", EnvTopicBacking("x"), "CLOUDCC_TOPIC_%s_BACKING"},
		{"config.py", EnvConfig("x"), "CLOUDCC_CONFIG_%s"},
		{"expose.py", EnvGatewayURL("x"), "CLOUDCC_GATEWAY_%s_URL"},
	}
	for _, c := range cases {
		src := shimSource(t, c.shim)
		if !strings.Contains(src, c.pyFormat) {
			t.Errorf("%s does not build %q; the compiler emits %q", c.shim, c.pyFormat, c.goName)
			continue
		}
		// The Go name for id "x" must match what the Python format produces.
		want := strings.Replace(c.pyFormat, "%s", "X", 1)
		if c.goName != want {
			t.Errorf("%s: compiler emits %q but the shim reads %q", c.shim, c.goName, want)
		}
	}
}

func TestEndpointOverrideIsHonouredByEveryClient(t *testing.T) {
	src := shimSource(t, "_client.py")
	if !strings.Contains(src, EnvEndpointOverride) {
		t.Errorf("_client.py does not honour %s, so a compiled app cannot be pointed at an emulator (D15)", EnvEndpointOverride)
	}
	if !strings.Contains(src, "endpoint_url") {
		t.Errorf("_client.py reads the override but never passes endpoint_url:\n%s", src)
	}
}

// TestSlugMatchesEnvVar runs the shims' own slug function against the Go one.
// It is the only way to be sure both spell an id the same way.
func TestSlugMatchesEnvVar(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH")
	}
	ids := []string{"petsByOwner", "pet-api", "log.level", "9lives", "a_b", "ALLCAPS"}

	script := `
import sys
def slug(id):
    return "".join(c.upper() if c.isalnum() else "_" for c in id)
for line in sys.stdin.read().splitlines():
    print(slug(line))
`
	cmd := exec.Command(python, "-c", script)
	cmd.Stdin = strings.NewReader(strings.Join(ids, "\n"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the shim's slug function failed: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(got) != len(ids) {
		t.Fatalf("expected %d results, got %d", len(ids), len(got))
	}
	for i, id := range ids {
		want := strings.TrimSuffix(strings.TrimPrefix(EnvConfig(id), "CLOUDCC_CONFIG_"), "")
		if got[i] != want {
			t.Errorf("id %q: the shim spells it %q, the compiler spells it %q", id, got[i], want)
		}
	}
}

// TestSlugFunctionIsTheOneInTheShim guards the test above: if the shim's
// implementation changes, the copy embedded in the test must change with it.
func TestSlugFunctionIsTheOneInTheShim(t *testing.T) {
	src := shimSource(t, "_client.py")
	want := `return "".join(c.upper() if c.isalnum() else "_" for c in id)`
	if !strings.Contains(src, want) {
		t.Errorf("the shim's slug implementation changed; update TestSlugMatchesEnvVar to match:\n%s", src)
	}
}
