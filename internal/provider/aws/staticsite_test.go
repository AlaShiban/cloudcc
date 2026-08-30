package aws

import (
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/ir"
)

// A static unit has two shapes. `s3` serves the objects from the bucket's own
// website endpoint; `cloudfront` keeps the bucket private and reaches it through
// an origin access identity. They are two types rather than a flag because they
// are not the same architecture -- and these tests are mostly about what each
// one must *not* leave behind from the other.

func siteProgram(t *testing.T, typ string) *ir.Program {
	t.Helper()

	p := ir.NewProgram()
	site := &ir.StaticSite{
		StaticFiles:   "public/**/*",
		IndexDocument: "index.html",
		Files:         []string{"public/index.html"},
		// What the static-units plugin works out and records. The resolver
		// reads it rather than deriving it, so a fixture that omits it is a
		// fixture describing a program the compiler cannot produce.
		Prefix: "public",
	}
	site.ID = "docs"
	if err := site.Configure(config.ResourceConfig{Type: typ}); err != nil {
		t.Fatal(err)
	}
	p.AddIntent(site)

	r := &Resolver{App: "test", Program: p, Config: config.New(), StaticDir: "static"}
	if err := r.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return p
}

func TestABucketBackedSiteIsServedFromItsWebsiteEndpoint(t *testing.T) {
	p := siteProgram(t, "s3")

	website := resource(t, p, KindS3Website, "docs")
	if got := website.EnvOutputs()[EnvStaticURL("docs")].Prop; got != "websiteEndpoint" {
		t.Errorf("the site's URL is bound to %q", got)
	}
	// None of the CDN plumbing, and in particular no bucket policy: a bucket
	// nobody put a policy on is one whose access is still whatever the account
	// says, which is the existing behaviour and not this change's business.
	noResource(t, p, KindCloudFront, "docs")
	noResource(t, p, KindCloudFrontOAI, "docs")
	noResource(t, p, KindS3BucketPolicy, "docs")
}

func TestACDNBackedSiteIsServedFromTheDistributionAndNotTheBucket(t *testing.T) {
	p := siteProgram(t, "cloudfront")

	dist := resource(t, p, KindCloudFront, "docs")
	if got := dist.EnvOutputs()[EnvStaticURL("docs")].Prop; got != "domainName" {
		t.Errorf("the site's URL is bound to %q, not the distribution's domain", got)
	}
	// The website configuration would be a second address for the same content
	// -- one that is not cached, not logged, and outlives any decision made
	// about the distribution. It must not exist.
	noResource(t, p, KindS3Website, "docs")

	// The objects still go to the bucket; only the way in changes.
	resource(t, p, KindS3Bucket, "docs")
	resource(t, p, KindS3Object, "docs/index.html")
}

// The origin access identity is the only reader. Without the bucket policy the
// distribution exists, resolves, and returns 403 for every object -- a deploy
// that reports success and serves nothing.
func TestACDNBackedSiteGrantsReadsToItsOriginAccessIdentityAlone(t *testing.T) {
	p := siteProgram(t, "cloudfront")

	oaiKey := ir.Key{Kind: KindCloudFrontOAI, ID: "docs"}
	resource(t, p, KindCloudFrontOAI, "docs")

	policy := resource(t, p, KindS3BucketPolicy, "docs")
	doc, ok := policy.Props()["policy"].(ir.JSONDoc)
	if !ok {
		t.Fatalf("the bucket policy document is %T", policy.Props()["policy"])
	}
	statements := doc.Value.(map[string]any)["Statement"].([]any)
	if len(statements) != 1 {
		t.Fatalf("the bucket policy has %d statements; one principal means one", len(statements))
	}
	stmt := statements[0].(map[string]any)
	principal := stmt["Principal"].(map[string]any)
	if got := principal["AWS"]; got != (ir.Ref{Key: oaiKey, Prop: "iamArn"}) {
		t.Errorf("the reader is %v, not the origin access identity", got)
	}
	if actions := stmt["Action"].([]any); len(actions) != 1 || actions[0] != "s3:GetObject" {
		t.Errorf("the policy grants %v; a static site is read", actions)
	}

	// And the distribution reads through that identity rather than over the
	// public internet: an S3 origin config, not a custom origin.
	origin := dist(t, p).Props()["origins"].([]any)[0].(map[string]any)
	s3Origin, ok := origin["s3OriginConfig"].(map[string]any)
	if !ok {
		t.Fatalf("the origin is not an S3 origin: %v", origin)
	}
	if got := s3Origin["originAccessIdentity"]; got != (ir.Ref{Key: oaiKey, Prop: "cloudfrontAccessIdentityPath"}) {
		t.Errorf("the origin signs as %v", got)
	}
	if got := origin["domainName"]; got != (ir.Ref{Key: ir.Key{Kind: KindS3Bucket, ID: "docs"}, Prop: "bucketRegionalDomainName"}) {
		// The website endpoint would be the public one, which is the whole
		// thing this shape exists to avoid.
		t.Errorf("the origin is %v, not the bucket's REST endpoint", got)
	}
}

// The index document has to survive the move: it is what the website
// configuration was doing, and CloudFront's equivalent is a different field.
func TestACDNBackedSiteKeepsItsIndexDocument(t *testing.T) {
	if got := dist(t, siteProgram(t, "cloudfront")).Props()["defaultRootObject"]; got != "index.html" {
		t.Errorf("the distribution's root object is %v", got)
	}
}

func dist(t *testing.T, p *ir.Program) ir.Resource {
	t.Helper()
	return resource(t, p, KindCloudFront, "docs")
}
