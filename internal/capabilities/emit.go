package capabilities

import (
	"fmt"
	"path"

	"github.com/cloudcompiler/cc/internal/compiler"
	"github.com/cloudcompiler/cc/internal/config"
	"github.com/cloudcompiler/cc/internal/ir"
	"github.com/spf13/afero"
)

// StaticDir is the output subdirectory holding static site bundles.
const StaticDir = "static"

// writeUnitTrees copies each execution unit's file closure into its own output
// directory, and each static site's claimed assets into static/<id>/.
//
// The user's tree is never touched: everything downstream of here operates on
// this copy (D13).
func writeUnitTrees(ctx *compiler.Context) error {
	for _, id := range config.SortedKeys(ctx.UnitFiles) {
		for _, rel := range ctx.UnitFiles[id] {
			f, ok := ctx.Files.Get(rel)
			if !ok {
				continue
			}
			if err := writeOut(ctx.Out, path.Join(id, rel), f.Content); err != nil {
				return err
			}
		}
	}
	for _, in := range ctx.Graph.IntentsOfKind(config.KindStaticUnit) {
		site, ok := in.(*ir.StaticSite)
		if !ok {
			continue
		}
		for _, rel := range site.Files {
			f, ok := ctx.Files.Get(rel)
			if !ok {
				continue
			}
			if err := writeOut(ctx.Out, path.Join(StaticDir, site.ID, siteRelative(site, rel)), f.Content); err != nil {
				return err
			}
		}
	}
	return nil
}

// siteRelative strips the declaring module's directory from an asset path, so
// public/index.html is uploaded as index.html rather than public/index.html.
func siteRelative(site *ir.StaticSite, rel string) string {
	base := stripPrefixDir(rel, site.Root)
	// Also drop the first path segment of the glob when it is a fixed
	// directory, which is the common "./public/**/*" shape.
	if dir := globRootDir(site.StaticFiles); dir != "" {
		base = stripPrefixDir(base, dir)
	}
	return base
}

func stripPrefixDir(p, dir string) string {
	if dir == "" || dir == "." {
		return p
	}
	if len(p) > len(dir)+1 && p[:len(dir)] == dir && p[len(dir)] == '/' {
		return p[len(dir)+1:]
	}
	return p
}

// globRootDir returns the leading fixed directory of a glob, "" when the
// pattern starts with a wildcard.
func globRootDir(pattern string) string {
	pattern = trimDotSlash(pattern)
	dir := ""
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '/' {
			seg := pattern[:i]
			if containsMeta(seg) {
				return dir
			}
			dir = seg
			continue
		}
	}
	return dir
}

func trimDotSlash(s string) string {
	if len(s) > 2 && s[0] == '.' && s[1] == '/' {
		return s[2:]
	}
	return s
}

func containsMeta(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*', '?', '[', '{':
			return true
		}
	}
	return false
}

func writeOut(fs afero.Fs, rel string, content []byte) error {
	if dir := path.Dir(rel); dir != "." && dir != "" {
		if err := fs.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	if err := afero.WriteFile(fs, rel, content, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", rel, err)
	}
	return nil
}
