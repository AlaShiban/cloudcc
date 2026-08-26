package capabilities

import (
	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/provider/aws"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
)

// persistKinds are the capability kinds this plugin handles.
var persistKinds = []string{
	config.KindPersistKV,
	config.KindPersistFS,
	config.KindPersistSecret,
	config.KindPersistORM,
	config.KindPersistRedis,
}

// PersistPlugin turns persist_* hints into store intents, with a `uses` edge
// from every execution unit that can reach the declaration.
//
// One id means one store. Two units that both import the module declaring
// cloudcc.persist_kv("petsByOwner") share a single table and each get their own
// environment binding -- the multi-unit case Klotho 1 never exercised.
type PersistPlugin struct{ base }

// NewPersistPlugin returns the persist stage.
func NewPersistPlugin() *PersistPlugin {
	return &PersistPlugin{base{name: PluginPersist, deps: []string{PluginExecUnits}}}
}

func (p *PersistPlugin) Transform(ctx *compiler.Context) error {
	for _, kind := range persistKinds {
		for _, h := range ctx.HintsFor(kind) {
			id := h.ID()
			store := &ir.Persist{Kind: kind, Models: h.StrList("models"), Library: h.ClientLibrary}
			store.ID = id

			existing, seen := ctx.Graph.Intent(store.Key())
			if seen {
				// The same id declared twice with a different capability is a
				// genuine conflict; the same capability twice is just a
				// re-declaration and converges on one node.
				store = existing.(*ir.Persist)
			} else {
				cfg := ctx.Config.Lookup(kind, id)
				// The client a program reached for is the weakest layer of
				// configuration: a Redis client asks for ElastiCache, but
				// cloudcc.yaml still gets to say MemoryDB. An explicit entry
				// for this id therefore wins; the client only fills a gap.
				if h.ClientType != "" && !ctx.Config.HasExplicitType(kind, id) {
					cfg.Type = h.ClientType
				}
				if err := store.Configure(cfg); err != nil {
					return err
				}
				ctx.Config.Record(kind, id, cfg)
			}
			if other, clash := findOtherKind(ctx, id, kind); clash {
				ctx.Diags.Errorf(ctx.HintPos(h), kind,
					"%q is already declared as %s; each id names one store", id, other)
				continue
			}
			ctx.Graph.AddIntent(store)
			p.connectUsers(ctx, h, store.Key())
		}
	}
	return nil
}

// connectUsers records a uses edge from every unit that bundles the declaring
// file, which is what later drives both environment wiring and IAM policies.
func (p *PersistPlugin) connectUsers(ctx *compiler.Context, h sdkdetect.Hint, target ir.Key) {
	units := ctx.UnitsFor(h.File)
	if len(units) == 0 {
		ctx.Diags.Warnf(ctx.HintPos(h), h.Capability,
			"%q is declared in a file no execution unit imports, so nothing will be wired to it", h.ID())
		return
	}
	for _, unit := range units {
		unitKey := ir.Key{Kind: config.KindExecutionUnit, ID: unit}
		if _, ok := ctx.Graph.Intent(unitKey); !ok {
			continue
		}
		ctx.Graph.Connect(unitKey, target, ir.EdgeUses)
	}
}

// findOtherKind reports a conflicting declaration of the same id under a
// different persist capability.
func findOtherKind(ctx *compiler.Context, id, kind string) (string, bool) {
	for _, other := range persistKinds {
		if other == kind {
			continue
		}
		if _, ok := ctx.Graph.Intent(ir.Key{Kind: other, ID: id}); ok {
			return other, true
		}
	}
	return "", false
}

