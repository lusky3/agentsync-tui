package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/mask"
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
                                       space            a page down (long views)
  home / end        first / last row
  r                 refresh the screen

Confirm dialog
  y                 run          n / esc   cancel (enter does not confirm)
  destructive or guarded actions: type the verb shown, then enter
  a chain (stage, then commit) is confirmed once; a failed step stops it`

const noProjectText = "no project directory: start with --project <path>"

// handleKey routes a key press: ctrl+c first, then the modal, then a
// key-capturing screen, then the global keys, then the active screen.
func (a *App) handleKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	switch {
	case s == "ctrl+c":
		return a.ctrlC()
	case a.modal != nil:
		return a.modalKey(k)
	case a.activeCapturesKeys():
		return a.updateActive(k)
	}
	return a.globalKey(s, k)
}

// ctrlC interrupts the running action, or quits when nothing runs.
func (a *App) ctrlC() tea.Cmd {
	if a.running != nil {
		return a.interrupt()
	}
	a.shutdown()
	return tea.Quit
}

// activeCapturesKeys reports whether the active screen is taking text input
// (KeyCapturer), so every key but ctrl+c goes to it. Never without a
// project, whose placeholder replaces the screen.
func (a *App) activeCapturesKeys() bool {
	scr := a.current()
	if scr == nil || a.noProject {
		return false
	}
	kc, ok := scr.(KeyCapturer)
	return ok && kc.CapturesKeys()
}

// quit handles q: a running write is waited for, a running read cancelled
// first; otherwise the program quits at once.
func (a *App) quit() tea.Cmd {
	if a.running != nil && a.running.cancel != nil {
		if !a.running.act.ReadOnly {
			return a.quitAfterWrite()
		}
		return a.quitWhenCancelled()
	}
	a.shutdown()
	return tea.Quit
}

// globalKey handles the shell's own keys (s is k.String()); any other key
// goes to the active screen unless help or the no-project placeholder is
// on show.
func (a *App) globalKey(s string, k tea.KeyPressMsg) tea.Cmd {
	switch s {
	case "q":
		return a.quit()
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
// the new scope's tree (HomeFor), handing every ScopeSetter screen the new
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
		if sa, ok := scr.(ScopeSetter); ok {
			sa.SetScope(a.run, next, a.home)
		}
	}
	a.markOthersStale()
	return tea.Batch(chip, a.refreshActive())
}

// GotoMsg asks the shell to show the first screen titled Title (a screen's
// offer to jump, such as Drift or Apply after a pull), refreshing it if its
// data is stale. An unknown title only sets a warning.
type GotoMsg struct{ Title string }

// goTo handles a GotoMsg.
func (a *App) goTo(title string) tea.Cmd {
	for i, s := range a.screens {
		if s.Title() == title {
			return a.selectScreen(i)
		}
	}
	a.setStatus(statusWarn, "no screen named "+mask.OneLine(title))
	return nil
}
