package pulumi_ts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cloudcompiler/cc/internal/config"
	"github.com/cloudcompiler/cc/internal/iac"
	"github.com/cloudcompiler/cc/internal/ir"
	"github.com/cloudcompiler/cc/internal/provider/aws"
	"github.com/cloudcompiler/cc/internal/sanitize"
	"github.com/spf13/afero"
)

// BackendName is this backend's registry key.
const BackendName = "pulumi-ts"

func init() { iac.Register(Backend{}) }

// Backend emits a Pulumi TypeScript project.
type Backend struct{}

// Name implements iac.Backend.
func (Backend) Name() string { return BackendName }

// Emit writes index.ts and the surrounding project files.
func (b Backend) Emit(req iac.Request) error {
	index, err := renderIndex(req)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"index.ts":      index,
		"Pulumi.yaml":   renderProject(req.Config),
		"package.json":  renderPackageJSON(req.Config),
		"tsconfig.json": []byte(tsconfigJSON),
		".gitignore":    []byte("node_modules/\nbuild/\n"),
	}
	for _, name := range config.SortedKeys(files) {
		if err := write(req.Out, name, files[name]); err != nil {
			return err
		}
	}
	// A stack settings file holds the user's own configuration; recompiling
	// must not throw that away.
	stackFile := "Pulumi." + req.Config.App + ".yaml"
	if exists, _ := afero.Exists(req.Out, stackFile); !exists {
		if err := write(req.Out, stackFile, []byte(renderStack(req.Config))); err != nil {
			return err
		}
	}
	return nil
}

// renderIndex walks the resolved resources in dependency order and emits one
// const per resource, plus the per-unit environment objects and the stack
// outputs the test harness reads.
func renderIndex(req iac.Request) ([]byte, error) {
	order, err := req.Program.ResourceOrder()
	if err != nil {
		return nil, err
	}

	namer := newVarNamer()
	for _, name := range []string{"pulumi", "aws", "config"} {
		namer.reserve(name)
	}
	// Pulumi names resources per type, so two resources of the same class may
	// not share a logical name. Resolving that here, once, means no mapping has
	// to remember it.
	logical := logicalNames(req.Program, order)
	// Names are assigned in dependency order so a resource can always refer to
	// something declared above it.
	for _, key := range order {
		res, ok := req.Program.Resource(key)
		if !ok {
			continue
		}
		tmpl, err := Lookup(res.Template())
		if err != nil {
			return nil, err
		}
		namer.assign(key, tmpl.VarSuffix)
	}

	env, err := unitEnvironments(req, namer)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString(header(req.Config))

	if secrets := secretConfigReads(req); secrets != "" {
		b.WriteString(secrets)
	}

	emittedEnv := map[string]bool{}
	for _, key := range order {
		res, ok := req.Program.Resource(key)
		if !ok {
			continue
		}
		// A unit's environment object has to exist before the compute that
		// receives it, and after everything that environment references.
		if key.Kind == aws.KindLambda || key.Kind == aws.KindECSTask {
			if block, ok := env[key.ID]; ok && !emittedEnv[key.ID] {
				b.WriteString(block)
				emittedEnv[key.ID] = true
			}
		}
		rendered, err := renderResource(namer, res, logical[key.String()])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		b.WriteString(rendered)
	}

	outputs, err := renderOutputs(req, namer)
	if err != nil {
		return nil, err
	}
	b.WriteString(outputs)
	return []byte(b.String()), nil
}

// logicalNames assigns each resource the name Pulumi will track it under:
// its id, or the generated variable name when two resources of the same class
// would otherwise collide.
func logicalNames(p *ir.Program, order []ir.Key) map[string]string {
	taken := map[string]bool{}
	out := map[string]string{}
	for _, key := range order {
		res, ok := p.Resource(key)
		if !ok {
			continue
		}
		tmpl, err := Lookup(res.Template())
		if err != nil {
			continue
		}
		name := key.ID
		for i := 2; taken[tmpl.Class+"|"+name]; i++ {
			name = fmt.Sprintf("%s-%d", key.ID, i)
		}
		taken[tmpl.Class+"|"+name] = true
		out[key.String()] = name
	}
	return out
}

