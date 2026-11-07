package gitrepo

import (
	"slices"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

// mlOpener is a TOML multi-line string that a pair opened: the pair, its
// closing quotes, whether its key is a header (auth) or secret-named, and
// whether it is an env or header value (env: under an env, environment or
// headers table, Ruling 43).
type mlOpener struct {
	pair              kv
	delim             string
	auth, secret, env bool
}

// multiLineBodies maps each line number of post (a whole file, its
// post-image) that lies in the body of a TOML multi-line string (triple
// single or double quotes) opened by a secret-named or header key, or
// under an env, environment or headers table (the current [table], a
// dotted key or an inline table), to that opener. The opener line itself
// is not a body line; the closing line is. A string that never closes runs
// to the end of the file. A string that is an array element is not
// followed (README, known limits).
func multiLineBodies(post string) map[int]mlOpener {
	out := map[int]mlOpener{}
	var cur *mlOpener
	envTable := false
	for i, l := range strings.Split(post, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if cur == nil {
			if tableRe.MatchString(l) {
				// Every key-like word on the header line counts, a
				// comment's included (fail closed).
				envTable = kv{key: l}.underEnvTable()
			}
			cur = openerOf(l, envTable)
			continue
		}
		if cur.auth || cur.secret || cur.env {
			out[i+1] = *cur
		}
		if delimIndex(l, cur.delim) >= 0 {
			cur = nil
		}
	}
	return out
}

// openerOf is the multi-line string the line opens and does not close, or
// nil: the first such pair at any depth (allPairs). It is an env or header
// value when the line is under an env table (envTable) or the pair or one
// enclosing it on the line is under one.
func openerOf(l string, envTable bool) *mlOpener {
	pairs := allPairs(l)
	for i, p := range pairs {
		if q := openedDelim(strings.TrimSpace(p.value)); q != "" {
			_, secret := mask.Credential(p.lastKey() + " = x")
			env := envTable || slices.ContainsFunc(pairs[:i+1], kv.underEnvTable)
			return &mlOpener{pair: p, delim: q, auth: authKey(p), secret: secret, env: env}
		}
	}
	return nil
}

// openedDelim is the delimiter of the multi-line string the TOML value v
// opens and does not close on its line, or "".
func openedDelim(v string) string {
	for _, q := range []string{`"""`, "'''"} {
		if strings.HasPrefix(v, q) && delimIndex(v[len(q):], q) < 0 {
			return q
		}
	}
	return ""
}

// delimIndex is the index in s of the first closing delimiter q of a
// multi-line string, or -1. In a basic string (double quotes) a backslash
// escapes the byte after it, so quotes preceded by an odd number of
// backslashes are no delimiter; a literal string has no escapes.
func delimIndex(s, q string) int {
	if q != `"""` {
		return strings.Index(s, q)
	}
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\':
			i++
		case strings.HasPrefix(s[i:], q):
			return i
		}
	}
	return -1
}

// classifyBody decides a body line of o's string as the value of o's key:
// a header value with a literal is hard everywhere, a secret-named key's
// literal is hard in a config file and soft elsewhere (R5), and so is a
// credential or token-shaped word (outside the exempt digests) in an env
// or header value (Ruling 43). References and blank lines are not
// findings.
func (o mlOpener) classifyBody(text string, config bool) (kind string, hard, found bool) {
	body := text
	if i := delimIndex(text, o.delim); i >= 0 {
		body = text[:i]
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false, false
	}
	if o.auth {
		return mask.KindAuthHeader, true, authLiteral(kv{key: o.pair.key, value: body})
	}
	kind, found = mask.Credential(o.pair.lastKey() + " = '''" + body + "'''")
	if found || !o.env {
		return kind, config, found
	}
	if hasTokenWord(exemptRe.ReplaceAllString(mask.Sanitize(body), " ")) {
		return KindTokenShapedWord, config, true
	}
	return "", false, false
}
