// Package ui is the Bubble Tea application shell: tabs, the confirm modal,
// the activity log, interactive exec handover and the user/project scope
// switch. Feature screens (tasks 7-16) implement Screen and are registered
// with NewApp.
//
// UI toolkit: Bubble Tea v2 — charm.land/bubbletea/v2 v2.1.1,
// charm.land/lipgloss/v2 v2.0.6 (charm.land/bubbles/v2 v2.2.1 is the matching
// components module if a screen needs one). Tests drive programs with
// github.com/charmbracelet/x/exp/teatest/v2
// v2.0.0-20261101151554-27ecf3e4d454. Note the v2 shapes: Model.View returns
// tea.View (use tea.NewView(s)), keys arrive as tea.KeyPressMsg and are
// matched with msg.String() ("tab", "shift+tab", "ctrl+c", "esc", "enter").
//
// Rendering safety: every string the shell renders from outside (screen
// titles, command output, errors, action titles) goes through mask.Sanitize,
// and command output and errors through mask.Text. Commands are only ever
// shown via actions.Action.Display (masked), never raw Argv.
package ui
