package importer

import (
	"strings"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// gate is the preview-then-run rule, kept free of screen state so it can be
// shared later. Its rules match the apply screen's:
//
//   - the real command is allowed only after a successful dry run of the
//     same argv (minus --dry-run) in the same scope;
//   - a preview survives Refresh;
//   - it is dropped by invalidate (input edits, scope switch), by ANY
//     completed real run of ours (success, failure or cancel) and by any
//     foreign non-ReadOnly RunMsg; a foreign ReadOnly RunMsg is harmless;
//   - results for requests that are no longer current are dropped.
type gate struct {
	seq  int    // bumped by every invalidation; older results are dropped
	req  string // argv key of the in-flight dry run ("" = none)
	rseq int    // seq the in-flight dry run was issued at
	ok   string // argv key of the real command a successful preview allows
	okSc runner.Scope
	live string // argv key of the real command handed to the shell
}

// outcome of observing a RunMsg.
type outcome int

const (
	notOurs     outcome = iota
	previewOK           // our dry run succeeded
	previewFail         // our dry run failed
	ranReal             // our real run completed (any result)
	dropped             // a foreign mutation dropped the preview
)

func argvKey(a actions.Action) string { return strings.Join(a.Argv, "\x00") }

// realKey maps a dry-run argv key to the matching real command's key.
func realKey(dry string) string {
	parts := strings.Split(dry, "\x00")
	out := parts[:0]
	for _, p := range parts {
		if p != "--dry-run" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\x00")
}

// invalidate forgets the preview and any dry run still in flight.
func (g *gate) invalidate() {
	g.seq++
	g.req, g.ok = "", ""
}

// request records dry as the current preview request.
func (g *gate) request(dry actions.Action) {
	g.invalidate()
	g.req, g.rseq = argvKey(dry), g.seq
}

// allows reports whether real may be handed to the shell in scope sc.
func (g *gate) allows(real actions.Action, sc runner.Scope) bool {
	return g.ok != "" && g.ok == argvKey(real) && g.okSc == sc
}

// arm records real as handed to the shell.
func (g *gate) arm(real actions.Action) { g.live = argvKey(real) }

// observe applies a finished run to the gate.
func (g *gate) observe(m ui.RunMsg, sc runner.Scope) outcome {
	if len(m.Action.Argv) == 0 {
		return notOurs
	}
	key := argvKey(m.Action)
	if m.Action.ReadOnly {
		if g.req == "" || key != g.req || g.rseq != g.seq {
			return notOurs // foreign or stale read: harmless
		}
		g.req = ""
		if m.Err == nil && m.Result.ExitCode == 0 {
			g.ok, g.okSc = realKey(key), sc
			return previewOK
		}
		g.ok = ""
		return previewFail
	}
	if g.live != "" && key == g.live {
		g.live = ""
		g.invalidate() // the disk may have changed whatever the result
		return ranReal
	}
	g.invalidate() // a mutation elsewhere may change what an import would do
	return dropped
}
