package model

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// Plugin is one line of `agentsync plugin list`.
type Plugin struct {
	Name    string // bare name, or id@marketplace; always a valid id
	Version string // "" when absent or not a safe token
	SHA     string
	Enabled bool
}

// Outdated is one pending bump from `agentsync plugin outdated`.
type Outdated struct {
	Name     string
	From, To string
	Mode     string // e.g. "track"; "" when absent
}

// Marketplace is one line of `agentsync marketplace list`.
type Marketplace struct {
	Name string
	URL  string // raw as printed (the UI masks it); "" if it held control characters
	SHA  string
}

var (
	idPartRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	tokenRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+:-]{0,127}$`)
	modeRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)
	errNoRows = errors.New("no rows in output")
)

// validPluginID accepts "name" or "name@marketplace", validating both halves
// (the same rules as actions.ValidID, which this package cannot import).
func validPluginID(s string) bool {
	id, mkt, has := strings.Cut(s, "@")
	if !idPartRe.MatchString(id) {
		return false
	}
	return !has || idPartRe.MatchString(mkt)
}

func cleanText(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// lines returns the trimmed non-blank lines of s and whether there are any.
func lines(s string) (out []string, nonBlank bool) {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, len(out) > 0
}

// ParsePluginList parses `plugin list` (no --json exists). Formats seen with
// agentsync dev, 2026-11:
//
//	hello                version=1.0.0      sha=tree:v1:9995   enabled
//	other                version=1.0.0      sha=tree:v1:76cc   disabled
//	(no plugins installed; try: agentsync plugin add <id@marketplace>)
//
// A line is a plugin when its first field is a valid plugin id and a later
// field is "enabled", "disabled" or version=…; unknown tokens, blank lines
// and unparseable lines are ignored. Non-blank input with nothing
// recognisable (and no parenthesised note) is an error.
func ParsePluginList(stdout string) ([]Plugin, error) {
	var out []Plugin
	ls, nonBlank := lines(stdout)
	recognised := false
	for _, line := range ls {
		if strings.HasPrefix(line, "(") {
			recognised = true
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 || !validPluginID(f[0]) {
			continue
		}
		p, known := Plugin{Name: f[0]}, false
		for _, tok := range f[1:] {
			k, v, eq := strings.Cut(tok, "=")
			switch {
			case !eq && tok == "enabled":
				p.Enabled, known = true, true
			case !eq && tok == "disabled":
				known = true
			case eq && k == "version":
				known = true
				if tokenRe.MatchString(v) {
					p.Version = v
				}
			case eq && k == "sha" && tokenRe.MatchString(v):
				p.SHA = v
			}
		}
		if known {
			out = append(out, p)
			recognised = true
		}
	}
	if nonBlank && !recognised {
		return nil, errors.New("plugin list: " + errNoRows.Error())
	}
	return out, nil
}

// ParseOutdated parses `plugin outdated`:
//
//	✅ fetched marketplace localmkt (sha=)
//
//	pending bumps (1):
//	  hello                1.0.0 → 1.1.0  [track]
//
//	Run `agentsync plugin upgrade --all` to upgrade and apply.
//
// or "✅ all plugins are up to date". A bump line is "<id> <from> → <to>
// [mode]" ("->" is accepted too). Other lines are ignored; non-blank input
// with neither a bump nor a recognised status line is an error.
func ParseOutdated(stdout string) ([]Outdated, error) {
	var out []Outdated
	ls, nonBlank := lines(stdout)
	recognised := false
	for _, line := range ls {
		low := strings.ToLower(line)
		if strings.Contains(low, "up to date") || strings.Contains(low, "pending bumps") || strings.Contains(low, "fetched marketplace") {
			recognised = true
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 || (f[2] != "→" && f[2] != "->") || !validPluginID(f[0]) ||
			!tokenRe.MatchString(f[1]) || !tokenRe.MatchString(f[3]) {
			continue
		}
		o := Outdated{Name: f[0], From: f[1], To: f[3]}
		if len(f) > 4 && strings.HasPrefix(f[4], "[") && strings.HasSuffix(f[4], "]") {
			if m := f[4][1 : len(f[4])-1]; modeRe.MatchString(m) {
				o.Mode = m
			}
		}
		out = append(out, o)
		recognised = true
	}
	if nonBlank && !recognised {
		return nil, errors.New("plugin outdated: " + errNoRows.Error())
	}
	return out, nil
}

// ParseMarketplaces parses `marketplace list`:
//
//	localmkt             url=/path/or/https://host/repo.git   sha=
//	(no marketplaces registered; try: agentsync marketplace add <url>)
//
// The url runs to the last " sha=" so a path with spaces survives. Names
// must be plain ids (no "@"). Unparseable lines are ignored; non-blank
// input with nothing recognisable is an error.
func ParseMarketplaces(stdout string) ([]Marketplace, error) {
	var out []Marketplace
	ls, nonBlank := lines(stdout)
	recognised := false
	for _, line := range ls {
		if strings.HasPrefix(line, "(") {
			recognised = true
			continue
		}
		name, rest, ok := strings.Cut(line, " ")
		if !ok || !idPartRe.MatchString(name) {
			continue
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "url=") {
			continue
		}
		rest = rest[len("url="):]
		m := Marketplace{Name: name}
		if i := strings.LastIndex(" "+rest, " sha="); i >= 0 {
			sha := strings.Fields(rest[max(i-1, 0)+len(" sha="):])
			rest = strings.TrimSpace(rest[:max(i-1, 0)])
			if len(sha) > 0 && tokenRe.MatchString(sha[0]) {
				m.SHA = sha[0]
			}
		}
		if cleanText(rest) {
			m.URL = rest
		}
		out = append(out, m)
		recognised = true
	}
	if nonBlank && !recognised {
		return nil, errors.New("marketplace list: " + errNoRows.Error())
	}
	return out, nil
}
