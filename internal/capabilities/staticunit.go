package capabilities

import (
	"path"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// StaticUnitsPlugin turns cloudcc.static_unit hints into StaticSite intents and
// claims the files they match.
//
// This runs before exec-units on purpose: claiming has to happen while the
// file pool is still whole, or static assets get swallowed into compute
// bundles. Klotho 1 ordered it this way for exactly that reason.
type StaticUnitsPlugin struct{ base }

// NewStaticUnitsPlugin returns the static-units stage.
func NewStaticUnitsPlugin() *StaticUnitsPlugin {
	return &StaticUnitsPlugin{base{name: PluginStaticUnits, deps: []string{PluginDetect}}}
}

func (p *StaticUnitsPlugin) Transform(ctx *compiler.Context) error {
	for _, h := range ctx.HintsFor(config.KindStaticUnit) {
		id := h.ID()
		site := &ir.StaticSite{
			StaticFiles:   h.Str("static_files"),
			SharedFiles:   h.Str("shared_files"),
			IndexDocument: h.Str("index_document"),
			Root:          path.Dir(h.File),
		}
		site.ID = id
		if site.IndexDocument == "" {
			site.IndexDocument = "index.html"
		}

		cfg := ctx.Config.Lookup(config.KindStaticUnit, id)
		// Configuration may override the globs, which is how a site can be
		// re-pointed without touching the source.
		if cfg.StaticFiles != "" {
			site.StaticFiles = cfg.StaticFiles
		}
		if cfg.SharedFiles != "" {
			site.SharedFiles = cfg.SharedFiles
		}
		if cfg.IndexDocument != "" {
			site.IndexDocument = cfg.IndexDocument
		}

		claimed := p.match(ctx, site.Root, site.StaticFiles)
		shared := p.match(ctx, site.Root, site.SharedFiles)

		if len(claimed) == 0 && len(shared) == 0 {
			ctx.Diags.Warnf(ctx.HintPos(h), config.KindStaticUnit,
				"static_files pattern %q matched no files", site.StaticFiles)
		}

		// static_files leave the shared pool; shared_files stay usable by
		// execution units as well as being uploaded.
		for _, f := range claimed {
			if owner, taken := ctx.ClaimedFiles[f]; taken && owner != id {
				ctx.Diags.Errorf(ctx.HintPos(h), config.KindStaticUnit,
					"%s is already claimed by static unit %q", f, owner)
				continue
			}
			ctx.ClaimedFiles[f] = id
		}

		site.Files = union(claimed, shared)
		if err := site.Configure(cfg); err != nil {
			return err
		}
		ctx.Config.Record(config.KindStaticUnit, id, withGlobs(cfg, site))
		ctx.Graph.AddIntent(site)
	}
	return nil
}

// withGlobs folds the resolved globs back into the recorded configuration so
// compiled/cloudcc.yaml documents what was actually used.
func withGlobs(cfg config.ResourceConfig, site *ir.StaticSite) config.ResourceConfig {
	cfg.StaticFiles = site.StaticFiles
	cfg.SharedFiles = site.SharedFiles
	cfg.IndexDocument = site.IndexDocument
	return cfg
}

// match expands a glob, interpreted relative to the directory of the file that
// declared it, against the current file set.
func (p *StaticUnitsPlugin) match(ctx *compiler.Context, root, pattern string) []string {
	if pattern == "" {
		return nil
	}
	full := normalizeGlob(root, pattern)
	var out []string
	for _, candidate := range ctx.Files.Paths() {
		if ok, err := doublestar.Match(full, candidate); err == nil && ok {
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	return out
}

// normalizeGlob resolves "./x" and bare "x" patterns against the declaring
// file's directory, producing a source-root-relative pattern.
func normalizeGlob(root, pattern string) string {
	pattern = strings.TrimPrefix(pattern, "./")
	if strings.HasPrefix(pattern, "/") {
		return strings.TrimPrefix(pattern, "/")
	}
	if root == "" || root == "." {
		return pattern
	}
	return path.Join(root, pattern)
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
