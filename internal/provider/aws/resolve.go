package aws

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"path"
	"sort"
	"strings"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
	"github.com/cloudcompiler/cloudcc/internal/sanitize"
)

// BuildDir is where bin/package.sh writes each unit's deployment artefact,
// relative to the output root.
const BuildDir = "build"

// Resolver expands intents into concrete AWS resources.
type Resolver struct {
	App     string
	Program *ir.Program
	Config  *config.App
	// StaticDir is where static site assets were written, relative to the
	// output root.
	StaticDir string

	// usedNames maps "service|name" to the capability id that claimed it.
	// Sanitising is lossy -- "gw.1" and "gw-1" reduce to the same string -- so
	// two distinct ids can arrive at one physical name and silently share a
	// resource. Collisions are a property of the set of names in one
	// application, not of any name on its own, which is why they are resolved
	// here rather than inside the sanitisers.
	usedNames map[string]string
}

// uniqueName returns the physical name for a resource, disambiguating with a
// short digest of the id when a different id already claimed that name.
// Resolution runs in sorted order, so which id gets the plain name is stable.
func (r *Resolver) uniqueName(service, id string, sanitise func(app, id string) string) string {
	if r.usedNames == nil {
		r.usedNames = map[string]string{}
	}
	name := sanitise(r.App, id)
	if owner, taken := r.usedNames[service+"|"+name]; taken && owner != id {
		name = sanitise(r.App, id+"-"+shortDigest(id))
	}
	r.usedNames[service+"|"+name] = id
	return name
}

// shortDigest is a stable eight-character tag derived from an id.
func shortDigest(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:8]
}

// Resolve expands every intent in the program. Iteration is over sorted keys
// so the resource set -- and therefore the generated project -- is
// byte-deterministic (D18).
func (r *Resolver) Resolve() error {
	for _, in := range r.Program.Intents() {
		var err error
		switch typed := in.(type) {
		case *ir.Persist:
			err = r.persist(typed)
		case *ir.Topic:
			err = r.topic(typed)
		case *ir.StaticSite:
			err = r.staticSite(typed)
		case *ir.ConfigVar:
			// Config values are pure environment wiring; they resolve to no
			// infrastructure of their own (D17).
		}
		if err != nil {
			return err
		}
	}
	// Execution units and gateways come second: they reference the stores
	// above, so those resources must already exist in the graph.
	for _, in := range r.Program.IntentsOfKind(config.KindExecutionUnit) {
		if err := r.execUnit(in.(*ir.ExecUnit)); err != nil {
			return err
		}
	}
	for _, in := range r.Program.IntentsOfKind(config.KindExpose) {
		if err := r.expose(in.(*ir.Expose)); err != nil {
			return err
		}
	}
	// Policies and environment ordering read what a unit uses, and a unit can
	// use another unit, so this needs every unit to already exist.
	//
	// It also has to come *before* subscriptions. A subscription resolves from
	// the topic intent, so once it exists every publisher's `uses` edge reaches
	// it too -- and a subscription depends on the subscriber's function, which
	// uses the topic, which is a cycle in the emitted project's declaration
	// order. Running first means this sees the topic and not the plumbing
	// hanging off it.
	if err := r.unitWiring(); err != nil {
		return err
	}
	// Subscriptions reference both a topic and a function, so they come last.
	if err := r.subscriptions(); err != nil {
		return err
	}
	r.linkReferences()
	return nil
}

// linkReferences turns every reference inside a resource's properties into a
// dependency edge.
//
// Declaration order in the emitted project follows these edges, so a resource
// that mentions another must depend on it. Leaving this to each mapping to
// remember is exactly the kind of invisible ordering requirement the plugin
// DAG was meant to eliminate, so it is enforced here once for everything.
func (r *Resolver) linkReferences() {
	for _, res := range r.Program.Resources() {
		for _, ref := range ir.RefsIn(res.Props()) {
			if ref.Key == res.Key() {
				continue
			}
			if _, ok := r.Program.Resource(ref.Key); ok {
				r.Program.Connect(res.Key(), ref.Key, ir.EdgeDependsOn)
			}
		}
		for _, binding := range res.EnvOutputs() {
			for _, ref := range ir.RefsIn(binding.Expr) {
				if ref.Key == res.Key() {
					continue
				}
				if _, ok := r.Program.Resource(ref.Key); ok {
					r.Program.Connect(res.Key(), ref.Key, ir.EdgeDependsOn)
				}
			}
		}
	}
}

// ---------------------------------------------------------------- stores

func (r *Resolver) persist(p *ir.Persist) error {
	switch p.Kind {
	case config.KindPersistKV:
		return r.dynamoTable(p)
	case config.KindPersistFS:
		return r.fsBucket(p)
	case config.KindPersistSecret:
		return r.secret(p)
	case config.KindPersistORM:
		return r.database(p)
	case config.KindPersistRedis:
		return r.cache(p)
	}
	return fmt.Errorf("no AWS mapping for %s", p.Kind)
}

