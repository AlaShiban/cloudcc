package py

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"text/template"

	"github.com/cloudcompiler/cloudcc/internal/sanitize"
)

// RuntimePackage is the injected package's name.
const RuntimePackage = "_cloudcc_runtime"

//go:embed all:templates
var templateFS embed.FS

// PythonVersion is the interpreter the generated artefacts target. Lambda's
// python3.12 runtime and the generated Dockerfile agree on it.
const PythonVersion = "3.12"

// LambdaRuntime is the managed AWS runtime these bundles target.
const LambdaRuntime = "python3.12"

// LambdaEntryModule is the generated entrypoint module for a Lambda unit.
const LambdaEntryModule = "cloudcc_lambda_entry"

// LambdaHandler is the handler string the generated Lambda function uses.
const LambdaHandler = LambdaEntryModule + ".handler"

// DockerfileName is the container build file, generated only when the user has
// not supplied one (D13).
const DockerfileName = "Dockerfile"

// RuntimeFiles returns the injected _cloudcc_runtime package as path -> content,
// with paths relative to a unit's bundle root.
func RuntimeFiles() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(templateFS, "templates/"+RuntimePackage, func(p string, d fs.DirEntry, err error) error {
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
		return nil, fmt.Errorf("the %s templates were not embedded", RuntimePackage)
	}
	return out, nil
}

// UnitTemplateData is what the per-unit templates are rendered against.
type UnitTemplateData struct {
	// Unit is the execution unit id.
	Unit string
	// EntryModule is the dotted module name of the unit's entrypoint.
	EntryModule string
	// ASGIApp is the module-level ASGI application variable, "" when the unit
	// serves no HTTP.
	ASGIApp string
}

// RenderLambdaEntry produces the generated Lambda entrypoint for a unit.
func RenderLambdaEntry(data UnitTemplateData) ([]byte, error) {
	return render("templates/cloudcc_lambda_entry.py.tmpl", data)
}

// RenderDockerfile produces the generated container build file for a unit.
func RenderDockerfile(data UnitTemplateData) ([]byte, error) {
	return render("templates/Dockerfile.tmpl", data)
}

// PackageUnit is one entry in the packaging script. The fragment that builds
// it comes from that unit's language frontend, so this script does not have to
// know what any unit was written in.
type PackageUnit struct {
	ID string
	// Container is true for units deployed as an image rather than a zip.
	Container bool
	// Fragment is the shell that builds this unit's artefact.
	Fragment string
}

// PackageData is what the packaging script is rendered against.
type PackageData struct {
	PythonVersion string
	Units         []PackageUnit
}

// RenderPackageScript produces bin/package.sh, which installs each unit's
// dependencies and zips it. Pulumi does not pip-install for you, so this has to
// run before `pulumi up`.
func RenderPackageScript(units []PackageUnit) ([]byte, error) {
	sorted := append([]PackageUnit(nil), units...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	return render("templates/package.sh.tmpl", PackageData{
		PythonVersion: PythonVersion,
		Units:         sorted,
	})
}

// PackagingScript returns the shell fragment that builds one unit's artefact.
// It is per-language because how a unit is packaged is the one part of
// deployment that genuinely depends on what it was written in.
func PackagingScript(unit string, container bool) string {
	name := "templates/fragment-zip.sh.tmpl"
	if container {
		name = "templates/fragment-container.sh.tmpl"
	}
	out, err := render(name, struct {
		ID            string
		PythonVersion string
	}{ID: unit, PythonVersion: PythonVersion})
	if err != nil {
		// The templates are embedded constants, so a failure here is a
		// programming error rather than anything a user can cause.
		panic("cloudcc: rendering the packaging fragment: " + err.Error())
	}
	return string(out)
}

// RenderPushScript produces bin/push-images.sh.
//
// Container images cannot be pushed before `pulumi up`, because the registry
// they go to does not exist until then. Splitting the two scripts is what lets
// `cloudcc deploy` sequence them correctly: package, up, push.
func RenderPushScript(units []PackageUnit) ([]byte, error) {
	sorted := append([]PackageUnit(nil), units...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	return render("templates/push-images.sh.tmpl", PackageData{
		PythonVersion: PythonVersion,
		Units:         sorted,
	})
}

// templateFuncs are shared by the generated scripts. ecrOutput must agree with
// aws.EnvECRRepo; a naming test pins the two together.
var templateFuncs = template.FuncMap{
	"ecrOutput": func(unit string) string {
		return "CLOUDCC_ECR_" + sanitize.EnvVar(unit) + "_URL"
	},
}

func render(name string, data any) ([]byte, error) {
	body, err := templateFS.ReadFile(name)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(path.Base(name)).Funcs(templateFuncs).Parse(string(body))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ShimRequirements are the packages the injected runtime needs, keyed by the
// capability that pulls them in. They are merged into a unit's
// requirements.txt only when that unit actually uses the capability, so a
// program with no Redis does not ship a Redis client.
// Each capability pulls in the library whose type the program declared, so the
// compiled bundle holds the same client the source did.
var ShimRequirements = map[string][]string{
	"base": {"boto3>=1.34"},
	"asgi": {"mangum>=0.17"},
	// Keyed by client library rather than by capability, because the capability
	// does not say enough: an async SQLAlchemy engine needs an async driver,
	// and a bundle without one fails on its first connection.
	"redis-py":         {"redis>=5.0"},
	"sqlalchemy":       {"SQLAlchemy>=2.0"},
	"sqlalchemy-async": {"SQLAlchemy[asyncio]>=2.0", "asyncpg>=0.30"},
	"pathlib":          {"cloudpathlib[s3]>=0.20"},
}

// MergeRequirements folds the shim requirements into an existing
// requirements.txt: entries are de-duplicated by distribution name, sorted, and
// a pin the user already declared is never replaced.
func MergeRequirements(existing []byte, add []string) []byte {
	type entry struct{ name, line string }
	order := []string{}
	byName := map[string]entry{}

	var preamble []string
	for _, raw := range strings.Split(string(existing), "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
			// Comments and pip options (-r, --index-url) keep their place at
			// the top rather than being sorted into the package list.
			preamble = append(preamble, trimmed)
			continue
		}
		name := distributionName(trimmed)
		if _, seen := byName[name]; !seen {
			order = append(order, name)
		}
		byName[name] = entry{name, trimmed}
	}

	for _, req := range add {
		name := distributionName(req)
		if _, userPinned := byName[name]; userPinned {
			continue // never clobber a pin the user chose
		}
		order = append(order, name)
		byName[name] = entry{name, req}
	}

	sort.Strings(order)
	var out strings.Builder
	for _, line := range preamble {
		out.WriteString(line)
		out.WriteString("\n")
	}
	seen := map[string]bool{}
	for _, name := range order {
		if seen[name] {
			continue
		}
		seen[name] = true
		out.WriteString(byName[name].line)
		out.WriteString("\n")
	}
	return []byte(out.String())
}

// distributionName extracts the package name from a requirement line.
func distributionName(req string) string {
	req = strings.TrimSpace(req)
	for _, sep := range []string{"==", ">=", "<=", "~=", "!=", ">", "<", "[", ";", " @ "} {
		if i := strings.Index(req, sep); i >= 0 {
			req = req[:i]
		}
	}
	return strings.ToLower(strings.TrimSpace(req))
}
