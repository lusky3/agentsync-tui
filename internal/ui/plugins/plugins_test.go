package plugins

import (
	"context"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const canary = "CANARYx1"

// fakeRunner answers by joined argv; unknown commands exit 1.
type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	out   map[string]runner.Result
	errs  map[string]error
	ctxs  map[string]context.Context
}

func newFake() *fakeRunner {
	return &fakeRunner{out: map[string]runner.Result{}, errs: map[string]error{}, ctxs: map[string]context.Context{}}
}

func (f *fakeRunner) Run(ctx context.Context, args ...string) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := strings.Join(args, " ")
	f.calls = append(f.calls, args)
	f.ctxs[k] = ctx
	if e := f.errs[k]; e != nil {
		return runner.Result{}, e
	}
	if r, ok := f.out[k]; ok {
		return r, nil
	}
	return runner.Result{ExitCode: 1, Stderr: "unknown " + k}, nil
}

func (f *fakeRunner) set(k, stdout string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out[k] = runner.Result{Stdout: stdout}
	delete(f.errs, k)
}

func (f *fakeRunner) saw(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../model/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func view(s *Screen) string { return ansi.Strip(s.View().Content) }

// drive runs cmd and everything it produces, feeding results to the screen;
// DoMsgs are returned (the shell is not present).
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
		case ui.DoMsg:
			out = append(out, m)
		default:
			_, next := s.Update(m)
			queue = append(queue, next)
		}
	}
	return out
}

func standardFake(t *testing.T) *fakeRunner {
	fr := newFake()
	fr.set("plugin list", fixture(t, "plugin-list.txt"))
	fr.set("marketplace list", fixture(t, "marketplace-list.txt"))
	return fr
}

func loaded(t *testing.T, fr *fakeRunner) *Screen {
	t.Helper()
	s := New(ui.Deps{Runner: fr, Scope: runner.ScopeUser})
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	drive(s, s.Refresh())
	return s
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return tea.KeyPressMsg{Code: rune(k[0]), Text: k}
}

func press(s *Screen, k string) []tea.Msg {
	_, cmd := s.Update(keyMsg(k))
	return drive(s, cmd)
}

