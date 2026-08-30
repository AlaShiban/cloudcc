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
	// Before anything is built. A `resources:` block somewhere nothing reads it
	// is a setting that looks applied and is not, and finding that out from a
	// deployed stack is much worse than finding it out here.
	if err := CheckConfigurationIsSupported(r.Config); err != nil {
		return err
	}

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
		// Optional chaining, not s[0]. A list-valued output can be empty --
		// during the first up, and permanently against an emulator that
		// provisions the instance without reporting a managed secret -- and
		// indexing it then throws a TypeError that takes down the whole Pulumi
		// program, not just this binding. An empty string here means the shim
		// fails later with a message naming what is missing, which is a much
		// better failure than "Cannot read properties of undefined".
		EnvORMSecretARN(p.ID), ir.FromExpr(ir.Ref{Key: key, Prop: "masterUserSecrets.apply(s => s?.[0]?.secretArn ?? \"\")"}),
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
			EnvRedisEndpoint(p.ID), ir.FromExpr(ir.Ref{Key: key, Prop: "clusterEndpoints.apply(e => e?.[0]?.address ?? \"\")"}),
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
		EnvRedisEndpoint(p.ID), ir.FromExpr(ir.Ref{Key: key, Prop: "cacheNodes.apply(n => n?.[0]?.address ?? \"\")"}),
		EnvRedisPort(p.ID), ir.FromExpr("6379"),
		EnvRedisTLS(p.ID), ir.FromExpr("false"),
	), p.Config())
	return nil
}

func (r *Resolver) topic(t *ir.Topic) error {
	// Which service this is was decided from the program's requirements, not
	// from configuration -- see SelectTopicBacking. All this does is build it.
	switch t.Config().Type {
	case TopicSQS:
		return r.queue(t)
	case TopicSNS:
		props := map[string]any{"name": r.uniqueName("sns", t.ID, sanitize.SNSTopic)}
		r.resolve(t.Key(), KindSNSTopic, t.ID, "aws.sns.Topic", props, ir.Env(
			EnvTopicARN(t.ID), "arn",
			EnvTopicBacking(t.ID), ir.FromExpr(TopicSNS),
		), t.Config())
		return nil
	}
	return fmt.Errorf("no AWS mapping for topic type %q", t.Config().Type)
}

// queue builds the SQS form of a topic: one queue, consumed by whichever units
// subscribe to it.
//
// The difference from SNS is not only the resource. SNS pushes, so a
// subscription plus a resource policy on the function is the whole wiring; SQS
// is pulled, so the function needs an event source mapping and permission of
// its own -- both of which are attached in subscriptions().
func (r *Resolver) queue(t *ir.Topic) error {
	props := map[string]any{
		"name": r.uniqueName("sqs", t.ID, sanitize.SQSQueue),
		// How long a message a subscriber never got round to survives. Zero
		// means the program did not say, and AWS's own default (4 days) is the
		// honest answer to that.
		"visibilityTimeoutSeconds": r.queueVisibility(t),
	}
	if t.Requires.RetentionHours > 0 {
		props["messageRetentionSeconds"] = t.Requires.RetentionHours * 3600
	}
	r.resolve(t.Key(), KindSQSQueue, t.ID, "aws.sqs.Queue", props, ir.Env(
		// Both, because the two ends need different ones: a publisher sends to
		// a URL and a policy is written against an ARN.
		EnvTopicURL(t.ID), "url",
		EnvTopicARN(t.ID), "arn",
		EnvTopicBacking(t.ID), ir.FromExpr(TopicSQS),
	), t.Config())
	return nil
}

// queueVisibility is how long a message stays invisible after a consumer picks
// it up, and it is derived rather than defaulted.
//
// AWS refuses an event source mapping whose function can run for longer than
// its queue's visibility timeout, because a function still working on a message
// that has become visible again means a second consumer gets a copy. Defaulting
// this to 30 seconds and letting the user discover the rule from an API error
// eight minutes into a deploy is exactly the failure the compiler exists to
// take on: the subscribers are in the graph already, so their timeouts are
// knowable here.
func (r *Resolver) queueVisibility(t *ir.Topic) int {
	const floor = 30 // the AWS default for a queue, and for a function
	longest := floor
	for _, edge := range r.Program.EdgesTo(t.Key(), ir.EdgeSubscribes) {
		in, ok := r.Program.Intent(edge.From)
		if !ok {
			continue
		}
		unit, ok := in.(*ir.ExecUnit)
		if !ok {
			continue
		}
		if timeout := unit.Config().Timeout; timeout > longest {
			longest = timeout
		}
	}
	return longest
}

