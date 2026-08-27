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
	// Library identifies which client the program actually reached for, so the
	// shim can hand back one of the same kind.
	//
	// Capability is not enough on its own. Two Redis libraries have different
	// APIs, and a synchronous SQLAlchemy engine is not an asynchronous one:
	// returning the wrong one compiles cleanly and then fails on the first
	// call, which is the failure this whole design exists to prevent.
	Library string
	// Why is shown in a diagnostic when a client is almost but not quite
	// recognised, so the message can say what was expected.
	Why string
}

// Client library identifiers. These are passed to the injected shim, which
// dispatches on them, so they are part of the contract between the compiler and
// the runtime rather than free-form labels.
const (
	LibRedisPy      = "redis-py"
	LibRedisPyAsync = "redis-py-async"
	LibSQLAlchemy   = "sqlalchemy"
	LibSQLAlchemyA  = "sqlalchemy-async"
	LibPathlib      = "pathlib"

	LibBoto3DDB = "boto3-dynamodb"

	LibIORedis   = "ioredis"
	LibNodeRedis = "node-redis"
	LibPg        = "pg"
	LibKnex      = "knex"
	LibAwsSdkDDB = "aws-sdk-dynamodb"
	LibAwsSdkS3  = "aws-sdk-s3"
)

// pythonClients maps a Python constructor to the capability it declares.
//
// Matching is on the final attribute of the call -- `redis.Redis`, `Redis` and
// `redis.asyncio.Redis` all reduce to "Redis" -- which is safe because this
// table is only consulted inside a persist() call, where the argument is
// already known to be a client.
var pythonClients = map[string]Client{
	"Redis":       {config.KindPersistRedis, "elasticache", LibRedisPy, "a Redis client"},
	"StrictRedis": {config.KindPersistRedis, "elasticache", LibRedisPy, "a Redis client"},
	"Valkey":      {config.KindPersistRedis, "elasticache", LibRedisPy, "a Valkey client"},

	"create_engine":       {config.KindPersistORM, "", LibSQLAlchemy, "a SQLAlchemy engine"},
	"create_async_engine": {config.KindPersistORM, "", LibSQLAlchemyA, "an async SQLAlchemy engine"},

	"Path":      {config.KindPersistFS, "s3", LibPathlib, "a filesystem path"},
	"PosixPath": {config.KindPersistFS, "s3", LibPathlib, "a filesystem path"},

	// A key/value store is a boto3 Table. There used to be a cloudcc.KVStore
	// class here; it is gone, because this project supplies no objects for data
	// stores. A supplied class is a dialect nobody else speaks, and its methods
	// have to be kept in step with the shim's forever.
	"Table": {config.KindPersistKV, "dynamodb", LibBoto3DDB, "a DynamoDB table"},

	// Supplied by this SDK: the two capabilities that are not data stores, and
	// so have no client to wrap. They have no library -- the shim's own class
	// is the only implementation.
	"Topic":  {config.KindPubSub, "sns", "", "a topic"},
	"Secret": {config.KindPersistSecret, "secretsmanager", "", "a secret"},
}

// nodeClients is the same table for JavaScript and TypeScript.
//
// Node has no pathlib and no boto3, so where Python wraps a Table or a Path,
// Node wraps the AWS SDK client for the same service. The client is not bound
// to one resource the way a boto3 Table is, so the shim installs a middleware
// that rewrites the logical name the program wrote -- the id it declared -- to
// the physical one the compiler chose. The program still names its own
// resources, which is what makes the uncompiled run readable.
var nodeClients = map[string]Client{
	"Redis":        {config.KindPersistRedis, "elasticache", LibIORedis, "an ioredis client"},
	"createClient": {config.KindPersistRedis, "elasticache", LibNodeRedis, "a node-redis client"},

	"Pool":   {config.KindPersistORM, "", LibPg, "a pg connection pool"},
	"Client": {config.KindPersistORM, "", LibPg, "a pg client"},
	"knex":   {config.KindPersistORM, "", LibKnex, "a Knex instance"},

	// Sequelize is deliberately absent. Its constructor takes the password up
	// front, with no async provider and no async connection factory, so a shim
	// could only return it from an async connect() -- which would make the
	// compiled binding a Promise where the uncompiled one is a client. An
	// unrecognised client is a compile error naming what is supported, which
	// is a much better outcome than a bundle that fails on its first query.

	"DynamoDBClient": {config.KindPersistKV, "dynamodb", LibAwsSdkDDB, "a DynamoDB client"},
	"S3Client":       {config.KindPersistFS, "s3", LibAwsSdkS3, "an S3 client"},

	"Topic":  {config.KindPubSub, "sns", "", "a topic"},
	"Secret": {config.KindPersistSecret, "secretsmanager", "", "a secret"},
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

// asyncModules are import paths whose objects are awaitable, keyed by the
// synchronous library they would otherwise be mistaken for.
//
// Two libraries ship both halves under one constructor name. SQLAlchemy gives
// them separate constructors -- create_engine against create_async_engine -- so
// the table above already tells them apart; redis-py does not, and
// `redis.asyncio.Redis` reduces to the same "Redis" as the synchronous one.
var asyncModules = map[string]struct {
	prefix  string
	library string
}{
	LibRedisPy: {"redis.asyncio", LibRedisPyAsync},
}

// RefineClient upgrades a client to its asynchronous variant when the fully
// qualified constructor says the program reached for one.
//
// qualified is the dotted path with its head expanded through the file's
// imports, or "" when the caller could not resolve it -- in which case the
// table's answer stands unchanged.
func RefineClient(c Client, qualified string) Client {
	rule, ok := asyncModules[c.Library]
	if !ok || qualified == "" {
		return c
	}
	if qualified == rule.prefix || strings.HasPrefix(qualified, rule.prefix+".") {
		c.Library = rule.library
		c.Why = "an async " + strings.TrimPrefix(c.Why, "a ")
	}
	return c
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
