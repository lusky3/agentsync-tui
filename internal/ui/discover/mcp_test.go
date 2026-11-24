package discover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/discover/dockercat"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpreg"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpsrc"
	"github.com/lusky3/agentsync-tui/internal/netx"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const wantRequests = "requests = %d, want %d"

func wantReqs(t *testing.T, f *fake, n int) {
	t.Helper()
	if got := len(f.requests()); got != n {
		t.Fatalf(wantRequests, got, n)
	}
}

// Opening the tab, refreshing, typing, switching the source or the scope
// and leaving the field make no request; enter in the field makes one.
func TestNoNetworkUntilEnter(t *testing.T) {
	f := newFake(t)
	s := newScreen(f.deps())
	drive(s, s.Init())
	drive(s, s.Refresh())
	press(s, "/")
	if !s.CapturesKeys() {
		t.Fatal("the focused field does not capture keys")
	}
	typeText(s, braveQuery+" mn")
	press(s, keyEsc, "m", "m", "n", "i", keyEnter)
	s.SetScope(nil, runner.ScopeProject, "")
	drive(s, s.Refresh())
	_ = s.View()
	wantReqs(t, f, 0)

	press(s, "/", keyEnter)
	wantReqs(t, f, 1)
	if got := f.requests()[0].Query().Get("search"); got != braveQuery+" mn" {
		t.Errorf("search = %q", got)
	}
}

func TestSearchRendersRowsAndMarksDeprecated(t *testing.T) {
	f := newFake(t)
	s := newScreen(f.deps())
	search(s, braveQuery)
	v := view(s)
	for _, want := range []string{braveRegName + "  Brave search from the registry", markDeprecated + oldRegName, "2 results"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, markDeprecated+braveRegName) {
		t.Error("an active entry is marked deprecated")
	}
}

// --offline and a missing client say so and never call a source.
func TestOfflineShowsMessageAndNoRequest(t *testing.T) {
	f := newFake(t)
	for _, c := range []struct {
		name string
		disc ui.Discover
		want string
	}{
		{"offline", ui.Discover{Offline: true, HTTP: f.client()}, NoteOffline},
		{"no client", ui.Discover{}, NoteUnavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newScreen(ui.Deps{Discover: c.disc})
			if !strings.Contains(view(s), c.want) {
				t.Errorf("tab does not say %q on open:\n%s", c.want, view(s))
			}
			press(s, "/")
			typeText(s, braveQuery)
			_, cmd := s.Update(keyMsg(keyEnter))
			if cmd != nil {
				t.Fatal("enter returned a command")
			}
			press(s, "n")
			if !strings.Contains(view(s), c.want) {
				t.Errorf("view lacks %q:\n%s", c.want, view(s))
			}
		})
	}
	wantReqs(t, f, 0)
}

// A refusal is one error, one request, no retry; 429 says rate limited;
// a body that is not a page says so; other errors show their text.
func TestBlockedShowsOneErrorNoRetry(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
		code             int
	}{
		{"forbidden", "nope", "refused the request", http.StatusForbidden},
		{"rate limited", "slow down", mcpreg.Host + ": " + NoteRateLimited, http.StatusTooManyRequests},
		{"html", "<html>challenge</html>", "refused the request", http.StatusOK},
		{"bad json", `{"servers": [`, "unreadable response from " + mcpreg.Host, http.StatusOK},
		{"server error", "boom", "HTTP 500", http.StatusInternalServerError},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			f.registry = func(url.Values) (int, string) { return c.code, c.body }
			s := newScreen(f.deps())
			if out := search(s, braveQuery); len(out) != 0 {
				t.Errorf("messages for the shell: %#v", out)
			}
			wantReqs(t, f, 1)
			v := view(s)
			if strings.Count(v, c.want) != 1 {
				t.Errorf("want %q once:\n%s", c.want, v)
			}
			if strings.Contains(v, c.body) && c.code != http.StatusOK {
				t.Errorf("the response body is shown:\n%s", v)
			}
		})
	}
}

