package discover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpreg"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpsrc"
	"github.com/lusky3/agentsync-tui/internal/netx"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	cursorA   = "A"
	cursorKey = "cursor"
	getFmt    = "GET x: %w"
)

// A failed next page (a 429, an interruption) leaves n working: the
// cursor counts as used only once its page has arrived.
func TestFailedNextPageCanBeRetried(t *testing.T) {
	f := newFake(t)
	var limited atomic.Bool
	limited.Store(true)
	f.registry = func(q url.Values) (int, string) {
		if q.Get(cursorKey) == cursorA {
			if limited.Load() {
				return http.StatusTooManyRequests, "slow down"
			}
			return http.StatusOK, regPage("", regEntry(ociServer, ""))
		}
		return http.StatusOK, regPage(cursorA, regEntry(braveServer, ""))
	}
	s := newScreen(f.deps())
	search(s, braveQuery)
	press(s, "n")
	wantReqs(t, f, 2)
	if !strings.Contains(view(s), NoteRateLimited) {
		t.Fatalf("429 not shown:\n%s", view(s))
	}
	limited.Store(false)
	press(s, "n")
	wantReqs(t, f, 3)
	if r := s.cur(); len(r.entries) != 2 || r.next != "" || r.err != "" {
		t.Errorf("after the retry: %d entries, next %q, err %q", len(r.entries), r.next, r.err)
	}
}

// An interrupted next page (source or scope switch) can be asked for again.
func TestInterruptedNextPageCanBeRetried(t *testing.T) {
	for name, interrupt := range map[string]func(s *MCPScreen){
		"source switch": func(s *MCPScreen) { press(s, "m", "m") },
		"scope switch":  func(s *MCPScreen) { s.SetScope(nil, runner.ScopeUser, "") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.registry = func(q url.Values) (int, string) {
				if q.Get(cursorKey) == cursorA {
					return http.StatusOK, regPage("", regEntry(ociServer, ""))
				}
				return http.StatusOK, regPage(cursorA, regEntry(braveServer, ""))
			}
			s := newScreen(f.deps())
			search(s, braveQuery)
			s.Update(keyMsg("n")) // the command is never run
			interrupt(s)
			press(s, "n")
			wantReqs(t, f, 2)
			if len(s.cur().entries) != 2 {
				t.Errorf("next page not fetched after the interruption:\n%s", view(s))
			}
		})
	}
}

// n with nothing more to load says so instead of doing nothing silently.
func TestNextPageWithNothingMoreSaysSo(t *testing.T) {
	f := newFake(t)
	s := newScreen(f.deps())
	search(s, braveQuery)
	press(s, "n")
	wantReqs(t, f, 1)
	if !strings.Contains(view(s), noteNoMore) {
		t.Errorf("no feedback for n:\n%s", view(s))
	}
}

// A note goes stale on list movement and when a next page is asked for.
func TestStaleNoteCleared(t *testing.T) {
	f := newFake(t)
	f.registry = func(q url.Values) (int, string) {
		if q.Get(cursorKey) == cursorA {
			return http.StatusOK, regPage("", regEntry(ociServer, ""))
		}
		return http.StatusOK, regPage(cursorA, regEntry(evilServer, ""), regEntry(oldServer, ""))
	}
	s := newScreen(f.deps())
	search(s, "x")
	press(s, "i")
	if !strings.HasPrefix(s.note, noteNoInstall) {
		t.Fatalf("note %q", s.note)
	}
	press(s, "j")
	if s.note != "" {
		t.Errorf("note %q kept after moving", s.note)
	}
	press(s, "k", "i")
	_, cmd := s.Update(keyMsg("n"))
	if s.note != "" || !strings.Contains(view(s), "searching") {
		t.Errorf("note %q outranks the next page:\n%s", s.note, view(s))
	}
	drive(s, cmd)
}

func TestPasteIntoSearchField(t *testing.T) {
	s := newScreen(ui.Deps{})
	s.Update(tea.PasteMsg{Content: "ignored"})
	if s.query.Value != "" {
		t.Fatalf("paste reached an unfocused field: %q", s.query.Value)
	}
	press(s, "/")
	s.Update(tea.PasteMsg{Content: "bra\r\nve\n"})
	if s.query.Value != braveQuery {
		t.Errorf("query %q, want %q", s.query.Value, braveQuery)
	}
}

// timeoutErr is a net.Error that timed out.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestErrNoteWording(t *testing.T) {
	const h = "reg.example"
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"403", fmt.Errorf("%w: %w", mcpreg.ErrBlocked, &netx.StatusError{Code: http.StatusForbidden}), h + " refused the request (HTTP 403)"},
		{"500", &netx.StatusError{Code: http.StatusInternalServerError}, h + " refused the request (HTTP 500)"},
		{"html", mcpreg.ErrBlocked, h + " refused the request"},
		{"429", &netx.StatusError{Code: http.StatusTooManyRequests}, h + ": " + NoteRateLimited},
		{"deadline", fmt.Errorf(getFmt, context.DeadlineExceeded), "timed out reaching " + h},
		{"net timeout", fmt.Errorf(getFmt, timeoutErr{}), "timed out reaching " + h},
		{"dns", fmt.Errorf(getFmt, &net.DNSError{Err: "no such host", Name: "x"}), "could not reach " + h},
		{"refused", fmt.Errorf(getFmt, &net.OpError{Op: "dial", Err: errors.New("connection refused")}), "could not reach " + h},
		{"bad", mcpreg.ErrBadResponse, "unreadable response from " + h},
		{"other", errors.New("netx: response too large: token=plainCANARY"), "request to " + h + " failed: "},
	} {
		got := errNote(h, c.err)
		if !strings.HasPrefix(got, c.want) || (c.name != "other" && got != c.want) {
			t.Errorf("%s: errNote = %q, want %q", c.name, got, c.want)
		}
		if strings.Contains(got, "CANARY") {
			t.Errorf("%s: errNote leaks: %q", c.name, got)
		}
	}
}

