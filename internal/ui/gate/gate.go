// Package gate is the preview-then-run rule shared by the apply/revert and
// import screens: a mutation may be handed to the shell only after a
// successful dry run of the same command, in the same scope. It holds no
// screen state and never runs anything; the screens emit the actions and
// feed it the shell's RunMsg.
package gate

import (
	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// Gate binds a successful preview to (real argv, scope). Its rules:
//
//   - the real command is allowed only after a successful dry run of the
//     same Previewed pair in the same scope (Allows);
//   - a preview survives Refresh;
//   - it is dropped by Invalidate (input edits, scope switch), by Drop (an
//     $EDITOR edit starting or finishing), by ANY completed real run of
//     ours (success, failure or cancel) and by any foreign non-ReadOnly
//     RunMsg; a foreign ReadOnly RunMsg is harmless;
//   - results for requests that are no longer current are dropped.
//
// The zero value is an empty gate.
type Gate struct {
	seq   int    // bumped by every invalidation; older results are dropped
	req   string // argv key of the in-flight dry run ("" = none)
	rseq  int    // seq the in-flight dry run was issued at
	rreal string // argv key of the real command the in-flight dry run previews
	rsc   runner.Scope
	ok    string // argv key of the real command a successful preview allows
	okSc  runner.Scope
	live  string // argv key of the real command handed to the shell
}

// Outcome of observing a RunMsg.
type Outcome int

// The outcomes.
const (
	NotOurs     Outcome = iota // a foreign or stale run: nothing changed
	PreviewOK                  // our dry run succeeded
	PreviewFail                // our dry run failed
	RanReal                    // our real run completed (any result)
	Dropped                    // a foreign mutation dropped the preview
)

// Invalidate forgets the preview and any dry run still in flight.
func (g *Gate) Invalidate() {
	g.seq++
	g.req, g.ok = "", ""
}

// Pending reports whether a preview is held or a dry run is in flight.
func (g *Gate) Pending() bool { return g.req != "" || g.ok != "" }

// Drop is Invalidate for an $EDITOR edit; it reports whether a preview (or
// a dry run in flight) was dropped.
func (g *Gate) Drop() bool {
	had := g.Pending()
	g.Invalidate()
	return had
}

// Reset forgets everything, including the armed real command (scope switch).
func (g *Gate) Reset() {
	g.Invalidate()
	g.live = ""
}

// Request records p.Preview as the current dry run, to allow p.Real in sc.
func (g *Gate) Request(p actions.Previewed, sc runner.Scope) {
	g.Invalidate()
	g.req, g.rseq, g.rreal, g.rsc = p.Preview.Key(), g.seq, p.Real.Key(), sc
}

// Allows reports whether the real (writing) action act may be handed to the
// shell in scope sc.
func (g *Gate) Allows(act actions.Action, sc runner.Scope) bool {
	return g.ok != "" && g.ok == act.Key() && g.okSc == sc
}

// Arm records the real (writing) action act as handed to the shell.
func (g *Gate) Arm(act actions.Action) { g.live = act.Key() }

// Observe applies a finished run to the gate.
func (g *Gate) Observe(m ui.RunMsg) Outcome {
	if len(m.Action.Argv) == 0 {
		return NotOurs
	}
	key := m.Action.Key()
	if m.Action.ReadOnly {
		if g.req == "" || key != g.req || g.rseq != g.seq {
			return NotOurs // foreign or stale read: harmless
		}
		g.req = ""
		if m.Err == nil && m.Result.ExitCode == 0 {
			g.ok, g.okSc = g.rreal, g.rsc
			return PreviewOK
		}
		g.ok = ""
		return PreviewFail
	}
	if g.live != "" && key == g.live {
		g.live = ""
		g.Invalidate() // the disk may have changed whatever the result
		return RanReal
	}
	g.Invalidate() // a mutation elsewhere may change what a preview described
	return Dropped
}
