package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

const helpText = `Keys
  tab / shift+tab   next / previous screen
  1-9               jump to screens 1-9 (tab / shift+tab reach the rest)
  s                 toggle scope (user / project)
  l                 toggle activity log
  ?                 toggle this help
  q                 quit (waits for a running write to finish)
  ctrl+c            cancel a running read; a running write needs ctrl+c
                    twice (it gets SIGINT, then is killed after 10s);
                    quit when idle

Lists and long views (every screen)
  up/k, down/j      move the cursor, or scroll, one row
  ctrl+u, pgup      a page up          ctrl+d, pgdown   a page down
  home / end        first / last row
  r                 refresh the screen

Confirm dialog
  y                 run          n / esc   cancel (enter does not confirm)
  destructive actions: type the verb shown, then enter`

const noProjectText = "no project directory: start with --project <path>"

// handleKey routes a key press: ctrl+c first, then the modal, then a
// key-capturing screen, then the global keys, then the active screen.
func (a *App) handleKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		if a.running != nil {
			return a.interrupt()
		}
		a.shutdown()
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
			if !a.running.act.ReadOnly {
				return a.quitAfterWrite()
			}
			return a.quitWhenCancelled()
		}
		a.shutdown()
		return tea.Quit
	case "tab":
		return a.selectScreen(a.active + 1)
	case "shift+tab":
		return a.selectScreen(a.active - 1)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if i := int(s[0] - '1'); i < len(a.screens) {
			return a.selectScreen(i)
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

// selectScreen makes screen i (wrapping) the active one and refreshes it if
// its data is stale (never loaded, or loaded before a scope switch or a
// write elsewhere).
func (a *App) selectScreen(i int) tea.Cmd {
	n := len(a.screens)
	if n == 0 {
		return nil
	}
	a.active = ((i % n) + n) % n
	if a.stale[a.active] {
		return a.refreshActive()
	}
	return nil
}

// toggleScope switches user<->project, rebuilding the runner and
// interactive builder through Deps.WithScope, moving the canonical home to
// the new scope's tree (HomeFor), handing every ScopeAware screen the new
// pair and refreshing the active screen; the others load when shown.
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
		return a.chipCmd() // drops in-flight results; the chip shows unavailable
	}
	a.noProject = false
	a.run, a.interactive = a.d.WithScope(next)
	a.home = HomeFor(a.d, next)
	chip := a.chipCmd()
	a.setStatus(statusInfo, "scope: "+string(next))
	for _, scr := range a.screens {
		if sa, ok := scr.(ScopeAware); ok {
			sa.SetScope(a.run, next, a.home)
		}
	}
	a.markOthersStale()
	return tea.Batch(chip, a.refreshActive())
}
