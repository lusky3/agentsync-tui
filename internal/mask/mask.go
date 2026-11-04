// Package mask is the single choke point that keeps secret values out of the
// terminal, logs and error messages. It errs toward over-masking.
package mask

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lusky3/agentsync-tui/internal/model"
)

// Mask is the fixed placeholder. It reveals nothing about the original,
// including its length.
const Mask = "••••"

const refPat = `\$\{(?:secret:|env:)?[A-Za-z_][A-Za-z0-9_.\-]*\}`

// secretKeyPat matches the tail of a secret-named key.
// The key must not sit inside a reference: "${secret:NAME}" is not a pair.
const secretKeyPat = `(?:^|[^{]|[^$]\{)(?:token|secret|password|passwd|pwd|api[-_]?key|credential|private[-_]?key|access[-_]?key)`

// wordKeyPat matches keys that are secrets only as whole words: "auth" and
// "pat" (personal access token), so AUTH=, github_pat= and PAT: are masked
// while oauth=, compat= and path= are not.
const wordKeyPat = `(?:^|[^A-Za-z0-9{])(?:auth|pat)`

// rule is one Text rule: group 1 is kept, group 2 masked. bare marks the
// bare-value KV rule, whose trailing closers belong to the surrounding text.
type rule struct {
	re   *regexp.Regexp
	bare bool
}

// kvRules builds the three KV rules for a key pattern: a secret-named key
// (token, password, api_key, apiKey, ...) ending the word, then ':' or '=',
// then a value: bare (one word, or one ${ref}), "double"- or 'single'-quoted
// (JSON/YAML shapes). The key is not masked; plurals ("passwords: 3") and
// prose ("token expired") have no ':'/'=' directly after the word and stay
// as is. A bare value that starts with a reference keeps the conservative
// class (so "A=${env:A},B=${env:B}" shows both refs); a literal value runs
// to the next whitespace so a separator cannot end the masked region early
// ("password=ab,x"). Text re-appends trailing closers unmasked so
// "(token=x)" keeps its shape.
func kvRules(keyPat string) []rule {
	return []rule{
		{re: regexp.MustCompile(`(?i)(` + keyPat + `["']?[ \t]*[:=][ \t]*)(\$\{[^}\s]*\}(?:\$\{[^}\s]*\}|[^\s"',;)}\]])*|(?:[^\s"'$]|\$[^{\s])[^\s]*)`), bare: true},
		{re: regexp.MustCompile(`(?i)(` + keyPat + `["']?[ \t]*[:=][ \t]*")((?:\\.|[^"\\\n])+)"`)},
		{re: regexp.MustCompile(`(?i)(` + keyPat + `["']?[ \t]*[:=][ \t]*')((?:\\.|[^'\\\n])+)'`)},
	}
}