func renderResource(namer *varNamer, res ir.Resource, logicalName string) (string, error) {
	tmpl, err := Lookup(res.Template())
	if err != nil {
		return "", err
	}
	name, _ := namer.get(res.Key())
	if logicalName == "" {
		logicalName = res.Key().ID
	}
	props, err := namer.object(res.Props(), 0)
	if err != nil {
		return "", err
	}
	if tmpl.Func != "" {
		// A data source is a call, not a construction, so it takes no Pulumi
		// resource name.
		return fmt.Sprintf("export const %s = %s(%s);\n\n", name, tmpl.Func, props), nil
	}
	return fmt.Sprintf("export const %s = new %s(%s, %s);\n\n",
		name, tmpl.Class, quote(logicalName), props), nil
}

// unitEnvironments builds the environment object for each execution unit:
// its config values plus the bindings of everything it uses (D17). There is no
// separate env-vars plugin because this is the only place that needs to know.
func unitEnvironments(req iac.Request, namer *varNamer) (map[string]string, error) {
	out := map[string]string{}
	for _, in := range req.Program.IntentsOfKind(config.KindExecutionUnit) {
		unit := in.(*ir.ExecUnit)
		bindings := map[string]string{}

		for name, binding := range req.UnitEnv[unit.ID] {
			rendered, err := renderBinding(namer, ir.Key{}, binding)
			if err != nil {
				return nil, err
			}
			bindings[name] = rendered
		}

		for _, edge := range req.Program.EdgesFrom(unit.Key(), ir.EdgeUses) {
			for _, res := range req.Program.ResolvedFrom(edge.To) {
				for name, binding := range res.EnvOutputs() {
					rendered, err := renderBinding(namer, res.Key(), binding)
					if err != nil {
						return nil, err
					}
					bindings[name] = rendered
				}
			}
		}

		// Anything the user set in cc.yaml wins over a derived binding.
		for name, value := range unit.Config().EnvironmentVariables {
			bindings[name] = quote(value)
		}

		var b strings.Builder
		fmt.Fprintf(&b, "const %s: { [key: string]: pulumi.Input<string> } = {\n",
			aws.EnvConstName(unit.ID))
		for _, name := range config.SortedKeys(bindings) {
			fmt.Fprintf(&b, "    %s: %s,\n", propertyKey(name), bindings[name])
		}
		b.WriteString("};\n\n")
		out[unit.ID] = b.String()
	}
	return out, nil
}

// renderBinding turns one environment binding into a TypeScript expression.
// Everything is coerced to a string, because environment variables are.
func renderBinding(namer *varNamer, owner ir.Key, binding ir.EnvBinding) (string, error) {
	if binding.Prop != "" {
		name, ok := namer.get(owner)
		if !ok {
			return "", fmt.Errorf("environment binding refers to %s, which no resource defines", owner)
		}
		return fmt.Sprintf("pulumi.interpolate`${%s.%s}`", name, binding.Prop), nil
	}
	rendered, err := namer.value(binding.Expr)
	if err != nil {
		return "", err
	}
	if _, isString := binding.Expr.(string); isString {
		return rendered, nil
	}
	if _, isInterp := binding.Expr.(ir.Interp); isInterp {
		return rendered, nil
	}
	return fmt.Sprintf("pulumi.interpolate`${%s}`", rendered), nil
}

