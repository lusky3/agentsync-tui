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
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/source"
	"github.com/lusky3/agentsync-tui/internal/ui"
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
	base  context.Context
	limit time.Duration // bound on one local read (ui.Deps.Limits().Local())
	run   runner.Runner
	scope runner.Scope // the current scope (the vault itself is user-level)
	home  string       // canonical tree whose servers the cross-check scans
	reads ui.Rounds    // the list read; superseded rounds are cancelled

	loaded bool
	keys   []string // as the CLI returned them
	vault  vaultState
	// vaultMsg is the masked CLI message when the backend is not configured.
	vaultMsg string
	// uses maps every key referenced by a server to the (sanitised) ids of
	// the servers referencing it, including keys that are not in the vault.
	uses    map[string][]string
	srvNote string // why the server cross-check is incomplete (display-safe)
	list    ui.List
	// loadNote explains why the last `secret list` failed (display-safe);
	// the last good list is kept.
	loadNote string
	note     string // last screen message, display-safe

	// verify is the pre-removal re-read of vkey.
	verify ui.Verifier
	vkey   string

	// The key-name field. locked means an invalid character was typed: the
	// buffer was wiped and further input is ignored until esc.
	typing   bool
	input    ui.Field
	locked   bool
	fieldErr string

	width, height int
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

// verification is what the pre-removal re-read brings back.
type verification struct {
	key   string
	fresh listMsg
}

// New returns the secrets screen.
func New(d ui.Deps) *Screen {
	return &Screen{
		base: ui.BaseCtx(d), limit: d.Limits().Local(), run: d.Runner, scope: d.Scope, home: ui.HomeFor(d, d.Scope),
		input: ui.Field{Accept: keyRune}, width: 80, height: 20,
	}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Secrets" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware: swap the runner and the home the
// cross-check scans, and drop the uses loaded from the old tree.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, home string) {
	s.run, s.scope, s.home = r, sc, home
	s.reads.Supersede()
	s.uses, s.srvNote = nil, ""
	s.invalidate()
}

// Shutdown implements ui.Shutdowner.
func (s *Screen) Shutdown() {
	s.reads.Cancel()
	s.verify.Invalidate()
}

// invalidate drops any in-flight verification: its result will be ignored.
func (s *Screen) invalidate() {
	if s.verify.Busy() {
		s.note = ""
	}
	s.verify.Invalidate()
}

// CapturesKeys implements ui.KeyCapturer: true while the key-name field has
// focus.
func (s *Screen) CapturesKeys() bool { return s.typing }

// Refresh re-reads the vault keys and the servers' references, cancelling
// the previous read.
func (s *Screen) Refresh() tea.Cmd {
	s.invalidate()
	gen, ctx := s.reads.Next(s.base)
	r, home, limit := s.run, s.home, s.limit
	return func() tea.Msg {
		m := readList(ctx, gen, r, limit)
		m.uses, m.srv = loadUses(home)
		return m
	}
}

func readList(ctx context.Context, gen int, r runner.Runner, timeout time.Duration) listMsg {
	res, err := ui.Read(ctx, r, timeout, "secret", "list")
	if err == nil && res.ExitCode != 0 {
		if model.IsBackendNotConfigured(res.Stderr) || model.IsBackendNotConfigured(res.Stdout) {
			text := res.Stderr
			if !model.IsBackendNotConfigured(text) {
				text = res.Stdout
			}
			return listMsg{gen: gen, vault: vaultNotConfigured, msg: mask.FirstLine(text)}
		}
	}
	if note := ui.ReadNote("secret list", res, err); note != "" {
		return listMsg{gen: gen, note: note}
	}
	keys, err := model.ParseSecretKeys(res.Stdout)
	if err != nil {
		return listMsg{gen: gen, note: ui.Unreadable("secret list", res, err)}
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
		slices.Sort(ids)
	}
	note := ""
	if err != nil {
		note = mask.FirstLine(err.Error())
	}
	return uses, note
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case listMsg:
		if s.reads.Current(msg.gen) {
			s.apply(msg)
		}
	case ui.VerifiedMsg:
		if v, ok := s.verify.Accept(msg); ok {
			return s, s.verified(v.(verification))
		}
	case tea.PasteMsg:
		if s.typing {
			s.rejectPaste()
		}
	case tea.KeyPressMsg:
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
	s.list.Clamp(len(s.keys))
	if s.verify.Busy() && s.selected() != s.vkey {
		s.invalidate()
	}
}

// selected returns the key under the cursor, or "".
func (s *Screen) selected() string {
	if s.list.Cursor < 0 || s.list.Cursor >= len(s.keys) {
		return ""
	}
	return s.keys[s.list.Cursor]
}

func (s *Screen) key(k string) tea.Cmd {
	if s.list.Key(k, len(s.keys)) {
		s.invalidate()
		return nil
	}
	switch k {
	case "r":
		s.note = ""
		return s.Refresh()
	case "n":
		s.invalidate() // a pending verification must not open a modal mid-typing
		s.typing, s.locked, s.fieldErr = true, false, ""
		s.input.Clear()
	case "u":
		return s.emit(actions.SecretSet(s.selected()))
	case "E":
		return s.emit(actions.SecretEdit(), nil)
	case "x":
		return s.remove()
	}
	return nil
}

// emit hands a built action to the shell; a builder error is shown as an
// invalid key (the only input these builders validate).
func (s *Screen) emit(act actions.Action, err error) tea.Cmd {
	if err != nil {
		s.note = "invalid secret key"
		return nil
	}
	s.note = ""
	return ui.Do(act, s.scope)
}