var (
	refRe    = regexp.MustCompile(refPat)
	wholeRef = regexp.MustCompile(`^` + refPat + `$`)

	// Each Text rule keeps group 1 and masks group 2 unless
	// that group consists only of references.
	textRules = append([]rule{
		// Authorization-style headers with any scheme.
		{re: regexp.MustCompile(`(?i)(\b(?:proxy-)?authorization["']?\s*:\s*["']?(?:(?:bearer|basic|token|digest)\s+)?)([^\s"']+)`)},
		{re: regexp.MustCompile(`(?i)(\bx-api-key["']?\s*:\s*["']?)([^\s"']+)`)},
		// Cookie headers: the whole cookie string is the secret.
		{re: regexp.MustCompile(`(?i)(\b(?:set-)?cookie["']?\s*:\s*["']?)([^\n"']+)`)},
		{re: regexp.MustCompile(`(?i)(\bBearer\s+)([^\s"']+)`)},
		// userinfo in URLs.
		{re: regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)([^/\s@"']+)@`)},
		// credential-looking query parameters.
		{re: regexp.MustCompile(`(?i)([?&;][A-Za-z0-9_.\-]*(?:token|key|secret|password|passwd|auth|sig|signature)=)([^&\s"'#]+)`)},
		// key/token/secret/password followed by = or : and a long opaque run.
		{re: regexp.MustCompile(`(?i)((?:token|key|secret|password)["']?\s*[:=]\s*["']?)([A-Za-z0-9_\-]{24,})`)},
	}, append(kvRules(secretKeyPat), kvRules(wordKeyPat)...)...)
	// Known token shapes: the whole match is the secret.
	shapeRes = []*regexp.Regexp{
		regexp.MustCompile(`gg_pat_[^\s"']+`),
		regexp.MustCompile(`ghp_[^\s"']+`),
		regexp.MustCompile(`github_pat_[^\s"']+`),
		regexp.MustCompile(`\bsk-[^\s"']+`),
		regexp.MustCompile(`xox[bap]-[^\s"']+`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]*`),
	}

	schemeOnlyRe = regexp.MustCompile(`^\s*(?i:bearer|basic|token)?\s*[:\s]*$`)
	headerNameRe = regexp.MustCompile(`^([A-Za-z0-9_\-]+:[ \t]+)`)
	sensitiveRe  = regexp.MustCompile(`(?i)token|secret|pass|key|auth|credential|bearer|cookie|session`)
	secretFlagRe = regexp.MustCompile(`(?i)^-{1,2}(?:h|header|pat|auth|[a-z0-9_\-]*(?:token|key|secret|password|passwd|auth|authorization|bearer|credential)[a-z0-9_\-]*)$`)
)

// IsRef reports whether s is exactly one reference: ${secret:NAME},
// ${env:NAME} or ${NAME}.
func IsRef(s string) bool { return wholeRef.MatchString(strings.TrimSpace(s)) }

// onlyRefs: s holds at least one reference and nothing else but whitespace
// and ':' separators (e.g. ${U}:${P}).
func onlyRefs(s string) bool {
	if !refRe.MatchString(s) {
		return false
	}
	rest := refRe.ReplaceAllString(s, "")
	return strings.Trim(rest, " \t:") == ""
}

// Value returns a configuration value for display. References pass through,
// literal text containing a reference stays visible apart from token-shaped
// secrets, and everything else becomes Mask. The decision never depends on
// the key the value belongs to, so an unrecognised key cannot leak a value.
func Value(val string) string {
	if strings.TrimSpace(val) == "" {
		return Sanitize(val)
	}
	val = Sanitize(val)
	if onlyRefs(val) {
		return val
	}
	if refRe.MatchString(val) && schemeOnlyRe.MatchString(refRe.ReplaceAllString(val, "")) {
		return Text(val)
	}
	return Mask
}

// Text scrubs known secret shapes from free text. It sanitises first so control
// characters cannot split a token past the rules.
func Text(s string) string {
	s = Sanitize(s)
	// URLs first: their path, query and fragment are masked by structure,
	// and the KV rules then see only placeholders where values were.
	s = urlRe.ReplaceAllStringFunc(s, maskURL)
	for _, r := range textRules {
		re := r.re
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			sub := re.FindStringSubmatch(m)
			secret := sub[2]
			if onlyRefs(secret) {
				return m
			}
			tail := ""
			if r.bare {
				// Trailing closers belong to the surrounding text.
				keep := strings.TrimRight(secret, `,;)}]"'`)
				if keep == "" {
					return m
				}
				tail = secret[len(keep):]
			}
			return sub[1] + Mask + tail + m[len(sub[1])+len(secret):]
		})
	}
	for _, re := range shapeRes {
		s = re.ReplaceAllString(s, Mask)
	}
	return s
}

// Args returns a masked copy of a command line. The input is not modified.
func Args(args []string) []string {
	out := make([]string, len(args))
	valueNext := false
	for i, a := range args {
		a = Sanitize(a)
		switch {
		case valueNext && !strings.HasPrefix(a, "--"):
			out[i] = flagValue(a)
			valueNext = false
			continue
		case valueNext:
			valueNext = false
		}
		if name, val, ok := strings.Cut(a, "="); ok && (secretFlagRe.MatchString(name) || sensitiveRe.MatchString(name)) {
			out[i] = Text(name) + "=" + flagValue(val)
			continue
		}
		if secretFlagRe.MatchString(a) {
			valueNext = true
			out[i] = a
			continue
		}
		out[i] = Text(a)
	}
	return out
}

// flagValue masks the value of a secret-bearing flag. Header-shaped values
// keep their name; reference-only values stay visible.
func flagValue(v string) string {
	if onlyRefs(v) {
		return v
	}
	if t := Text(v); t != v {
		return t
	}
	// A reference keeps the value visible only when nothing but an optional
	// header name and auth scheme surrounds it: "K=${secret:A},L=lit" or
	// "prefix-${X}" would otherwise leak the literal part.
	if refRe.MatchString(v) && schemeOnlyRe.MatchString(refRe.ReplaceAllString(headerNameRe.ReplaceAllString(v, ""), "")) {
		return v
	}
	if m := headerNameRe.FindString(v); m != "" {
		return m + Mask
	}
	return Mask
}

// Hunk returns h with its content made safe to display. Path and Pointer are
// structural and only sanitised.
func Hunk(h model.Hunk) model.Hunk {
	h.Path = Sanitize(h.Path)
	h.Pointer = Sanitize(h.Pointer)
	forced := false
	last := ""
	for _, seg := range strings.Split(h.Pointer, "/") {
		// An args element addressed by index ("/args/1") carries no key of
		// its own, so the args segment itself forces masking.
		forced = forced || secretKey(seg) || strings.EqualFold(seg, "args")
		last = seg
	}
	mcp := forced || underMCPServers(h.Pointer)
	h.Source = hunkSide(h.Source, last, forced, mcp)
	h.Dest = hunkSide(h.Dest, last, forced, mcp)
	return h
}

