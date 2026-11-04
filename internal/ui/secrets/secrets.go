// Package secrets is the secrets screen: it lists the KEYS in agentsync's
// secrets vault (never values) with the MCP servers that reference each one,
// warns about referenced keys that are missing, and drives set, edit and
// remove through the agentsync CLI via ui.DoMsg.
//
// Scope: the vault is per machine (the `secret` group refuses --scope and
// --project), so the key list is the same in both scopes. The cross-check of
// ${secret:KEY} references, however, scans the servers of the CURRENT
// scope's canonical home (ui.HomeFor): in project scope "used by" and
// "missing" describe <project>/.agentsync/mcp, not the user tree.
//
// The TUI never sees a secret value. There is no `secret get` action; set and
// edit hand the terminal to agentsync's own prompt (Interactive actions), and
// the only text field on this screen takes a key NAME and refuses every
// character outside the key charset, so a typed assignment is rejected and
// wiped rather than stored.
package secrets

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/source"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// listTimeout bounds one `secret list` refresh; verifyTimeout bounds the
// pre-removal re-read, which the user is waiting on.
const (
	listTimeout   = 30 * time.Second
	verifyTimeout = 5 * time.Second
)

const (
	staleNote  = "secret list changed — refreshed"
	rejectNote = "key name only — the value is entered in agentsync's own prompt"
	pasteNote  = "paste is disabled here: type the key NAME (values are entered in agentsync's own prompt)"
	valueNote  = "this looks like a secret value, not a key name"
)

type vaultState int

const (
	vaultUnknown       vaultState = iota // no successful read yet
	vaultOK                              // list read
	vaultNotConfigured                   // no [secrets] backend
)

// refRe finds ${secret:KEY} references anywhere in a string.
var refRe = regexp.MustCompile(`\$\{secret:([A-Za-z0-9][A-Za-z0-9._-]*)\}`)

// Screen is the secrets tab.
type Screen struct {
	d     ui.Deps
	run   runner.Runner
	scope runner.Scope
	home  string // canonical tree whose servers the cross-check scans
	gen   int    // refresh generation; older results are dropped

	loaded bool
	keys   []string // as the CLI returned them
	vault  vaultState
	// vaultMsg is the masked CLI message when the backend is not configured.
	vaultMsg string
	// uses maps every key referenced by a server to the (sanitised) ids of
	// the servers referencing it, including keys that are not in the vault.
	uses    map[string][]string
	srvNote string // why the server cross-check is incomplete (display-safe)
	cursor  int
	// loadNote explains why the last `secret list` failed (display-safe);
	// the last good list is kept.
	loadNote string
	note     string // last screen message, display-safe

	// verifying is set while a pre-removal re-read of vkey is in flight.
	verifying bool
	vreq      int
	vkey      string

	// The key-name field. locked means an invalid character was typed: the
	// buffer was wiped and further input is ignored until esc.
	typing   bool
	input    string
	locked   bool
	fieldErr string

	width, height int

	// reached counts key presses that reached a key handler (tests only).
	reached int
}

// listMsg carries one `secret list` read plus the server cross-check data.
type listMsg struct {
	gen   int
	keys  []string
	vault vaultState
	msg   string // masked CLI message (vaultNotConfigured)
	note  string // non-empty: the read failed
	uses  map[string][]string
	srv   string // server load note
}

// verifyMsg carries the fresh read for one pending removal.
type verifyMsg struct {
	req   int
	key   string
	fresh listMsg
}

