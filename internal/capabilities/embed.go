package capabilities

import (
	"sort"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
)

// EmbedAssetsPlugin makes files matched by cloudcc.embed_assets travel with the
// execution unit that referenced them.
//
// Without it, a data file that no module imports is unreachable and gets
// pruned with a warning; embed_assets is how a program says "this file is
// mine" about something static analysis cannot follow.
type EmbedAssetsPlugin struct{ base }

// NewEmbedAssetsPlugin returns the embed-assets stage. It runs before
// exec-units so the claimed files are known when closures are assembled.
func NewEmbedAssetsPlugin() *EmbedAssetsPlugin {
	return &EmbedAssetsPlugin{base{name: PluginEmbedAssets, deps: []string{PluginDetect}}}
}

func (p *EmbedAssetsPlugin) Transform(ctx *compiler.Context) error {
	for _, h := range ctx.Hints {
		if h.Func != sdkdetect.FnEmbedAssets {
			continue
		}
		matches := matchEmbedded(ctx, h)
		if len(matches) == 0 {
			ctx.Diags.Warnf(ctx.HintPos(h), CapEmbedAssets,
				"pattern %q matched no files", h.Str("pattern"))
			continue
		}
		ctx.Embedded[h.File] = union(ctx.Embedded[h.File], matches)
	}
	return nil
}

// CapEmbedAssets is the diagnostic label for embed_assets.
const CapEmbedAssets = sdkdetect.CapEmbedAssets

// matchEmbedded expands the pattern relative to the declaring file's
// directory, ignoring anything a static unit already claimed.
func matchEmbedded(ctx *compiler.Context, h sdkdetect.Hint) []string {
	pattern := normalizeGlob(dirOf(h.File), h.Str("pattern"))
	var out []string
	for _, candidate := range ctx.Files.Paths() {
		if _, claimed := ctx.ClaimedFiles[candidate]; claimed {
			continue
		}
		if ok, err := doublestar.Match(pattern, candidate); err == nil && ok {
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	return out
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}