// secretConfigReads emits the stack-config reads for secret config values.
// Secrets are read from Pulumi's encrypted stack config, never written into
// the generated source (D21).
func secretConfigReads(req iac.Request) string {
	var secrets []*ir.ConfigVar
	for _, in := range req.Program.IntentsOfKind(config.KindConfig) {
		if v := in.(*ir.ConfigVar); v.Secret {
			secrets = append(secrets, v)
		}
	}
	if len(secrets) == 0 {
		return ""
	}
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].ID < secrets[j].ID })

	var b strings.Builder
	b.WriteString("// Secret configuration values come from the encrypted stack config.\n")
	b.WriteString("// Set them with: pulumi config set --secret cc:<name> <value>\n")
	b.WriteString("const ccConfig = new pulumi.Config(\"cc\");\n")
	for _, v := range secrets {
		fmt.Fprintf(&b, "const %s = ccConfig.requireSecret(%s);\n",
			secretConstName(v.ID), quote(v.ID))
	}
	b.WriteString("\n")
	return b.String()
}

// SecretConstName is the generated const holding one secret config value.
func SecretConstName(id string) string { return secretConstName(id) }

func secretConstName(id string) string {
	return sanitize.Identifier(id + "-secret")
}

// renderOutputs exports every environment binding under its exact environment
// variable name, plus each gateway and website URL. The e2e harness wires a
// locally-run application from `pulumi output --json`, so the names have to
// match what the shims read.
func renderOutputs(req iac.Request, namer *varNamer) (string, error) {
	exports := map[string]string{}

	for _, res := range req.Program.Resources() {
		for name, binding := range res.EnvOutputs() {
			rendered, err := renderBinding(namer, res.Key(), binding)
			if err != nil {
				return "", err
			}
			exports[name] = rendered
		}
	}
	for _, res := range req.Program.Resources() {
		tmpl, err := Lookup(res.Template())
		if err != nil {
			return "", err
		}
		if tmpl.URLProp == "" {
			continue
		}
		name, _ := namer.get(res.Key())
		exports[sanitize.Identifier(res.Key().ID+"-url")] = name + "." + tmpl.URLProp
	}

	if len(exports) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("// Stack outputs. Every environment binding is exported under the exact\n")
	b.WriteString("// name the runtime shims read, so a compiled unit can be run locally with:\n")
	b.WriteString("//   eval \"$(pulumi output --json | jq -r 'to_entries[]|\"export \\(.key)=\\(.value)\"')\"\n")
	for _, name := range config.SortedKeys(exports) {
		fmt.Fprintf(&b, "export const %s = %s;\n", propertyKey(name), exports[name])
	}
	return b.String(), nil
}

func header(cfg *config.App) string {
	return fmt.Sprintf(`// Generated by cc for application %q. Do not edit: recompiling overwrites
// this file. To change what is generated, edit cc.yaml in your source tree.
import * as pulumi from "@pulumi/pulumi";
import * as aws from "@pulumi/aws";

`, cfg.App)
}

func renderProject(cfg *config.App) []byte {
	return []byte(fmt.Sprintf(`# Generated by cc.
name: %s
runtime: nodejs
description: Infrastructure compiled from the %s application by cc.
`, cfg.App, cfg.App))
}

func renderStack(cfg *config.App) string {
	return fmt.Sprintf(`# Stack settings for %s. cc creates this file once and never overwrites it,
# so anything you configure here survives recompiling.
config:
  aws:region: us-east-1
`, cfg.App)
}

func renderPackageJSON(cfg *config.App) []byte {
	return []byte(fmt.Sprintf(`{
    "name": %s,
    "private": true,
    "devDependencies": {
        "@types/node": "^22",
        "typescript": "^5.7"
    },
    "dependencies": {
        "@pulumi/aws": "^6.66.0",
        "@pulumi/pulumi": "^3.144.0"
    }
}
`, quote(cfg.App)))
}

const tsconfigJSON = `{
    "compilerOptions": {
        "strict": true,
        "outDir": "bin",
        "target": "es2020",
        "module": "commonjs",
        "moduleResolution": "node",
        "sourceMap": true,
        "experimentalDecorators": true,
        "pretty": true,
        "noFallthroughCasesInSwitch": true,
        "noImplicitReturns": true,
        "forceConsistentCasingInFileNames": true
    },
    "files": ["index.ts"]
}
`

func write(fs afero.Fs, name string, content []byte) error {
	return afero.WriteFile(fs, name, content, 0o644)
}