// New returns the secrets screen.
func New(d ui.Deps) *Screen {
	return &Screen{d: d, run: d.Runner, scope: d.Scope, home: ui.HomeFor(d, d.Scope), width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Secrets" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware: swap the runner and the home the
// cross-check scans, and drop the uses loaded from the old tree.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, home string) {
	s.run, s.scope, s.home = r, sc, home
	s.gen++
	s.uses, s.srvNote = nil, ""
	s.invalidate()
}

// invalidate drops any in-flight verification: its result will be ignored.
func (s *Screen) invalidate() {
	if s.verifying {
		s.verifying, s.note = false, ""
	}
	s.vreq++
}

// CapturesKeys implements ui.KeyCapturer: true while the key-name field has
// focus.
func (s *Screen) CapturesKeys() bool { return s.typing }

// Refresh re-reads the vault keys and the servers' references.
func (s *Screen) Refresh() tea.Cmd {
	s.gen++
	s.invalidate()
	gen, r, home := s.gen, s.run, s.home
	return func() tea.Msg {
		m := readList(gen, r, listTimeout)
		m.uses, m.srv = loadUses(home)
		return m
	}
}

func readList(gen int, r runner.Runner, timeout time.Duration) listMsg {
	if r == nil {
		return listMsg{gen: gen, note: "no runner"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := r.Run(ctx, "secret", "list")
	if err != nil {
		return listMsg{gen: gen, note: oneLine(mask.Text(err.Error()))}
	}
	if res.ExitCode != 0 {
		if model.IsBackendNotConfigured(res.Stderr) || model.IsBackendNotConfigured(res.Stdout) {
			text := res.Stderr
			if !model.IsBackendNotConfigured(text) {
				text = res.Stdout
			}
			return listMsg{gen: gen, vault: vaultNotConfigured, msg: firstLine(mask.Text(text))}
		}
		n := "secret list exited " + itoa(res.ExitCode)
		if l := firstLine(mask.Text(res.Stderr)); l != "" {
			n += ": " + l
		}
		return listMsg{gen: gen, note: n}
	}
	keys, err := model.ParseSecretKeys(res.Stdout)
	if err != nil {
		return listMsg{gen: gen, note: "secret list output unreadable"}
	}
	return listMsg{gen: gen, keys: keys, vault: vaultOK}
}

// loadUses scans the canonical servers for ${secret:KEY} references. A load
// error yields a note; partial results are kept.
func loadUses(home string) (map[string][]string, string) {
	servers, err := source.LoadServers(home)
	uses := map[string][]string{}
	for _, sv := range servers {
		id := mask.Sanitize(sv.ID)
		seen := map[string]bool{}
		add := func(v string) {
			for _, m := range refRe.FindAllStringSubmatch(v, -1) {
				if !seen[m[1]] {
					seen[m[1]] = true
					uses[m[1]] = append(uses[m[1]], id)
				}
			}
		}
		add(sv.Command)
		add(sv.URL)
		for _, a := range sv.Args {
			add(a)
		}
		for _, v := range sv.Env {
			add(v)
		}
		for _, v := range sv.Headers {
			add(v)
		}
	}
	for _, ids := range uses {
		sort.Strings(ids)
	}
	note := ""
	if err != nil {
		note = firstLine(mask.Text(err.Error()))
	}
	return uses, note
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case listMsg:
		if msg.gen == s.gen {
			s.apply(msg)
		}
	case verifyMsg:
		return s, s.verified(msg)
	case tea.PasteMsg:
		if s.typing {
			s.rejectPaste()
		}
	case tea.KeyPressMsg:
		s.reached++
		if s.typing {
			return s, s.typingKey(msg)
		}
		return s, s.key(msg.String())
	}
	return s, nil
}

func (s *Screen) apply(m listMsg) {
	s.loaded = true
	s.uses, s.srvNote = m.uses, m.srv
	switch {
	case m.note != "":
		s.loadNote = m.note // keep the last good list
		return
	case m.vault == vaultNotConfigured:
		s.keys, s.vault, s.vaultMsg, s.loadNote = nil, vaultNotConfigured, m.msg, ""
	default:
		s.keys, s.vault, s.vaultMsg, s.loadNote = m.keys, vaultOK, "", ""
	}
	s.cursor = max(min(s.cursor, len(s.keys)-1), 0)
	if s.verifying && s.selected() != s.vkey {
		s.invalidate()
	}
}

// selected returns the key under the cursor, or "".
func (s *Screen) selected() string {
	if s.cursor < 0 || s.cursor >= len(s.keys) {
		return ""
	}
	return s.keys[s.cursor]
}

func (s *Screen) key(k string) tea.Cmd {
	switch k {
	case "up", "k":
		s.cursor = max(s.cursor-1, 0)
		s.invalidate()
	case "down", "j":
		s.cursor = max(min(s.cursor+1, len(s.keys)-1), 0)
		s.invalidate()
	case "r":
		s.note = ""
		return s.Refresh()
	case "n":
		s.invalidate() // a pending verification must not open a modal mid-typing
		s.typing, s.input, s.locked, s.fieldErr = true, "", false, ""
	case "u":
		return s.emit(actions.SecretSet(s.selected()))
	case "E":
		return s.emit(actions.SecretEdit())
	case "x":
		return s.remove()
	}
	return nil
}

// emit hands a built action to the shell; a builder error is shown instead.
func (s *Screen) emit(act actions.Action, err error) tea.Cmd {
	if err != nil {
		s.note = "invalid secret key"
		return nil
	}
	s.note = ""
	return func() tea.Msg { return ui.DoMsg{Action: act} }
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// oneLine makes s safe for a single terminal line.
func oneLine(s string) string {
	s = mask.Sanitize(s)
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(s)
}

// firstLine returns the first non-blank line of s, trimmed and sanitised.
func firstLine(s string) string {
	for _, l := range strings.Split(mask.Sanitize(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return oneLine(l)
		}
	}
	return ""
}