// topicRequires reads the requirement set from the wrapped Topic()'s
// arguments, falling back to the defaults a bare Topic() has always meant:
// fan-out, unordered, at-least-once, which is SNS.
func topicRequires(args map[string]any) ir.TopicRequires {
	d := aws.DefaultTopicRequirements()
	out := ir.TopicRequires{
		Subscribers:  d.Subscribers,
		Ordering:     d.Ordering,
		Delivery:     d.Delivery,
		MaxMessageKB: d.MaxMessageKB,
	}
	str := func(key string, into *string) {
		if v, ok := args[key].(string); ok {
			*into = v
		}
	}
	str("subscribers", &out.Subscribers)
	str("ordering", &out.Ordering)
	str("delivery", &out.Delivery)
	if v, ok := args["replay"].(bool); ok {
		out.Replay = v
	}
	if v, ok := args["retention_hours"].(int); ok {
		out.RetentionHours = v
	}
	if v, ok := args["max_message_kb"].(int); ok {
		out.MaxMessageKB = v
	}
	return out
}

// requirementsOf converts the IR's copy back into the provider's type. They are
// separate on purpose: the IR is provider-agnostic (D7), and a second provider
// would read the same requirements and reach different services.
func requirementsOf(r ir.TopicRequires) aws.TopicRequirements {
	return aws.TopicRequirements{
		Subscribers:    r.Subscribers,
		Ordering:       r.Ordering,
		Delivery:       r.Delivery,
		Replay:         r.Replay,
		RetentionHours: r.RetentionHours,
		MaxMessageKB:   r.MaxMessageKB,
	}
}

// PubSubPlugin turns pubsub_topic hints into Topic intents and records which
// units publish to and subscribe from them.
type PubSubPlugin struct{ base }

// NewPubSubPlugin returns the pubsub stage.
func NewPubSubPlugin() *PubSubPlugin {
	return &PubSubPlugin{base{name: PluginPubSub, deps: []string{PluginExecUnits}}}
}

func (p *PubSubPlugin) Transform(ctx *compiler.Context) error {
	for _, h := range ctx.HintsFor(config.KindPubSub) {
		id := h.ID()
		topic := &ir.Topic{}
		topic.ID = id

		if _, seen := ctx.Graph.Intent(topic.Key()); !seen {
			cfg := ctx.Config.Lookup(config.KindPubSub, id)
			topic.Requires = topicRequires(h.ClientArgs)

			// The backing is chosen from the requirements, not configured.
			// A type in cloudcc.yaml is checked against them rather than
			// obeyed: everywhere else the file is the stronger layer because
			// the choice is between variants that behave alike, but SNS and a
			// FIFO queue do not behave alike, and a file that overrode this
			// would be asking for messages to arrive out of order.
			choice, err := aws.SelectTopicBacking(requirementsOf(topic.Requires))
			if err != nil {
				ctx.Diags.Errorf(ctx.HintPos(h), config.KindPubSub,
					"topic %q: %s", id, err.Error())
			} else if ctx.Config.HasExplicitType(config.KindPubSub, id) {
				if err := aws.TopicSatisfies(cfg.Type, requirementsOf(topic.Requires)); err != nil {
					ctx.Diags.Errorf(ctx.HintPos(h), config.KindPubSub,
						"topic %q: %s", id, err.Error())
				}
				topic.Because = choice.Because
			} else {
				cfg.Type = choice.Type
				topic.Because = choice.Because
			}

			if err := topic.Configure(cfg); err != nil {
				return err
			}
			ctx.Config.Record(config.KindPubSub, id, cfg)
			ctx.Graph.AddIntent(topic)
		}

		handle := h.Receives
		if handle == "" {
			ctx.Diags.Warnf(ctx.HintPos(h), config.KindPubSub,
				"the topic is not assigned to a variable, so no publisher or subscriber can be detected")
		}

		// Every unit that bundles the declaring file is wired to the topic,
		// exactly as it is for a store -- and for the same reason, which has
		// nothing to do with whether it sends or receives anything.
		//
		// The declaration is executed at import: the shim reads the topic's ARN
		// out of the environment when the module loads. A module that declares
		// two topics is one import, so a unit that subscribes to the second one
		// still runs the line that connects the first. Without this edge that
		// unit is handed an ARN for one topic and not the other, and dies on
		// startup with a message about an environment variable -- deployed,
		// where the two topics happen to be declared side by side.
		//
		// It is invisible locally, because a locally run unit is usually given
		// every stack output at once. That is what makes it worth an edge
		// rather than a rule about how to write modules.
		p.connectHolders(ctx, h, topic.Key())

		for _, unitID := range config.SortedKeys(ctx.UnitFiles) {
			unitKey := ir.Key{Kind: config.KindExecutionUnit, ID: unitID}
			if _, ok := ctx.Graph.Intent(unitKey); !ok {
				continue
			}
			front, ok := ctx.Frontend(unitID)
			if !ok {
				continue
			}
			publishes, subscribes := false, false
			for _, call := range front.MethodCalls(ctx.Files, ctx.UnitFiles[unitID]) {
				if call.Object != handle {
					continue
				}
				switch call.Method {
				case "publish":
					publishes = true
				case "subscribe":
					subscribes = true
				}
			}
			if publishes {
				ctx.Graph.Connect(unitKey, topic.Key(), ir.EdgePublishes)
				ctx.Graph.Connect(unitKey, topic.Key(), ir.EdgeUses)
			}
			if subscribes {
				ctx.Graph.Connect(unitKey, topic.Key(), ir.EdgeSubscribes)
				ctx.Graph.Connect(unitKey, topic.Key(), ir.EdgeUses)
			}
		}
	}
	return nil
}

