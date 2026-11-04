// Package mask is the single choke point that keeps secret values out of the
// terminal, logs and error messages. It errs toward over-masking.
package mask

import (
	"regexp"
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
	if refRe.MatchString(val) {
		return Text(val)
	}
	return Mask
}

// Text scrubs known secret shapes from free text. It does not strip control
// characters; use Sanitize for that.
func Text(s string) string {
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
		if name, val, ok := strings.Cut(a, "="); ok && secretFlagRe.MatchString(name) {
			out[i] = name + "=" + flagValue(val)
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
	if refRe.MatchString(v) {
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
	sens := sensitiveRe.MatchString(lastSegment(h.Pointer))
	h.Path = Sanitize(h.Path)
	h.Pointer = Sanitize(h.Pointer)
	h.Source = hunkSide(h.Source, sens)
	h.Dest = hunkSide(h.Dest, sens)
	return h
}

func hunkSide(s string, sensitiveKey bool) string {
	s = Sanitize(s)
	t := strings.TrimSpace(s)
	if sensitiveKey && t != "" && t != "null" && !strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[") {
		inner, quoted := t, false
		if len(t) >= 2 && t[0] == '"' && t[len(t)-1] == '"' {
			inner, quoted = t[1:len(t)-1], true
		}
		if !onlyRefs(inner) && !refRe.MatchString(inner) {
			if quoted {
				return `"` + Mask + `"`
			}
			return Mask
		}
	}
	return Text(s)
}

func lastSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