// An unreadable body is reported as exactly that, naming the source's host.
func TestBadResponseNamesItsHost(t *testing.T) {
	f := newFake(t)
	f.catalog = "registry: [unclosed"
	f.registry = func(url.Values) (int, string) { return http.StatusOK, `{"servers": 5}` }
	s := newScreen(f.deps())
	search(s, "")
	if got, want := s.cur().err, "unreadable response from "+mcpreg.Host; got != want {
		t.Errorf("registry note %q, want %q", got, want)
	}
	press(s, "m")
	search(s, "")
	if got, want := s.cur().err, "unreadable response from "+dockercat.Host; got != want {
		t.Errorf("catalog note %q, want %q", got, want)
	}
}

func TestErrNoteRateLimitedEvenUnwrapped(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &netx.StatusError{Code: http.StatusTooManyRequests})
	if got := errNote("h", err); got != "h: "+NoteRateLimited {
		t.Errorf("errNote = %q", got)
	}
}

// hostile carries an ANSI SGR, a screen clear, an OSC title and hyperlink,
// a bidi override, a zero-width space, a newline and a tab.
const hostile = "evil\x1b[38;5;200mred\x1b[2J\x1b]0;title\x07\u202eoverride\u200bzw\x1b]8;;http://x\x07\nnext\tcol"

// sgrOnly matches text whose every escape is a plain SGR (our styling).
var sgrOnly = regexp.MustCompile(`^(?:[^\x1b]|\x1b\[[0-9;]*m)*$`)

// assertInert checks a raw (unstripped) view: only our SGR styling, no
// payload escape, bidi or zero-width rune, and nothing on extra lines.
func assertInert(t *testing.T, what, raw string) {
	t.Helper()
	if !sgrOnly.MatchString(raw) {
		t.Errorf("%s: an escape other than styling reached the view: %q", what, raw)
	}
	for _, bad := range []string{"[2J", "]0;", "]8;", "200m", "\u202e", "\u200b", "\x07"} {
		if strings.Contains(raw, bad) {
			t.Errorf("%s: %q reached the view", what, bad)
		}
	}
	if !strings.Contains(raw, "next col") {
		t.Errorf("%s: newline/tab not folded onto one line: %q", what, raw)
	}
}

// Hostile text is made inert at render time even when a source let it
// through: titles, descriptions, labels, reasons, provenance, trust lines
// and prompt descriptions. The source path (registry JSON) is covered too.
func TestHostileDescriptionRenderedInert(t *testing.T) {
	s := newScreen(ui.Deps{})
	e := mcpsrc.Entry{
		Source: mcpsrc.OfficialName, ID: "x/" + hostile, Title: "t " + hostile, Description: hostile,
		Trust: []string{"trust " + hostile},
		Installs: []mcpsrc.Install{
			{Label: "ok " + hostile, Draft: &mcpreg.Draft{
				Spec:       actions.AddSpec{ID: "x", Type: "stdio", Command: "npx"},
				Provenance: "prov " + hostile, Trust: []string{"opt " + hostile},
				Prompts: []mcpreg.Prompt{{Name: "N", Description: hostile}},
			}},
			{Label: "no " + hostile, Unsupported: "why " + hostile},
		},
	}
	bad := mcpsrc.Entry{Source: mcpsrc.OfficialName, ID: "y", Title: "u", Installs: []mcpsrc.Install{{Label: "l", Unsupported: "r " + hostile}}}
	s.res[0] = results{searched: true, query: hostile, entries: []mcpsrc.Entry{e, bad}}
	assertInert(t, "list", s.View().Content)
	press(s, "j")
	assertInert(t, "unsupported row", s.View().Content)
	press(s, "k", keyEnter)
	assertInert(t, "detail", s.View().Content)
	press(s, "j", "i")
	assertInert(t, "refusal note", s.View().Content)

	f := newFake(t)
	srv := `{"name": "io.example/h", "description": ` + jsonQuote(hostile) + `,
		"remotes": [{"type": "sse", "url": "https://h.example/sse"}]}`
	f.registry = func(url.Values) (int, string) { return http.StatusOK, regPage("", regEntry(srv, "")) }
	s = newScreen(f.deps())
	search(s, "h")
	assertInert(t, "registry list", s.View().Content)
	press(s, keyEnter)
	assertInert(t, "registry detail", s.View().Content)
}

