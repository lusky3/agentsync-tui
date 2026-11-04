package drift

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// fakeRunner answers by the space-joined argv; unknown commands fail the test.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	res   map[string]runner.Result
	err   error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.err != nil {
		return runner.Result{}, f.err
	}
	return f.res[strings.Join(args, " ")], nil
}

func (f *fakeRunner) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "model", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func drive(s *Screen, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch m := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, m...)
		default:
			_, next := s.Update(m)
			queue = append(queue, next)
		}
	}
}

func press(s *Screen, keys ...string) {
	for _, k := range keys {
		_, cmd := s.Update(key(k))
		drive(s, cmd)
	}
}

func view(s *Screen) string { return ansi.Strip(s.View().Content) }

func newLoaded(t *testing.T, r runner.Runner) *Screen {
	t.Helper()
	s := New(ui.Deps{Runner: r, Scope: runner.ScopeUser})
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	drive(s, s.Refresh())
	return s
}

const (
	hPath = "/h/.gemini/mcp_config.json"
	hPtr  = "/mcpServers/stdio-srv"
)

const multiStatus = `{"agents":[
 {"agent":"antigravity","items":[
  {"path":"/h/a.json","pointer":"/mcpServers/ok1","class":"clean"},
  {"path":"/h/a.json","pointer":"/mcpServers/ok2","class":"clean"},
  {"path":"/h/a.json","pointer":"/mcpServers/d1","class":"drift"},
  {"path":"/h/a.json","pointer":"/mcpServers/c1","class":"conflict"}]},
 {"agent":"claude","items":[
  {"path":"/h/c.json","pointer":"/mcpServers/ok3","class":"clean"},
  {"path":"/h/c.json","pointer":"/other~1x","class":"orphan"}]}
]}`

func statusRunner(status string) *fakeRunner {
	return &fakeRunner{res: map[string]runner.Result{"status --json": {Stdout: status}}}
}

func TestInitEqualsRefresh(t *testing.T) {
	r := statusRunner(multiStatus)
	s := New(ui.Deps{Runner: r})
	drive(s, s.Init())
	if !strings.Contains(view(s), "antigravity") {
		t.Fatalf("Init did not load:\n%s", view(s))
	}
	if got := r.called(); len(got) != 1 || got[0] != "status --json" {
		t.Fatalf("calls = %v", got)
	}
}

func TestGroupsByAgentAndFiltersDrift(t *testing.T) {
	s := newLoaded(t, statusRunner(multiStatus))
	v := view(s)
	for _, w := range []string{"antigravity", "claude", "ok1", "d1", "c1", "ok3", "/other/x"} {
		if !strings.Contains(v, w) {
			t.Errorf("all view missing %q:\n%s", w, v)
		}
	}
	if strings.Index(v, "antigravity") > strings.Index(v, "claude") {
		t.Error("agent order not preserved")
	}
	press(s, "f")
	v = view(s)
	for _, w := range []string{"d1", "c1", "antigravity", "claude"} {
		if !strings.Contains(v, w) {
			t.Errorf("attention view missing %q:\n%s", w, v)
		}
	}
	for _, w := range []string{"ok1", "ok3", "/other/x"} {
		if strings.Contains(v, w) {
			t.Errorf("attention view should hide %q:\n%s", w, v)
		}
	}
}

func TestFilterToggle(t *testing.T) {
	s := newLoaded(t, statusRunner(multiStatus))
	if !s.all {
		t.Fatal("default filter should be all")
	}
	press(s, "f")
	if s.all {
		t.Fatal("f should switch to needs-attention")
	}
	if !strings.Contains(view(s), "needs attention") {
		t.Errorf("filter label missing:\n%s", view(s))
	}
	press(s, "f")
	if !s.all {
		t.Fatal("f should switch back")
	}
}

func TestGroupHeaderCounts(t *testing.T) {
	s := newLoaded(t, statusRunner(multiStatus))
	v := view(s)
	if !strings.Contains(v, "antigravity  2 clean · 1 drift · 1 conflict") {
		t.Errorf("antigravity header:\n%s", v)
	}
	if !strings.Contains(v, "claude  1 clean · 1 orphan") {
		t.Errorf("claude header:\n%s", v)
	}
	press(s, "f") // counts stay the full counts under the filter
	if !strings.Contains(view(s), "antigravity  2 clean · 1 drift · 1 conflict") {
		t.Errorf("filtered header lost counts:\n%s", view(s))
	}
}