// underMCPServers reports whether the pointer's first segment is the MCP
// server table ("mcpServers", "mcp_servers", ...): anything there that
// cannot be walked as JSON is masked whole rather than scrubbed.
func underMCPServers(ptr string) bool {
	first, _, _ := strings.Cut(strings.TrimPrefix(ptr, "/"), "/")
	first = strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(first))
	return first == "mcpservers"
}

// hunkSide masks one side of a hunk. One decoder walks every JSON document
// on the side in turn (a JSONL-like side is linear, not quadratic), so
// trailing bytes or extra documents cannot smuggle a value past the walk.
// Whatever remains unparseable (a truncated document, a TOML rendering) is
// masked outright under a secret-bearing or mcpServers pointer and scrubbed
// with Text otherwise.
func hunkSide(s, key string, forced, mcp bool) string {
	s = Sanitize(s)
	if strings.TrimSpace(s) == "" {
		return s
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var parts []string
	off := 0 // end of the last document decoded
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if rest := s[off:]; strings.TrimSpace(rest) != "" {
				if mcp {
					parts = append(parts, Value(rest))
				} else {
					parts = append(parts, Text(rest))
				}
			}
			break
		}
		off = int(dec.InputOffset())
		parts = append(parts, encodeMasked(walk(v, key, forced)))
	}
	return strings.Join(parts, "\n")
}

// encodeMasked renders one walked document as indented JSON.
func encodeMasked(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if enc.Encode(v) != nil {
		return Mask
	}
	return strings.TrimRight(buf.String(), "\n")
}

// SensitiveKey reports whether k names something whose scalar value must be
// masked outright: the env/headers tables and any key that reads like a
// credential. It is the one definition shared by Hunk and the screens.
func SensitiveKey(k string) bool {
	switch strings.ToLower(k) {
	case "env", "environment", "headers":
		return true
	}
	return sensitiveRe.MatchString(k)
}

func secretKey(k string) bool { return SensitiveKey(k) }

// walk masks a decoded JSON tree. Under a secret-bearing key every scalar
// (string, number, bool) becomes Mask unless it is a reference; elsewhere
// args arrays go through Args and other strings through Text.
func walk(v any, key string, forced bool) any {
	switch x := v.(type) {
	case string:
		if forced {
			return Value(x)
		}
		return Text(x)
	case json.Number, bool:
		if forced {
			return Mask
		}
		return v
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			mk := Text(k)
			for n := 2; ; n++ { // masked keys may collide; never drop a value
				if _, dup := out[mk]; !dup {
					break
				}
				mk = Text(k) + "#" + strconv.Itoa(n)
			}
			sk := Sanitize(k)
			out[mk] = walk(x[k], sk, forced || secretKey(sk))
		}
		return out
	case []any:
		if !forced && strings.EqualFold(key, "args") {
			// Scalars are stringified so the flag-to-next-value logic still
			// applies across a number ("--port", 8080, "--token", "x");
			// containers are walked in place.
			strs := make([]string, len(x))
			for i, c := range x {
				switch c := c.(type) {
				case string:
					strs[i] = c
				case json.Number, bool:
					strs[i] = fmt.Sprint(c)
				}
			}
			masked := Args(strs)
			out := make([]any, len(x))
			for i, c := range x {
				switch c.(type) {
				case string, json.Number, bool:
					out[i] = masked[i]
				default:
					out[i] = walk(c, key, forced)
				}
			}
			return out
		}
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = walk(c, key, forced)
		}
		return out
	}
	return v
}

// OneLine sanitises s and folds newlines and tabs into spaces, for status
// lines and notes that must stay on one row. It does not mask: callers pass
// text that is already masked, or identifiers.
func OneLine(s string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(Sanitize(s))
}

// FirstLine returns the first non-blank line of s, masked (Text), sanitised
// and trimmed, or "" when there is none. It is the one way an error message
// or a stderr stream becomes a status note.
func FirstLine(s string) string {
	for _, l := range strings.Split(Text(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return OneLine(l)
		}
	}
	return ""
}

// Capped masks (Text) and sanitises s, then caps it at n bytes. The cap is
// applied after masking so a cut can never leave the prefix of a secret
// behind, and it falls on the last line break within the budget (or, when
// there is none, on a rune boundary). truncated reports whether anything
// was dropped.
func Capped(s string, n int) (text string, truncated bool) {
	s = Sanitize(Text(s))
	if n <= 0 || len(s) <= n {
		return s, false
	}
	cut := s[:n]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		return cut[:i], true
	}
	return strings.ToValidUTF8(cutRune(cut), ""), true
}

// cutRune drops a trailing partial UTF-8 sequence: the last rune start is
// found and the bytes from it are dropped only if they do not form a whole
// rune, so a cut exactly on a rune boundary keeps every rune.
func cutRune(s string) string {
	i := len(s) - 1
	for i >= 0 && len(s)-i < utf8.UTFMax && !utf8.RuneStart(s[i]) {
		i--
	}
	if i < 0 || utf8.FullRuneInString(s[i:]) {
		return s
	}
	return s[:i]
}