func (r *Resolver) dynamoTable(p *ir.Persist) error {
	props := map[string]any{
		"name":        r.uniqueName("dynamodb", p.ID, sanitize.DynamoTable),
		"billingMode": "PAY_PER_REQUEST",
		"hashKey":     "id",
		"attributes": []any{
			map[string]any{"name": "id", "type": "S"},
		},
	}
	r.resolve(p.Key(), KindDynamoTable, p.ID, "aws.dynamodb.Table", props,
		ir.Env(EnvKVTable(p.ID), "name"), p.Config())
	return nil
}

func (r *Resolver) fsBucket(p *ir.Persist) error {
	props := map[string]any{
		"bucket":       r.uniqueName("s3", p.ID, sanitize.S3Bucket),
		"forceDestroy": true,
	}
	r.resolve(p.Key(), KindS3Bucket, p.ID, "aws.s3.BucketV2", props,
		ir.Env(EnvFSBucket(p.ID), "bucket"), p.Config())
	return nil
}

func (r *Resolver) secret(p *ir.Persist) error {
	props := map[string]any{
		"name":                 r.uniqueName("secretsmanager", p.ID, sanitize.SecretName),
		"recoveryWindowInDays": 0,
	}
	r.resolve(p.Key(), KindSecret, p.ID, "aws.secretsmanager.Secret", props,
		ir.Env(EnvSecretARN(p.ID), "arn"), p.Config())
	return nil
}

func (r *Resolver) database(p *ir.Persist) error {
	name := r.uniqueName("rds", p.ID, sanitize.RDSIdentifier)
	dbName := sanitize.DBIdentifier(p.ID)
	engine, port := "postgres", 5432
	if p.Config().Type == "rds_mysql" {
		engine, port = "mysql", 3306
	}
	props := map[string]any{
		"identifier":               name,
		"engine":                   engine,
		"instanceClass":            "db.t4g.micro",
		"allocatedStorage":         20,
		"dbName":                   dbName,
		"username":                 "ccadmin",
		"manageMasterUserPassword": true,
		"skipFinalSnapshot":        true,
		"publiclyAccessible":       false,
		"dbSubnetGroupName":        ir.Ref{Key: r.subnetGroup("rds", "aws.rds.SubnetGroup"), Prop: "name"},
		"vpcSecurityGroupIds":      []any{ir.Ref{Key: securityGroupKey(), Prop: "id"}},
	}
	key := ir.Key{Kind: KindRDS, ID: p.ID}
	// AWS manages the master password, so the URL carries no credential: the
	// shim fetches the password from the managed secret and splices it in.
	// Putting a password in an environment variable would defeat D21.
	scheme := "postgresql"
	if engine == "mysql" {
		scheme = "mysql"
	}
	_ = port
	url := ir.Lit(
		scheme+"://ccadmin@", ir.Ref{Key: key, Prop: "address"},
		":", ir.Ref{Key: key, Prop: "port"},
		"/", dbName,
	)
	r.resolve(p.Key(), KindRDS, p.ID, "aws.rds.Instance", props, ir.Env(
		EnvORMURL(p.ID), ir.FromExpr(url),
		EnvORMSecretARN(p.ID), ir.FromExpr(ir.Ref{Key: key, Prop: "masterUserSecrets.apply(s => s[0].secretArn)"}),
	), p.Config())
	return nil
}

func (r *Resolver) cache(p *ir.Persist) error {
	name := r.uniqueName("elasticache", p.ID, sanitize.ElastiCacheCluster)
	if p.Config().Type == "memorydb" {
		props := map[string]any{
			"name":             name,
			"nodeType":         "db.t4g.small",
			"numShards":        1,
			"aclName":          "open-access",
			"tlsEnabled":       true,
			"subnetGroupName":  ir.Ref{Key: r.subnetGroup("memorydb", "aws.memorydb.SubnetGroup"), Prop: "name"},
			"securityGroupIds": []any{ir.Ref{Key: securityGroupKey(), Prop: "id"}},
		}
		key := ir.Key{Kind: KindMemoryDB, ID: p.ID}
		// A list output cannot be indexed directly in TypeScript, so the
		// element is selected inside an apply.
		r.resolve(p.Key(), KindMemoryDB, p.ID, "aws.memorydb.Cluster", props, ir.Env(
			EnvRedisEndpoint(p.ID), ir.FromExpr(ir.Ref{Key: key, Prop: "clusterEndpoints.apply(e => e[0].address)"}),
			EnvRedisPort(p.ID), ir.FromExpr("6379"),
			EnvRedisTLS(p.ID), ir.FromExpr("true"),
		), p.Config())
		return nil
	}
	props := map[string]any{
		"clusterId":        name,
		"engine":           "redis",
		"nodeType":         "cache.t4g.micro",
		"numCacheNodes":    1,
		"port":             6379,
		"subnetGroupName":  ir.Ref{Key: r.subnetGroup("elasticache", "aws.elasticache.SubnetGroup"), Prop: "name"},
		"securityGroupIds": []any{ir.Ref{Key: securityGroupKey(), Prop: "id"}},
	}
	key := ir.Key{Kind: KindElastiCache, ID: p.ID}
	r.resolve(p.Key(), KindElastiCache, p.ID, "aws.elasticache.Cluster", props, ir.Env(
		EnvRedisEndpoint(p.ID), ir.FromExpr(ir.Ref{Key: key, Prop: "cacheNodes.apply(n => n[0].address)"}),
		EnvRedisPort(p.ID), ir.FromExpr("6379"),
		EnvRedisTLS(p.ID), ir.FromExpr("false"),
	), p.Config())
	return nil
}

