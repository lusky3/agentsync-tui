// Package model parses the JSON that agentsync emits for status, diff and
// explain.
package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// StatusReport is the result of `agentsync status --json`.
type StatusReport struct {
	Agents []AgentStatus `json:"agents"`
}

// AgentStatus is one agent's items.
type AgentStatus struct {
	Agent string       `json:"agent"`
	Items []StatusItem `json:"items"`
}

// StatusItem is one managed destination key. State carries the raw
// classification string (the JSON key is "class").
type StatusItem struct {
	Path    string `json:"path"`
	Pointer string `json:"pointer"`
	State   string `json:"class"`
}

// attention lists the classifications that mean the destination will be
// overwritten or reclaimed (the legend's ✗ entries); settled the ones where
// destination and canonical agree.
var (
	attention = map[string]bool{"drift": true, "conflict": true, "foreign-collision": true, "orphan-drifted": true}
	settled   = map[string]bool{"clean": true, "converged": true}
)

// NeedsAttention reports whether class means the destination was edited
// outside agentsync (the drift filter, the red state cell).
func NeedsAttention(class string) bool { return attention[class] }

// IsSettled reports whether class means destination and canonical agree.
func IsSettled(class string) bool { return settled[class] }

// Drifted returns the items whose destination was edited outside agentsync.
func (r StatusReport) Drifted() []StatusItem {
	var out []StatusItem
	for _, a := range r.Agents {
		for _, it := range a.Items {
			if NeedsAttention(it.State) {
				out = append(out, it)
			}
		}
	}
	return out
}

// MCPServerID returns <id> for a JSON pointer of exactly "/mcpServers/<id>"
// (pointer-unescaped: ~1 is '/', ~0 is '~'); ok is false for any other
// shape, including deeper pointers.
func MCPServerID(pointer string) (id string, ok bool) {
	rest, ok := strings.CutPrefix(pointer, "/mcpServers/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return UnescapePointer(rest), true
}

// UnescapePointer undoes JSON-pointer escaping (RFC 6901) on one segment.
func UnescapePointer(seg string) string {
	return strings.NewReplacer("~1", "/", "~0", "~").Replace(seg)
}

// ParseStatus parses `agentsync status --json` stdout.
//
// Observed with agentsync dev ~v0.17.0 (captured in a hermetic home):
//   - Item JSON fields: "path", "pointer", "class" (not "state"); top level is
//     {"agents":[{"agent","items":[...]}],"summary":{<class>:<count>}}.
//   - `status --json` wrote nothing to stderr. The "ℹ INFO ..." notices
//     (e.g. "scope: user") go to stderr (seen on `apply`), so stdout is
//     clean JSON; leading non-JSON bytes before the first '{' are stripped
//     anyway.
//   - "class" values observed: "new", "clean", "drift". The legend
//     (`status --legend`) defines: clean, converged, new, pending, drift,
//     conflict, foreign-collision, orphan, orphan-drifted.
func ParseStatus(stdout string) (StatusReport, error) {
	var r StatusReport
	if err := decode(stdout, &r, true); err != nil {
		return StatusReport{}, fmt.Errorf("parse status: %w", err)
	}
	return r, nil
}

// decode strips bytes before the first '{' and unmarshals leniently (unknown
// fields are ignored). When echo is false, errors carry only the input length
// because the input may hold file content.
func decode(in string, v any, echo bool) error {
	i := strings.IndexByte(in, '{')
	if i < 0 {
		return inputErr(in, echo, "no JSON object found")
	}
	if err := json.Unmarshal([]byte(in[i:]), v); err != nil {
		return inputErr(in, echo, err.Error())
	}
	return nil
}

func inputErr(in string, echo bool, msg string) error {
	if !echo {
		return fmt.Errorf("%s (input %d bytes)", msg, len(in))
	}
	head := in
	if len(head) > 80 {
		head = head[:80]
	}
	head = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' || r == 0x7f {
			return -1
		}
		return r
	}, head)
	return fmt.Errorf("%s (input starts: %q)", msg, head)
}
