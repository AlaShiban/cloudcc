package ir

import (
	"github.com/cloudcompiler/cloudcc/internal/config"
)

// base carries the fields every intent shares.
type base struct {
	ID  string                `json:"id"`
	Cfg config.ResourceConfig `json:"config,omitempty"`
}

func (b *base) Configure(cfg config.ResourceConfig) error { b.Cfg = cfg; return nil }
func (b *base) Config() config.ResourceConfig             { return b.Cfg }

// ExecUnit is one deployable compute unit: a set of source files with an
// entrypoint module.
type ExecUnit struct {
	base
	// Entrypoints are the unit-root modules, relative to the source root and
	// sorted. A unit usually has one; several cloudcc.execution_unit calls sharing
	// an id give it several, and the closure is their union.
	Entrypoints []string `json:"entrypoints"`
	// Files is the sorted transitive local-import closure of Entrypoint,
	// relative to the source root. A file may belong to several units.
	Files []string `json:"files"`
	// ASGIApp names the module-level variable holding the ASGI app, when the
	// unit is exposed (e.g. "app"). Empty for non-HTTP units.
	ASGIApp string `json:"asgi_app,omitempty"`

	// The fields below are filled in by the shims stage, which decides how the
	// unit is packaged, and read by the provider resolver.

	// EntryModule is the dotted module name of Entrypoint within the bundle.
	EntryModule string `json:"entry_module,omitempty"`
	// Language is the frontend that owns this unit, chosen from its
	// entrypoint. A program may contain units in different languages.
	Language string `json:"language,omitempty"`
	// Runtime is the managed runtime identifier, e.g. "python3.12". It comes
	// from the unit's language, which is why the provider does not name one.
	Runtime string `json:"runtime,omitempty"`
	// Artifact is the deployment archive, relative to the output root.
	Artifact string `json:"artifact,omitempty"`
	// Handler is the Lambda handler string, empty for non-Lambda units.
	Handler string `json:"handler,omitempty"`
	// DockerfileProvided reports that the user supplied their own Dockerfile,
	// in which case cloudcc generated none (D13).
	DockerfileProvided bool `json:"dockerfile_provided,omitempty"`
}

// Entrypoint returns the unit's primary entry module: the exposed module when
// one is known, otherwise the first sorted entrypoint.
func (e *ExecUnit) Entrypoint() string {
	if len(e.Entrypoints) == 0 {
		return ""
	}
	return e.Entrypoints[0]
}

func (e *ExecUnit) Key() Key           { return Key{Kind: config.KindExecutionUnit, ID: e.ID} }
func (e *ExecUnit) Capability() string { return config.KindExecutionUnit }

// Route is one HTTP route discovered on an exposed ASGI app.
type Route struct {
	Verb string `json:"verb"`
	Path string `json:"path"`
}

// Expose is a public entrypoint fronting an execution unit.
type Expose struct {
	base
	// Target is the SDK's `target` argument, e.g. "public".
	Target string `json:"target"`
	// Unit is the execution unit this gateway fronts.
	Unit string `json:"unit"`
	// Routes are the routes discovered on the exposed app, sorted.
	Routes []Route `json:"routes"`
}

func (e *Expose) Key() Key           { return Key{Kind: config.KindExpose, ID: e.ID} }
func (e *Expose) Capability() string { return config.KindExpose }

// Persist is a stateful store. Kind is one of the persist_* capability names,
// which is what decides the resolution table row.
type Persist struct {
	base
	Kind string `json:"kind"`
	// Models lists ORM model names, when known (persist_orm only).
	Models []string `json:"models,omitempty"`
	// Library is the client library the program declared -- "ioredis",
	// "sqlalchemy-async" and so on. It decides which client the injected shim
	// builds and which package the bundle carries. Empty for the capabilities
	// this SDK supplies a class for, which have no library.
	Library string `json:"library,omitempty"`
}

func (p *Persist) Key() Key           { return Key{Kind: p.Kind, ID: p.ID} }
func (p *Persist) Capability() string { return p.Kind }

// Topic is a pub/sub topic.
type Topic struct {
	base
	// Requires is what the program declared about how its messages must move.
	// The backing service is chosen from it, so it belongs in the IR: a plan
	// that showed `sns` without saying which requirements produced it would be
	// unreviewable.
	Requires TopicRequires `json:"requires"`
	// Because names the requirements that forced the chosen backing, for the
	// plan and for the error when that backing is not implemented.
	Because string `json:"because,omitempty"`
}

// TopicRequires is the requirement set, mirroring the SDK's Topic arguments.
type TopicRequires struct {
	Subscribers    string `json:"subscribers"`
	Ordering       string `json:"ordering"`
	Delivery       string `json:"delivery"`
	Replay         bool   `json:"replay,omitempty"`
	RetentionHours int    `json:"retention_hours,omitempty"`
	MaxMessageKB   int    `json:"max_message_kb"`
}

func (t *Topic) Key() Key           { return Key{Kind: config.KindPubSub, ID: t.ID} }
func (t *Topic) Capability() string { return config.KindPubSub }

// StaticSite is a bundle of static assets served from object storage.
type StaticSite struct {
	base
	// StaticFiles and SharedFiles are the globs from the SDK call.
	StaticFiles string `json:"static_files"`
	SharedFiles string `json:"shared_files,omitempty"`
	// IndexDocument is the site's index, e.g. "index.html".
	IndexDocument string `json:"index_document"`
	// Files are the claimed paths relative to the source root, sorted.
	Files []string `json:"files"`
	// Root is the directory, relative to the source root, that Files are
	// uploaded relative to.
	Root string `json:"root"`
	// Prefix is the directory a site key is relative to: strip it from a
	// claimed path and what is left is the object's key.
	//
	// Recorded here rather than re-derived, because deriving it needs both the
	// declaring module's directory *and* the glob, resolved against each other
	// -- and doing that twice is how `../public/**/*` from a module in src/
	// ended up uploading `public/index.html` while the distribution asked for
	// `index.html` and got a 404.
	Prefix string `json:"prefix,omitempty"`
}

func (s *StaticSite) Key() Key           { return Key{Kind: config.KindStaticUnit, ID: s.ID} }
func (s *StaticSite) Capability() string { return config.KindStaticUnit }

// ConfigVar is a runtime configuration value delivered as an environment
// variable. Secret values become Pulumi stack secrets (D21).
type ConfigVar struct {
	base
	Default string `json:"default,omitempty"`
	Secret  bool   `json:"secret,omitempty"`
}

func (c *ConfigVar) Key() Key           { return Key{Kind: config.KindConfig, ID: c.ID} }
func (c *ConfigVar) Capability() string { return config.KindConfig }

// Compile-time proof that every intent type satisfies the interface.
var (
	_ Intent = (*ExecUnit)(nil)
	_ Intent = (*Expose)(nil)
	_ Intent = (*Persist)(nil)
	_ Intent = (*Topic)(nil)
	_ Intent = (*StaticSite)(nil)
	_ Intent = (*ConfigVar)(nil)
)