// i on the chosen option emits exactly what the add form needs.
func TestInstallEmitsComposeMsgForChosenPackage(t *testing.T) {
	f := newFake(t)
	s := newScreen(f.deps())
	search(s, braveQuery)
	press(s, keyEnter)
	e, ok := s.shown()
	if !ok || e.ID != braveRegName || len(e.Installs) != 3 {
		t.Fatalf("detail shows %#v", e)
	}
	if s.opt.Cursor != 0 {
		t.Fatalf("first installable option not chosen: %d", s.opt.Cursor)
	}
	out := press(s, "j", "i") // the pypi package
	if len(out) != 1 {
		t.Fatalf("messages: %#v", out)
	}
	got, ok := out[0].(ui.ComposeMcpMsg)
	if !ok {
		t.Fatalf("got %T", out[0])
	}
	in := e.Installs[1]
	want := ui.ComposeMcpMsg{
		Spec:       in.Draft.Spec,
		Provenance: in.Draft.Provenance,
		Trust:      mcpsrc.InstallTrust(e, in),
		RefID:      in.Draft.Spec.ID,
	}
	for _, p := range in.Draft.Prompts {
		want.Prompts = append(want.Prompts, ui.FieldPrompt{Name: p.Name, Required: p.Required, Default: p.Default, Description: p.Description})
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
	if got.Spec.Args[0] == "" || got.RefID != got.Spec.ID || got.Refusal() != "" {
		t.Errorf("message not usable: %#v (%q)", got, got.Refusal())
	}
	if !strings.Contains(strings.Join(got.Spec.Args, " "), "brave-mcp") || got.Spec.Command != "uvx" {
		t.Errorf("not the pypi option: %#v", got.Spec)
	}
	got.Spec.Args[0] = "changed" // the message shares nothing with the listed entry
	if e2, _ := s.shown(); e2.Installs[1].Draft.Spec.Args[0] == "changed" {
		t.Error("the message aliases the entry's spec")
	}
}

// The npm option has prompts: they arrive as FieldPrompts, never Secret.
func TestInstallCarriesPromptsAndList(t *testing.T) {
	f := newFake(t)
	s := newScreen(f.deps())
	search(s, braveQuery)
	out := press(s, "i") // several options: the detail opens to choose
	if len(out) != 0 || s.detail != braveRegName || s.note != noteChoose {
		t.Fatalf("i on a row with several options: out=%v detail=%q note=%q", out, s.detail, s.note)
	}
	out = press(s, "i")
	m := out[0].(ui.ComposeMcpMsg)
	if len(m.Prompts) != 1 || m.Prompts[0].Name != "REGION" || !m.Prompts[0].Required ||
		m.Prompts[0].Default != "us" || m.Prompts[0].Secret || m.Prompts[0].Description == "" {
		t.Errorf("prompts = %#v", m.Prompts)
	}
	if !strings.Contains(m.Spec.Env["BRAVE_API_KEY"], "${secret:") {
		t.Errorf("secret not a reference: %#v", m.Spec.Env)
	}

	// A row with one installable option installs straight from the list.
	f.registry = func(url.Values) (int, string) { return http.StatusOK, regPage("", regEntry(oldServer, "")) }
	search(s, "old")
	if out := press(s, "i"); len(out) != 1 {
		t.Errorf("single option from the list: %#v", out)
	}
}

// A draft whose prompt asks for a secret value is never emitted (the
// message's Refusal is checked first); the reason is shown.
func TestRefusedDraftShowsReasonNotEmitted(t *testing.T) {
	draft := func(mut func(*mcpreg.Draft)) mcpsrc.Entry {
		d := &mcpreg.Draft{Spec: actions.AddSpec{ID: "x", Type: "stdio", Command: "npx"}}
		mut(d)
		return mcpsrc.Entry{ID: "x", Title: "x", Installs: []mcpsrc.Install{{Label: "l", Draft: d}}}
	}
	secret := draft(func(d *mcpreg.Draft) { d.Prompts = []mcpreg.Prompt{{Name: "TOKEN", Secret: true}} })
	s := newScreen(ui.Deps{})
	s.res[0] = results{searched: true, entries: []mcpsrc.Entry{secret}}
	if out := press(s, "i"); len(out) != 0 {
		t.Errorf("secret prompt emitted %#v", out)
	}
	if !strings.HasPrefix(s.note, noteRefused) || !strings.Contains(view(s), "secret value") {
		t.Errorf("note %q", s.note)
	}
	// RefID always equals Spec.ID (D37): it is taken from the spec.
	e := draft(func(*mcpreg.Draft) {})
	if m, why := composeFor(e, e.Installs[0]); why != "" || m.RefID != m.Spec.ID {
		t.Errorf("composeFor = %#v, %q", m, why)
	}
}

func TestSourceToggleSwitchesBackendsAndKeepsQuery(t *testing.T) {
	f := newFake(t)
	s := newScreen(f.deps())
	search(s, braveQuery)
	press(s, "m")
	if s.query.Value != braveQuery || !strings.Contains(view(s), "source: docker") {
		t.Fatalf("after m: query %q\n%s", s.query.Value, view(s))
	}
	if strings.Contains(view(s), braveRegName) {
		t.Error("the official results show under docker")
	}
	press(s, "/", keyEnter)
	reqs := f.requests()
	if len(reqs) != 2 || reqs[0].Path != registryPath || reqs[1].Path != catalogPath {
		t.Fatalf("requests: %v", reqs)
	}
	if v := view(s); !strings.Contains(v, "Brave Search") || strings.Contains(v, "Loose") {
		t.Errorf("docker results (filtered by the kept query):\n%s", v)
	}
	press(s, "m")
	if v := view(s); !strings.Contains(v, braveRegName) || strings.Contains(v, "Brave Search  ") {
		t.Errorf("official results not kept:\n%s", v)
	}
	wantReqs(t, f, 2)
}

func TestUnsupportedEntryShownWithReasonAndNotInstallable(t *testing.T) {
	f := newFake(t)
	f.registry = func(url.Values) (int, string) { return http.StatusOK, regPage("", regEntry(evilServer, "")) }
	s := newScreen(f.deps())
	search(s, "evil")
	reason := mcpreg.ErrUnsafeIdentifier.Error()
	if v := view(s); !strings.Contains(v, evilRegName+"  "+markUnsupported) || !strings.Contains(v, reason) {
		t.Errorf("row:\n%s", v)
	}
	if out := press(s, "i"); len(out) != 0 || !strings.HasPrefix(s.note, noteNoInstall) {
		t.Errorf("i from the list: %v, note %q", out, s.note)
	}
	press(s, keyEnter)
	if out := press(s, "i"); len(out) != 0 || !strings.Contains(view(s), reason) {
		t.Errorf("i in detail: %v\n%s", out, view(s))
	}

	// The docker entry without a digest, likewise.
	press(s, keyEsc, "m")
	search(s, "loose")
	if v := view(s); !strings.Contains(v, dockercat.ReasonNoDigest) {
		t.Errorf("docker row:\n%s", v)
	}
	if out := press(s, "i"); len(out) != 0 {
		t.Errorf("docker i: %v", out)
	}
}

// The same server in both sources is two rows, one per source, each
// keeping its own identity (source, ID) and its own install.
func TestSameServerInBothSourcesListedPerSource(t *testing.T) {
	f := newFake(t)
	s := newScreen(f.deps())
	search(s, braveQuery)
	press(s, "m")
	search(s, braveQuery)
	off, dock := s.res[0].entries, s.res[1].entries
	if len(off) != 2 || len(dock) != 1 {
		t.Fatalf("official %d, docker %d", len(off), len(dock))
	}
	if off[0].Source != mcpsrc.OfficialName || dock[0].Source != dockercat.Name || dock[0].ID != braveKey {
		t.Errorf("identities: %q/%q, %q/%q", off[0].Source, off[0].ID, dock[0].Source, dock[0].ID)
	}
	m := press(s, "i")[0].(ui.ComposeMcpMsg)
	if m.Spec.Command != "docker" || !strings.HasPrefix(m.Provenance, "docker catalog: brave") {
		t.Errorf("docker install: %#v", m)
	}
}

func TestTrustLinesShownInDetail(t *testing.T) {
	f := newFake(t)
	f.registry = func(url.Values) (int, string) { return http.StatusOK, regPage("", regEntry(ociServer, "")) }
	s := newScreen(f.deps())
	search(s, "image")
	press(s, keyEnter)
	v := view(s)
	for _, want := range []string{mcpsrc.TrustUnreviewed, mcpsrc.TrustNamespace, "image tag is mutable"} {
		if strings.Count(v, want) != 1 {
			t.Errorf("want %q once in detail:\n%s", want, v)
		}
	}

	press(s, keyEsc, "m")
	search(s, "")
	press(s, "j", keyEnter) // loose: unsupported, trust still shown
	if v := view(s); !strings.Contains(v, dockercat.TrustNotVerified) || !strings.Contains(v, dockercat.TrustDocker) {
		t.Errorf("unsupported docker detail lacks trust:\n%s", v)
	}
	press(s, keyEsc, "k", keyEnter)
	if v := view(s); !strings.Contains(v, dockercat.TrustDigestPrefix) || !strings.Contains(v, dockercat.TrustNotVerified) {
		t.Errorf("docker detail:\n%s", v)
	}
}

// Shutdown cancels the request in flight; its late result changes nothing.
func TestCancelledOnShutdown(t *testing.T) {
	f := newFake(t)
	f.hold = true
	s := newScreen(f.deps())
	press(s, "/")
	_, cmd := s.Update(keyMsg(keyEnter))
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	waitFor(t, func() bool { return len(f.requests()) == 1 })
	s.Shutdown()
	select {
	case <-f.held:
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not cancelled")
	}
	m := (<-done).(pageMsg)
	if !errors.Is(m.err, context.Canceled) {
		t.Errorf("err = %v", m.err)
	}
	s.Update(m)
	if v := view(s); strings.Contains(v, "canceled") || strings.Contains(v, "unavailable") {
		t.Errorf("a cancelled search shows as a failure:\n%s", v)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A result from a superseded round (a newer search, a source switch, a
// scope switch, quit) is ignored.
func TestResultsTaggedByOwnRounds(t *testing.T) {
	stale := mcpsrc.Page{Entries: []mcpsrc.Entry{{ID: "stale", Title: "stale-entry"}}}
	for name, supersede := range map[string]func(s *MCPScreen){
		"newer search":  func(s *MCPScreen) { search(s, "newer") },
		"source switch": func(s *MCPScreen) { press(s, "m", "m") },
		"scope switch":  func(s *MCPScreen) { s.SetScope(nil, runner.ScopeUser, "") },
		"shutdown":      func(s *MCPScreen) { s.Shutdown() },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			s := newScreen(f.deps())
			press(s, "/")
			s.Update(keyMsg(keyEnter)) // the command is never run
			old := s.rounds.Gen()
			supersede(s)
			s.Update(pageMsg{gen: old, src: 0, page: stale})
			if strings.Contains(view(s), "stale-entry") {
				t.Errorf("stale result shown:\n%s", view(s))
			}
		})
	}
	// The current round's result is shown.
	s := newScreen(ui.Deps{})
	s.Update(pageMsg{gen: s.rounds.Gen(), src: 0, page: stale})
	if !strings.Contains(view(s), "stale-entry") {
		t.Error("a current result is not shown")
	}
}

// Skipped records are counted; paging stops at an empty or repeated
// cursor (A -> B -> A); a new search starts over.
func TestSkippedShownAndCyclicCursorStopsPaging(t *testing.T) {
	f := newFake(t)
	bad := `{"server": {"name": 5}}`
	f.registry = func(q url.Values) (int, string) {
		switch q.Get("cursor") {
		case "":
			return http.StatusOK, regPage("A", regEntry(braveServer, ""), bad)
		case "A":
			return http.StatusOK, regPage("B", regEntry(oldServer, ""), bad)
		}
		return http.StatusOK, regPage("A", regEntry(ociServer, ""), regEntry(braveServer, ""))
	}
	s := newScreen(f.deps())
	search(s, braveQuery)
	if v := view(s); !strings.Contains(v, "1 entries could not be read") || !strings.Contains(v, "n for more") {
		t.Errorf("first page:\n%s", v)
	}
	press(s, "n", "n")
	wantReqs(t, f, 3)
	if r := s.cur(); len(r.entries) != 3 || r.next != "" || r.skipped != 2 {
		t.Errorf("after the cycle: %d entries, next %q, skipped %d", len(r.entries), r.next, r.skipped)
	}
	press(s, "n")
	wantReqs(t, f, 3)
	if strings.Contains(view(s), "n for more") {
		t.Error("n still offered after a repeated cursor")
	}
	search(s, braveQuery)
	if r := s.cur(); len(r.entries) != 1 || r.next != "A" || r.skipped != 1 {
		t.Errorf("new search did not reset: %d entries, next %q, skipped %d", len(r.entries), r.next, r.skipped)
	}
	press(s, "n")
	wantReqs(t, f, 5)
}

// The cache is opened inside the command, not in Update, and not before
// the first search; a cache that cannot open only means no cache.
func TestCacheOpenedInsideCommandOnly(t *testing.T) {
	f := newFake(t)
	dir := filepath.Join(t.TempDir(), "cache")
	d := f.deps()
	d.Discover.Cache = ui.NewLazyCache(dir)
	s := newScreen(d)
	press(s, "/")
	typeText(s, braveQuery)
	_, cmd := s.Update(keyMsg(keyEnter))
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache dir exists before the command ran: %v", err)
	}
	drive(s, cmd)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("cache not opened by the search: %v", err)
	}
	search(s, braveQuery) // answered from the cache
	wantReqs(t, f, 1)

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d.Discover.Cache = ui.NewLazyCache(blocker) // not a directory: Open fails
	s = newScreen(d)
	search(s, braveQuery)
	if len(s.cur().entries) != 2 {
		t.Errorf("search failed without a cache:\n%s", view(s))
	}
}

