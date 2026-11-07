package gitrepo

import (
	"strings"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

// mlOpener is a TOML multi-line string that a pair opened: the pair, its
// closing quotes, and whether its key is a header (auth) or secret-named.
type mlOpener struct {
	pair         kv
	delim        string
	auth, secret bool
}

// multiLineBodies maps each line number of post (a whole file, its
// post-image) that lies in the body of a TOML multi-line string (triple
// single or double quotes) opened by a secret-named or header key to that
// opener. The opener line itself is not a body line; the closing line is.
// A string that never closes runs to the end of the file.
func multiLineBodies(post string) map[int]mlOpener {
	out := map[int]mlOpener{}
	var cur *mlOpener
	for i, l := range strings.Split(post, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if cur == nil {
			cur = openerOf(l)
			continue
		}
		if cur.auth || cur.secret {
			out[i+1] = *cur
		}
		if strings.Contains(l, cur.delim) {
			cur = nil
		}
	}
	return out
}

// openerOf is the multi-line string the line opens and does not close, or
// nil: the first such pair at any depth (allPairs).
func openerOf(l string) *mlOpener {
	for _, p := range allPairs(l) {
		v := strings.TrimSpace(p.value)
		for _, q := range []string{`"""`, "'''"} {
			if strings.HasPrefix(v, q) && strings.Count(v, q) == 1 {
				_, secret := mask.Credential(p.lastKey() + " = x")
				return &mlOpener{pair: p, delim: q, auth: authKey(p), secret: secret}
			}
		}
	}
	return nil
}

// classifyBody decides a body line of o's string as the value of o's key:
// a header value with a literal is hard everywhere, a secret-named key's
// literal is hard in a config file and soft elsewhere (R5). References
// and blank lines are not findings.
func (o mlOpener) classifyBody(text string, config bool) (kind string, hard, found bool) {
	body, _, _ := strings.Cut(text, o.delim)
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false, false
	}
	if o.auth {
		return mask.KindAuthHeader, true, authLiteral(kv{key: o.pair.key, value: body})
	}
	kind, found = mask.Credential(o.pair.lastKey() + " = '''" + body + "'''")
	return kind, config, found
}
