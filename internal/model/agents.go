package model

import (
	"errors"
	"regexp"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/ident"
)

// Agent is one line of `agentsync agent list [--all]`.
type Agent struct {
	Name       string
	Enabled    bool
	Scope      string // "" when not shown (unregistered) or not a safe token
	Registered bool
}

var scopeRe = regexp.MustCompile(`^[a-z]+$`)

// ParseAgentList parses `agent list` / `agent list --all` text output.
//
// Formats seen with agentsync (dev build, 2026-11):
//
//	claude     enabled=true scope=user              (agent list)
//	claude       registered enabled=true scope=user (agent list --all)
//	amp          available                          (agent list --all: unregistered)
//	(no agents registered; try: ...)                (agent list, nothing registered)
//
// A line is an agent when its first field is a valid name and a later field
// is a known status token ("available", "registered" or "enabled=…");
// unknown trailing tokens are ignored, as are blank lines, parenthesised
// notes and lines that do not parse. Non-blank input from which nothing
// parses (and which holds no note) is an error.
func ParseAgentList(stdout string) ([]Agent, error) {
	var out []Agent
	recognised := false
	nonBlank := false
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		nonBlank = true
		if strings.HasPrefix(line, "(") {
			recognised = true // e.g. "(no agents registered; ...)"
			continue
		}
		if a, ok := parseAgentLine(line); ok {
			out = append(out, a)
			recognised = true
		}
	}
	if nonBlank && !recognised {
		return nil, errors.New("agent list: no agents in output")
	}
	return out, nil
}

func parseAgentLine(line string) (Agent, bool) {
	f := strings.Fields(line)
	if len(f) < 2 || !ident.Valid(f[0]) {
		return Agent{}, false
	}
	a := Agent{Name: f[0]}
	known := false
	for _, tok := range f[1:] {
		k, v, hasEq := strings.Cut(tok, "=")
		switch {
		case !hasEq && tok == "available":
			known = true
		case !hasEq && tok == "registered":
			a.Registered, known = true, true
		case hasEq && k == "enabled":
			a.Registered, a.Enabled, known = true, v == "true", true
		case hasEq && k == "scope":
			if scopeRe.MatchString(v) {
				a.Scope = v
			}
		}
	}
	return a, known
}