func TestNavigationSkipsHeaders(t *testing.T) {
	s := newLoaded(t, statusRunner(multiStatus))
	press(s, "j", "j", "j", "j", "j", "j", "j", "j", "j")
	if it, ok := s.selected(); !ok || it.Pointer != "/other~1x" {
		t.Fatalf("cursor should clamp on last item, got %+v", it)
	}
	press(s, "k", "k", "k", "k", "k", "k", "k", "k", "k")
	if it, _ := s.selected(); it.Pointer != "/mcpServers/ok1" {
		t.Fatalf("cursor should clamp on first item, got %+v", it)
	}
}

func TestCleanState(t *testing.T) {
	s := newLoaded(t, statusRunner(fixture(t, "status-clean.json")))
	press(s, "f")
	v := view(s)
	if !strings.Contains(v, "no drift") {
		t.Errorf("want no-drift state:\n%s", v)
	}
	if !strings.Contains(v, "clean") {
		t.Errorf("counts should stay visible:\n%s", v)
	}
}

func TestNoAgentsEmptyState(t *testing.T) {
	s := newLoaded(t, statusRunner(`{"agents":[],"summary":{}}`))
	if !strings.Contains(view(s), "no agents registered") {
		t.Errorf("view:\n%s", view(s))
	}
}

func TestExitCode2IsNotAnError(t *testing.T) {
	r := &fakeRunner{res: map[string]runner.Result{
		"status --json": {Stdout: multiStatus, ExitCode: 2, Stderr: "ℹ INFO scope: user"},
	}}
	s := newLoaded(t, r)
	v := view(s)
	if !strings.Contains(v, "d1") || strings.Contains(v, "unavailable") || strings.Contains(v, "failed") {
		t.Errorf("exit 2 with JSON must render normally:\n%s", v)
	}
}

func TestStatusFailureShowsNote(t *testing.T) {
	r := &fakeRunner{res: map[string]runner.Result{
		"status --json": {ExitCode: 1, Stderr: "\x1b[2Jboom token=CANARYx1CANARYx1CANARYx1CANARYx1\nsecond"},
	}}
	s := newLoaded(t, r)
	v := view(s)
	if !strings.Contains(v, "status exited 1") || !strings.Contains(v, "boom") {
		t.Errorf("want failure note:\n%s", v)
	}
	if strings.Contains(v, "CANARYx1") || strings.Contains(v, "second") || strings.Contains(s.View().Content, "\x1b[2J") {
		t.Errorf("note leaked:\n%q", s.View().Content)
	}
	// Spawn errors and a missing binary are notes too, never crashes.
	for _, e := range []error{errors.New("spawn: nope"), runner.ErrNotInstalled} {
		s = newLoaded(t, &fakeRunner{err: e})
		if !strings.Contains(view(s), "status unavailable") {
			t.Errorf("err %v: %s", e, view(s))
		}
	}
	// Unparseable JSON at exit 0.
	s = newLoaded(t, statusRunner("not json"))
	if !strings.Contains(view(s), "status output unreadable") {
		t.Errorf("parse failure: %s", view(s))
	}
}

func TestItemPathMasked(t *testing.T) {
	st := `{"agents":[{"agent":"a","items":[{"path":"https://u:CANARYx1@h/x?token=CANARYx2\u001b[2J","pointer":"/mcpServers/s","class":"drift"}]}]}`
	s := newLoaded(t, statusRunner(st))
	raw := s.View().Content
	if strings.Contains(raw, "CANARYx") || strings.Contains(raw, "\x1b[2J") {
		t.Errorf("leak: %q", raw)
	}
}

func detailRunner(diff, explain string) *fakeRunner {
	return &fakeRunner{res: map[string]runner.Result{
		"status --json":                             {Stdout: `{"agents":[{"agent":"antigravity","items":[{"path":"` + hPath + `","pointer":"` + hPtr + `","class":"drift"}]}]}`},
		"diff " + hPath + " --json":                 {Stdout: diff},
		"explain " + hPath + "#" + hPtr + " --json": {Stdout: explain},
	}}
}