func typeText(s *Screen, text string) {
	for _, r := range text {
		s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func only(t *testing.T, msgs []tea.Msg) actions.Action {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("want exactly one DoMsg, got %v", msgs)
	}
	return msgs[0].(ui.DoMsg).Action
}

func TestListsPluginsAndMarketplaces(t *testing.T) {
	fr := standardFake(t)
	s := loaded(t, fr)
	v := view(s)
	for _, want := range []string{"hello", "other", "1.0.0", "enabled", "disabled"} {
		if !strings.Contains(v, want) {
			t.Errorf("plugin pane missing %q:\n%s", want, v)
		}
	}
	press(s, "m")
	v = view(s)
	for _, want := range []string{"localmkt", "/tmp/hermetic/mkt"} {
		if !strings.Contains(v, want) {
			t.Errorf("marketplace pane missing %q:\n%s", want, v)
		}
	}
}

func TestInitEqualsRefresh(t *testing.T) {
	fr := standardFake(t)
	s := New(ui.Deps{Runner: fr})
	drive(s, s.Init())
	if !fr.saw("plugin list") || !fr.saw("marketplace list") || fr.count() != 2 {
		t.Fatalf("Init calls = %v", fr.calls)
	}
	if len(s.plugins) != 2 || len(s.markets) != 1 {
		t.Fatalf("Init did not load: %d %d", len(s.plugins), len(s.markets))
	}
}

func TestRefreshNeverRunsOutdated(t *testing.T) {
	fr := standardFake(t)
	s := New(ui.Deps{Runner: fr})
	drive(s, s.Init())
	drive(s, s.Refresh())
	press(s, "r")
	if fr.saw("plugin outdated") {
		t.Fatalf("network call ran without being asked: %v", fr.calls)
	}
}

func TestOutdatedOnDemandMarksPlugins(t *testing.T) {
	fr := standardFake(t)
	fr.set("plugin outdated", fixture(t, "plugin-outdated.txt"))
	s := loaded(t, fr)
	if fr.saw("plugin outdated") {
		t.Fatal("ran at startup")
	}
	if msgs := press(s, "o"); len(msgs) != 0 {
		t.Fatalf("outdated check must not be an Action: %v", msgs)
	}
	if !fr.saw("plugin outdated") {
		t.Fatal("o did not run plugin outdated")
	}
	v := view(s)
	if !strings.Contains(v, "↑ 1.1.0") || !strings.Contains(v, "network") || !strings.Contains(v, "pending bumps") {
		t.Errorf("outdated not shown/labelled:\n%s", v)
	}
	dl, ok := fr.ctxs["plugin outdated"].Deadline()
	if !ok || dl.IsZero() {
		t.Error("outdated read has no network timeout")
	}
}

func TestOutdatedFailureNoteMasked(t *testing.T) {
	fr := standardFake(t)
	fr.out["plugin outdated"] = runner.Result{ExitCode: 1, Stderr: "boom token=" + canary + canary + canary + "\nmore"}
	s := loaded(t, fr)
	press(s, "o")
	if v := view(s); strings.Contains(v, canary) || !strings.Contains(v, "outdated check failed") {
		t.Errorf("view:\n%s", v)
	}
}

func TestToggleBuildsCorrectAction(t *testing.T) {
	fr := standardFake(t)
	s := loaded(t, fr)
	act := only(t, press(s, "e")) // hello is enabled
	if !reflect.DeepEqual(act.Argv, []string{"plugin", "disable", "hello"}) || act.Destructive {
		t.Errorf("act = %+v", act)
	}
	press(s, "j")
	act = only(t, press(s, "e")) // other is disabled
	if !reflect.DeepEqual(act.Argv, []string{"plugin", "enable", "other"}) {
		t.Errorf("act = %+v", act)
	}
}

func TestRemoveIsDestructiveWithWarning(t *testing.T) {
	s := loaded(t, standardFake(t))
	act := only(t, press(s, "x"))
	if !reflect.DeepEqual(act.Argv, []string{"plugin", "remove", "hello"}) || !act.Destructive {
		t.Errorf("act = %+v", act)
	}
	if !strings.Contains(act.Warning, "cached files") || ui.Verb(act) != "remove" {
		t.Errorf("warning %q verb %q", act.Warning, ui.Verb(act))
	}
}

func TestAddPluginValidatesInline(t *testing.T) {
	s := loaded(t, standardFake(t))
	for _, bad := range []string{"-rf", "../x", "a b", "a@", "a@-b", "x\ay", ""} {
		press(s, "a")
		if !s.CapturesKeys() {
			t.Fatal("add field must capture keys")
		}
		s.buf = bad // straight into the buffer: typing already strips controls
		if got := press(s, "enter"); len(got) != 0 {
			t.Errorf("%q emitted %v", bad, got)
		}
		if !s.CapturesKeys() || s.inlineE == "" {
			t.Errorf("%q: field closed or no inline error (%q)", bad, s.inlineE)
		}
		if !strings.Contains(view(s), "invalid") && bad != "" {
			t.Errorf("%q: inline error not visible:\n%s", bad, view(s))
		}
		press(s, "esc")
		if s.CapturesKeys() {
			t.Fatal("esc must close the field")
		}
	}
	press(s, "a")
	typeText(s, "hello@localmkt")
	act := only(t, press(s, "enter"))
	if !reflect.DeepEqual(act.Argv, []string{"plugin", "add", "hello@localmkt"}) || !act.Network {
		t.Errorf("act = %+v", act)
	}
	if s.CapturesKeys() {
		t.Error("field must close after submit")
	}
}

func TestUpgradeIsNetwork(t *testing.T) {
	s := loaded(t, standardFake(t))
	act := only(t, press(s, "u"))
	if !reflect.DeepEqual(act.Argv, []string{"plugin", "upgrade", "hello"}) || !act.Network || act.Destructive {
		t.Errorf("act = %+v", act)
	}
}

func TestUpgradeAllIsNetworkAndConfirms(t *testing.T) {
	s := loaded(t, standardFake(t))
	act := only(t, press(s, "U"))
	if !reflect.DeepEqual(act.Argv, []string{"plugin", "upgrade", "--all"}) || !act.Network || act.ReadOnly {
		t.Errorf("act = %+v", act) // not ReadOnly: the shell shows the confirm modal
	}
	press(s, "m")
	if got := press(s, "U"); len(got) != 0 {
		t.Error("U must only work in the plugins pane")
	}
}

func TestMarketplaceAddMasksCredentials(t *testing.T) {
	s := loaded(t, standardFake(t))
	press(s, "m")
	press(s, "a")
	url := "https://user:" + canary + "@host.example/repo.git"
	for _, r := range url {
		s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		if v := view(s); strings.Contains(v, canary) {
			t.Fatalf("credential visible while typing %q:\n%s", s.buf, v)
		}
	}
	if !strings.Contains(view(s), "host.example") {
		t.Errorf("host should stay readable once the authority is closed:\n%s", view(s))
	}
	act := only(t, press(s, "enter"))
	if !reflect.DeepEqual(act.Argv, []string{"marketplace", "add", url}) || !act.Network {
		t.Errorf("argv = %q network=%v", act.Argv, act.Network)
	}
	if strings.Contains(act.Display(), canary) {
		t.Errorf("Display leaks the credential: %s", act.Display())
	}
}

func TestMarketplaceRemoveIsDestructive(t *testing.T) {
	s := loaded(t, standardFake(t))
	press(s, "m")
	act := only(t, press(s, "x"))
	if !reflect.DeepEqual(act.Argv, []string{"marketplace", "remove", "localmkt"}) || !act.Destructive {
		t.Errorf("act = %+v", act)
	}
	if !strings.Contains(act.Warning, "cached files") || ui.Verb(act) != "remove" {
		t.Errorf("warning %q verb %q", act.Warning, ui.Verb(act))
	}
	if got := press(s, "e"); len(got) != 0 {
		t.Error("e is plugin-only")
	}
}

func TestMarketplaceSourceMaskedWhenStored(t *testing.T) {
	fr := standardFake(t)
	fr.set("marketplace list", "evil url=https://user:"+canary+"@host/repo.git sha=abc\n")
	s := loaded(t, fr)
	if strings.Contains(s.markets[0].URL, canary) {
		t.Errorf("stored URL not masked: %q", s.markets[0].URL)
	}
	press(s, "m")
	press(s, "enter")
	if v := view(s); strings.Contains(v, canary) || strings.Contains(s.detail, canary) {
		t.Errorf("detail leaks:\n%s", v)
	}
}

func TestDetailExplainMaskedAndSanitised(t *testing.T) {
	fr := standardFake(t)
	fr.set("plugin explain hello", "▸ hello@localmkt v1.0.0\n  token="+canary+canary+canary+" \x1b[31mred\x1b]0;pwn\x07 \u202eevil\n  Authorization: Bearer "+canary+"\n")
	s := loaded(t, fr)
	press(s, "enter")
	for _, got := range []string{s.detail, view(s)} {
		for _, bad := range []string{canary, "\x1b", "\x07", "\u202e"} {
			if strings.Contains(got, bad) {
				t.Errorf("contains %q:\n%q", bad, got)
			}
		}
	}
	if !strings.Contains(s.detail, "hello@localmkt") {
		t.Errorf("stored detail = %q", s.detail)
	}
	if !strings.Contains(view(s), "hello@localmkt") {
		t.Errorf("view:\n%s", view(s))
	}
	press(s, "esc")
	if s.detailOpen {
		t.Error("esc must close the detail")
	}
}

func TestDetailCappedAndScrollable(t *testing.T) {
	fr := standardFake(t)
	fr.set("plugin explain hello", strings.Repeat("line of output\n", 20000))
	s := loaded(t, fr)
	press(s, "enter")
	if len(s.detail) > maxDetail+64 {
		t.Errorf("stored %d bytes", len(s.detail))
	}
	if s.scroll != 0 || !strings.Contains(view(s), "more below") {
		t.Fatalf("not scrollable:\n%s", view(s))
	}
	press(s, "j")
	press(s, "j")
	if s.scroll != 2 {
		t.Errorf("scroll = %d", s.scroll)
	}
}

func TestDetailFailureNoteMasked(t *testing.T) {
	fr := standardFake(t)
	fr.out["plugin explain hello"] = runner.Result{ExitCode: 1, Stderr: "bad Authorization: Bearer " + canary + "\n"}
	s := loaded(t, fr)
	press(s, "enter")
	if v := view(s); strings.Contains(v, canary) || !strings.Contains(v, "explain unavailable") {
		t.Errorf("view:\n%s", v)
	}
}

// ---- staleness --------------------------------------------------------------

func TestStaleStateBlocksMutation(t *testing.T) {
	flips := map[string]string{
		"enabled": "hello                version=1.0.0      sha=tree:v1:9995   disabled\n",
		"version": "hello                version=2.0.0      sha=tree:v1:9995   enabled\n",
		"sha":     "hello                version=1.0.0      sha=tree:v1:ffff   enabled\n",
		"gone":    "other                version=1.0.0      sha=tree:v1:76cc   disabled\n",
	}
	for name, fresh := range flips {
		for _, k := range []string{"e", "x", "u"} {
			fr := standardFake(t)
			s := loaded(t, fr)
			fr.set("plugin list", fresh)
			if got := press(s, k); len(got) != 0 {
				t.Errorf("%s/%s: stale mutation emitted %v", name, k, got)
			}
			if s.verifying {
				t.Errorf("%s/%s: still verifying", name, k)
			}
		}
	}
	// unchanged state emits
	fr := standardFake(t)
	s := loaded(t, fr)
	if got := press(s, "e"); len(got) != 1 {
		t.Errorf("fresh state emitted %v", got)
	}
}

func TestStaleMarketplaceBlocksRemove(t *testing.T) {
	flips := map[string]string{
		"url":  "localmkt             url=/elsewhere                  sha=\n",
		"sha":  "localmkt             url=/tmp/hermetic/mkt                  sha=abc\n",
		"gone": "x                    url=/p                  sha=\n",
	}
	for name, fresh := range flips {
		fr := standardFake(t)
		s := loaded(t, fr)
		press(s, "m")
		fr.set("marketplace list", fresh)
		if got := press(s, "x"); len(got) != 0 {
			t.Errorf("%s: stale remove emitted %v", name, got)
		}
	}
}

func TestVerifyFailureBlocksWithNote(t *testing.T) {
	fr := standardFake(t)
	s := loaded(t, fr)
	fr.out["plugin list"] = runner.Result{ExitCode: 3, Stderr: "disk on fire token=" + canary + canary + canary}
	if got := press(s, "x"); len(got) != 0 {
		t.Fatalf("emitted %v", got)
	}
	if v := view(s); !strings.Contains(v, "cannot verify") || strings.Contains(v, canary) {
		t.Errorf("view:\n%s", v)
	}
}

func TestVerifyDoesNotBlockUpdate(t *testing.T) {
	fr := standardFake(t)
	s := loaded(t, fr)
	before := fr.count()
	_, cmd := s.Update(keyMsg("x"))
	if fr.count() != before {
		t.Fatal("Update ran the CLI synchronously")
	}
	if !s.verifying {
		t.Fatal("not verifying")
	}
	// mutating keys are ignored meanwhile; navigation and sizing still work.
	if _, c := s.Update(keyMsg("e")); c != nil {
		t.Error("mutating key accepted while verifying")
	}
	s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if s.width != 100 {
		t.Error("Update blocked on verify")
	}
	if got := drive(s, cmd); len(got) != 1 {
		t.Errorf("verified remove emitted %v", got)
	}
}

func TestVerifyDroppedOnCursorMove(t *testing.T) {
	fr := standardFake(t)
	s := loaded(t, fr)
	_, cmd := s.Update(keyMsg("x"))
	s.Update(keyMsg("j"))
	if s.verifying {
		t.Error("cursor move must clear verifying")
	}
	if got := drive(s, cmd); len(got) != 0 {
		t.Errorf("stale verify emitted %v", got)
	}
}

func TestVerifyDroppedWhenItemGone(t *testing.T) {
	fr := standardFake(t)
	s := loaded(t, fr)
	_, cmd := s.Update(keyMsg("x")) // verify in flight for "hello"
	// A list read lands (same generation) and the selected plugin is gone.
	s.Update(pluginsMsg{gen: s.gen, plugins: []model.Plugin{{Name: "other", Version: "1.0.0", SHA: "tree:v1:76cc"}}})
	if got := drive(s, cmd); len(got) != 0 {
		t.Fatalf("emitted %v", got)
	}
	if s.verifying {
		t.Fatal("verifying stuck after the verify result was dropped")
	}
	// and the screen is usable again.
	fr.set("plugin list", "other                version=1.0.0      sha=tree:v1:76cc   disabled\n")
	if got := press(s, "e"); len(got) != 1 {
		t.Errorf("mutation blocked after drop: %v", got)
	}
}

func TestVerifyDroppedOnRefreshAndScope(t *testing.T) {
	fr := standardFake(t)
	s := loaded(t, fr)
	_, cmd := s.Update(keyMsg("x"))
	s.Refresh()
	if got := drive(s, cmd); len(got) != 0 {
		t.Errorf("verify after Refresh emitted %v", got)
	}
	_, cmd = s.Update(keyMsg("x"))
	other := standardFake(t)
	s.SetScope(other, runner.ScopeProject)
	if got := drive(s, cmd); len(got) != 0 || s.verifying {
		t.Errorf("verify after SetScope emitted %v (verifying=%v)", got, s.verifying)
	}
}

// ---- failure, scope, input ----------------------------------------------------

func TestPartialFailureKeepsOtherPane(t *testing.T) {
	fr := standardFake(t)
	s := loaded(t, fr)
	fr.out["marketplace list"] = runner.Result{ExitCode: 2, Stderr: "no network token=" + canary + canary + canary}
	drive(s, s.Refresh())
	if len(s.plugins) != 2 || len(s.markets) != 1 {
		t.Fatalf("last good data lost: %d %d", len(s.plugins), len(s.markets))
	}
	press(s, "m")
	v := view(s)
	if !strings.Contains(v, "unavailable") || strings.Contains(v, canary) || !strings.Contains(v, "localmkt") {
		t.Errorf("view:\n%s", v)
	}
	// other direction: plugin list fails, marketplaces refresh fine.
	fr.set("marketplace list", fixture(t, "marketplace-list.txt"))
	fr.out["plugin list"] = runner.Result{ExitCode: 2, Stderr: "boom"}
	drive(s, s.Refresh())
	press(s, "m") // back to plugins
	press(s, "m") // and to marketplaces
	if len(s.markets) != 1 || len(s.plugins) != 2 || !strings.Contains(view(s), "localmkt") {
		t.Errorf("one failure hid the other pane:\n%s", view(s))
	}
}

func TestScopeAwareSwapsRunner(t *testing.T) {
	var _ ui.ScopeAware = (*Screen)(nil)
	a, b := standardFake(t), standardFake(t)
	s := loaded(t, a)
	n := a.count()
	s.SetScope(b, runner.ScopeProject)
	drive(s, s.Refresh())
	if a.count() != n || !b.saw("plugin list") {
		t.Errorf("runner not swapped: a=%d b=%v", a.count()-n, b.calls)
	}
}

func TestKeyCapturerWhileTyping(t *testing.T) {
	var _ ui.KeyCapturer = (*Screen)(nil)
	s := loaded(t, standardFake(t))
	if s.CapturesKeys() {
		t.Fatal("idle screen captures keys")
	}
	press(s, "a")
	if !s.CapturesKeys() {
		t.Fatal("not capturing while typing")
	}
	typeText(s, "mxeoUq") // shell/global and screen hotkeys are text here
	if s.pane != panePlugins || s.buf != "mxeoUq" {
		t.Errorf("hotkeys leaked while typing: pane=%d buf=%q", s.pane, s.buf)
	}
	press(s, "esc")
	if s.CapturesKeys() {
		t.Error("still capturing after esc")
	}
}

func TestStaleListResultIgnored(t *testing.T) {
	fr := standardFake(t)
	s := loaded(t, fr)
	s.Update(pluginsMsg{gen: s.gen - 1, plugins: nil})
	if len(s.plugins) != 2 {
		t.Error("old-generation result applied")
	}
}

func TestHostileNamesNeverActionable(t *testing.T) {
	fr := newFake()
	fr.set("plugin list", "\x1b[31mred version=1 enabled\n-rf version=1 enabled\n../x version=1 enabled\na/b version=1 enabled\nevil\u202e version=1 enabled\nok version=1 enabled\n")
	fr.set("marketplace list", "\x1b[31mred url=/p sha=\n-rf url=/p sha=\n../x url=/p sha=\nok url=/p sha=\n")
	s := loaded(t, fr)
	if len(s.plugins) != 1 || s.plugins[0].Name != "ok" || len(s.markets) != 1 || s.markets[0].Name != "ok" {
		t.Fatalf("plugins=%v markets=%v", s.plugins, s.markets)
	}
	for _, pane := range []string{"", "m"} {
		if pane != "" {
			press(s, pane)
		}
		if v := view(s); strings.ContainsAny(v, "\x1b\u202e") || strings.Contains(v, "../x") || strings.Contains(v, "-rf") {
			t.Errorf("hostile text rendered:\n%q", v)
		}
	}
	// A hostile selection (e.g. injected via a stale model) is refused.
	s.pane = panePlugins
	s.plugins = []model.Plugin{{Name: "-rf"}}
	s.pCur = 0
	if got := press(s, "x"); len(got) != 0 || s.verifying {
		t.Errorf("hostile selection actionable: %v", got)
	}
	if got := press(s, "enter"); len(got) != 0 || fr.saw("plugin explain") {
		t.Error("hostile selection explained")
	}
}

func TestPluginsNeverEmitWithoutSelection(t *testing.T) {
	fr := newFake()
	fr.set("plugin list", "(no plugins installed; try: agentsync plugin add <id@marketplace>)\n")
	fr.set("marketplace list", "(no marketplaces registered; try: agentsync marketplace add <url>)\n")
	s := loaded(t, fr)
	for _, k := range []string{"e", "x", "u", "enter"} {
		if got := press(s, k); len(got) != 0 {
			t.Errorf("%s emitted %v", k, got)
		}
	}
	if !strings.Contains(view(s), "no plugins installed") {
		t.Errorf("empty state missing:\n%s", view(s))
	}
}
