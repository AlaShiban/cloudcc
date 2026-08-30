// Package lang is the seam between the compiler and the source language it is
// reading.
//
// Everything downstream of detection -- intents, provider resolution, the IaC
// backend, deployment -- is already language-neutral. What is not, and what
// this interface collects, is the handful of jobs that genuinely depend on
// reading a particular language: parsing it, seeing SDK calls in it, following
// its imports, finding its HTTP routes, rewriting it, and knowing how it is
// packaged for a runtime.
//
// A frontend is chosen per execution unit, from its entrypoint's extension, so
// a Python worker beside a Node API is not a special case.
package lang

import (
	"fmt"
	"sort"

	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
	"github.com/cloudcompiler/cloudcc/internal/source"
)

// Language names.
const (
	Python = "python"
	Node   = "node"
)

// Unresolved is a local import that matched no file in the source tree. Only
// imports that are unambiguously local are reported: an ordinary third-party
// dependency is not a problem, and warning about it would drown the useful
// signal.
type Unresolved struct {
	// Rendered is how the import was written, for the diagnostic.
	Rendered string
	// Offset is the statement's byte offset in the importing file.
	Offset int
	// Why explains a resolution the reader would not otherwise expect, and is
	// appended to the diagnostic. A relative import that names no file needs no
	// explanation; an alias that a tsconfig pattern claimed and could not
	// satisfy does, because there is no file on disk with that name to look for.
	Why string
}

// MethodCall is one `receiver.method(...)` site, which is how a publisher is
// told apart from a subscriber.
type MethodCall struct {
	Object string
	Method string
	File   string
	Offset int
}

// RemoteFunction is one function a unit's entry module offers to callers, as
// seen by cloudcc.remote.
//
// Async is recorded rather than filtered because the two cases need different
// answers. A name that is not here at all is a typo, and the fix is to spell it
// correctly. A name that is here but synchronous is a design question -- the
// call is going to take a network round trip whatever its signature says -- and
// the diagnostic has to be able to tell them apart.
type RemoteFunction struct {
	Name  string
	Async bool
	// Offset is the byte position of the definition, so a diagnostic about the
	// callee can point at the callee.
	Offset int
}

// Packaging is how one execution unit is built and run.
type Packaging struct {
	// LambdaRuntime is the managed runtime identifier, e.g. "python3.12".
	LambdaRuntime string
	// Handler is the Lambda handler string.
	Handler string
	// EntryModule is the entrypoint as the runtime refers to it.
	EntryModule string
	// Artifact is the deployment archive, relative to the output root.
	Artifact string
	// ContainerPort is the port a generated container listens on.
	ContainerPort int
}

// Frontend is everything about a source language the rest of the compiler does
// not want to know.
type Frontend interface {
	// Name is the language identifier, one of the constants above.
	Name() string

	// Owns reports whether a source path belongs to this language.
	Owns(path string) bool

	// Parse prepares a file for the queries the other methods run.
	Parse(f *source.File) error

	// Detect finds every SDK hint call in a parsed file.
	Detect(f *source.File, diags *diag.Diagnostics) []sdkdetect.Hint

	// Closure returns the transitive local-import closure of an entry module,
	// skipping anything already claimed by another capability.
	Closure(files *source.Set, entry string, excluded map[string]string) ([]string, []Unresolved)

	// Routes finds the HTTP routes declared on an exposed application object.
	Routes(files *source.Set, unitFiles []string, appVar string) []ir.Route

	// RouterWarning reports a router construct whose routes cannot be
	// discovered, so the user is told rather than left guessing. An empty
	// result means nothing to report.
	RouterWarning(files *source.Set, unitFiles []string) (file string, offset int, found bool)

	// MethodCalls returns every `receiver.method(...)` site in a unit.
	MethodCalls(files *source.Set, unitFiles []string) []MethodCall

	// RemoteFunctions returns the module-level functions an entry module
	// offers to callers of cloudcc.remote, sorted by name.
	//
	// The second result is whether this language can be called into at all,
	// which is a different question from whether a particular module offers
	// anything: a frontend with no inbound dispatcher returns false, and the
	// remote stage explains that gap rather than reporting that the function
	// the author asked for does not exist.
	RemoteFunctions(files *source.Set, entry string) ([]RemoteFunction, bool)

	// EntrypointCandidates returns the modules that could serve as the entry
	// for an undeclared single unit, best first.
	EntrypointCandidates(files *source.Set, exposedIn []string, excluded map[string]string) []string

	// Rewrite replaces SDK hint calls with runtime client calls, in place.
	// It is called for every source file, including those with no hints,
	// because an SDK import has to go whether or not it was used.
	Rewrite(f *source.File, hints []sdkdetect.Hint) error

	// RuntimeFiles returns the runtime client package injected into each
	// unit's bundle, keyed by path relative to the bundle root.
	RuntimeFiles() (map[string][]byte, error)

	// UnitFiles returns the generated per-unit files -- entrypoint,
	// dependency manifest, container build file -- for one unit.
	UnitFiles(u *ir.ExecUnit, opts UnitOptions) (map[string][]byte, error)

	// Packaging describes how the unit is built and run.
	Packaging(u *ir.ExecUnit) Packaging

	// PackagingScript returns the shell fragment that builds this unit's
	// deployment artefact, run from the output root.
	PackagingScript(u *ir.ExecUnit) string

	// Tools are the external programs this language needs, for `cloudcc doctor`.
	Tools() []Tool
}

// UnitOptions is the context a frontend needs to generate a unit's files.
type UnitOptions struct {
	// Manifest is the user's dependency manifest, empty when absent.
	Manifest []byte
	// ManifestPath is where that manifest came from, for diagnostics.
	ManifestPath string
	// Capabilities are the capability kinds this unit uses, sorted.
	Capabilities []string
	// Libraries are the client libraries this unit's stores declared, sorted.
	//
	// This is finer-grained than Capabilities on purpose: two Redis libraries
	// are both persist_redis but are different packages with different APIs,
	// so a program using ioredis must not be shipped node-redis. A frontend
	// adds the package for each, and a bundle carries the same client the
	// source declared and nothing it does not.
	Libraries []string
	// Container reports whether the unit is deployed as an image.
	Container bool
	// UserDockerfile reports that the user supplied their own build file, in
	// which case none is generated.
	UserDockerfile bool
}

// Tool is an external program a language needs.
type Tool struct {
	Name     string
	Binary   string
	Required bool
	Install  string
	Why      string
}

var registry = map[string]Frontend{}

// Register adds a frontend. Registering twice is a programming error.
func Register(f Frontend) {
	if _, dup := registry[f.Name()]; dup {
		panic(fmt.Sprintf("lang: frontend %q is already registered", f.Name()))
	}
	registry[f.Name()] = f
}

// Get returns a registered frontend by name.
func Get(name string) (Frontend, bool) {
	f, ok := registry[name]
	return f, ok
}

// Names returns the registered languages, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// All returns every registered frontend, in sorted-name order.
func All() []Frontend {
	out := make([]Frontend, 0, len(registry))
	for _, name := range Names() {
		out = append(out, registry[name])
	}
	return out
}

// For returns the frontend that owns a source path.
func For(path string) (Frontend, bool) {
	for _, f := range All() {
		if f.Owns(path) {
			return f, true
		}
	}
	return nil, false
}

// Owned reports whether any registered frontend claims the path.
func Owned(path string) bool {
	_, ok := For(path)
	return ok
}