func (r *Resolver) staticSite(s *ir.StaticSite) error {
	bucketKey := ir.Key{Kind: KindS3Bucket, ID: s.ID}
	bucketProps := map[string]any{
		"bucket":       r.uniqueName("s3", s.ID, sanitize.S3Bucket),
		"forceDestroy": true,
	}
	r.resolve(s.Key(), KindS3Bucket, s.ID, "aws.s3.BucketV2", bucketProps,
		ir.Env(EnvStaticBucket(s.ID), "bucket"), s.Config())

	// Two shapes, and only one of them exists at a time. A CDN-fronted site has
	// no website endpoint -- the bucket stays private and is read through an
	// origin access identity -- so emitting the website configuration as well
	// would leave a second, public-looking address that serves nothing.
	switch s.Config().Type {
	case "cloudfront":
		if err := r.staticCDN(s, bucketKey); err != nil {
			return err
		}
	default:
		website := ir.NewResource(KindS3Website, s.ID, "aws.s3.BucketWebsiteConfigurationV2", map[string]any{
			"bucket":        ir.Ref{Key: bucketKey, Prop: "id"},
			"indexDocument": map[string]any{"suffix": s.IndexDocument},
		}, ir.Env(EnvStaticURL(s.ID), "websiteEndpoint"))
		r.Program.Resolve(s.Key(), website)
		r.Program.Connect(website.Key(), bucketKey, ir.EdgeDependsOn)
	}

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

// staticCDN puts a CloudFront distribution in front of a static site's bucket
// and closes the bucket to everything else.
//
// The bucket keeps no website configuration and no public access. What reaches
// the objects is an origin access identity: a principal CloudFront signs
// requests as, named in a bucket policy that grants it s3:GetObject and grants
// nobody else anything. The alternative -- a public bucket behind a CDN -- has
// two addresses for the same content, one of them cacheless and unlogged, and
// the bucket's URL outlives any decision made about the distribution.
//
// An origin access *identity* rather than the newer origin access *control*.
// OAC is what AWS documents today, and it is the better mechanism, but its
// bucket policy is written against the distribution's ARN while the
// distribution names the bucket -- a cycle that has to be broken by deploying
// twice or by an explicit dependency edge that says the policy may lag the
// thing it protects. OAI has no cycle. When this moves to OAC that is a change
// with a deploy-ordering consequence, which is why it is written down here.
func (r *Resolver) staticCDN(s *ir.StaticSite, bucketKey ir.Key) error {
	oai := ir.NewResource(KindCloudFrontOAI, s.ID, "aws.cloudfront.OriginAccessIdentity", map[string]any{
		"comment": "cloudcc " + r.App + " static unit " + s.ID,
	}, nil)
	r.Program.Resolve(s.Key(), oai)

	policy := ir.NewResource(KindS3BucketPolicy, s.ID, "aws.s3.BucketPolicy", map[string]any{
		"bucket": ir.Ref{Key: bucketKey, Prop: "id"},
		"policy": ir.JSONDoc{Value: map[string]any{
			"Version": "2012-10-17",
			"Statement": []any{map[string]any{
				"Effect":    "Allow",
				"Principal": map[string]any{"AWS": ir.Ref{Key: oai.Key(), Prop: "iamArn"}},
				"Action":    []any{"s3:GetObject"},
				"Resource":  []any{ir.Lit(ir.Ref{Key: bucketKey, Prop: "arn"}, "/*")},
			}},
		}},
	}, nil)
	r.Program.Resolve(s.Key(), policy)
	r.Program.Connect(policy.Key(), bucketKey, ir.EdgeDependsOn)
	r.Program.Connect(policy.Key(), oai.Key(), ir.EdgeDependsOn)

	originID := "s3-" + sanitize.Identifier(s.ID)
	dist := ir.NewResource(KindCloudFront, s.ID, "aws.cloudfront.Distribution", map[string]any{
		"enabled": true,
		"comment": "cloudcc " + r.App + " static unit " + s.ID,
		// The index document, which the bucket's website configuration would
		// otherwise have served. CloudFront only applies it at the root: a
		// request for /docs/ is not rewritten to /docs/index.html the way an S3
		// website would rewrite it. That is a real difference between the two
		// types and the reason they are two types.
		"defaultRootObject": s.IndexDocument,
		"origins": []any{map[string]any{
			"originId":   originID,
			"domainName": ir.Ref{Key: bucketKey, Prop: "bucketRegionalDomainName"},
			"s3OriginConfig": map[string]any{
				"originAccessIdentity": ir.Ref{Key: oai.Key(), Prop: "cloudfrontAccessIdentityPath"},
			},
		}},
		"defaultCacheBehavior": map[string]any{
			"targetOriginId": originID,
			// A static site is public content, and the whole point of the
			// bucket being private is that this is the only way in.
			"viewerProtocolPolicy": "redirect-to-https",
			"allowedMethods":       []any{"GET", "HEAD"},
			"cachedMethods":        []any{"GET", "HEAD"},
			"forwardedValues": map[string]any{
				"queryString": false,
				"cookies":     map[string]any{"forward": "none"},
			},
			"minTtl":     0,
			"defaultTtl": 3600,
			"maxTtl":     86400,
		},
		"restrictions": map[string]any{
			"geoRestriction": map[string]any{"restrictionType": "none"},
		},
		"viewerCertificate": map[string]any{"cloudfrontDefaultCertificate": true},
		// North America and Europe. The cheapest class that is still a CDN;
		// a site that needs the other edges says so with `pulumi_params`.
		"priceClass": "PriceClass_100",
	}, ir.Env(EnvStaticURL(s.ID), "domainName"))
	r.Program.Resolve(s.Key(), dist)
	r.Program.Connect(dist.Key(), bucketKey, ir.EdgeDependsOn)
	r.Program.Connect(dist.Key(), oai.Key(), ir.EdgeDependsOn)
	// Not for correctness -- CloudFront will serve once the policy lands -- but
	// so that a stack which has finished creating is a stack that serves. A
	// distribution reported as ready while its origin still returns 403 is a
	// deploy that looks done and is not.
	r.Program.Connect(dist.Key(), policy.Key(), ir.EdgeDependsOn)
	return nil
}

// siteRelative turns a claimed path into the object's key, so
// public/index.html is served as index.html.
//
// The prefix is read off the intent rather than derived here. It depends on the
// declaring module's directory and the glob resolved against each other, and
// deriving that in two places is how a `../public/**/*` glob from a module in
// src/ uploaded `public/index.html` while the distribution asked for
// `index.html` -- a stack that deployed clean and served a 404.
func siteRelative(s *ir.StaticSite, rel string) string {
	return trimDir(trimDir(rel, s.Root), s.Prefix)
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