// threeOptions is an entry whose three install options each carry a
// trust line and a prompt.
func threeOptions() mcpsrc.Entry {
	e := mcpsrc.Entry{
		Source: mcpsrc.OfficialName, ID: "io.example/three", Title: "three", Description: "three options",
		Trust: []string{"entry trust one", "entry trust two"},
	}
	for i := 1; i <= 3; i++ {
		n := fmt.Sprint(i)
		e.Installs = append(e.Installs, mcpsrc.Install{Label: "option " + n, Draft: &mcpreg.Draft{
			Spec:       actions.AddSpec{ID: "three", Type: "stdio", Command: "npx", Env: map[string]string{"MODE": "m" + n}},
			Provenance: "prov " + n,
			Trust:      []string{"opt trust " + n},
			Prompts:    []mcpreg.Prompt{{Name: "ASK_" + n, Required: true}},
		}})
	}
	return e
}

// On a short screen the chosen option's whole block stays in view, and
// the entry's trust lines are part of the fixed head.
func TestDetailKeepsChosenBlockInView(t *testing.T) {
	s := newScreen(ui.Deps{})
	s.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	s.res[0] = results{searched: true, entries: []mcpsrc.Entry{threeOptions()}}
	press(s, keyEnter, "j", "j")
	v := view(s)
	for _, want := range []string{"> option 3", "prov 3", "opt trust 3", "asks for ASK_3", "entry trust one", "entry trust two"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
}

// Each option lists the environment it sets; a secret-looking name's
// literal value is masked.
func TestDetailListsFixedEnv(t *testing.T) {
	e := threeOptions()
	e.Installs = e.Installs[:1]
	e.Installs[0].Draft.Spec.Env = map[string]string{
		"MODE": "fast", "API_TOKEN": "plainCANARY", "API_KEY": "${secret:three.api_key}", "MULTI": "a\nb",
	}
	s := newScreen(ui.Deps{})
	s.res[0] = results{searched: true, entries: []mcpsrc.Entry{e}}
	press(s, keyEnter)
	v := view(s)
	for _, want := range []string{"sets MODE=fast", "sets API_KEY=${secret:three.api_key}", "sets API_TOKEN=", "sets MULTI=a b"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "CANARY") {
		t.Errorf("a secret value is shown:\n%s", v)
	}
}

// An option taller than the screen can be scrolled through with the page
// keys, without changing which option is chosen; j/k still choose.
func TestDetailScrollsWithinTallOption(t *testing.T) {
	e := threeOptions()
	e.Installs = e.Installs[:2]
	env := map[string]string{}
	for i := 1; i <= 20; i++ {
		env[fmt.Sprintf("E%02d", i)] = "v"
	}
	e.Installs[0].Draft.Spec.Env = env
	e.Installs[0].Draft.Prompts = []mcpreg.Prompt{{Name: "ASK_LAST", Required: true}}
	s := newScreen(ui.Deps{})
	const height = 16
	s.Update(tea.WindowSizeMsg{Width: 80, Height: height})
	s.res[0] = results{searched: true, entries: []mcpsrc.Entry{e}}
	press(s, keyEnter)
	fits := func(when string) string {
		t.Helper()
		v := view(s)
		if n := strings.Count(v, "\n") + 1; n > height {
			t.Errorf("%s: %d lines, want at most %d", when, n, height)
		}
		return v
	}
	if v := fits("opened"); !strings.Contains(v, "> option 1") || strings.Contains(v, "ASK_LAST") {
		t.Fatalf("opened: want the option's head, not its tail:\n%s", v)
	}
	for i := 0; i < 5 && !strings.Contains(view(s), "asks for ASK_LAST"); i++ {
		press(s, "ctrl+d")
	}
	if v := fits("scrolled"); !strings.Contains(v, "asks for ASK_LAST") || !strings.Contains(v, "sets E20=v") {
		t.Errorf("scrolled: the option's tail is not reachable:\n%s", v)
	}
	if s.opt.Cursor != 0 {
		t.Errorf("scrolling moved the chosen option to %d", s.opt.Cursor)
	}
	press(s, "ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u", "ctrl+u")
	if v := fits("back"); !strings.Contains(v, "> option 1") {
		t.Errorf("scrolled back: the option's label is not in view:\n%s", v)
	}
	press(s, "ctrl+d", "j")
	if v := fits("next option"); s.opt.Cursor != 1 || !strings.Contains(v, "> option 2") || !strings.Contains(v, "prov 2") {
		t.Errorf("j: cursor %d, want option 2 chosen and in view:\n%s", s.opt.Cursor, v)
	}
}
