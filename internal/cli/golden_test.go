package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// update regenerates the committed golden trees: go test ./internal/cli -update
var update = flag.Bool("update", false, "rewrite the golden output trees")

// examples are the applications compiled by the golden tests. petstore-multi
// is the load-bearing one: two units sharing a KV store, a static site, and a
// topic with a publisher and a subscriber.
// kitchen-sink is the coverage example: every capability, both compute types,
// both gateway types, a VPC, secrets, and embedded assets.
// mixed holds units in two languages sharing one store, which is the case the
// language seam exists for and the only one where a regression in it shows up
// as something other than a Python change.
var examples = []string{"petstore", "petstore-multi", "kitchen-sink", "mixed", "petstore-node"}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd)) // internal/cli -> repo root
}

// compileExample runs the CLI exactly as a user would and returns the output
// directory.
func compileExample(t *testing.T, name, outDir string, extraArgs ...string) string {
	t.Helper()
	root := repoRoot(t)
	args := append([]string{filepath.Join(root, "examples", name), "-o", outDir}, extraArgs...)

	cmd := NewRootCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compiling %s failed: %v\nstderr:\n%s", name, err, stderr.String())
	}
	return outDir
}

// snapshot reads a directory tree into path -> content, skipping installed
// dependencies and build artefacts.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(abs string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, abs)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if rel == "node_modules" || rel == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		// The rendered PNG is deliberately not part of the golden tree: it
		// only exists when graphviz happens to be installed, and its bytes
		// depend on which version. topology.mmd and topology.dot are the
		// deterministic artefacts, and they are compared. That the PNG is
		// written at all is covered by the topology package's own tests.
		if strings.HasSuffix(rel, ".png") {
			return nil
		}
		data, rerr := os.ReadFile(abs)
		if rerr != nil {
			return rerr
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGoldenOutput(t *testing.T) {
	for _, name := range examples {
		t.Run(name, func(t *testing.T) {
			got := snapshot(t, compileExample(t, name, t.TempDir()))
			goldenDir := filepath.Join("testdata", "golden", name)

			if *update {
				if err := os.RemoveAll(goldenDir); err != nil {
					t.Fatal(err)
				}
				for _, rel := range sortedKeys(got) {
					abs := filepath.Join(goldenDir, filepath.FromSlash(rel))
					if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(abs, []byte(got[rel]), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("wrote %d golden files to %s", len(got), goldenDir)
				return
			}

			if _, err := os.Stat(goldenDir); os.IsNotExist(err) {
				t.Fatalf("no golden tree at %s; run: go test ./internal/cli -update", goldenDir)
			}
			want := snapshot(t, goldenDir)

			for _, rel := range sortedKeys(want) {
				gotContent, ok := got[rel]
				if !ok {
					t.Errorf("missing from output: %s", rel)
					continue
				}
				if gotContent != want[rel] {
					t.Errorf("%s differs:\n%s", rel, firstDifference(want[rel], gotContent))
				}
			}
			for _, rel := range sortedKeys(got) {
				if _, ok := want[rel]; !ok {
					t.Errorf("unexpected file in output: %s", rel)
				}
			}
		})
	}
}

// TestCompileIsDeterministic compiles each example twice into different
// directories; the trees must be byte-identical (D18). Golden testing and the
// deploy fingerprint both depend on this.
func TestCompileIsDeterministic(t *testing.T) {
	for _, name := range examples {
		t.Run(name, func(t *testing.T) {
			first := snapshot(t, compileExample(t, name, t.TempDir()))
			second := snapshot(t, compileExample(t, name, t.TempDir()))

			if len(first) != len(second) {
				t.Fatalf("run one produced %d files, run two produced %d", len(first), len(second))
			}
			for _, rel := range sortedKeys(first) {
				if first[rel] != second[rel] {
					t.Errorf("%s is not reproducible:\n%s", rel, firstDifference(first[rel], second[rel]))
				}
			}
		})
	}
}

// TestMultiUnitSharesOneStore is the case Klotho 1 never exercised: two units
// wired to a single table, each with its own environment.
func TestMultiUnitSharesOneStore(t *testing.T) {
	out := compileExample(t, "petstore-multi", t.TempDir())
	index := readFile(t, filepath.Join(out, "index.ts"))

	if strings.Count(index, "new aws.dynamodb.Table(") != 1 {
		t.Errorf("expected exactly one DynamoDB table:\n%s", index)
	}
	for _, unit := range []string{"api", "worker"} {
		envBlock := blockAfter(index, "const "+unit+"Env")
		if !strings.Contains(envBlock, "CLOUDCC_KV_PETSBYOWNER_TABLE") {
			t.Errorf("unit %q is not wired to the shared table:\n%s", unit, envBlock)
		}
	}
	// Each unit gets its own function and its own role.
	for _, want := range []string{
		`new aws.lambda.Function("api"`,
		`new aws.lambda.Function("worker"`,
		`new aws.iam.Role("api"`,
		`new aws.iam.Role("worker"`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("missing %q in the generated project", want)
		}
	}
}

// TestIAMIsLeastPrivilege pins the policy derivation: a unit may only reach
// the stores its code declares, scoped to those resources.
func TestIAMIsLeastPrivilege(t *testing.T) {
	out := compileExample(t, "petstore-multi", t.TempDir())
	index := readFile(t, filepath.Join(out, "index.ts"))

	apiPolicy := blockAfter(index, `new aws.iam.RolePolicy("api"`)
	workerPolicy := blockAfter(index, `new aws.iam.RolePolicy("worker"`)

	// Only the worker writes to the audit bucket, so only the worker may.
	if strings.Contains(apiPolicy, "petAuditBucket") {
		t.Errorf("the api unit was granted access to a bucket it never declares:\n%s", apiPolicy)
	}
	if !strings.Contains(workerPolicy, "petAuditBucket") {
		t.Errorf("the worker unit is missing access to the bucket it writes to:\n%s", workerPolicy)
	}
	// Only the api publishes, so only the api gets sns:Publish.
	if !strings.Contains(apiPolicy, "sns:Publish") {
		t.Errorf("the publisher is missing sns:Publish:\n%s", apiPolicy)
	}
	if strings.Contains(workerPolicy, "sns:Publish") {
		t.Errorf("a subscriber should not be granted sns:Publish:\n%s", workerPolicy)
	}
	// Both share the table, so both get table access, scoped to that table.
	for name, policy := range map[string]string{"api": apiPolicy, "worker": workerPolicy} {
		if !strings.Contains(policy, "dynamodb:GetItem") {
			t.Errorf("unit %q is missing table access:\n%s", name, policy)
		}
		if !strings.Contains(policy, "petsByOwnerTable.arn") {
			t.Errorf("unit %q's table grant is not scoped to the table ARN:\n%s", name, policy)
		}
		if strings.Contains(policy, `"*"`) {
			t.Errorf("unit %q has a wildcard resource grant:\n%s", name, policy)
		}
	}
}

// TestStaticAssetsNeverEnterAComputeBundle pins the ordering that makes
// static-units run before exec-units.
func TestStaticAssetsNeverEnterAComputeBundle(t *testing.T) {
	out := compileExample(t, "petstore-multi", t.TempDir())
	for _, unit := range []string{"api", "worker"} {
		if _, err := os.Stat(filepath.Join(out, unit, "public", "index.html")); err == nil {
			t.Errorf("a claimed static asset ended up inside the %q bundle", unit)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "static", "petstore-site", "index.html")); err != nil {
		t.Errorf("the static site was not written: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// blockAfter returns the text from marker up to the closing "});" line, which
// is enough to inspect a single generated resource.
func blockAfter(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i:]
	if j := strings.Index(rest, "\n});"); j >= 0 {
		return rest[:j+4]
	}
	if j := strings.Index(rest, "\n};"); j >= 0 {
		return rest[:j+3]
	}
	return rest
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// firstDifference renders the first differing line with a little context, so a
// golden failure points at the change instead of dumping two whole files.
func firstDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		w, g := "", ""
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w == g {
			continue
		}
		var b strings.Builder
		for j := max(0, i-3); j < i; j++ {
			b.WriteString("   " + wantLines[j] + "\n")
		}
		b.WriteString("  -" + w + "\n")
		b.WriteString("  +" + g + "\n")
		b.WriteString("  (line " + itoa(i+1) + "; run with -update to accept)")
		return b.String()
	}
	return "(files differ only in trailing content)"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestKitchenSinkCoversEveryCapability pins the whole resolution table in one
// place: if a capability stops resolving, or resolves to the wrong thing, this
// is where it shows up.
func TestKitchenSinkCoversEveryCapability(t *testing.T) {
	out := compileExample(t, "kitchen-sink", t.TempDir())
	index := readFile(t, filepath.Join(out, "index.ts"))

	for capability, want := range map[string]string{
		"persist_kv":     `new aws.dynamodb.Table("catalogue"`,
		"persist_fs":     `new aws.s3.BucketV2("itemDocs"`,
		"persist_secret": `new aws.secretsmanager.Secret("signingKey"`,
		"persist_orm":    `new aws.rds.Instance("shopdb"`,
		"persist_redis":  `new aws.elasticache.Cluster("itemCache"`,
		"pubsub":         `new aws.sns.Topic("itemEvents"`,
		"exec lambda":    `new aws.lambda.Function("api"`,
		"exec ecs":       `new aws.ecs.Service("reporter"`,
		"expose apigw":   `new aws.apigatewayv2.Api("shop-api"`,
		"expose alb":     `new aws.lb.LoadBalancer("reporter-web"`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("%s did not resolve: expected %s", capability, want)
		}
	}
}

// TestVPCAppearsOnlyWhenSomethingNeedsIt pins the rule that Lambda does not
// drag a VPC in: the cost is real and the benefit is not.
func TestVPCAppearsOnlyWhenSomethingNeedsIt(t *testing.T) {
	lambdaOnly := readFile(t, filepath.Join(compileExample(t, "petstore", t.TempDir()), "index.ts"))
	if strings.Contains(lambdaOnly, "aws.ec2.Vpc") {
		t.Errorf("a Lambda-only application should not create a VPC:\n%s", lambdaOnly)
	}

	withVPC := readFile(t, filepath.Join(compileExample(t, "kitchen-sink", t.TempDir()), "index.ts"))
	for _, want := range []string{"aws.ec2.Vpc", "aws.ec2.Subnet", "aws.ec2.SecurityGroup"} {
		if !strings.Contains(withVPC, want) {
			t.Errorf("an application with ECS, RDS and ElastiCache needs %s", want)
		}
	}
}

// TestSecretsNeverAppearInGeneratedSource pins D21.
func TestSecretsNeverAppearInGeneratedSource(t *testing.T) {
	out := compileExample(t, "kitchen-sink", t.TempDir())
	index := readFile(t, filepath.Join(out, "index.ts"))

	if !strings.Contains(index, `cloudccConfig.requireSecret("stripe_key")`) {
		t.Errorf("a secret config value should be read from the encrypted stack config:\n%s", index)
	}
	// The plain value is inlined; the secret one is not.
	if !strings.Contains(index, `CLOUDCC_CONFIG_LOG_LEVEL: "info"`) {
		t.Errorf("a plain config value should be inlined:\n%s", index)
	}
	if strings.Contains(index, `CLOUDCC_CONFIG_STRIPE_KEY: "`) {
		t.Errorf("a secret config value was inlined as plaintext:\n%s", index)
	}
}

// TestEmbeddedAssetsTravelWithTheDeclaringUnitOnly pins embed_assets.
func TestEmbeddedAssetsTravelWithTheDeclaringUnitOnly(t *testing.T) {
	out := compileExample(t, "kitchen-sink", t.TempDir())
	if _, err := os.Stat(filepath.Join(out, "api", "data", "seed.json")); err != nil {
		t.Errorf("embed_assets did not bundle the seed data with the unit that claimed it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "reporter", "data", "seed.json")); err == nil {
		t.Error("embedded assets leaked into a unit that never claimed them")
	}
}

// TestComputeTypeDecidesThePackaging pins the Lambda/ECS split: an entrypoint
// for one, a Dockerfile for the other, and Mangum only where it is used.
func TestComputeTypeDecidesThePackaging(t *testing.T) {
	out := compileExample(t, "kitchen-sink", t.TempDir())

	if _, err := os.Stat(filepath.Join(out, "api", "cloudcc_lambda_entry.py")); err != nil {
		t.Errorf("the Lambda unit has no entrypoint: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "api", "Dockerfile")); err == nil {
		t.Error("a Lambda unit should not get a Dockerfile")
	}
	if _, err := os.Stat(filepath.Join(out, "reporter", "Dockerfile")); err != nil {
		t.Errorf("the ECS unit has no Dockerfile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "reporter", "cloudcc_lambda_entry.py")); err == nil {
		t.Error("an ECS unit should not get a Lambda entrypoint")
	}

	apiReqs := readFile(t, filepath.Join(out, "api", "requirements.txt"))
	reporterReqs := readFile(t, filepath.Join(out, "reporter", "requirements.txt"))
	if !strings.Contains(apiReqs, "mangum") {
		t.Errorf("the Lambda unit needs the ASGI adapter:\n%s", apiReqs)
	}
	if strings.Contains(reporterReqs, "mangum") {
		t.Errorf("a container unit runs uvicorn directly and does not need Mangum:\n%s", reporterReqs)
	}
	// Both use Redis, so both get the client.
	for name, reqs := range map[string]string{"api": apiReqs, "reporter": reporterReqs} {
		if !strings.Contains(reqs, "redis") {
			t.Errorf("unit %q declares a cache but has no Redis client:\n%s", name, reqs)
		}
	}
}

// TestUserDockerfileWins pins D13's override.
func TestUserDockerfileWins(t *testing.T) {
	src := t.TempDir()
	root := repoRoot(t)
	copyTree(t, filepath.Join(root, "examples", "kitchen-sink"), src)
	mine := "# my own image\nFROM python:3.12-alpine\nCMD [\"echo\", \"mine\"]\n"
	if err := os.WriteFile(filepath.Join(src, "Dockerfile"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	cmd := NewRootCommand()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{src, "-o", outDir, "--app", "kitchen-sink"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compile failed: %v\n%s", err, stderr.String())
	}

	got := readFile(t, filepath.Join(outDir, "reporter", "Dockerfile"))
	if got != mine {
		t.Errorf("a user-supplied Dockerfile must be used as written, got:\n%s", got)
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(abs string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(from, abs)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(abs)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestPhysicalNamesAreUnique pins the rule that two capability ids can never
// end up sharing one cloud resource.
//
// Sanitising is lossy: "my_bucket" and "my-bucket" both reduce to
// "app-my-bucket". Two distinct stores would then silently share a bucket and
// each other's data, which is about the worst failure this compiler could
// have. A generated program with dotted ids is what surfaced it.
func TestPhysicalNamesAreUnique(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py": `from fastapi import FastAPI
import cloudcompiler as cloudcc

app = FastAPI()
cloudcc.expose(app, id="gw")

a = cloudcc.persist(Path("./data"), id="my_bucket")
b = cloudcc.persist(Path("./data"), id="my-bucket")
c = cloudcc.persist(Path("./data"), id="my.bucket")
x = cloudcc.persist(boto3.resource("dynamodb").Table("t"), id="my_table")
y = cloudcc.persist(boto3.resource("dynamodb").Table("t"), id="my-table")
`,
	})
	out := t.TempDir()
	if _, stderr, code := run(t, src, "-o", out, "--app", "demo"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}

	index := readFile(t, filepath.Join(out, "index.ts"))
	for _, class := range []string{"aws.s3.BucketV2", "aws.dynamodb.Table"} {
		field := "bucket"
		if class == "aws.dynamodb.Table" {
			field = "name"
		}
		// Anchored to a top-level property, so the `name` inside an
		// attributes list is not mistaken for the table's own name.
		physicalRe := regexp.MustCompile(`(?m)^    ` + field + `: "([^"]+)"`)

		names := map[string]string{}
		for _, block := range strings.Split(index, "new "+class+"(")[1:] {
			logical := block[:strings.Index(block, ",")]
			end := strings.Index(block, "\n});")
			if end < 0 {
				end = len(block)
			}
			m := physicalRe.FindStringSubmatch(block[:end])
			if m == nil {
				t.Errorf("%s %s has no %s property", class, logical, field)
				continue
			}
			if owner, taken := names[m[1]]; taken {
				t.Errorf("%s and %s both resolved to the physical name %q",
					owner, logical, m[1])
			}
			names[m[1]] = logical
		}
		if len(names) < 2 {
			t.Errorf("expected several %s resources, found %d", class, len(names))
		}
	}
}

// A secret with a default must not block deployment. requireSecret would
// contradict what the program said and fail every deploy with Pulumi's own
// message, which does not mention how to set the value.
func TestSecretWithADefaultDoesNotBlockDeployment(t *testing.T) {
	src := writeApp(t, map[string]string{
		"app.py": "import cloudcompiler as cloudcc\n" +
			"a = cloudcc.config_value(\"with_default\", default=\"fallback\", secret=True)\n" +
			"b = cloudcc.config_value(\"no_default\", secret=True)\n",
	})
	out := t.TempDir()
	if _, stderr, code := run(t, src, "-o", out, "--app", "demo"); code != ExitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	index := readFile(t, filepath.Join(out, "index.ts"))

	if !strings.Contains(index, `getSecret("with_default") ?? "fallback"`) {
		t.Errorf("a secret with a default should fall back to it:\n%s", index)
	}
	if !strings.Contains(index, `requireSecret("no_default")`) {
		t.Errorf("a secret with no default genuinely has to be supplied:\n%s", index)
	}
	// The generated file has to say how to supply it.
	if !strings.Contains(index, "pulumi config set --secret cloudcc:no_default") {
		t.Errorf("the file should carry the command that sets the value:\n%s", index)
	}
	// And the value itself must never be inlined.
	if strings.Contains(index, `CLOUDCC_CONFIG_NO_DEFAULT: "`) {
		t.Errorf("a secret was inlined as plaintext:\n%s", index)
	}
}
