package model

import (
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/lusky3/agentsync-tui/internal/ident"
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
	tokenRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+:-]{0,127}$`)
	modeRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)
	errNoRows = errors.New("no rows in output")
)

// ID returns the bare plugin id: the part before "@marketplace".
func (p Plugin) ID() string { id, _, _ := strings.Cut(p.Name, "@"); return id }

// ID returns the bare plugin id of a pending bump (before "@marketplace").
func (o Outdated) ID() string { id, _, _ := strings.Cut(o.Name, "@"); return id }

// validPluginID accepts "name" or "name@marketplace", validating both halves
// with the shared identifier rule (ident.Valid, actions.ValidID).
func validPluginID(s string) bool {
	id, mkt, has := strings.Cut(s, "@")
	if !ident.Valid(id) {
		return false
	}
	return !has || ident.Valid(mkt)
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

// parseRows is the shared shape of the plain-text list parsers: each
// trimmed non-blank line is either a status note (isNote: it counts as
// recognised output but is not a row) or a candidate row (parse: ok false
// skips it). Non-blank input with nothing recognised is an error naming
// cmd. No rows is a nil slice.
func parseRows[T any](stdout, cmd string, isNote func(line string) bool, parse func(line string) (T, bool)) ([]T, error) {
	var out []T
	ls, nonBlank := lines(stdout)
	recognised := false
	for _, line := range ls {
		if isNote(line) {
			recognised = true
			continue
		}
		if row, ok := parse(line); ok {
			out = append(out, row)
			recognised = true
		}
	}
	if nonBlank && !recognised {
		return nil, errors.New(cmd + ": " + errNoRows.Error())
	}
	return out, nil
}

// parenNote reports a parenthesised status line ("(no plugins installed;
// ...)"), as plugin list and marketplace list print when empty.
func parenNote(line string) bool { return strings.HasPrefix(line, "(") }

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
	return parseRows(stdout, "plugin list", parenNote, parsePluginLine)
}

// parsePluginLine parses one `plugin list` row; ok is false unless the
// line names a valid plugin id and holds at least one known field.
func parsePluginLine(line string) (p Plugin, ok bool) {
	f := strings.Fields(line)
	if len(f) < 2 || !validPluginID(f[0]) {
		return Plugin{}, false
	}
	p = Plugin{Name: f[0]}
	known := false
	for _, tok := range f[1:] {
		known = p.setField(tok) || known
	}
	return p, known
}

// setField applies one `plugin list` token to p and reports whether it is
// a known field: "enabled", "disabled" or version=. A version or sha that
// is not a safe token is left empty; sha= alone does not make a row.
func (p *Plugin) setField(tok string) (known bool) {
	k, v, eq := strings.Cut(tok, "=")
	switch {
	case !eq && tok == "enabled":
		p.Enabled = true
		return true
	case !eq && tok == "disabled":
		return true
	case eq && k == "version":
		if tokenRe.MatchString(v) {
			p.Version = v
		}
		return true
	case eq && k == "sha" && tokenRe.MatchString(v):
		p.SHA = v
	}
	return false
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
	return parseRows(stdout, "plugin outdated", outdatedNote, parseBump)
}

// outdatedNote reports one of the status lines `plugin outdated` prints
// around (or instead of) the bumps.
func outdatedNote(line string) bool {
	low := strings.ToLower(line)
	return strings.Contains(low, "up to date") || strings.Contains(low, "pending bumps") || strings.Contains(low, "fetched marketplace")
}

// parseBump parses one "<id> <from> → <to> [mode]" line; a mode that is
// not a safe word is left empty.
func parseBump(line string) (Outdated, bool) {
	f := strings.Fields(line)
	if len(f) < 4 || (f[2] != "→" && f[2] != "->") || !validPluginID(f[0]) ||
		!tokenRe.MatchString(f[1]) || !tokenRe.MatchString(f[3]) {
		return Outdated{}, false
	}
	o := Outdated{Name: f[0], From: f[1], To: f[3]}
	if len(f) > 4 && strings.HasPrefix(f[4], "[") && strings.HasSuffix(f[4], "]") {
		if m := f[4][1 : len(f[4])-1]; modeRe.MatchString(m) {
			o.Mode = m
		}
	}
	return o, true
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
	return parseRows(stdout, "marketplace list", parenNote, parseMarketplaceLine)
}

// parseMarketplaceLine parses one "<name> url=<url> sha=<sha>" row. A sha
// that is not a safe token, or a url holding control characters, is left
// empty; the row is still kept.
func parseMarketplaceLine(line string) (Marketplace, bool) {
	name, rest, ok := strings.Cut(line, " ")
	if !ok || !ident.Valid(name) {
		return Marketplace{}, false
	}
	rest, ok = strings.CutPrefix(strings.TrimSpace(rest), "url=")
	if !ok {
		return Marketplace{}, false
	}
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
	return m, true
}