// connectHolders records a uses edge from every unit that bundles the file
// declaring a topic, which is what puts the topic's ARN in that unit's
// environment. Publishing and subscribing are recorded separately, and they are
// what IAM and the subscriptions are derived from -- holding a handle grants
// nothing.
func (p *PubSubPlugin) connectHolders(ctx *compiler.Context, h sdkdetect.Hint, target ir.Key) {
	for _, unit := range ctx.UnitsFor(h.File) {
		unitKey := ir.Key{Kind: config.KindExecutionUnit, ID: unit}
		if _, ok := ctx.Graph.Intent(unitKey); ok {
			ctx.Graph.Connect(unitKey, target, ir.EdgeUses)
		}
	}
}

// ConfigVarsPlugin turns config_value hints into ConfigVar intents. There is
// no separate env-vars plugin: a unit's environment is computed at render time
// from its config vars plus the EnvOutputs of everything it uses (D17).
type ConfigVarsPlugin struct{ base }

// NewConfigVarsPlugin returns the config-vars stage.
func NewConfigVarsPlugin() *ConfigVarsPlugin {
	return &ConfigVarsPlugin{base{name: PluginConfigVars, deps: []string{PluginExecUnits}}}
}

func (p *ConfigVarsPlugin) Transform(ctx *compiler.Context) error {
	for _, h := range ctx.HintsFor(config.KindConfig) {
		id := h.ID()
		v := &ir.ConfigVar{Default: h.Str("default"), Secret: h.Bool("secret")}
		v.ID = id

		if existing, seen := ctx.Graph.Intent(v.Key()); seen {
			prev := existing.(*ir.ConfigVar)
			if prev.Secret != v.Secret {
				ctx.Diags.Errorf(ctx.HintPos(h), config.KindConfig,
					"%q is declared both as a secret and as a plain value", id)
			}
			if prev.Default != v.Default && v.Default != "" && prev.Default != "" {
				ctx.Diags.Warnf(ctx.HintPos(h), config.KindConfig,
					"%q has conflicting defaults %q and %q; %q wins", id, prev.Default, v.Default, prev.Default)
			}
			v = prev
		} else {
			cfg := ctx.Config.Lookup(config.KindConfig, id)
			if cfg.Value != "" {
				v.Default = cfg.Value
			}
			if cfg.Secret {
				v.Secret = true
			}
			cfg.Secret = v.Secret
			cfg.Value = v.Default
			if err := v.Configure(cfg); err != nil {
				return err
			}
			ctx.Config.Record(config.KindConfig, id, cfg)
			ctx.Graph.AddIntent(v)
		}

		for _, unit := range ctx.UnitsFor(h.File) {
			unitKey := ir.Key{Kind: config.KindExecutionUnit, ID: unit}
			if _, ok := ctx.Graph.Intent(unitKey); ok {
				ctx.Graph.Connect(unitKey, v.Key(), ir.EdgeUses)
			}
		}
	}
	return nil
}
