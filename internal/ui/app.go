package ui

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

// Screen is one tab of the application.
type Screen interface {
	tea.Model
	Title() string
	Refresh() tea.Cmd
}

// KeyCapturer is implemented by screens that sometimes need every key (for
// example while a text input is focused). While CapturesKeys reports true
// the shell forwards all keys except ctrl+c to the screen.
type KeyCapturer interface{ CapturesKeys() bool }

// ScopeAware is implemented by screens that run their own reads: on a scope
// switch the shell hands them the runner and the canonical home for the new
// scope (HomeFor) before calling Refresh. A screen that reads files from the
// home must read the new one from then on and drop what it loaded before.
type ScopeAware interface {
	SetScope(r runner.Runner, s runner.Scope, home string)
}

// Shutdowner is implemented by screens that run background reads: the shell
// calls Shutdown once when the program is quitting, so in-flight children are
// cancelled rather than orphaned.
type Shutdowner interface{ Shutdown() }

// shutdown cancels every background read the shell and its screens own.
func (a *App) shutdown() {
	a.rootCancel()
	if a.d.Cancel != nil {
		a.d.Cancel()
	}
	for _, s := range a.screens {
		if sd, ok := s.(Shutdowner); ok {
			sd.Shutdown()
		}
	}
}

// Deps is everything the shell needs from the outside world.
type Deps struct {
	Runner runner.Runner
	// Interactive builds the handover command for Interactive actions (in
	// production (*runner.Exec).Interactive). Nil disables handovers.
	Interactive func(args ...string) *exec.Cmd
	// AgentsyncHome is the user-scope canonical tree (~/.agentsync or
	// $AGENTSYNC_HOME).
	AgentsyncHome string
	// ProjectHome is the project-scope canonical tree, <ProjectDir>/.agentsync
	// (what `agentsync init --scope project` creates and every scope-aware
	// command with --project operates on). main computes it; when empty,
	// HomeFor derives it from ProjectDir.
	ProjectHome string
	Scope       runner.Scope
	ProjectDir  string
	// LogPath is the append-only activity log; empty disables the file.
	LogPath string
	// WithScope returns the runner and interactive builder for a scope.
	// Nil disables scope switching.
	WithScope func(runner.Scope) (runner.Runner, func(args ...string) *exec.Cmd)
	// Ctx is the root of every background read a screen starts (BaseCtx);
	// Cancel cancels it. main creates both before the screens are built;
	// App.shutdown calls Cancel so quitting stops in-flight children (the
	// Exec runner interrupts, then kills, the process when its context is
	// cancelled). Nil Ctx
	// means context.Background (reads then outlive a quit).
	Ctx    context.Context
	Cancel context.CancelFunc
	// Timeouts bounds the actions the shell runs (and the plugins screen's
	// network read); nil means DefaultTimeouts. See Limits.
	Timeouts *Timeouts
}

// BaseCtx is the context a screen derives its reads from: Deps.Ctx, or
// context.Background when none was given.
func BaseCtx(d Deps) context.Context {
	if d.Ctx != nil {
		return d.Ctx
	}
	return context.Background()
}

// HomeFor returns the canonical tree the given scope reads: AgentsyncHome for
// user scope; ProjectHome (or <ProjectDir>/.agentsync) for project scope, or
// "" when there is no project directory.
func HomeFor(d Deps, scope runner.Scope) string {
	if scope != runner.ScopeProject {
		return d.AgentsyncHome
	}
	if d.ProjectHome != "" {
		return d.ProjectHome
	}
	if d.ProjectDir != "" {
		return filepath.Join(d.ProjectDir, ".agentsync")
	}
	return ""
}

type statusKind int

const (
	statusInfo statusKind = iota
	statusOK
	statusWarn
	statusFail
)

// logPaneLines is the height of the activity pane when shown.
const logPaneLines = 6

// App is the root Bubble Tea model.
type App struct {
	d           Deps
	run         runner.Runner
	interactive func(args ...string) *exec.Cmd
	scope       runner.Scope
	home        string // canonical tree of the current scope (HomeFor)
	noProject   bool

	screens []Screen
	active  int

	width, height int
	modal         *modal
	showLog       bool
	showHelp      bool

	log        *activityLog
	logNote    string
	banner     banner
	status     string
	statusKind statusKind
	running    *running
	queue      []actions.Action // follow-ups (EditFileMsg.After) run in order
	quitting   bool             // q pressed while running: exit once the run is logged

	chip    chipState // header health chip
	chipGen int       // chip generation; older results are dropped

	// root is cancelled on quit; every background read (the chip's checks)
	// derives from it so quitting never orphans a child process.
	root       context.Context
	rootCancel context.CancelFunc
	chipCancel context.CancelFunc // the current chip round; superseded rounds are cancelled
}

// NewApp builds the shell around screens.
func NewApp(d Deps, screens ...Screen) *App {
	if d.Scope == "" {
		d.Scope = runner.ScopeUser
	}
	root, rootCancel := context.WithCancel(BaseCtx(d))
	return &App{
		root:        root,
		rootCancel:  rootCancel,
		d:           d,
		run:         d.Runner,
		interactive: d.Interactive,
		scope:       d.Scope,
		home:        HomeFor(d, d.Scope),
		noProject:   d.Scope == runner.ScopeProject && d.ProjectDir == "",
		screens:     screens,
		width:       80,
		height:      24,
		log:         newActivityLog(d.LogPath),
	}
}