func TestDetailFixtures(t *testing.T) {
	r := detailRunner(
		strings.ReplaceAll(fixture(t, "diff-drift.json"), "/tmp/TestCapture1159252381/001/home/.gemini/config/mcp_config.json", hPath),
		strings.ReplaceAll(fixture(t, "explain-drift.json"), "/tmp/TestCapture1159252381/001/home/.gemini/config/mcp_config.json", hPath))
	s := newLoaded(t, r)
	press(s, "enter")
	v := view(s)
	for _, w := range []string{"stdio-srv", "mcp/stdio-srv.toml", "mcp", "drift", "${env:FAKE_KEY}", `"disabled": true`, "source", "dest"} {
		if !strings.Contains(v, w) {
			t.Errorf("detail missing %q:\n%s", w, v)
		}
	}
	press(s, "esc")
	if strings.Contains(view(s), "mcp/stdio-srv.toml") {
		t.Error("esc should close detail")
	}
}

func hunkJSON(src, dst string) string {
	q := jsonStr
	return `{"hunks":[` +
		`{"path":"` + hPath + `","pointer":"/mcpServers/other","source":"{\"x\":\"OTHERHUNK\"}","dest":"{}"},` +
		`{"path":"` + hPath + `","pointer":"` + hPtr + `","source":` + q(src) + `,"dest":` + q(dst) + `}]}`
}

func TestDetailShowsMaskedHunk(t *testing.T) {
	src := `{"command":"npx","args":["-y","pkg","--token","CANARYa1","-e","API_KEY=CANARYa3"],"env":{"ZED":"CANARYb1","REF":"${env:FAKE_KEY}"},"headers":{"H1":"CANARYc1"}}`
	dst := `{"command":"npx","args":["-y","pkg","--token","CANARYa2","-e","API_KEY=CANARYa4"],"env":{"ZED":"CANARYb2","REF":"${env:FAKE_KEY}"},"headers":{"H1":"CANARYc2"}}`
	s := newLoaded(t, detailRunner(hunkJSON(src, dst), `{}`))
	press(s, "enter")
	v := view(s)
	if strings.Contains(v, "CANARY") {
		t.Fatalf("canary leaked:\n%s", v)
	}
	if strings.Contains(v, "OTHERHUNK") {
		t.Errorf("hunk for another pointer shown:\n%s", v)
	}
	for _, w := range []string{`"env"`, `"headers"`, `"ZED"`, `"command"`, "${env:FAKE_KEY}"} {
		if !strings.Contains(v, w) {
			t.Errorf("structure %q missing:\n%s", w, v)
		}
	}
}

func TestDetailExplainMasked(t *testing.T) {
	ex := `{"path":"` + hPath + `","pointer":"` + hPtr + `","scope":"user","owners":[{"agent":"antigravity","items":[{"pointer":"` + hPtr + `","ownership":"managed","component":"mcp","name":"n https://u:CANARYe1@h/","source":{"file":"mcp/x.toml?token=CANARYe2"},"drift":"drift","secrets":["${env:OK_REF}","CANARYe3"]}]}]}`
	s := newLoaded(t, detailRunner(`{"hunks":[]}`, ex))
	press(s, "enter")
	v := view(s)
	if strings.Contains(v, "CANARY") {
		t.Fatalf("explain leaked:\n%s", v)
	}
	if !strings.Contains(v, "${env:OK_REF}") {
		t.Errorf("ref name should show:\n%s", v)
	}
}

func TestDetailSanitisesControlBytes(t *testing.T) {
	dst := "{\"command\":\"a\x1b[2Jb\u202e\u0085c\u009b31m\"}"
	ex := `{"owners":[{"agent":"x\u001b[2J","items":[{"name":"y\u202e","component":"c","source":{"file":"f\u0085"}}]}]}`
	s := newLoaded(t, detailRunner(hunkJSON(`{"command":"a"}`, dst), ex))
	press(s, "enter")
	raw := s.View().Content
	if !strings.Contains(raw, `"command"`) {
		t.Fatalf("hunk not rendered:\n%s", raw)
	}
	for _, bad := range []string{"\x1b[2J", "\u202e", "\u0085", "\u009b"} {
		if strings.Contains(raw, bad) {
			t.Errorf("raw %q in view", bad)
		}
	}
	// the only escapes allowed are lipgloss SGR styling
	if strings.Contains(ansi.Strip(raw), "\x1b") {
		t.Error("non-SGR escape in view")
	}
}

