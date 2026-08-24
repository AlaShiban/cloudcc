// Package sanitize turns capability ids into names each AWS service will
// actually accept.
//
// The rules differ per service in ways that matter: an S3 bucket may not
// contain an underscore, a DynamoDB table may; an ElastiCache cluster id must
// start with a letter and may not end with a hyphen. Getting this wrong
// surfaces as an opaque provider error at deploy time, so each service gets
// its own function and its own tests.
//
// Every function is deterministic and idempotent: sanitising an already-valid
// name returns it unchanged.
package sanitize

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Identifier turns s into a valid, lowerCamelCase JavaScript identifier for use
// as a generated Pulumi variable name.
func Identifier(s string) string {
	var b strings.Builder
	upperNext := false
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			if upperNext {
				b.WriteRune(upper(r))
				upperNext = false
			} else if i == 0 || b.Len() == 0 {
				b.WriteRune(lower(r))
			} else {
				b.WriteRune(r)
			}
		case r >= '0' && r <= '9':
			if b.Len() == 0 {
				b.WriteString("n")
			}
			b.WriteRune(r)
			upperNext = false
		default:
			if b.Len() > 0 {
				upperNext = true
			}
		}
	}
	if b.Len() == 0 {
		return "resource"
	}
	if isReservedJS(b.String()) {
		return b.String() + "_"
	}
	return b.String()
}

// EnvVar turns s into a SCREAMING_SNAKE environment variable segment.
func EnvVar(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(upper(r))
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out[0] >= '0' && out[0] <= '9' {
		out = "V" + out
	}
	return out
}

// DynamoTable: 3-255 characters of [a-zA-Z0-9_.-].
func DynamoTable(app, id string) string {
	return clamp(keep(join(app, id), isDynamoRune, '-'), 3, 255, "tbl")
}

// S3Bucket: 3-63 lowercase characters of [a-z0-9.-], starting and ending with
// a letter or digit, and no consecutive dots.
func S3Bucket(app, id string) string {
	s := strings.ToLower(join(app, id))
	s = keep(s, isS3Rune, '-')
	s = collapse(s, '.')
	s = strings.Trim(s, ".-")
	s = clamp(s, 3, 63, "bkt")
	return strings.Trim(s, ".-")
}

// LambdaFunction: 1-64 characters of [a-zA-Z0-9-_].
func LambdaFunction(app, id string) string {
	return clamp(keep(join(app, id), isLambdaRune, '-'), 1, 64, "fn")
}

// SNSTopic: 1-256 characters of [a-zA-Z0-9_-].
func SNSTopic(app, id string) string {
	return clamp(keep(join(app, id), isLambdaRune, '-'), 1, 256, "topic")
}

// SecretName: 1-512 characters of [a-zA-Z0-9/_+=.@-].
func SecretName(app, id string) string {
	return clamp(keep(join(app, id), isSecretRune, '-'), 1, 512, "secret")
}

// ElastiCacheCluster: 1-50 lowercase characters, starting with a letter, no
// consecutive hyphens and no trailing hyphen.
func ElastiCacheCluster(app, id string) string {
	s := strings.ToLower(join(app, id))
	s = keep(s, isHyphenAlnum, '-')
	s = collapse(s, '-')
	s = strings.Trim(s, "-")
	s = leadWithLetter(s, "c")
	s = clamp(s, 1, 50, "cache")
	return strings.TrimRight(s, "-")
}

// RDSIdentifier: 1-63 lowercase characters, starting with a letter, no
// consecutive hyphens and no trailing hyphen.
func RDSIdentifier(app, id string) string {
	s := strings.ToLower(join(app, id))
	s = keep(s, isHyphenAlnum, '-')
	s = collapse(s, '-')
	s = strings.Trim(s, "-")
	s = leadWithLetter(s, "d")
	s = clamp(s, 1, 63, "db")
	return strings.TrimRight(s, "-")
}

// APIName: API Gateway accepts a broad character set; keep it readable.
func APIName(app, id string) string {
	return clamp(keep(join(app, id), isLambdaRune, '-'), 1, 128, "api")
}

// IAMName: 1-64 characters of [a-zA-Z0-9+=,.@_-].
func IAMName(app, id, suffix string) string {
	return clamp(keep(join(join(app, id), suffix), isIAMRune, '-'), 1, 64, "role")
}

// DBIdentifier turns an id into a Postgres database name: lowercase letters,
// digits and underscores, starting with a letter.
func DBIdentifier(id string) string {
	s := strings.ToLower(id)
	s = keep(s, func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_'
	}, '_')
	s = leadWithLetter(s, "d")
	return clamp(s, 1, 63, "db")
}

func join(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "-" + b
}

// keep replaces every rune the predicate rejects with sub, then collapses runs
// of sub so a name never grows a "---" from punctuation.
func keep(s string, allow func(rune) bool, sub rune) string {
	var b strings.Builder
	for _, r := range s {
		if allow(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(sub)
		}
	}
	return collapse(b.String(), sub)
}

func collapse(s string, r rune) string {
	double := string([]rune{r, r})
	for strings.Contains(s, double) {
		s = strings.ReplaceAll(s, double, string(r))
	}
	return s
}

// clamp enforces the length bounds. Truncation appends a short hash of the
// original so two long ids that share a prefix stay distinct.
func clamp(s string, min, max int, pad string) string {
	if s == "" {
		s = pad
	}
	for len(s) < min {
		s += pad
	}
	if len(s) > max {
		sum := sha256.Sum256([]byte(s))
		suffix := "-" + hex.EncodeToString(sum[:])[:8]
		s = s[:max-len(suffix)] + suffix
	}
	return s
}

func leadWithLetter(s, prefix string) string {
	if s == "" || !(s[0] >= 'a' && s[0] <= 'z') {
		return prefix + s
	}
	return s
}

func isDynamoRune(r rune) bool {
	return isAlnum(r) || r == '_' || r == '.' || r == '-'
}

func isS3Rune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-'
}

func isLambdaRune(r rune) bool {
	return isAlnum(r) || r == '-' || r == '_'
}

func isSecretRune(r rune) bool {
	return isAlnum(r) || strings.ContainsRune("/_+=.@-", r)
}

func isIAMRune(r rune) bool {
	return isAlnum(r) || strings.ContainsRune("+=,.@_-", r)
}

func isHyphenAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'
}

func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func upper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 32
	}
	return r
}

func lower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

var reservedJS = regexp.MustCompile(`^(await|break|case|catch|class|const|continue|debugger|default|delete|do|else|enum|export|extends|false|finally|for|function|if|implements|import|in|instanceof|interface|let|new|null|package|private|protected|public|return|static|super|switch|this|throw|true|try|typeof|var|void|while|with|yield)$`)

func isReservedJS(s string) bool { return reservedJS.MatchString(s) }
