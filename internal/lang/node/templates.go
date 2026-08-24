package node

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"text/template"

	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/lang"
)

//go:embed all:templates
var templateFS embed.FS

// LambdaRuntime is the managed AWS runtime these bundles target.
const LambdaRuntime = "nodejs22.x"

// LambdaEntryFile is the generated entrypoint, and LambdaHandler names the
// export Lambda calls.
//
// The handler is "index.handler" rather than the entrypoint's own name because
// the bundler emits a single index.mjs: what Lambda loads is the bundle, not
// the file the bundle was built from.
const (
	LambdaEntryFile = "cloudcc_lambda_entry.mjs"
	LambdaBundle    = "index.mjs"
	LambdaHandler   = "index.handler"
	DockerfileName  = "Dockerfile"
)

// shimDependencies are the packages the injected runtime needs, added to a
// unit's manifest only when that unit actually uses the capability. A program
// with no cache does not ship a Redis client.
var shimDependencies = map[string]map[string]string{
	"base": {
		"@aws-sdk/client-dynamodb":        "^3.700.0",
		"@aws-sdk/client-s3":              "^3.700.0",
		"@aws-sdk/client-sns":             "^3.700.0",
		"@aws-sdk/client-secrets-manager": "^3.700.0",
	},
	"http":          {"serverless-http": "^3.2.0"},
	"persist_redis": {"redis": "^4.7.0"},
}

// runtimeFiles returns the injected runtime package.
func runtimeFiles() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(templateFS, "templates/_cloudcc_runtime", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := templateFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[strings.TrimPrefix(p, "templates/")] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the Node runtime templates were not embedded")
	}
	return out, nil
}

// unitTemplateData is what the per-unit templates are rendered against.
type unitTemplateData struct {
	Unit string
	// EntryFile is the unit's entrypoint, relative to its bundle root. Node
	// imports files rather than dotted modules, so this stays a path.
	EntryFile string
	// ASGIApp is the exported binding holding the HTTP application, empty when
	// the unit serves none. The field keeps its cross-language name because
	// the IR does.
	ASGIApp string
}

// unitFiles generates the entrypoint, the manifest and, for a container unit,
// the build file.
func unitFiles(u *ir.ExecUnit, opts lang.UnitOptions) (map[string][]byte, error) {
	data := unitTemplateData{
		Unit:      u.ID,
		EntryFile: u.Entrypoint(),
		ASGIApp:   u.ASGIApp,
	}
	out := map[string][]byte{}

	if opts.Container {
		if !opts.UserDockerfile {
			rendered, err := render("templates/Dockerfile.tmpl", data)
			if err != nil {
				return nil, err
			}
			out[DockerfileName] = rendered
		}
	} else {
		rendered, err := render("templates/cloudcc_lambda_entry.mjs.tmpl", data)
		if err != nil {
			return nil, err
		}
		out[LambdaEntryFile] = rendered
	}

	deps := map[string]string{}
	for name, version := range shimDependencies["base"] {
		deps[name] = version
	}
	if u.ASGIApp != "" && !opts.Container {
		for name, version := range shimDependencies["http"] {
			deps[name] = version
		}
	}
	if opts.UsesRedis {
		for name, version := range shimDependencies["persist_redis"] {
			deps[name] = version
		}
	}

	manifest, err := MergeManifest(opts.Manifest, deps)
	if err != nil {
		return nil, err
	}
	out["package.json"] = manifest
	return out, nil
}

// MergeManifest folds the runtime's dependencies into the user's package.json.
//
// A version the user pinned is never replaced: they chose it, and a compiler
// quietly upgrading a dependency is a debugging session nobody asked for. The
// result is written with sorted keys so the output stays byte-deterministic.
func MergeManifest(existing []byte, add map[string]string) ([]byte, error) {
	manifest := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &manifest); err != nil {
			return nil, fmt.Errorf("parsing package.json: %w", err)
		}
	}

	// A bundle is ESM: the generated entrypoint uses import.
	manifest["type"] = "module"
	if _, ok := manifest["name"]; !ok {
		manifest["name"] = "cloudcc-unit"
	}
	if _, ok := manifest["private"]; !ok {
		manifest["private"] = true
	}

	deps, _ := manifest["dependencies"].(map[string]any)
	if deps == nil {
		deps = map[string]any{}
	}
	for name, version := range add {
		if _, pinned := deps[name]; pinned {
			continue
		}
		deps[name] = version
	}
	// The SDK is compile-time only and is not installed in a bundle.
	delete(deps, Package)
	manifest["dependencies"] = deps

	out, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// packagingScript returns the shell that builds one unit's artefact.
func packagingScript(u *ir.ExecUnit, container bool) string {
	name := "templates/fragment-bundle.sh.tmpl"
	if container {
		name = "templates/fragment-container.sh.tmpl"
	}
	entry := LambdaEntryFile
	if container {
		entry = u.Entrypoint()
	}
	out, err := render(name, struct {
		ID        string
		Entry     string
		Externals []string
	}{ID: u.ID, Entry: entry, Externals: externalsFor(u)})
	if err != nil {
		// The templates are embedded constants, so a failure here is a
		// programming error rather than anything a user can cause.
		panic("cloudcc: rendering the Node packaging fragment: " + err.Error())
	}
	return string(out)
}

// externalsFor returns the packages the bundler must leave alone, from the
// unit's pulumi_params escape hatch.
func externalsFor(u *ir.ExecUnit) []string {
	raw, ok := u.Config().PulumiParams["externals"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func render(name string, data any) ([]byte, error) {
	body, err := templateFS.ReadFile(name)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(path.Base(name)).Parse(string(body))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
