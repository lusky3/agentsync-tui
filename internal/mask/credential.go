package mask

import (
	"regexp"
	"strings"
)

// The kinds of credential Credential names (the R5 hard findings).
const (
	KindTokenShape       = "token-shape"
	KindAuthHeader       = "auth-header"
	KindURLUserinfo      = "url-userinfo"
	KindPrivateKey       = "private-key"
	KindAgeIdentity      = "age-identity"
	KindSecretAssignment = "secret-assignment"
)

var (
	privateKeyRe  = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	ageIdentityRe = regexp.MustCompile(`AGE-SECRET-KEY-1`)
)

// Credential reports whether s holds a literal credential, and of which
// kind. It applies the same rules Text masks with (token shapes,
// Authorization-style headers, URL userinfo, secret-named assignments),
// plus PEM private keys and age identities, but not Text's URL-structure
// pass: a plain URL's path and query are not a finding on their own. A
// value that is only references (${secret:X}, ${env:T}) is never one. s is
// sanitised first so a control character cannot split a token past the
// rules.
func Credential(s string) (kind string, found bool) {
	s = Sanitize(s)
	switch {
	case privateKeyRe.MatchString(s):
		return KindPrivateKey, true
	case ageIdentityRe.MatchString(s):
		return KindAgeIdentity, true
	}
	for _, re := range shapeRes {
		if re.MatchString(s) {
			return KindTokenShape, true
		}
	}
	for _, r := range textRules {
		for _, sub := range r.re.FindAllStringSubmatch(s, -1) {
			if literal(sub[2], r.bare) {
				return r.kind, true
			}
		}
	}
	return "", false
}

// literal reports whether a rule's value group is a literal Text would
// mask: not only references and, for a bare value, not only the trailing
// closers that belong to the surrounding text.
func literal(v string, bare bool) bool {
	if onlyRefs(v) {
		return false
	}
	return !bare || strings.TrimRight(v, `,;)}]"'`) != ""
}
