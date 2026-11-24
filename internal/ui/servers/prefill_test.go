package servers

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	braveImage   = "mcp/brave-search@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	braveRef     = "${secret:brave.brave_api_key}"
	braveProv    = "docker catalog: brave (digest 0123456789ab)"
	trustDigest  = "Docker-reviewed catalog; image pinned by digest 0123456789ab"
	runsCodeNote = ": the command below runs code on your machine"
	secretsTab   = "Secrets tab"
	addBrave     = "mcp add brave"
	braveEnv     = "BRAVE_API_KEY"
	typeHTTP     = "http"
	remoteURL    = "https://mcp.example.com/mcp"
	teamHeader   = "X-Team: ${secret:x.team_id}"
	region       = "REGION"
	takenID      = "http-srv"
	secretList   = "secret list"
	euWest       = "eu-west-1"
)

// braveCompose is a Docker-catalog-shaped draft: a stdio docker run with
// one generated secret reference, plus arguments that need quoting.
func braveCompose() ui.ComposeMcpMsg {
	return ui.ComposeMcpMsg{
		Spec: actions.AddSpec{
			ID: "brave", Type: "stdio", Command: "docker",
			Args: []string{"run", "-i", "--rm", "-e", braveEnv, braveImage, "--opt=a b", "it's", "x,y", `say "hi"`},
			Env:  map[string]string{braveEnv: braveRef},
		},
		Provenance: braveProv,
		Trust:      []string{trustDigest, "image signatures and attestations\tare NOT verified\nby this tool"},
		RefID:      "brave",
	}
}

// remoteCompose is a registry-shaped remote whose generated references name
// the server id x.
func remoteCompose() ui.ComposeMcpMsg {
	return ui.ComposeMcpMsg{
		Spec: actions.AddSpec{
			ID: "x", Type: typeHTTP, URL: remoteURL,
			Headers: []string{"Authorization: Bearer ${secret:x.api_key}", teamHeader},
		},
		Provenance: "official registry: io.example/x@1.2.0",
		RefID:      "x",
	}
}

// open loads a screen over a fixture home that also holds server x, and
// opens the prefilled form from m.
func open(t *testing.T, m ui.ComposeMcpMsg) *Screen {
	t.Helper()
	home := fixtureHome(t)
	writeServer(t, home, "x", offTOML)
	s := newLoaded(t, home, okRunner())
	s.Update(m)
	if s.form == nil || !s.CapturesKeys() {
		t.Fatal("prefilled form not open")
	}
	return s
}

func submitSpec(t *testing.T, s *Screen) actions.AddSpec {
	t.Helper()
	spec, err := s.form.spec()
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	return spec
}

// scripted answers by command: secretList and "mcp add" from the map,
// anything else with the status fixture.
type scripted struct {
	mu    sync.Mutex
	calls []string
	out   map[string]runner.Result
}

func (f *scripted) Run(_ context.Context, args ...string) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := strings.Join(args, " ")
	f.calls = append(f.calls, c)
	for prefix, res := range f.out {
		if strings.HasPrefix(c, prefix) {
			return res, nil
		}
	}
	return runner.Result{Stdout: statusJSON}, nil
}