func TestDetailArgumentSafety(t *testing.T) {
	cases := map[string][2]string{
		"relative":   {"rel/path.json", hPtr},
		"newline":    {"/h/a\n/b.json", hPtr},
		"leading -":  {"-rf", hPtr},
		"leading --": {"--help", hPtr},
		"empty":      {"", hPtr},
		"ctrl ptr":   {hPath, "/mcpServers/x\x1b[2J"},
		"nl ptr":     {hPath, "/mcpServers/x\ny"},
		"nul":        {"/h/a\x00b", hPtr},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			item := `[{"path":` + jsonStr(c[0]) + `,"pointer":` + jsonStr(c[1]) + `,"class":"drift"}]`
			r := statusRunner(`{"agents":[{"agent":"a","items":` + item + `}]}`)
			s := newLoaded(t, r)
			press(s, "enter")
			if got := r.called(); len(got) != 1 {
				t.Fatalf("extra commands run: %v", got)
			}
			if !strings.Contains(view(s), "path not diffable") {
				t.Errorf("want note:\n%s", view(s))
			}
		})
	}
}

func jsonStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteString(string("0123456789abcdef"[r>>4]))
			b.WriteString(string("0123456789abcdef"[r&15]))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestDetailFailureKeepsListUsable(t *testing.T) {
	r := detailRunner("garbage", "garbage")
	s := newLoaded(t, r)
	press(s, "enter")
	if !strings.Contains(view(s), "unavailable") {
		t.Errorf("want dim notes:\n%s", view(s))
	}
	press(s, "esc")
	if !strings.Contains(view(s), "antigravity") {
		t.Errorf("list should survive:\n%s", view(s))
	}
}

func TestStaleDetailResultDropped(t *testing.T) {
	s := newLoaded(t, detailRunner(hunkJSON(`{"command":"x"}`, `{"command":"STALEMARK"}`), `{}`))
	_, cmd := s.Update(key("enter"))
	press(s, "esc") // close before the results arrive
	drive(s, cmd)
	press(s, "enter") // reopen: fresh seq loads again
	if !strings.Contains(view(s), "STALEMARK") {
		t.Fatalf("fresh result should show:\n%s", view(s))
	}
	// A result for the previous seq must not overwrite the current one.
	old := detailMsg{gen: s.dreads.Gen() - 1, kind: kindDiff, note: "OLDNOTE"}
	s.Update(old)
	if strings.Contains(view(s), "OLDNOTE") {
		t.Error("stale detail applied")
	}
}

func TestLegendRendered(t *testing.T) {
	r := statusRunner(multiStatus)
	r.res["status --legend"] = runner.Result{Stdout: "clean      in sync\n\x1b[2Jdrift      edited outside\n"}
	s := newLoaded(t, r)
	press(s, "g")
	v := view(s)
	if !strings.Contains(v, "in sync") || !strings.Contains(v, "edited outside") {
		t.Errorf("legend:\n%s", v)
	}
	if strings.Contains(s.View().Content, "\x1b[2J") {
		t.Error("legend not sanitised")
	}
	press(s, "esc")
	if strings.Contains(view(s), "in sync") {
		t.Error("esc should close legend")
	}
}

func TestDetailScroll(t *testing.T) {
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, `"k`+string(rune('a'+i%26))+string(rune('a'+i/26))+`":1`)
	}
	big := `{` + strings.Join(lines, ",") + `}`
	s := newLoaded(t, detailRunner(hunkJSON(big, big), `{}`))
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	press(s, "enter")
	if s.dscroll.Offset != 0 {
		t.Fatal("scroll should start at 0")
	}
	press(s, "ctrl+d")
	if s.dscroll.Offset == 0 {
		t.Error("ctrl+d should scroll")
	}
	press(s, "ctrl+u")
	if s.dscroll.Offset != 0 {
		t.Error("ctrl+u should scroll back")
	}
	press(s, "ctrl+u")
	if s.dscroll.Offset != 0 {
		t.Error("scroll must clamp at 0")
	}
}

func TestScopeAwareSwapsRunner(t *testing.T) {
	old := statusRunner(multiStatus)
	s := newLoaded(t, old)
	var _ ui.ScopeAware = s
	var _ ui.KeyCapturer = s
	nr := statusRunner(`{"agents":[{"agent":"newagent","items":[]}]}`)
	s.SetScope(nr, runner.ScopeProject, "")
	if strings.Contains(view(s), "antigravity") {
		t.Error("loaded state should clear on scope switch")
	}
	drive(s, s.Refresh())
	if !strings.Contains(view(s), "newagent") {
		t.Errorf("new runner not used:\n%s", view(s))
	}
	if len(old.called()) != 1 {
		t.Errorf("old runner used after switch: %v", old.called())
	}
}

