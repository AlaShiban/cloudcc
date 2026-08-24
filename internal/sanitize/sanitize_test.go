package sanitize

import (
	"regexp"
	"strings"
	"testing"
)

func TestIdentifier(t *testing.T) {
	cases := map[string]string{
		"petsByOwner":   "petsByOwner",
		"pets-by-owner": "petsByOwner",
		"pet_api":       "petApi",
		"pet api":       "petApi",
		"9lives":        "n9lives",
		"":              "resource",
		"---":           "resource",
		"class":         "class_",
		"Pets":          "pets",
	}
	for in, want := range cases {
		if got := Identifier(in); got != want {
			t.Errorf("Identifier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIdentifierIsAValidJSName(t *testing.T) {
	re := regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	for _, in := range []string{"pets.by/owner", "1", "--x--", "petsByOwner", "for"} {
		got := Identifier(in)
		if !re.MatchString(got) {
			t.Errorf("Identifier(%q) = %q, which is not a valid JS identifier", in, got)
		}
	}
}

func TestEnvVar(t *testing.T) {
	cases := map[string]string{
		"petsByOwner": "PETSBYOWNER",
		"pet-api":     "PET_API",
		"log.level":   "LOG_LEVEL",
		"9lives":      "V9LIVES",
	}
	for in, want := range cases {
		if got := EnvVar(in); got != want {
			t.Errorf("EnvVar(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestS3BucketObeysAWSRules(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	cases := []struct{ app, id string }{
		{"petstore", "petsByOwner"},
		{"pet_store", "my_bucket"},
		{"UPPER", "CASE"},
		{"a", "b"},
		{strings.Repeat("long", 30), strings.Repeat("name", 30)},
		{"...", "..."},
	}
	for _, c := range cases {
		got := S3Bucket(c.app, c.id)
		if !valid.MatchString(got) {
			t.Errorf("S3Bucket(%q, %q) = %q, which AWS would reject", c.app, c.id, got)
		}
		if strings.Contains(got, "_") {
			t.Errorf("S3Bucket(%q, %q) = %q contains an underscore", c.app, c.id, got)
		}
		if strings.Contains(got, "..") {
			t.Errorf("S3Bucket(%q, %q) = %q contains consecutive dots", c.app, c.id, got)
		}
	}
}

func TestDynamoTableAllowsUnderscoresAndCase(t *testing.T) {
	got := DynamoTable("petstore", "pets_By.Owner")
	if got != "petstore-pets_By.Owner" {
		t.Errorf("DynamoTable = %q", got)
	}
	if len(got) < 3 || len(got) > 255 {
		t.Errorf("length %d is out of range", len(got))
	}
}

func TestLambdaFunctionLength(t *testing.T) {
	got := LambdaFunction(strings.Repeat("app", 30), strings.Repeat("unit", 30))
	if len(got) > 64 {
		t.Errorf("LambdaFunction produced %d characters: %q", len(got), got)
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(got) {
		t.Errorf("LambdaFunction = %q", got)
	}
}

func TestTruncationStaysDistinct(t *testing.T) {
	long := strings.Repeat("x", 100)
	a := LambdaFunction("app", long+"one")
	b := LambdaFunction("app", long+"two")
	if a == b {
		t.Errorf("two distinct long ids collapsed to the same name: %q", a)
	}
}

func TestElastiCacheRules(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)
	for _, id := range []string{"9cache", "my_cache_", "--weird--", "sessions"} {
		got := ElastiCacheCluster("petstore", id)
		if !valid.MatchString(got) {
			t.Errorf("ElastiCacheCluster(petstore, %q) = %q, which AWS would reject", id, got)
		}
		if strings.Contains(got, "--") {
			t.Errorf("%q contains consecutive hyphens", got)
		}
		if len(got) > 50 {
			t.Errorf("%q is %d characters", got, len(got))
		}
	}
}

func TestRDSIdentifierStartsWithALetter(t *testing.T) {
	got := RDSIdentifier("9app", "1db")
	if got[0] < 'a' || got[0] > 'z' {
		t.Errorf("RDSIdentifier = %q, which does not start with a letter", got)
	}
}

func TestDBIdentifier(t *testing.T) {
	cases := map[string]string{
		"maindb":  "maindb",
		"main-db": "main_db",
		"9db":     "d9db",
		"Main.DB": "main_db",
	}
	for in, want := range cases {
		if got := DBIdentifier(in); got != want {
			t.Errorf("DBIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIdempotent(t *testing.T) {
	fns := map[string]func(string, string) string{
		"S3Bucket":           S3Bucket,
		"DynamoTable":        DynamoTable,
		"LambdaFunction":     LambdaFunction,
		"SNSTopic":           SNSTopic,
		"ElastiCacheCluster": ElastiCacheCluster,
		"RDSIdentifier":      RDSIdentifier,
	}
	for name, fn := range fns {
		once := fn("petstore", "petsByOwner")
		twice := fn("", once)
		if once != twice {
			t.Errorf("%s is not idempotent: %q then %q", name, once, twice)
		}
	}
}

func TestDeterministic(t *testing.T) {
	for i := 0; i < 20; i++ {
		if S3Bucket("petstore", "petsByOwner") != S3Bucket("petstore", "petsByOwner") {
			t.Fatal("S3Bucket is not deterministic")
		}
	}
}
