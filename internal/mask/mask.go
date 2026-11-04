// Package mask is the single choke point that keeps secret values out of the
// terminal, logs and error messages. It errs toward over-masking.
package mask

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/model"
)

// Mask is the fixed placeholder. It reveals nothing about the original,
// including its length.
const Mask = "••••"

const refPat = `\$\{(?:secret:|env:)?[A-Za-z_][A-Za-z0-9_.\-]*\}`

var (
	refRe    = regexp.MustCompile(refPat)
	wholeRef = regexp.MustCompile(`^` + refPat + `$`)

	// Each Text rule keeps group 1 and masks group 2 unless
	// that group consists only of references.
	textRules = []*regexp.Regexp{
		// Authorization-style headers with any scheme.
		regexp.MustCompile(`(?i)(\b(?:proxy-)?authorization["']?\s*:\s*["']?(?:(?:bearer|basic|token|digest)\s+)?)([^\s"']+)`),
		regexp.MustCompile(`(?i)(\bx-api-key["']?\s*:\s*["']?)([^\s"']+)`),
		regexp.MustCompile(`(?i)(\bBearer\s+)([^\s"']+)`),
		// userinfo in URLs.
		regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)([^/\s@"']+)@`),
		// credential-looking query parameters.
		regexp.MustCompile(`(?i)([?&;][A-Za-z0-9_.\-]*(?:token|key|secret|password|passwd|auth|sig|signature)=)([^&\s"'#]+)`),
		// key/token/secret/password followed by = or : and a long opaque run.
		regexp.MustCompile(`(?i)((?:token|key|secret|password)["']?\s*[:=]\s*["']?)([A-Za-z0-9_\-]{24,})`),
	}
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
	sensitiveRe  = regexp.MustCompile(`(?i)token|secret|password|passwd|api[-_]?key|authorization|credential|bearer|cookie`)
	secretFlagRe = regexp.MustCompile(`(?i)^-{1,2}(?:h|header|[a-z0-9_\-]*(?:token|key|secret|password|passwd|auth|authorization|bearer|credential)[a-z0-9_\-]*)$`)
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

func maskUnlessRefs(s string) string {
	if onlyRefs(s) {
		return s
	}
	return Mask
}

// Value returns val for display. References pass through, literal text
// containing a reference stays visible apart from token-shaped secrets, and
// everything else becomes Mask. key is accepted for call-site clarity; the
// decision never depends on it, so an unrecognised key cannot leak a value.
func Value(key, val string) string {
	_ = key
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
	for _, re := range textRules {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			sub := re.FindStringSubmatch(m)
			secret := sub[2]
			if onlyRefs(secret) {
				return m
			}
			return sub[1] + Mask + m[len(sub[1])+len(secret):]
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
		forced = forced || secretKey(seg)
		last = seg
	}
	h.Source = hunkSide(h.Source, last, forced)
	h.Dest = hunkSide(h.Dest, last, forced)
	return h
}

// hunkSide masks one side of a hunk. It walks the first JSON document and
// handles any remainder recursively, so trailing bytes or extra documents
// cannot smuggle a value past the walk. Unparseable input is masked outright
// under a secret-bearing pointer and scrubbed with Text otherwise.
func hunkSide(s, key string, forced bool) string {
	s = Sanitize(s)
	if strings.TrimSpace(s) == "" {
		return s
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		if forced {
			return Value(key, s)
		}
		return Text(s)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if enc.Encode(walk(v, key, forced)) != nil {
		return Mask
	}
	out := strings.TrimRight(buf.String(), "\n")
	if rest := s[dec.InputOffset():]; strings.TrimSpace(rest) != "" {
		out += "\n" + hunkSide(rest, key, forced)
	}
	return out
}

func secretKey(k string) bool {
	switch strings.ToLower(k) {
	case "env", "environment", "headers":
		return true
	}
	return sensitiveRe.MatchString(k)
}

// walk masks a decoded JSON tree. Under a secret-bearing key every string is
// run through Value; elsewhere args arrays go through Args and other strings
// through Text.
func walk(v any, key string, forced bool) any {
	switch x := v.(type) {
	case string:
		if forced {
			return Value(key, x)
		}
		return Text(x)
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
			strs := make([]string, 0, len(x))
			all := true
			for _, c := range x {
				cs, ok := c.(string)
				all = all && ok
				strs = append(strs, cs)
			}
			if all {
				masked := Args(strs)
				out := make([]any, len(masked))
				for i, m := range masked {
					out[i] = m
				}
				return out
			}
		}
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = walk(c, key, forced)
		}
		return out
	}
	return v
}

func lastSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
