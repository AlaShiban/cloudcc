package sdkdetect

import (
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/config"
)

// Client is what a wrapped client's type declares.
type Client struct {
	// Capability is the config kind, e.g. persist_redis.
	Capability string
	// Type is the concrete resource the library asks for. Empty when the
	// library serves several and something else has to choose -- a SQLAlchemy
	// engine is Postgres or MySQL depending on its URL.
	Type string
	// Why is shown in a diagnostic when a client is almost but not quite
	// recognised, so the message can say what was expected.
	Why string
}

// pythonClients maps a Python constructor to the capability it declares.
//
// Matching is on the final attribute of the call -- `redis.Redis`, `Redis` and
// `redis.asyncio.Redis` all reduce to "Redis" -- which is safe because this
// table is only consulted inside a persist() call, where the argument is
// already known to be a client.
var pythonClients = map[string]Client{
	"Redis":       {config.KindPersistRedis, "elasticache", "a Redis client"},
	"StrictRedis": {config.KindPersistRedis, "elasticache", "a Redis client"},
	"Valkey":      {config.KindPersistRedis, "elasticache", "a Valkey client"},

	"create_engine":       {config.KindPersistORM, "", "a SQLAlchemy engine"},
	"create_async_engine": {config.KindPersistORM, "", "a SQLAlchemy engine"},

	"Path":      {config.KindPersistFS, "s3", "a filesystem path"},
	"PosixPath": {config.KindPersistFS, "s3", "a filesystem path"},

	// Supplied by this SDK, because the ecosystem has no standard for these.
	"KVStore": {config.KindPersistKV, "dynamodb", "a key/value store"},
	"Topic":   {config.KindPubSub, "sns", "a topic"},
	"Secret":  {config.KindPersistSecret, "secretsmanager", "a secret"},
}

// nodeClients is the same table for JavaScript and TypeScript.
//
// It carries a FileStore that the Python table does not, and the asymmetry is
// real rather than an oversight: Python has pathlib.Path, so the SDK wraps that
// and the compiled program gets a cloudpathlib S3Path of the same shape. Node's
// fs module is a set of functions with no object to wrap, so a file store here
// has to be a class this package supplies.
var nodeClients = map[string]Client{
	"Redis":        {config.KindPersistRedis, "elasticache", "a Redis client"},
	"createClient": {config.KindPersistRedis, "elasticache", "a Redis client"},

	"Pool":      {config.KindPersistORM, "", "a SQL connection pool"},
	"Client":    {config.KindPersistORM, "", "a SQL client"},
	"Sequelize": {config.KindPersistORM, "", "a Sequelize instance"},
	"knex":      {config.KindPersistORM, "", "a Knex instance"},

	"KVStore":   {config.KindPersistKV, "dynamodb", "a key/value store"},
	"Topic":     {config.KindPubSub, "sns", "a topic"},
	"Secret":    {config.KindPersistSecret, "secretsmanager", "a secret"},
	"FileStore": {config.KindPersistFS, "s3", "a file store"},
}

// LookupClient resolves a constructor name for a language.
func LookupClient(language, constructor string) (Client, bool) {
	table := pythonClients
	if language == "node" {
		table = nodeClients
	}
	c, ok := table[constructor]
	return c, ok
}

// KnownClients lists the constructors a language recognises, sorted, for the
// diagnostic shown when one is not.
func KnownClients(language string) []string {
	table := pythonClients
	if language == "node" {
		table = nodeClients
	}
	out := make([]string, 0, len(table))
	for name := range table {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// RelationalType picks the concrete database from a connection URL, so the
// library a program reached for still supplies the default when that library
// speaks to several engines.
//
// An unrecognised or absent URL falls back to Postgres, which is the sensible
// default for a new service and is what cloudcc.yaml would otherwise have to
// say explicitly.
func RelationalType(url string) string {
	scheme := url
	if i := strings.Index(url, "://"); i >= 0 {
		scheme = url[:i]
	}
	// SQLAlchemy spells drivers as `postgresql+psycopg://`.
	if i := strings.Index(scheme, "+"); i >= 0 {
		scheme = scheme[:i]
	}
	switch strings.ToLower(scheme) {
	case "mysql", "mariadb":
		return "rds_mysql"
	case "postgresql", "postgres", "sqlite", "":
		return "rds_postgres"
	}
	return "rds_postgres"
}