// Init starts the background version check and every screen's Init.
func (a *App) Init() tea.Cmd {
	var cmds []tea.Cmd
	if a.run != nil {
		cmds = append(cmds, checkVersionCmd(a.root, a.run))
	}
	cmds = append(cmds, a.chipCmd()) // unavailable, not pending, without a runner or project
	// Nothing screen-side runs in project scope without a directory; leaving
	// that state refreshes every screen (toggleScope).
	if !a.noProject {
		for _, s := range a.screens {
			cmds = append(cmds, s.Init())
		}
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, a.resize()
	case tea.KeyPressMsg:
		return a, a.handleKey(msg)
	case DoMsg:
		return a, a.request(msg.Action)
	case RunMsg:
		return a, a.finish(msg)
	case EditFileMsg:
		return a, a.editFile(msg)
	case editDoneMsg:
		return a, a.editDone(msg)
	case quitDeadlineMsg:
		if a.quitting {
			return a, a.quitDeadline()
		}
		return a, nil
	case chipMsg:
		if msg.gen == a.chipGen && msg.state != chipSuperseded {
			a.chip = msg.state
		}
		return a, nil
	case versionMsg:
		a.banner = bannerFor(msg)
		return a, a.resize()
	}
	return a, tea.Batch(a.broadcast(msg)...)
}

func (a *App) current() Screen {
	if len(a.screens) == 0 {
		return nil
	}
	return a.screens[a.active]
}

func (a *App) setStatus(k statusKind, s string) { a.status, a.statusKind = s, k }

// updateScreen forwards msg to screen i, keeping the returned model if it
// is still a Screen.
func (a *App) updateScreen(i int, msg tea.Msg) tea.Cmd {
	m, cmd := a.screens[i].Update(msg)
	if s, ok := m.(Screen); ok {
		a.screens[i] = s
	}
	return cmd
}

func (a *App) updateActive(msg tea.Msg) tea.Cmd {
	if len(a.screens) == 0 {
		return nil
	}
	return a.updateScreen(a.active, msg)
}

// broadcast forwards msg to every screen (screens ignore what is not theirs).
func (a *App) broadcast(msg tea.Msg) []tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(a.screens)+1)
	for i := range a.screens {
		cmds = append(cmds, a.updateScreen(i, msg))
	}
	if m, ok := msg.(EditOutcomeMsg); ok && m.Finished {
		cmds = append(cmds, a.chipCmd()) // the edit may have changed config
	}
	return cmds
}

// resize tells every screen the size of the body area.
func (a *App) resize() tea.Cmd {
	return tea.Batch(a.broadcast(tea.WindowSizeMsg{Width: a.width, Height: a.bodyHeight()})...)
}

func (a *App) chromeLines() int {
	n := 3 // header, status, footer
	if a.banner.kind != bannerNone {
		n++
	}
	if a.showLog {
		n += logPaneLines
	}
	return n
}

func (a *App) bodyHeight() int {
	if h := a.height - a.chromeLines(); h > 0 {
		return h
	}
	return 1
}

// View implements tea.Model.
func (a *App) View() tea.View {
	var lines []string
	lines = append(lines, a.header())
	if a.banner.kind != bannerNone {
		lines = append(lines, bannerStyle(a.banner.kind).Render(a.banner.text))
	}
	lines = append(lines, fit(a.body(), a.bodyHeight())...)
	if a.showLog {
		lines = append(lines, fit(a.logPane(), logPaneLines)...)
	}
	lines = append(lines, statusStyle(a.statusKind).Render(a.statusLine()))
	lines = append(lines, styleDim.Render("tab/1-9 switch • s scope • l log • ? help • q quit"))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, a.width, "")
	}
	if len(lines) > a.height {
		lines = lines[:a.height]
	}
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

func sanitizeScope(s runner.Scope) string { return mask.Sanitize(string(s)) }

func (a *App) body() string {
	switch {
	case a.modal != nil:
		return a.modal.view(a.width, a.bodyHeight())
	case a.showHelp:
		return helpText
	case a.noProject:
		return styleWarn.Render(noProjectText)
	case len(a.screens) == 0:
		return styleDim.Render("No screens registered")
	}
	return a.current().View().Content
}

func (a *App) logPane() string {
	es := a.log.entries()
	head := styleBold.Render("Activity")
	if a.logNote != "" {
		head += "  " + styleWarn.Render(a.logNote)
	}
	out := []string{head}
	if len(es) == 0 {
		return head + "\n" + styleDim.Render("(no activity yet)")
	}
	if len(es) > logPaneLines-1 {
		es = es[len(es)-(logPaneLines-1):]
	}
	for _, e := range es {
		out = append(out, e.String())
	}
	return strings.Join(out, "\n")
}

func (a *App) statusLine() string {
	if a.status == "" {
		return " "
	}
	return oneLine(a.status)
}

// fit pads or clips s to exactly n lines.
func fit(s string, n int) []string {
	ls := strings.Split(s, "\n")
	if len(ls) > n {
		ls = ls[:n]
	}
	for len(ls) < n {
		ls = append(ls, "")
	}
	return ls
}

// The shell's styles come from theme so every screen shares one palette.
var (
	styleBold      = theme.Bold
	styleDim       = theme.Dim
	styleError     = theme.Bad
	styleWarn      = theme.Warn
	styleInfo      = theme.Info
	styleOK        = theme.OK
	styleTab       = theme.Tab
	styleTabActive = theme.TabActive
	styleModal     = theme.Modal
)

func bannerStyle(k bannerKind) lipgloss.Style {
	switch k {
	case bannerError:
		return styleError
	case bannerWarn:
		return styleWarn
	case bannerInfo:
		return styleInfo
	}
	return styleDim
}

func statusStyle(k statusKind) lipgloss.Style {
	switch k {
	case statusOK:
		return styleOK
	case statusWarn:
		return styleWarn
	case statusFail:
		return styleError
	}
	return styleDim
}
