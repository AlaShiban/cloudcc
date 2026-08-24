// Package aws resolves provider-agnostic intents into concrete AWS resources.
//
// This is the expansion pass the whole architecture is built around (D7):
// capability plugins say "a KV store named petsByOwner"; this package decides
// that means an aws.dynamodb.Table with these properties, this environment
// binding and this IAM policy. Nothing else in the compiler creates concrete
// resources, and no other provider exists -- an unknown --provider is rejected
// at flag validation rather than silently falling back (D9).
//
// The mapping is a data table, not a switch: a new resource kind is a new row.
package aws

import (
	"fmt"
	"sort"

	"github.com/cloudcompiler/cloudcc/internal/config"
)

// Level says how well the provider supports a configured type.
type Level int

const (
	// Unknown means the type is not a recognised value for the capability.
	Unknown Level = iota
	// NotYetSupported means the type is a real, planned option that this
	// version does not implement. Selecting it is a clean error, never a
	// silent substitution.
	NotYetSupported
	// Supported means the resolver can expand it.
	Supported
)

// typeSupport lists every type each capability accepts, and whether it works.
var typeSupport = map[string]map[string]Level{
	config.KindExecutionUnit: {
		"lambda": Supported,
		"ecs":    Supported,
		"eks":    NotYetSupported,
	},
	config.KindExpose: {
		"apigateway": Supported,
		"alb":        Supported,
	},
	config.KindPersistKV: {
		"dynamodb": Supported,
	},
	config.KindPersistFS: {
		"s3": Supported,
	},
	config.KindPersistSecret: {
		"secretsmanager": Supported,
	},
	config.KindPersistORM: {
		"rds_postgres":           Supported,
		"rds_mysql":              Supported,
		"cockroachdb_serverless": NotYetSupported,
	},
	config.KindPersistRedis: {
		"elasticache": Supported,
		"memorydb":    Supported,
	},
	config.KindPubSub: {
		"sns": Supported,
	},
	config.KindStaticUnit: {
		"s3": Supported,
	},
	config.KindConfig: {},
}

// Support reports how well the provider supports typ for kind.
func Support(kind, typ string) Level {
	types, ok := typeSupport[kind]
	if !ok {
		return Unknown
	}
	if len(types) == 0 {
		// Capabilities with no concrete type -- config values -- accept
		// anything, because nothing is selected from it.
		return Supported
	}
	return types[typ]
}

// SupportedTypes returns the working types for a capability, sorted.
func SupportedTypes(kind string) []string {
	var out []string
	for typ, level := range typeSupport[kind] {
		if level == Supported {
			out = append(out, typ)
		}
	}
	sort.Strings(out)
	return out
}

// AllTypes returns every accepted type for a capability, sorted, including the
// ones that are not yet implemented.
func AllTypes(kind string) []string {
	var out []string
	for typ := range typeSupport[kind] {
		out = append(out, typ)
	}
	sort.Strings(out)
	return out
}

// errUnsupported builds the error used when a configuration is accepted by the
// schema but cannot be resolved.
func errUnsupported(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

// NeedsVPC reports whether any of the resolved types requires a VPC. Lambda
// deliberately does not: putting a function in a VPC costs cold-start time and
// buys nothing unless it must reach a private resource.
func NeedsVPC(types []string) bool {
	for _, t := range types {
		switch t {
		case "ecs", "rds_postgres", "elasticache", "memorydb", "alb":
			return true
		}
	}
	return false
}