// The field: enter runs, esc leaves; keys reach it only while focused.
func TestSearchFieldFocus(t *testing.T) {
	s := newScreen(ui.Deps{})
	if s.CapturesKeys() {
		t.Fatal("the field captures keys on open (tab and q would be trapped)")
	}
	press(s, "/")
	typeText(s, "ab")
	press(s, keyEsc)
	if s.CapturesKeys() || s.query.Value != "ab" {
		t.Errorf("after esc: focus %v, query %q", s.CapturesKeys(), s.query.Value)
	}
	typeText(s, "cd") // not focused: not typed
	if s.query.Value != "ab" {
		t.Errorf("query %q", s.query.Value)
	}
}

func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// An entry without any install option (which the sources never send) is
// shown and refused, not a crash.
func TestEntryWithoutInstallsDoesNotPanic(t *testing.T) {
	s := newScreen(ui.Deps{})
	s.res[0] = results{searched: true, entries: []mcpsrc.Entry{{ID: "bare", Title: "bare"}}}
	if out := press(s, "i"); len(out) != 0 || !strings.Contains(view(s), mcpsrc.ReasonNoInstall) {
		t.Errorf("list i: %v\n%s", out, view(s))
	}
	if out := press(s, keyEnter, "i"); len(out) != 0 {
		t.Errorf("detail i: %v", out)
	}
	_ = s.View()
}