func (r *Resolver) topic(t *ir.Topic) error {
	props := map[string]any{"name": r.uniqueName("sns", t.ID, sanitize.SNSTopic)}
	r.resolve(t.Key(), KindSNSTopic, t.ID, "aws.sns.Topic", props,
		ir.Env(EnvTopicARN(t.ID), "arn"), t.Config())
	return nil
}

func (r *Resolver) staticSite(s *ir.StaticSite) error {
	bucketKey := ir.Key{Kind: KindS3Bucket, ID: s.ID}
	bucketProps := map[string]any{
		"bucket":       r.uniqueName("s3", s.ID, sanitize.S3Bucket),
		"forceDestroy": true,
	}
	r.resolve(s.Key(), KindS3Bucket, s.ID, "aws.s3.BucketV2", bucketProps,
		ir.Env(EnvStaticBucket(s.ID), "bucket"), s.Config())

	website := ir.NewResource(KindS3Website, s.ID, "aws.s3.BucketWebsiteConfigurationV2", map[string]any{
		"bucket":        ir.Ref{Key: bucketKey, Prop: "id"},
		"indexDocument": map[string]any{"suffix": s.IndexDocument},
	}, ir.Env(EnvStaticURL(s.ID), "websiteEndpoint"))
	r.Program.Resolve(s.Key(), website)
	r.Program.Connect(website.Key(), bucketKey, ir.EdgeDependsOn)

	// One object per claimed file, in sorted order, so the emitted project is
	// stable across compiles.
	files := append([]string(nil), s.Files...)
	sort.Strings(files)
	for _, rel := range files {
		objectID := s.ID + "/" + siteRelative(s, rel)
		obj := ir.NewResource(KindS3Object, objectID, "aws.s3.BucketObject", map[string]any{
			"bucket":      ir.Ref{Key: bucketKey, Prop: "id"},
			"key":         siteRelative(s, rel),
			"source":      ir.Raw(fmt.Sprintf("new pulumi.asset.FileAsset(%q)", path.Join(r.StaticDir, s.ID, siteRelative(s, rel)))),
			"contentType": contentType(rel),
		}, nil)
		r.Program.Resolve(s.Key(), obj)
		r.Program.Connect(obj.Key(), bucketKey, ir.EdgeDependsOn)
	}
	return nil
}

// siteRelative strips the declaring module's directory and the glob's fixed
// root from an asset path, so public/index.html is served as index.html.
func siteRelative(s *ir.StaticSite, rel string) string {
	base := trimDir(rel, s.Root)
	return trimDir(base, globRootDir(s.StaticFiles))
}

func trimDir(p, dir string) string {
	if dir == "" || dir == "." {
		return p
	}
	if strings.HasPrefix(p, dir+"/") {
		return p[len(dir)+1:]
	}
	return p
}

func globRootDir(pattern string) string {
	pattern = strings.TrimPrefix(pattern, "./")
	dir := ""
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '/' {
			continue
		}
		seg := pattern[:i]
		if strings.ContainsAny(seg, "*?[{") {
			return dir
		}
		dir = seg
	}
	return dir
}

func contentType(rel string) string {
	if ct := mime.TypeByExtension(path.Ext(rel)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// resolve adds a concrete resource, merges pulumi_params over its props, and
// records the resolves_to edge.
func (r *Resolver) resolve(intent ir.Key, kind, id, template string, props map[string]any,
	env map[string]ir.EnvBinding, cfg config.ResourceConfig) *ir.GenericResource {
	merged := config.DeepMerge(props, r.Config.AllPulumiParams(cfg))
	res := ir.NewResource(kind, id, template, merged, env)
	r.Program.Resolve(intent, res)
	return res
}