func (f *scripted) ran(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// shell is the real App around a loaded servers screen and an other tab.
func shell(t *testing.T, r runner.Runner) (*ui.App, *Screen) {
	t.Helper()
	home := fixtureHome(t)
	d := ui.Deps{Runner: r, AgentsyncHome: home, Scope: runner.ScopeUser, LogPath: filepath.Join(t.TempDir(), "activity.log")}
	scr := New(d)
	app := ui.NewApp(d, otherTab{}, scr)
	appDrive(app, func() tea.Msg { return tea.WindowSizeMsg{Width: 200, Height: 60} })
	appDrive(app, scr.Refresh())
	return app, scr
}

// otherTab is a do-nothing first tab, so the hand-off has a tab to leave.
type otherTab struct{}

func (otherTab) Init() tea.Cmd                         { return nil }
func (o otherTab) Update(tea.Msg) (tea.Model, tea.Cmd) { return o, nil }
func (otherTab) View() tea.View                        { return tea.NewView("other tab body") }
func (otherTab) Title() string                         { return "Other" }
func (otherTab) Refresh() tea.Cmd                      { return nil }

func appView(app *ui.App) string { return ansi.Strip(app.View().Content) }

func TestComposeActivatesServersTabAndOpensForm(t *testing.T) {
	app, scr := shell(t, okRunner())
	if v := appView(app); !strings.Contains(v, "other tab body") {
		t.Fatalf("precondition: first tab on show:\n%s", v)
	}
	appDrive(app, func() tea.Msg { return braveCompose() })
	v := appView(app)
	if !strings.Contains(v, "Add MCP server") || !strings.Contains(v, "brave") || !scr.CapturesKeys() {
		t.Fatalf("prefilled form not on show:\n%s", v)
	}
	if strings.Contains(v, "other tab body") {
		t.Errorf("still on the first tab:\n%s", v)
	}
}

func TestPrefilledFormValuesAreMaskedLikeManualEntry(t *testing.T) {
	m := braveCompose()
	m.Spec.Env["SHARED"] = "${secret:x}"
	m.Prompts = []ui.FieldPrompt{
		{Name: "API_TOKEN", Required: true, Description: "token for the service"},
		{Name: region, Default: euWest},
	}
	s := open(t, m)
	v := view(s)
	for _, want := range []string{braveEnv + " = " + braveRef, "SHARED = ${secret:x}", region, euWest} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	// A literal typed into a secret-named prompt is bullets, as in manual entry.
	press(s, "tab", "tab", "tab", "tab", "tab", "tab", "tab")
	typeText(s, canary)
	if v := view(s); strings.Contains(v, "CANARY") || !strings.Contains(v, "API_TOKEN") || !strings.Contains(v, "••••") {
		t.Errorf("secret-named prompt echoes its value:\n%s", v)
	}
	// So is a literal in a secret-named env entry typed into the env input.
	press(s, "shift+tab", "shift+tab", "shift+tab")
	typeText(s, "MY_SECRET="+canary)
	press(s, "enter")
	if v := view(s); strings.Contains(v, "CANARY") || !strings.Contains(v, "MY_SECRET = ••••") {
		t.Errorf("env literal shown:\n%s", v)
	}
	act := onlyDo(t, press(s, "ctrl+s"))
	if strings.Contains(act.Display(), "CANARY") {
		t.Errorf("display leaks: %s", act.Display())
	}
}

func TestPrefilledSubmitBuildsSameActionAsManual(t *testing.T) {
	m := braveCompose()
	s := open(t, m)
	if got := submitSpec(t, s); !reflect.DeepEqual(got, m.Spec) {
		t.Fatalf("spec round trip:\n got %#v\nwant %#v", got, m.Spec)
	}
	act := onlyDo(t, press(s, "ctrl+s"))
	want := mustAct(actions.McpAdd(m.Spec))
	if !slices.Equal(act.Argv, want.Argv) || act.Display() != want.Display() {
		t.Errorf("action\n got %q / %s\nwant %q / %s", act.Argv, act.Display(), want.Argv, want.Display())
	}
	if s.form != nil {
		t.Error("form still open after submit")
	}
}

func TestPrefilledStillGoesThroughConfirmModal(t *testing.T) {
	fr := &scripted{}
	app, _ := shell(t, fr)
	appDrive(app, func() tea.Msg { return braveCompose() })
	appPress(app, "ctrl+s")
	v := appView(app)
	if !strings.Contains(v, addBrave) || !strings.Contains(v, "Run it?") {
		t.Fatalf("confirm modal not shown:\n%s", v)
	}
	if fr.ran("mcp add") != 0 {
		t.Fatal("add ran before it was confirmed")
	}
	appPress(app, "y")
	if fr.ran(addBrave) != 1 {
		t.Errorf("add did not run once confirmed: %q", fr.calls)
	}
}

func TestTakenIDIsRefused(t *testing.T) {
	m := braveCompose()
	m.Spec.ID, m.RefID = takenID, takenID
	s := open(t, m)
	if !strings.Contains(view(s), "already exists") {
		t.Errorf("taken id not flagged on open:\n%s", view(s))
	}
	if msgs := press(s, "ctrl+s"); len(msgs) != 0 {
		t.Fatalf("submitted a taken id: %#v", msgs)
	}
	if s.form == nil || !strings.Contains(view(s), "already exists — press E on it to edit") {
		t.Errorf("view:\n%s", view(s))
	}
}

func TestSecretRefsFollowEditedID(t *testing.T) {
	m := remoteCompose() // id x is taken
	m.Spec.Args = []string{keyFlag, "${secret:x.k}"}
	s := open(t, m)
	// The user types a reference of their own into the args value that
	// holds a generated one; it names x too, but was not in the message.
	press(s, "tab", "tab", "tab")
	typeText(s, " ${secret:x.other}")
	press(s, "shift+tab", "shift+tab", "shift+tab")
	// An invalid id rewrites nothing; neither does a taken one (pasted
	// whole, so no valid prefix is typed on the way).
	press(s, "backspace")
	for _, id := range []string{"-bad", takenID} {
		s.Update(tea.PasteMsg{Content: id})
		if got := submitSpec(t, s).Headers; got[1] != teamHeader {
			t.Errorf("id %q rewrote a reference: %q", id, got)
		}
		for range id {
			press(s, "backspace")
		}
	}
	typeText(s, "yz")
	press(s, "backspace")
	want := []string{yAuth, "X-Team: ${secret:y.team_id}"}
	wantArgs := []string{keyFlag, yKeyRef, "${secret:x.other}"}
	got := submitSpec(t, s)
	if got.ID != "y" || !slices.Equal(got.Headers, want) || !slices.Equal(got.Args, wantArgs) {
		t.Fatalf("after renaming to y: id %q headers %q args %q, want %q %q", got.ID, got.Headers, got.Args, want, wantArgs)
	}
	v := view(s)
	if !strings.Contains(v, "Bearer ${secret:y.api_key}") {
		t.Errorf("view does not show the rewritten reference:\n%s", v)
	}
	act := onlyDo(t, press(s, "ctrl+s"))
	if !slices.Equal(act.Argv, mustAct(actions.McpAdd(actions.AddSpec{
		ID: "y", Type: typeHTTP, URL: remoteURL, Headers: want, Args: wantArgs,
	})).Argv) {
		t.Errorf("argv %q", act.Argv)
	}
	// The after-add note checks the final references.
	s.run = &scripted{out: map[string]runner.Result{secretList: {Stdout: "y.api_key\n"}}}
	drive(s, func() tea.Msg { return ui.RunMsg{Action: act} })
	v = view(s)
	for _, want := range []string{"y.team_id", "y.k", "x.other", secretsTab} {
		if !strings.Contains(v, want) {
			t.Errorf("note missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "x.api_key") || strings.Contains(v, "y.api_key") || strings.Contains(v, "x.team_id") {
		t.Errorf("note names a stale or set reference:\n%s", v)
	}
}

// A generated reference the user removed is no longer tracked: typing it
// back makes it the user's, and a later rename leaves it alone.
func TestRemovedRefIsNoLongerTracked(t *testing.T) {
	s := open(t, remoteCompose())
	press(s, "tab", "tab", "tab", "tab", "tab", "ctrl+d") // drops X-Team
	typeText(s, teamHeader)
	press(s, "enter", "shift+tab", "shift+tab", "shift+tab", "shift+tab", "shift+tab", "backspace")
	typeText(s, "y")
	want := []string{yAuth, teamHeader}
	if got := submitSpec(t, s).Headers; !slices.Equal(got, want) {
		t.Errorf("headers %q, want %q", got, want)
	}
}

func TestRequiredPromptsMustBeFilledBeforeSubmit(t *testing.T) {
	m := braveCompose()
	m.Prompts = []ui.FieldPrompt{
		{Name: region, Required: true, Description: "the\tAWS\nregion"},
		{Name: "LOG_LEVEL", Default: "info"},
	}
	s := open(t, m)
	if v := view(s); !strings.Contains(v, "the AWS region") {
		t.Errorf("description not on one line:\n%s", v)
	}
	if msgs := press(s, "ctrl+s"); len(msgs) != 0 {
		t.Fatalf("submitted with a required prompt empty: %#v", msgs)
	}
	if !strings.Contains(view(s), "REGION is required") {
		t.Errorf("view:\n%s", view(s))
	}
	press(s, "tab", "tab", "tab", "tab", "tab", "tab", "tab")
	typeText(s, euWest)
	act := onlyDo(t, press(s, "ctrl+s"))
	spec := m.Spec
	spec.Env = map[string]string{braveEnv: braveRef, region: euWest, "LOG_LEVEL": "info"}
	if !slices.Equal(act.Argv, mustAct(actions.McpAdd(spec)).Argv) {
		t.Errorf("argv %q", act.Argv)
	}
}

func TestProvenanceBannerShown(t *testing.T) {
	s := open(t, braveCompose())
	v := view(s)
	for _, want := range []string{
		"from " + braveProv + runsCodeNote,
		trustDigest,
		"image signatures and attestations are NOT verified by this tool",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	m := braveCompose()
	m.Provenance = "official registry: evil\x1b[31m\u202e\nname@1.0"
	m.Trust = []string{"line\x1b]0;title\x07 one\u200b"}
	m.Prompts = []ui.FieldPrompt{{Name: region, Description: "pick\x1b[2J\u202eone\u200b\x1b]8;;http://x\x07"}}
	s = open(t, m)
	raw := s.View().Content
	for _, bad := range []string{"\x1b]", "\x1b[31m", "\x1b[2J", "\u202e", "\u200b"} {
		if strings.Contains(raw, bad) {
			t.Errorf("hostile text rendered live (%q): %q", bad, raw)
		}
	}
	if !strings.Contains(view(s), "evil") || !strings.Contains(view(s), "name@1.0"+runsCodeNote) {
		t.Errorf("provenance not on one line:\n%s", view(s))
	}
}

func TestScopeSwitchClosesPrefilledForm(t *testing.T) {
	s := open(t, braveCompose())
	s.SetScope(okRunner(), runner.ScopeProject, s.home)
	if s.form != nil || s.CapturesKeys() {
		t.Error("prefilled form survived a scope switch")
	}
	if v := view(s); strings.Contains(v, "Add MCP server") || strings.Contains(v, braveProv) {
		t.Errorf("view:\n%s", v)
	}
}

func TestAfterAddNoteListsSecretsToSet(t *testing.T) {
	m := braveCompose()
	m.Spec.Env["OTHER_TOKEN"] = "${secret:brave.other_token}"
	fr := &scripted{out: map[string]runner.Result{secretList: {Stdout: "brave.other_token\nGITHUB_TOKEN\n"}}}
	app, _ := shell(t, fr)
	appDrive(app, func() tea.Msg { return m })
	appPress(app, "ctrl+s", "y")
	if fr.ran(addBrave) != 1 || fr.ran(secretList) != 1 {
		t.Fatalf("calls %q", fr.calls)
	}
	v := appView(app)
	if !strings.Contains(v, "brave.brave_api_key") || !strings.Contains(v, secretsTab) {
		t.Errorf("note does not name the unset secret:\n%s", v)
	}
	if strings.Contains(v, "brave.other_token") {
		t.Errorf("note names a secret that is set:\n%s", v)
	}
	if fr.ran("secret set") != 0 || strings.Contains(v, "secret set") {
		t.Errorf("the note offers to set a value:\n%s", v)
	}
}

// A failed `secret list` is reported as "could not check", never as
// "nothing missing".
func TestAfterAddNoteSaysWhenItCouldNotCheck(t *testing.T) {
	fr := &scripted{out: map[string]runner.Result{secretList: {ExitCode: 1, Stderr: `set backend = "age" in agentsync.toml [secrets]`}}}
	app, _ := shell(t, fr)
	appDrive(app, func() tea.Msg { return braveCompose() })
	appPress(app, "ctrl+s", "y")
	v := appView(app)
	if !strings.Contains(v, "could not check") || !strings.Contains(v, "brave.brave_api_key") {
		t.Errorf("note:\n%s", v)
	}
	if strings.Contains(v, "every secret") {
		t.Errorf("note claims the secrets are set:\n%s", v)
	}
}

// An add that references no vault secret, or that was cancelled or failed,
// runs no `secret list`.
func TestAfterAddNoteOnlyForSuccessfulAddWithSecrets(t *testing.T) {
	fr := &scripted{out: map[string]runner.Result{"mcp add plain": {ExitCode: 1, Stderr: "boom"}}}
	app, _ := shell(t, fr)
	m := braveCompose()
	m.Spec.Env = nil
	appDrive(app, func() tea.Msg { return m })
	appPress(app, "ctrl+s", "y")
	m = braveCompose()
	appDrive(app, func() tea.Msg { return m })
	appPress(app, "ctrl+s", "n")
	m.Spec.ID, m.RefID = "plain", "plain"
	appDrive(app, func() tea.Msg { return m })
	appPress(app, "ctrl+s", "y")
	if fr.ran("mcp add") != 2 || fr.ran(secretList) != 0 {
		t.Errorf("calls %q", fr.calls)
	}
}

// Header values may hold commas: the CLI takes each --header whole (see
// the actions package's CLI test), so the form passes them through.
func TestPrefilledHeaderWithCommaSubmits(t *testing.T) {
	m := remoteCompose()
	m.Spec.ID, m.RefID = "commas", "commas"
	m.Spec.Headers = []string{"Accept: application/json, text/event-stream"}
	s := open(t, m)
	act := onlyDo(t, press(s, "ctrl+s"))
	if !slices.Contains(act.Argv, "Accept: application/json, text/event-stream") {
		t.Errorf("argv %q", act.Argv)
	}
}
