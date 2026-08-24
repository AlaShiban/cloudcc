package source

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkipDirs are directory names never descended into.
var SkipDirs = map[string]bool{
	".git":           true,
	".hg":            true,
	".svn":           true,
	".venv":          true,
	"venv":           true,
	"node_modules":   true,
	"__pycache__":    true,
	".pytest_cache":  true,
	".mypy_cache":    true,
	".ruff_cache":    true,
	"vendor":         true,
	".pulumi":        true,
	".cloudcc-cache": true,
}

// Options controls a walk.
type Options struct {
	// Root is the directory to read.
	Root string
	// SkipPaths are additional root-relative paths to exclude, used to keep a
	// previous out_dir from being swallowed back into the input.
	SkipPaths []string
	// MaxFileSize skips files larger than this many bytes (0 = no limit).
	MaxFileSize int64
}

// Walk reads Root into a Set, parsing every .py file.
func Walk(opts Options) (*Set, error) {
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("reading source path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source path %s is not a directory", opts.Root)
	}

	skip := map[string]bool{}
	for _, p := range opts.SkipPaths {
		if p == "" {
			continue
		}
		skip[filepath.ToSlash(filepath.Clean(p))] = true
	}

	set := NewSet(root)
	walkErr := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, abs)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if SkipDirs[d.Name()] || skip[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if skip[rel] || !d.Type().IsRegular() {
			return nil
		}
		if opts.MaxFileSize > 0 {
			fi, ferr := d.Info()
			if ferr == nil && fi.Size() > opts.MaxFileSize {
				return nil
			}
		}
		content, rerr := os.ReadFile(abs)
		if rerr != nil {
			return fmt.Errorf("reading %s: %w", rel, rerr)
		}
		f := &File{Path: rel, Content: content, SHA256: Fingerprint(content)}
		if strings.HasSuffix(rel, ".py") {
			if perr := f.ParsePython(); perr != nil {
				return perr
			}
		}
		set.Add(f)
		return nil
	})
	if walkErr != nil {
		set.Close()
		return nil, walkErr
	}

	set.RequirementsPath, set.PyProjectPath = findDependencyManifests(set, root)
	return set, nil
}

// findDependencyManifests locates requirements.txt and pyproject.toml. It
// prefers a manifest inside the tree (shallowest first) and otherwise walks
// upward from the root, which is what makes a package inside a monorepo work.
func findDependencyManifests(set *Set, root string) (requirements, pyproject string) {
	shallowest := func(name string) string {
		var best string
		bestDepth := -1
		for _, p := range set.Paths() {
			if filepath.Base(p) != name {
				continue
			}
			depth := strings.Count(p, "/")
			if bestDepth < 0 || depth < bestDepth {
				best, bestDepth = p, depth
			}
		}
		return best
	}
	requirements = shallowest("requirements.txt")
	pyproject = shallowest("pyproject.toml")
	if requirements != "" && pyproject != "" {
		return
	}
	// Walk upward for anything still missing. Paths outside the tree are
	// returned as absolute, marked by a leading "/" that callers treat as
	// read-only reference material.
	dir := root
	for i := 0; i < 8; i++ {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		if requirements == "" {
			if p := filepath.Join(dir, "requirements.txt"); fileExists(p) {
				requirements = p
			}
		}
		if pyproject == "" {
			if p := filepath.Join(dir, "pyproject.toml"); fileExists(p) {
				pyproject = p
			}
		}
		if requirements != "" && pyproject != "" {
			break
		}
	}
	return
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// SortedCopy returns a sorted copy of s. Small helper used wherever a
// deterministic file list must be produced.
func SortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
