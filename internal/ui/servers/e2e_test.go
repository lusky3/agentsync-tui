package servers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// appDrive runs cmd and feeds every resulting message back into the real
// shell, as the Bubble Tea runtime would (synchronously, deterministic).
func appDrive(app *ui.App, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0 && n < 10000; n++ {
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
			_, next := app.Update(m)
			queue = append(queue, next)
		}
	}
}

func appPress(app *ui.App, keys ...string) {
	for _, k := range keys {
		_, cmd := app.Update(key(k))
		appDrive(app, cmd)
	}
}

func appType(app *ui.App, text string) {
	for _, r := range text {
		k := tea.KeyPressMsg{Code: r, Text: string(r)}
		if r == ' ' {
			k.Code = tea.KeySpace
		}
		_, cmd := app.Update(k)
		appDrive(app, cmd)
	}
}

// TestAddFormLiteralsNeverReachModalOrLog submits literals through the real
// shell: neither the confirm modal nor the activity log may show them.
func TestAddFormLiteralsNeverReachModalOrLog(t *testing.T) {
	home := fixtureHome(t)
	logPath := filepath.Join(t.TempDir(), "activity.log")
	fr := okRunner()
	d := ui.Deps{Runner: fr, AgentsyncHome: home, Scope: runner.ScopeUser, LogPath: logPath}
	scr := New(d)
	app := ui.NewApp(d, scr)
	appDrive(app, func() tea.Msg { return tea.WindowSizeMsg{Width: 400, Height: 60} })
	appDrive(app, scr.Refresh())

	appPress(app, "a")
	appType(app, "srv")
	appPress(app, "tab", "tab")
	appType(app, "npx")
	appPress(app, "tab")
	appType(app, "-y pkg --api-key "+canary+" --token="+canary)
	appPress(app, "tab")
	for _, e := range []string{"DEBUG=1", "SECRET_TOKEN=CANARYs", "DB_PASS=" + canary, "MY_PAT=" + canary, "API_KEY=${env:A}"} {
		appType(app, e)
		appPress(app, "enter")
	}
	appPress(app, "tab")
	for _, h := range []string{"X-Custom: " + canary, "Authorization: Bearer ${env:T}"} {
		appType(app, h)
		appPress(app, "enter")
	}
	appPress(app, "ctrl+s") // -> DoMsg -> confirm modal

	modal := ansi.Strip(app.View().Content)
	if !strings.Contains(modal, "mcp add srv") {
		t.Fatalf("confirm modal not shown:\n%s", modal)
	}
	if strings.Contains(modal, "CANARY") {
		t.Fatalf("confirm modal leaks:\n%s", modal)
	}
	for _, keep := range []string{"API_KEY=${env:A}", "${env:T}"} {
		if !strings.Contains(modal, keep) {
			t.Errorf("modal hides ref %q:\n%s", keep, modal)
		}
	}
	appPress(app, "y")
	ran := false
	for _, c := range fr.callStrings() {
		if strings.HasPrefix(c, "mcp add srv") {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("add never ran: %q", fr.callStrings())
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "mcp add srv") || strings.Contains(string(b), "CANARY") {
		t.Errorf("activity log: %s", b)
	}
	if v := ansi.Strip(app.View().Content); strings.Contains(v, "CANARY") {
		t.Errorf("view after run leaks:\n%s", v)
	}
}

// TestLiveArgsEchoMasksWithUnterminatedQuote: a half-typed quoted secret
// must not be echoed.
func TestLiveArgsEchoMasksWithUnterminatedQuote(t *testing.T) {
	s := newLoaded(t, fixtureHome(t), okRunner())
	press(s, "a")
	typeText(s, "srv")
	press(s, "tab", "tab")
	typeText(s, "npx")
	press(s, "tab")
	for _, in := range []string{`--api-key "` + canary, `--api-key '` + canary, `--token=` + canary + `\`,
		"-e DB_PASS=" + canary, "-e MY_PAT=" + canary, "--pat " + canary, "--api-key=" + canary, "--mode=" + canary,
		`"--password ` + canary, `--privateKey ` + canary} {
		press(s, "ctrl+u")
		typeText(s, in)
		if v := view(s); strings.Contains(v, "CANARY") {
			t.Errorf("live args %q leaks:\n%s", in, v)
		}
	}
}

// TestLiveCommandEchoMasks: the command field is free text too.
func TestLiveCommandEchoMasks(t *testing.T) {
	s := newLoaded(t, fixtureHome(t), okRunner())
	press(s, "a")
	typeText(s, "srv")
	press(s, "tab", "tab")
	for _, in := range []string{"mytool --password " + canary, "mytool --api-key=" + canary,
		`sh -c 'curl -H "Authorization: Bearer ` + canary + `"'`, `mytool --password "` + canary,
		`sh -c 'mytool --password ` + canary + `'`, `sh -c "mytool --password ` + canary, `mytool --accessKey ` + canary} {
		press(s, "ctrl+u")
		typeText(s, in)
		if v := view(s); strings.Contains(v, "CANARY") {
			t.Errorf("live command %q leaks:\n%s", in, v)
		}
	}
	// Benign input stays readable; refs stay visible.
	press(s, "ctrl+u")
	typeText(s, "npx")
	press(s, "tab")
	typeText(s, "-y @upstash/context7-mcp --port 8080 -e API_KEY=${env:A}")
	if v := view(s); !strings.Contains(v, "-y @upstash/context7-mcp --port 8080 -e API_KEY=${env:A}") {
		t.Errorf("benign args not readable:\n%s", v)
	}
}

func TestFormShowsFreeTextWarning(t *testing.T) {
	s := newLoaded(t, fixtureHome(t), okRunner())
	press(s, "a")
	want := "arguments and command are free text: put secrets in env/headers as ${env:NAME} or ${secret:NAME}; literal secrets in args are stored in plain text and can't always be masked in the preview"
	if v := strings.Join(strings.Fields(view(s)), " "); !strings.Contains(v, want) {
		t.Errorf("warning missing:\n%s", view(s))
	}
}

// TestAddFormFreeTextSecretsNeverReachModalOrLog: round-2 vectors end to end.
func TestAddFormFreeTextSecretsNeverReachModalOrLog(t *testing.T) {
	home := fixtureHome(t)
	logPath := filepath.Join(t.TempDir(), "activity.log")
	fr := okRunner()
	d := ui.Deps{Runner: fr, AgentsyncHome: home, Scope: runner.ScopeUser, LogPath: logPath}
	scr := New(d)
	app := ui.NewApp(d, scr)
	appDrive(app, func() tea.Msg { return tea.WindowSizeMsg{Width: 400, Height: 60} })
	appDrive(app, scr.Refresh())
	appPress(app, "a")
	appType(app, "srv")
	appPress(app, "tab", "tab")
	appType(app, "sh -c 'mytool --password "+canary+"'")
	appPress(app, "tab")
	appType(app, "-e DB_PASS="+canary+" -e MY_PAT="+canary+" --pat "+canary)
	appPress(app, "ctrl+s")
	modal := ansi.Strip(app.View().Content)
	if !strings.Contains(modal, "mcp add srv") || strings.Contains(modal, "CANARY") {
		t.Fatalf("modal:\n%s", modal)
	}
	appPress(app, "y")
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "mcp add srv") || strings.Contains(string(b), "CANARY") {
		t.Errorf("activity log: %s", b)
	}
	for _, c := range fr.callStrings() {
		if strings.HasPrefix(c, "mcp add srv") && !strings.Contains(c, "DB_PASS="+canary) {
			t.Errorf("argv that runs must keep the literal: %s", c)
		}
	}
}
