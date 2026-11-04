// Package ui is the Bubble Tea application shell: tabs, the confirm modal,
// the activity log, interactive exec handover and the user/project scope
// switch. Feature screens implement Screen and are registered with NewApp.
// It also holds what the screens share: the list and scroll viewport
// (List, Scroll), the footer hint wrapper (KeyLines), the text input
// (Field), the read helpers (Read, Rounds, Unreadable), the pre-mutation
// check (Verifier) and the output store (Output).
//
// UI toolkit: Bubble Tea v2 — charm.land/bubbletea/v2 v2.1.1,
// charm.land/lipgloss/v2 v2.0.6 (charm.land/bubbles/v2 v2.2.1 is the matching
// components module if a screen needs one). Tests drive programs with
// github.com/charmbracelet/x/exp/teatest/v2
// v2.0.0-20261101151554-27ecf3e4d454. Note the v2 shapes: Model.View returns
// tea.View (use tea.NewView(s)), keys arrive as tea.KeyPressMsg and are
// matched with msg.String() ("tab", "shift+tab", "ctrl+c", "esc", "enter").
//
// Scope: the shell owns the current scope and its canonical home (HomeFor:
// Deps.AgentsyncHome in user scope, ProjectHome(Deps.ProjectDir), that is
// <project>/.agentsync, in project scope). A scope switch hands every
// ScopeAware screen the new runner and home, moves the edit boundary
// (EditFileMsg paths must resolve inside the current home) and the confirm
// modal names the scope a command runs under. Every DoMsg a screen emits is
// stamped with the scope it was built under (Do, DoOr), and the shell
// refuses a scope-aware action whose scope is no longer the current one.
//
// Ending: q and ctrl+c are handled by the shell (a running write is waited
// for or interrupted, see exec.go); SIGTERM, SIGHUP and an off-TTY SIGINT
// end the program through Bubble Tea, and App.Filter (tea.WithFilter) logs
// a run still in flight as abandoned on the way out.
//
// Loading: only the active screen reads at startup; the others are stale
// and refresh when first shown, after a scope switch, or after a write or
// an edit elsewhere. Every screen read is cancelled when it is superseded
// (Rounds) and on quit (Deps.Cancel).
//
// Rendering safety: every string the shell renders from outside (screen
// titles, command output, errors, action titles) goes through mask.Sanitize,
// and command output and errors through mask.Text. Commands are only ever
// shown via actions.Action.Display (masked), never raw Argv.
package ui
