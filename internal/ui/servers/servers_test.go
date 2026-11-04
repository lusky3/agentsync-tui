package servers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/source"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const canary = "CANARYc4n4ry0123456789abcdef"

// ---- fakes and helpers ------------------------------------------------------

// fakeRunner answers every call synchronously with a fixed result.
type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	res   runner.Result
	err   error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	return f.res, f.err
}

func (f *fakeRunner) callStrings() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

const statusJSON = `{"agents":[
 {"agent":"antigravity","items":[
  {"path":"/h/.gemini/mcp_config.json","pointer":"/mcpServers/http-srv","class":"clean"},
  {"path":"/h/.gemini/mcp_config.json","pointer":"/mcpServers/stdio-srv","class":"drift"},
  {"path":"/h/.gemini/mcp_config.json","pointer":"/mcpServers/stdio-srv/env","class":"conflict"}]},
 {"agent":"claude","items":[
  {"path":"/h/.claude.json","pointer":"/mcpServers/stdio-srv","class":"clean"},
  {"path":"/h/.claude.json","pointer":"/other/http-srv","class":"orphan"}]}
],"summary":{}}`

const (
	httpTOML = `[server]
type = 'http'
url = 'https://developerknowledge.googleapis.com/mcp'
agents = ['*']

[server.headers]
X-Goog-Api-Key = '${GOOGLE_DEVELOPER_KNOWLEDGE_API_KEY}'
`
	stdioTOML = `[server]
type = 'stdio'
command = 'npx'
args = ['-y', '@upstash/context7-mcp']
agents = ['*']

[server.env]
CONTEXT7_API_KEY = '${CONTEXT7_API_TOKEN}'
`
	offTOML = `[server]
type = 'stdio'
command = 'echo'
agents = ['claude', 'kiro']
enabled = false
`
)

func writeServer(t *testing.T, home, id, body string) {
	t.Helper()
	dir := filepath.Join(home, "mcp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixtureHome writes the three standard servers.
func fixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	writeServer(t, home, "http-srv", httpTOML)
	writeServer(t, home, "stdio-srv", stdioTOML)
	writeServer(t, home, "off-srv", offTOML)
	return home
}

func okRunner() *fakeRunner { return &fakeRunner{res: runner.Result{Stdout: statusJSON}} }

// newLoaded builds a screen and runs its first refresh to completion.
func newLoaded(t *testing.T, home string, fr runner.Runner) *Screen {
	t.Helper()
	s := New(ui.Deps{Runner: fr, AgentsyncHome: home, Scope: runner.ScopeUser})
	s.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	drive(s, s.Refresh())
	return s
}

// drive executes cmd (recursively through batches), feeds every result back
// into s and returns the messages addressed to the shell.
func drive(s *Screen, cmd tea.Cmd) []tea.Msg {
	var out []tea.Msg
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
		case ui.DoMsg, ui.EditFileMsg:
			out = append(out, m)
		default:
			_, next := s.Update(m)
			queue = append(queue, next)
		}
	}
	return out
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

// press sends named keys and returns the shell-bound messages they produced.
func press(s *Screen, keys ...string) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		_, cmd := s.Update(key(k))
		out = append(out, drive(s, cmd)...)
	}
	return out
}

// typeText types every rune of text as a key press.
func typeText(s *Screen, text string) {
	for _, r := range text {
		k := tea.KeyPressMsg{Code: r, Text: string(r)}
		if r == ' ' {
			k.Code = tea.KeySpace
		}
		_, cmd := s.Update(k)
		drive(s, cmd)
	}
}

func view(s *Screen) string { return ansi.Strip(s.View().Content) }

func selectID(t *testing.T, s *Screen, id string) {
	t.Helper()
	for i, srv := range s.servers {
		if srv.ID == id {
			s.cursor = i
			return
		}
	}
	t.Fatalf("server %q not loaded", id)
}

