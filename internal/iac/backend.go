// Package iac defines the infrastructure-as-code backend interface and its
// registry.
//
// One implementation exists today, pulumi-ts. The interface is what lets an
// OpenTofu backend slot in later without touching provider resolution (D10):
// resolution produces resources, properties and environment bindings as data,
// and a backend decides how to spell them.
package iac

import (
	"fmt"
	"sort"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/spf13/afero"
)

// Request is everything a backend needs to emit a project.
type Request struct {
	Program *ir.Program
	Config  *config.App
	Out     afero.Fs
	// Units maps execution unit id to the environment variables it should
	// receive, beyond those derived from its uses edges. Config values land
	// here (D17).
	UnitEnv map[string]map[string]ir.EnvBinding
}

// Backend emits an infrastructure project.
type Backend interface {
	// Name is the registry key, e.g. "pulumi-ts".
	Name() string
	// Emit writes the project into req.Out.
	Emit(req Request) error
}

var registry = map[string]Backend{}

// Register adds a backend. Registering the same name twice is a programming
// error, so it panics rather than silently replacing.
func Register(b Backend) {
	if _, dup := registry[b.Name()]; dup {
		panic(fmt.Sprintf("iac: backend %q is already registered", b.Name()))
	}
	registry[b.Name()] = b
}

// Get returns a registered backend.
func Get(name string) (Backend, error) {
	b, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown infrastructure backend %q; available: %v", name, Names())
	}
	return b, nil
}

// Names returns the registered backend names, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