func TestNonZeroExitWithJSONIsFailure(t *testing.T) {
	r := &fakeRunner{res: map[string]runner.Result{
		"status --json": {Stdout: `{"error":"boom"}`, ExitCode: 1, Stderr: "error: boom"},
	}}
	s := newLoaded(t, r)
	v := view(s)
	if !strings.Contains(v, "status exited 1") || !strings.Contains(v, "boom") {
		t.Errorf("want failure note:\n%s", v)
	}
	if strings.Contains(v, "no agents registered") {
		t.Errorf("misleading empty state:\n%s", v)
	}
}

func TestFailedRefreshKeepsLoadedReport(t *testing.T) {
	r := statusRunner(multiStatus)
	s := newLoaded(t, r)
	r.res["status --json"] = runner.Result{Stdout: `{"error":"x"}`, ExitCode: 1, Stderr: "error: boom"}
	drive(s, s.Refresh())
	v := view(s)
	if !strings.Contains(v, "status exited 1") || !strings.Contains(v, "d1") {
		t.Errorf("want note and the previous report:\n%s", v)
	}
}

func TestRefreshDuringDetailLoadKeepsDetail(t *testing.T) {
	s := newLoaded(t, detailRunner(hunkJSON(`{"command":"x"}`, `{"command":"INFLIGHT"}`), `{}`))
	_, cmd := s.Update(key("enter"))
	s.Refresh() // e.g. an action finished; bumps gen while detail is loading
	drive(s, cmd)
	if !strings.Contains(view(s), "INFLIGHT") {
		t.Errorf("detail dropped by refresh:\n%s", view(s))
	}
}

func TestRefreshDuringLegendLoadKeepsLegend(t *testing.T) {
	r := statusRunner(multiStatus)
	r.res["status --legend"] = runner.Result{Stdout: "clean  in sync\n"}
	s := newLoaded(t, r)
	_, cmd := s.Update(key("g"))
	s.Refresh()
	drive(s, cmd)
	if !strings.Contains(view(s), "in sync") {
		t.Errorf("legend dropped by refresh:\n%s", view(s))
	}
}

func TestStaleStatusResultDropped(t *testing.T) {
	s := newLoaded(t, statusRunner(multiStatus))
	oldGen := s.reads.Gen()
	s.SetScope(statusRunner(`{"agents":[{"agent":"newagent","items":[]}]}`), runner.ScopeProject, "")
	stale := readStatus(context.Background(), oldGen, statusRunner(multiStatus), 0)
	s.Update(stale)
	if strings.Contains(view(s), "antigravity") || s.loaded {
		t.Errorf("status from before scope switch applied:\n%s", view(s))
	}
	cmd := s.Refresh()
	old := statusMsg{gen: s.reads.Gen() - 1, note: "OLDSTATUSNOTE"}
	drive(s, cmd)
	s.Update(old)
	if strings.Contains(view(s), "OLDSTATUSNOTE") {
		t.Error("older-generation status applied after newer refresh")
	}
}

func TestSetScopeClosesOverlaysAndDropsInFlight(t *testing.T) {
	r := detailRunner(hunkJSON(`{"command":"x"}`, `{"command":"SCOPEMARK"}`), `{}`)
	s := newLoaded(t, r)
	_, cmd := s.Update(key("enter"))
	s.SetScope(statusRunner(multiStatus), runner.ScopeProject, "")
	drive(s, cmd)
	if s.detail || strings.Contains(view(s), "SCOPEMARK") {
		t.Error("detail from before scope switch applied")
	}
}

func TestHunkFromOtherFileHidden(t *testing.T) {
	d := `{"hunks":[{"path":"/h/other.json","pointer":"` + hPtr + `","source":"{\"x\":\"OTHERFILE\"}","dest":"{}"}]}`
	s := newLoaded(t, detailRunner(d, `{}`))
	press(s, "enter")
	if strings.Contains(view(s), "OTHERFILE") {
		t.Errorf("same-pointer hunk from another file shown:\n%s", view(s))
	}
}

func TestHashInPathNotDiffable(t *testing.T) {
	item := `[{"path":"/h/a#b.json","pointer":"` + hPtr + `","class":"drift"}]`
	r := statusRunner(`{"agents":[{"agent":"a","items":` + item + `}]}`)
	s := newLoaded(t, r)
	press(s, "enter")
	if len(r.called()) != 1 || !strings.Contains(view(s), "path not diffable") {
		t.Errorf("calls %v\n%s", r.called(), view(s))
	}
}
