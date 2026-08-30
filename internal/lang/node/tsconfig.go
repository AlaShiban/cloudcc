package node

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/source"
)

// tsconfig is the part of a TypeScript project's configuration that decides
// which file a specifier names.
//
// Only the resolution keys are read. Everything else in a tsconfig describes
// what the *type checker* does, and this compiler does not typecheck -- the
// bundler does the rest, and reading settings we then ignore would be a
// configuration that looks applied and is not.
type tsconfig struct {
	// Dir is the directory holding the tsconfig, relative to the source root.
	// Every path below is resolved against it.
	Dir string
	// BaseURL is `compilerOptions.baseUrl`, joined to Dir. A bare specifier is
	// resolved against it before being treated as a package.
	BaseURL string
	// Paths is `compilerOptions.paths`: a pattern such as "@/*" mapped to the
	// locations that satisfy it, in the order TypeScript would try them.
	Paths map[string][]string
}

// raw mirrors the JSON, so the merge of `extends` happens on the parsed form
// rather than on text.
type rawTSConfig struct {
	Extends         string `json:"extends"`
	CompilerOptions struct {
		BaseURL string              `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
}

// maxExtends is how deep an `extends` chain is followed.
//
// One level covers the common case -- a project extending a shared base -- and
// stopping there is reported rather than silently ignored, because a partially
// read configuration resolves *some* aliases and not others, which is the worst
// of both answers.
const maxExtends = 1

// resolver answers "which file does this specifier name", holding the
// per-directory tsconfigs it has already read.
//
// A struct rather than a package-level cache: the compiler may hold more than
// one program in a process (the fuzz suite does), and a cache keyed only by
// directory would leak one program's configuration into another's.
type resolver struct {
	files *source.Set
	// configs maps a directory to the nearest tsconfig at or above it. A miss
	// is recorded as nil, so the walk up the tree happens once per directory.
	configs map[string]*tsconfig
	// problems collects configurations that could not be read, so a caller can
	// report them once rather than per import.
	problems map[string]string
}

func newResolver(files *source.Set) *resolver {
	return &resolver{
		files:    files,
		configs:  map[string]*tsconfig{},
		problems: map[string]string{},
	}
}

// configFor returns the tsconfig governing a file, which is the nearest one at
// or above its directory. Nil when the program has none.
func (r *resolver) configFor(from string) *tsconfig {
	// Every directory on the way up gets the answer, not just the one that held
	// the file. Caching only the miss is a bug that hides until the second
	// lookup: "src" would be remembered as having no config, and the root
	// config found on its behalf would never be seen again.
	var walked []string
	dir := path.Dir(from)
	for {
		if cfg, seen := r.configs[dir]; seen {
			r.remember(walked, cfg)
			return cfg
		}
		walked = append(walked, dir)
		if cfg := r.readConfigIn(dir); cfg != nil {
			r.remember(walked, cfg)
			return cfg
		}
		if dir == "." || dir == "/" || dir == "" {
			r.remember(walked, nil)
			return nil
		}
		parent := path.Dir(dir)
		if parent == dir {
			r.remember(walked, nil)
			return nil
		}
		dir = parent
	}
}

func (r *resolver) remember(dirs []string, cfg *tsconfig) {
	for _, d := range dirs {
		r.configs[d] = cfg
	}
}

// readConfigIn reads dir/tsconfig.json, following `extends` one level.
func (r *resolver) readConfigIn(dir string) *tsconfig {
	raw, at, ok := r.readRaw(joinConfig(dir, "tsconfig.json"), 0)
	if !ok {
		return nil
	}
	cfg := &tsconfig{Dir: dir, Paths: map[string][]string{}}
	if raw.CompilerOptions.BaseURL != "" {
		cfg.BaseURL = path.Clean(path.Join(at, raw.CompilerOptions.BaseURL))
	}
	for pattern, targets := range raw.CompilerOptions.Paths {
		cfg.Paths[pattern] = targets
	}
	return cfg
}

// joinConfig joins a directory and a filename, keeping a root-level path bare
// so it matches how the source set spells it.
func joinConfig(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return path.Join(dir, name)
}

// readRaw reads one tsconfig *file* and merges what it extends underneath it.
// Returns the merged options and the directory paths inside them are relative
// to, which is the directory of the file that *declared* baseUrl.
//
// Keyed by file rather than by directory, because `extends` names a file:
// "./tsconfig.base.json" sits beside the config that extends it, and a
// directory-keyed read finds tsconfig.json again and recurses into itself.
func (r *resolver) readRaw(p string, depth int) (rawTSConfig, string, bool) {
	f, ok := r.files.Get(p)
	if !ok {
		return rawTSConfig{}, "", false
	}
	dir := path.Dir(p)

	var cur rawTSConfig
	if err := json.Unmarshal(stripJSONC(f.Content), &cur); err != nil {
		r.problems[p] = fmt.Sprintf("%s could not be read (%v), so its path aliases were not applied", p, err)
		return rawTSConfig{}, "", false
	}

	base := dir
	if cur.Extends != "" && strings.HasPrefix(cur.Extends, ".") {
		if depth >= maxExtends {
			r.problems[p] = fmt.Sprintf(
				"%s extends a configuration more than %d level deep; only the first is read, "+
					"so aliases declared further up were not applied", p, maxExtends)
		} else {
			parentPath := path.Clean(path.Join(dir, cur.Extends))
			if parent, parentAt, ok := r.readRaw(parentPath, depth+1); ok {
				// The child wins, key by key, which is what TypeScript does.
				if cur.CompilerOptions.BaseURL == "" && parent.CompilerOptions.BaseURL != "" {
					cur.CompilerOptions.BaseURL = parent.CompilerOptions.BaseURL
					base = parentAt
				}
				if cur.CompilerOptions.Paths == nil {
					cur.CompilerOptions.Paths = map[string][]string{}
				}
				for pattern, targets := range parent.CompilerOptions.Paths {
					if _, overridden := cur.CompilerOptions.Paths[pattern]; !overridden {
						cur.CompilerOptions.Paths[pattern] = targets
					}
				}
			}
		}
	} else if cur.Extends != "" {
		r.problems[p] = fmt.Sprintf(
			"%s extends %q, which is a package rather than a file in this tree, "+
				"so any aliases it declares were not applied", p, cur.Extends)
	}
	return cur, base, true
}

// aliasCandidates expands a specifier through a tsconfig's paths and baseUrl,
// returning the targets to try in order. The second result reports whether the
// specifier looked like an alias at all, which is what separates "this program
// meant a local file and it is missing" from "this is an npm package".
func (cfg *tsconfig) aliasCandidates(specifier string) ([]string, bool) {
	if cfg == nil {
		return nil, false
	}
	base := cfg.BaseURL
	if base == "" {
		base = cfg.Dir
	}

	var out []string
	matched := false
	// Longest pattern first: TypeScript prefers the most specific match, and
	// "@app/models/*" must win over "@app/*".
	for _, pattern := range sortedBySpecificity(cfg.Paths) {
		star := strings.IndexByte(pattern, '*')
		if star < 0 {
			if pattern != specifier {
				continue
			}
			matched = true
			for _, target := range cfg.Paths[pattern] {
				out = append(out, path.Clean(path.Join(base, target)))
			}
			continue
		}
		prefix, suffix := pattern[:star], pattern[star+1:]
		// The length check is not redundant with the two below it. For the
		// pattern "@a/*/b", the specifier "@a/b" starts with "@a/" and ends
		// with "/b" while being shorter than both together -- and the slice
		// that follows would panic on it. A tsconfig is user input.
		if len(specifier) < len(prefix)+len(suffix) ||
			!strings.HasPrefix(specifier, prefix) || !strings.HasSuffix(specifier, suffix) {
			continue
		}
		stem := specifier[len(prefix) : len(specifier)-len(suffix)]
		matched = true
		for _, target := range cfg.Paths[pattern] {
			out = append(out, path.Clean(path.Join(base, strings.Replace(target, "*", stem, 1))))
		}
	}

	// With baseUrl set and no pattern matched, TypeScript still resolves a bare
	// specifier against it. That one is not reported as an alias when it fails:
	// every npm package import also lands here, and warning about those would
	// bury the real cases.
	if cfg.BaseURL != "" {
		out = append(out, path.Clean(path.Join(cfg.BaseURL, specifier)))
	}
	return out, matched
}

// sortedKeys orders a map's keys, so a diagnostic set is emitted in the same
// order on every compile (D18).
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedBySpecificity orders patterns longest-prefix first, deterministically.
func sortedBySpecificity(paths map[string][]string) []string {
	out := make([]string, 0, len(paths))
	for pattern := range paths {
		out = append(out, pattern)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := len(out[i]), len(out[j])
		if li != lj {
			return li > lj
		}
		return out[i] < out[j]
	})
	return out
}

// stripJSONC removes the comments and trailing commas a tsconfig is allowed to
// contain and encoding/json is not.
//
// Written as a scanner rather than a regular expression because a `//` inside a
// string is not a comment, and a URL in a `paths` entry is exactly where one
// appears.
func stripJSONC(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString, escaped := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return removeTrailingCommas(out)
}

// removeTrailingCommas drops a comma that is followed only by whitespace and a
// closing brace or bracket.
func removeTrailingCommas(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString, escaped := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
				j++
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}
