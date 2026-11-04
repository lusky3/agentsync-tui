package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

const helpText = `Keys
  tab / shift+tab   next / previous screen
  1-9               jump to screen
  s                 toggle scope (user / project)
  l                 toggle activity log
  ?                 toggle this help
  q                 quit
  ctrl+c            cancel the running action, or quit when idle

Confirm dialog
  y / enter         run          n / esc   cancel
  destructive actions: type the verb shown, then enter`

const noProjectText = "no project directory: start with --project <path>"

// handleKey routes a key press: ctrl+c first, then the modal, then a
// key-capturing screen, then the global keys, then the active screen.
func (a *App) handleKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		if a.running != nil {
			a.cancelRunning()
			return nil
		}
		return tea.Quit
	}
	if a.modal != nil {
		return a.modalKey(k)
	}
	if scr := a.current(); scr != nil && !a.noProject {
		if kc, ok := scr.(KeyCapturer); ok && kc.CapturesKeys() {
			return a.updateActive(k)
		}
	}
	switch s {
	case "q":
		if a.running != nil && a.running.cancel != nil {
			return a.quitWhenCancelled()
		}
		return tea.Quit
	case "tab":
		a.selectScreen(a.active + 1)
	case "shift+tab":
		a.selectScreen(a.active - 1)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if i := int(s[0] - '1'); i < len(a.screens) {
			a.selectScreen(i)
		}
	case "s":
		return a.toggleScope()
	case "l":
		a.showLog = !a.showLog
		return a.resize()
	case "?":
		a.showHelp = !a.showHelp
	case "esc":
		if a.showHelp {
			a.showHelp = false
			return nil
		}
		return a.updateActive(k)
	default:
		if a.showHelp || a.noProject {
			return nil
		}
		return a.updateActive(k)
	}
	return nil
}

func (a *App) selectScreen(i int) {
	n := len(a.screens)
	if n == 0 {
		return
	}
	a.active = ((i % n) + n) % n
}

// toggleScope switches user<->project, rebuilding the runner and
// interactive builder through Deps.WithScope and refreshing every screen.
func (a *App) toggleScope() tea.Cmd {
	if a.d.WithScope == nil {
		a.setStatus(statusWarn, "scope switching unavailable")
		return nil
	}
	if a.running != nil {
		a.setStatus(statusFail, "busy: another action is in progress")
		return nil
	}
	next := runner.ScopeProject
	if a.scope == runner.ScopeProject {
		next = runner.ScopeUser
	}
	a.scope = next
	if next == runner.ScopeProject && a.d.ProjectDir == "" {
		a.noProject = true
		return nil
	}
	a.noProject = false
	a.run, a.interactive = a.d.WithScope(next)
	a.setStatus(statusInfo, "scope: "+string(next))
	cmds := make([]tea.Cmd, 0, len(a.screens))
	for _, scr := range a.screens {
		if sa, ok := scr.(ScopeAware); ok {
			sa.SetScope(a.run, next)
		}
		cmds = append(cmds, scr.Refresh())
	}
	return tea.Batch(cmds...)
}