func onlyDo(t *testing.T, msgs []tea.Msg) actions.Action {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("want one DoMsg, got %#v", msgs)
	}
	d, ok := msgs[0].(ui.DoMsg)
	if !ok {
		t.Fatalf("want DoMsg, got %#v", msgs[0])
	}
	return d.Action
}

func mustAct(a actions.Action, err error) actions.Action {
	if err != nil {
		panic(err)
	}
	return a
}

// ---- tests --------------------------------------------------------------------

func TestListShowsServersAndPerAgentState(t *testing.T) {
	s := newLoaded(t, fixtureHome(t), okRunner())
	v := view(s)
	for _, want := range []string{
		"http-srv", "https://developerknowledge.googleapis.com/mcp",
		"stdio-srv", "npx -y @upstash/context7-mcp",
		"off-srv", "off", "claude,kiro", "*",
		"antigravity:clean", "antigravity:drift!", "claude:clean",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	// Sorted by ID; only /mcpServers/<id> pointers count.
	if !(strings.Index(v, "http-srv") < strings.Index(v, "off-srv") && strings.Index(v, "off-srv") < strings.Index(v, "stdio-srv")) {
		t.Errorf("not sorted:\n%s", v)
	}
	if strings.Contains(v, "conflict") || strings.Contains(v, "orphan") {
		t.Errorf("non-server pointers leaked into state:\n%s", v)
	}
	for in, want := range map[string]string{
		"/mcpServers/a~1b": "a/b", "/mcpServers/a~0b": "a~b", "/mcpServers/x": "x",
		"/mcpServers/x/env": "", "/other/x": "", "/mcpServers/": "", "mcpServers/x": "",
	} {
		if got := pointerID(in); got != want {
			t.Errorf("pointerID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetailMasksEnvHeadersAndInlineArgs(t *testing.T) {
	home := t.TempDir()
	writeServer(t, home, "leaky", `[server]
type = 'stdio'
command = 'npx'
args = ['-y', 'srv', '--header', 'Authorization: Bearer gg_pat_`+canary+`', '--api-key=`+canary+`']
agents = ['*']
api_token = '`+canary+`'
note = 'gg_pat_`+canary+`'
list = ['`+canary+`']

[server.env]
API_KEY = '`+canary+`'
REF = '${env:TOKEN}'

[server.headers]
X-Api-Key = '`+canary+`'
Authorization = 'Bearer ${secret:GH}'

[server.nested]
secret = '`+canary+`'
`)
	writeServer(t, home, "remote", `[server]
type = 'http'
url = 'https://user:`+canary+`@example.com/mcp?token=`+canary+`'
`)
	s := newLoaded(t, home, okRunner())
	if v := view(s); strings.Contains(v, "CANARY") {
		t.Fatalf("list leaks canary:\n%s", v)
	}
	for _, id := range []string{"leaky", "remote"} {
		selectID(t, s, id)
		press(s, "enter")
		v := view(s)
		if strings.Contains(v, "CANARY") {
			t.Fatalf("detail of %s leaks canary:\n%s", id, v)
		}
		if !strings.Contains(v, "••••") {
			t.Errorf("detail of %s shows no mask:\n%s", id, v)
		}
		if id == "leaky" {
			for _, want := range []string{"REF = ${env:TOKEN}", "Authorization = Bearer ${secret:GH}", "API_KEY = ••••", "-y", "nested", "list"} {
				if !strings.Contains(v, want) {
					t.Errorf("detail missing %q:\n%s", want, v)
				}
			}
		} else if !strings.Contains(v, "example.com/mcp") {
			t.Errorf("detail missing url host:\n%s", v)
		}
		press(s, "esc")
	}
}

func TestToggleEnableBuildsCorrectAction(t *testing.T) {
	s := newLoaded(t, fixtureHome(t), okRunner())
	selectID(t, s, "off-srv")
	if got := onlyDo(t, press(s, "e")); !reflect.DeepEqual(got, mustAct(actions.McpEnable("off-srv"))) {
		t.Errorf("disabled -> %#v", got)
	}
	selectID(t, s, "http-srv")
	if got := onlyDo(t, press(s, "e")); !reflect.DeepEqual(got, mustAct(actions.McpDisable("http-srv"))) {
		t.Errorf("enabled -> %#v", got)
	}
}

func TestRemoveIsDestructiveAction(t *testing.T) {
	s := newLoaded(t, fixtureHome(t), okRunner())
	selectID(t, s, "stdio-srv")
	got := onlyDo(t, press(s, "x"))
	if !got.Destructive || !reflect.DeepEqual(got, mustAct(actions.McpRemove("stdio-srv"))) {
		t.Errorf("remove -> %#v", got)
	}
}

func TestEditOpensEditorThenRunsCheck(t *testing.T) {
	home := fixtureHome(t)
	s := newLoaded(t, home, okRunner())
	selectID(t, s, "stdio-srv")
	msgs := press(s, "E")
	path, _ := source.ServerPath(home, "stdio-srv")
	want := ui.EditFileMsg{Path: path, After: []actions.Action{actions.Check()}}
	if len(msgs) != 1 || !reflect.DeepEqual(msgs[0], want) {
		t.Errorf("got %#v, want %#v", msgs, want)
	}
}

func TestStaleListRefreshesBeforeMutation(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		for _, k := range []string{"e", "x"} {
			home := fixtureHome(t)
			s := newLoaded(t, home, okRunner())
			selectID(t, s, "stdio-srv")
			if err := os.Remove(filepath.Join(home, "mcp", "stdio-srv.toml")); err != nil {
				t.Fatal(err)
			}
			if msgs := press(s, k); len(msgs) != 0 {
				t.Fatalf("%s: action emitted for a removed server: %#v", k, msgs)
			}
			v := view(s)
			if !strings.Contains(v, "server list changed — refreshed") || strings.Contains(v, "stdio-srv") {
				t.Errorf("%s: view:\n%s", k, v)
			}
		}
	})
	t.Run("toggled elsewhere", func(t *testing.T) {
		home := fixtureHome(t)
		s := newLoaded(t, home, okRunner())
		selectID(t, s, "http-srv")
		writeServer(t, home, "http-srv", strings.Replace(httpTOML, "agents = ['*']", "agents = ['*']\nenabled = false", 1))
		if msgs := press(s, "e"); len(msgs) != 0 {
			t.Fatalf("toggle emitted against a stale state: %#v", msgs)
		}
		if !strings.Contains(view(s), "server list changed — refreshed") {
			t.Errorf("view:\n%s", view(s))
		}
		if got := onlyDo(t, press(s, "e")); !reflect.DeepEqual(got, mustAct(actions.McpEnable("http-srv"))) {
			t.Errorf("after refresh -> %#v", got)
		}
	})
}

func TestEmptyState(t *testing.T) {
	missing := newLoaded(t, filepath.Join(t.TempDir(), "nope"), okRunner())
	if v := view(missing); !strings.Contains(v, "no MCP servers — press a to add one") || !strings.Contains(v, "mcp/ directory not found") {
		t.Errorf("missing dir view:\n%s", v)
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "mcp"), 0o700); err != nil {
		t.Fatal(err)
	}
	empty := newLoaded(t, home, okRunner())
	if v := view(empty); !strings.Contains(v, "no MCP servers — press a to add one") || strings.Contains(v, "not found") {
		t.Errorf("empty dir view:\n%s", v)
	}
	// Keys that need a selection do nothing on an empty list.
	if msgs := press(empty, "e", "x", "E", "enter"); len(msgs) != 0 {
		t.Errorf("actions on empty list: %#v", msgs)
	}
}

func TestPartialLoadErrorShowsWarning(t *testing.T) {
	home := fixtureHome(t)
	writeServer(t, home, "bad", "[server\ntype = '"+canary+"'\n")
	writeServer(t, home, "worse", "not toml at all = = =")
	s := newLoaded(t, home, okRunner())
	v := view(s)
	for _, want := range []string{"2 file(s) unreadable", "bad.toml", "worse.toml", "http-srv", "stdio-srv"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "CANARY") {
		t.Errorf("warning leaks file content:\n%s", v)
	}
}

func TestStatusFailureStillListsServers(t *testing.T) {
	for name, fr := range map[string]*fakeRunner{
		"exit":        {res: runner.Result{ExitCode: 2, Stderr: "boom"}},
		"parse":       {res: runner.Result{Stdout: "garbage"}},
		"not-install": {err: runner.ErrNotInstalled},
		"spawn":       {err: errors.New("spawn failed token=" + canary)},
	} {
		s := newLoaded(t, fixtureHome(t), fr)
		v := view(s)
		if !strings.Contains(v, "http-srv") || !strings.Contains(v, "stdio-srv") || !strings.Contains(v, "agent state unavailable") {
			t.Errorf("%s: view:\n%s", name, v)
		}
		if strings.Contains(v, "CANARY") || strings.Contains(v, "antigravity") {
			t.Errorf("%s: view:\n%s", name, v)
		}
	}
}

func TestScopeAwareSwapsRunner(t *testing.T) {
	var _ ui.ScopeAware = (*Screen)(nil)
	var _ ui.KeyCapturer = (*Screen)(nil)
	var _ ui.Screen = (*Screen)(nil)
	fr1, fr2 := okRunner(), okRunner()
	s := newLoaded(t, fixtureHome(t), fr1)
	n1 := len(fr1.callStrings())
	s.SetScope(fr2, runner.ScopeProject)
	drive(s, s.Refresh())
	if len(fr1.callStrings()) != n1 || strings.Join(fr2.callStrings(), "|") != "status --json" {
		t.Errorf("runner1 %q runner2 %q", fr1.callStrings(), fr2.callStrings())
	}
}

func TestInitEqualsRefresh(t *testing.T) {
	kinds := func(cmd tea.Cmd) []string {
		var out []string
		queue := []tea.Cmd{cmd}
		for len(queue) > 0 {
			c := queue[0]
			queue = queue[1:]
			if c == nil {
				continue
			}
			switch m := c().(type) {
			case tea.BatchMsg:
				queue = append(queue, m...)
			default:
				out = append(out, reflect.TypeOf(m).String())
			}
		}
		sort.Strings(out)
		return out
	}
	fr := okRunner()
	s := New(ui.Deps{Runner: fr, AgentsyncHome: fixtureHome(t)})
	a, b := kinds(s.Init()), kinds(s.Refresh())
	if len(a) == 0 || !reflect.DeepEqual(a, b) {
		t.Errorf("Init %q vs Refresh %q", a, b)
	}
	if strings.Join(fr.callStrings(), "|") != "status --json|status --json" {
		t.Errorf("calls %q", fr.callStrings())
	}
}

func TestRefreshKeyAndStaleResultsDropped(t *testing.T) {
	home := fixtureHome(t)
	fr := okRunner()
	s := newLoaded(t, home, fr)
	old := s.Refresh() // generation N, results not delivered yet
	writeServer(t, home, "new-srv", offTOML)
	press(s, "r") // generation N+1, delivered
	if !strings.Contains(view(s), "new-srv") {
		t.Fatalf("r did not refresh:\n%s", view(s))
	}
	if err := os.Remove(filepath.Join(home, "mcp", "new-srv.toml")); err != nil {
		t.Fatal(err)
	}
	// Results of the older refresh were computed now but carry a stale
	// generation, so they must be ignored.
	drive(s, old)
	if !strings.Contains(view(s), "new-srv") {
		t.Errorf("stale refresh result was applied:\n%s", view(s))
	}
}
