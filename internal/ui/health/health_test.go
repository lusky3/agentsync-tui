package health

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const canary = "CANARYx1"

// fakeRunner answers by space-joined argv; unknown commands return a zero
// Result. A non-nil err wins for every command.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	res   map[string]runner.Result
	errs  map[string]error
}

func newFake() *fakeRunner {
	return &fakeRunner{res: map[string]runner.Result{}, errs: map[string]error{}}
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (runner.Result, error) {
	k := strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, k)
	return f.res[k], f.errs[k]
}

func (f *fakeRunner) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func newScreen(r runner.Runner) *Screen {
	s := New(ui.Deps{Runner: r, Scope: runner.ScopeUser})
	s.Update(tea.WindowSizeMsg{Width: 300, Height: 400})
	return s
}

// collect runs cmd (flattening batches) and returns every message.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	m := cmd()
	if b, ok := m.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, collect(c)...)
		}
		return out
	}
	if m == nil {
		return nil
	}
	return []tea.Msg{m}
}

func deliver(s *Screen, msgs []tea.Msg) {
	for _, m := range msgs {
		s.Update(m)
	}
}

func refresh(s *Screen) { deliver(s, collect(s.Refresh())) }

func key(k string) tea.KeyPressMsg {
	switch k {
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

func view(s *Screen) string { return s.View().Content }

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRefreshRunsDoctorAndCheckReadOnly(t *testing.T) {
	fr := newFake()
	s := newScreen(fr)
	msgs := collect(s.Refresh())
	got := fr.called()
	if len(got) != 2 || (got[0] != "doctor" || got[1] != "check") && (got[0] != "check" || got[1] != "doctor") {
		t.Fatalf("argv = %v", got)
	}
	for _, m := range msgs {
		if _, isDo := m.(ui.DoMsg); isDo {
			t.Fatal("Refresh emitted a DoMsg (the shell would loop)")
		}
	}
	// Delivering the results must not request anything either.
	for _, m := range msgs {
		if _, cmd := s.Update(m); cmd != nil {
			t.Fatalf("result handling returned a cmd for %T", m)
		}
	}
}

func TestNonZeroExitIsAReportNotAnError(t *testing.T) {
	fr := newFake()
	fr.res["doctor"] = runner.Result{Stdout: "agentsync doctor\n  ✗ schema invalid\n", ExitCode: 1}
	fr.res["check"] = runner.Result{Stderr: "✗ ERROR  check: parse boom\n", ExitCode: 1}
	s := newScreen(fr)
	refresh(s)
	v := view(s)
	for _, want := range []string{"doctor — machine readiness", "check — config validity", "schema invalid",
		"parse boom", "problems found", "(exit 1)", "check: failed"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if s.doctor.note != "" || s.check.note != "" || strings.Contains(v, "unavailable") {
		t.Errorf("a non-zero exit was reported as an error: %q %q", s.doctor.note, s.check.note)
	}
}

func TestSummariesFromExitCodesOnly(t *testing.T) {
	// Wording that says the opposite of the exit code must not matter.
	fr := newFake()
	fr.res["doctor"] = runner.Result{Stdout: "all checks passed\n", ExitCode: 3}
	fr.res["check"] = runner.Result{Stdout: "FAILED ERROR invalid\n", ExitCode: 0}
	s := newScreen(fr)
	refresh(s)
	v := view(s)
	if !strings.Contains(v, "doctor: problems found (exit 3)") || !strings.Contains(v, "check: OK") {
		t.Errorf("summary follows wording, not exit codes:\n%s", v)
	}
	if got := s.doctor.summary("doctor", "problems found"); got != "doctor: problems found (exit 3)" {
		t.Errorf("summary = %q", got)
	}
}

func TestOutputStoredMasked(t *testing.T) {
	dirty := "password: " + canary + "\n" +
		"token=" + canary + "\n" +
		"Authorization: Bearer " + canary + "\n" +
		"\x1b[31mred\x1b[0m \u202eevil\u202c \x1b]0;title\x07end\n"
	for _, stream := range []string{"stdout", "stderr"} {
		fr := newFake()
		r := runner.Result{ExitCode: 1}
		if stream == "stdout" {
			r.Stdout = dirty
		} else {
			r.Stderr = dirty
		}
		fr.res["doctor"], fr.res["check"] = r, r
		s := newScreen(fr)
		refresh(s)
		for name, got := range map[string]string{"doctor": s.doctor.text, "check": s.check.text, "view": ansi.Strip(view(s))} {
			if strings.Contains(got, canary) || strings.ContainsRune(got, 0x1b) ||
				strings.ContainsRune(got, 0x202e) || strings.ContainsRune(got, 0x202c) {
				t.Errorf("%s/%s not clean: %q", stream, name, got)
			}
		}
		if !strings.Contains(s.doctor.text, "red") {
			t.Errorf("%s: sanitising ate the text: %q", stream, s.doctor.text)
		}
	}
}

func TestSpawnErrorNote(t *testing.T) {
	for _, kind := range []string{"doctor", "check"} {
		for _, err := range []error{
			errors.New("exec: password: " + canary + " failed\nsecond line"),
			errors.New("exec: token=" + canary + " \x1b[31mred"),
			runner.ErrNotInstalled,
		} {
			fr := newFake()
			fr.errs[kind] = err
			s := newScreen(fr)
			refresh(s)
			v := ansi.Strip(view(s)) // our own styling is not the command's
			if strings.Contains(v, canary) || strings.ContainsRune(v, 0x1b) {
				t.Errorf("%s: error text leaked: %q", kind, v)
			}
			if !strings.Contains(v, kind+" unavailable") {
				t.Errorf("%s: no dim note: %q", kind, v)
			}
			if s.doctor.note != "" && kind == "check" || s.check.note != "" && kind == "doctor" {
				t.Errorf("%s: the other section got the note", kind)
			}
		}
	}
	// No runner at all.
	s := newScreen(nil)
	refresh(s)
	if v := view(s); !strings.Contains(v, "doctor unavailable") || !strings.Contains(v, "check unavailable") {
		t.Errorf("nil runner: %q", v)
	}
}

func TestFailureKeepsLastOutput(t *testing.T) {
	fr := newFake()
	fr.res["doctor"] = runner.Result{Stdout: "LASTGOOD-doctor\n"}
	fr.res["check"] = runner.Result{Stdout: "LASTGOOD-check\n"}
	s := newScreen(fr)
	refresh(s)
	fr.errs["doctor"], fr.errs["check"] = errors.New("boom"), errors.New("boom")
	refresh(s)
	v := view(s)
	for _, want := range []string{"LASTGOOD-doctor", "LASTGOOD-check", "doctor unavailable", "check unavailable", "doctor: OK"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	// A later success clears the note.
	delete(fr.errs, "doctor")
	refresh(s)
	if s.doctor.note != "" {
		t.Errorf("note not cleared: %q", s.doctor.note)
	}
}

func TestCapAfterMaskingRuneSafe(t *testing.T) {
	for pad := 0; pad < 3; pad++ {
		// The secret starts a few bytes before the cap; pad shifts the cap
		// into the middle of a 2-byte rune.
		prefix := strings.Repeat("é", (ui.MaxOutput-8)/2) + strings.Repeat("x", pad)
		text := prefix + "password: " + canary + "\n" + strings.Repeat("é", 100)
		fr := newFake()
		fr.res["doctor"] = runner.Result{Stdout: text}
		s := newScreen(fr)
		refresh(s)
		got := s.doctor.text
		if len(got) > ui.MaxOutput || !s.doctor.trunc {
			t.Errorf("pad %d: len %d trunc %v", pad, len(got), s.doctor.trunc)
		}
		if !utf8.ValidString(got) {
			t.Errorf("pad %d: cut split a rune", pad)
		}
		if strings.Contains(got, "CANARY") || strings.Contains(got, canary[:4]) {
			t.Errorf("pad %d: canary (or a prefix of it) survived the cut", pad)
		}
		if !strings.Contains(view(s), "output truncated") && !strings.Contains(strings.Join(s.lines(), "\n"), "output truncated") {
			t.Errorf("pad %d: no truncation note", pad)
		}
	}
}

func TestScopeAwareSwapsRunner(t *testing.T) {
	var _ ui.ScopeAware = (*Screen)(nil)
	old, fresh := newFake(), newFake()
	old.res["doctor"] = runner.Result{Stdout: "OLD\n"}
	fresh.res["doctor"] = runner.Result{Stdout: "NEW\n"}
	s := newScreen(old)
	refresh(s)
	inflight := collect(s.Refresh()) // a result for the old scope
	s.SetScope(fresh, runner.ScopeProject, "")
	if strings.Contains(view(s), "OLD") {
		t.Error("old scope's output survived the switch")
	}
	deliver(s, inflight)
	if strings.Contains(view(s), "OLD") {
		t.Error("an in-flight old-scope result was adopted after the switch")
	}
	refresh(s)
	if !strings.Contains(view(s), "NEW") || len(fresh.called()) != 2 {
		t.Errorf("fresh runner not used: %v\n%s", fresh.called(), view(s))
	}
}

func TestInitEqualsRefresh(t *testing.T) {
	a, b := newFake(), newFake()
	sa, sb := newScreen(a), newScreen(b)
	collect(sa.Init())
	collect(sb.Refresh())
	if strings.Join(a.called(), ",") != strings.Join(b.called(), ",") || len(a.called()) != 2 {
		t.Errorf("Init %v != Refresh %v", a.called(), b.called())
	}
	if sa.reads.Gen() != sb.reads.Gen() {
		t.Errorf("generation differs: %d vs %d", sa.reads.Gen(), sb.reads.Gen())
	}
}

func TestKeyCapturerFalse(t *testing.T) {
	var kc ui.KeyCapturer = newScreen(newFake())
	if kc.CapturesKeys() {
		t.Error("health has no text input")
	}
	if got := newScreen(nil).Title(); got != "Health" {
		t.Errorf("title = %q", got)
	}
}

// Every key gets a fresh screen; a reached counter proves the loop ran each.
func TestKeysNeverStartAnythingButRefresh(t *testing.T) {
	keys := []string{"x", "y", "n", "d", "a", "enter", "esc", "down", "up", "j", "k", "ctrl+d", "ctrl+u", "pgdown", "pgup", "r", "q", "s", "l", "?"}
	reached := 0
	for _, k := range keys {
		fr := newFake()
		s := newScreen(fr)
		_, cmd := s.Update(key(k))
		reached++
		msgs := collect(cmd)
		for _, m := range msgs {
			if _, ok := m.(ui.DoMsg); ok {
				t.Errorf("key %q produced a DoMsg", k)
			}
		}
		want := 0
		if k == "r" {
			want = 2
		}
		if n := len(fr.called()); n != want {
			t.Errorf("key %q ran %d commands, want %d", k, n, want)
		}
	}
	if reached != len(keys) {
		t.Fatalf("reached %d of %d keys", reached, len(keys))
	}
}

func TestStaleResultDropped(t *testing.T) {
	fr := newFake()
	fr.res["doctor"] = runner.Result{Stdout: "STALE\n"}
	s := newScreen(fr)
	stale := collect(s.Refresh())
	fr.res["doctor"] = runner.Result{Stdout: "FRESH\n"}
	fresh := collect(s.Refresh())
	deliver(s, fresh)
	deliver(s, stale)
	if v := view(s); !strings.Contains(v, "FRESH") || strings.Contains(v, "STALE") {
		t.Errorf("stale result won:\n%s", v)
	}
}

func TestScrollBounds(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("line-" + strings.Repeat("z", 3) + "\n")
	}
	sb.WriteString("LASTLINE\n")
	fr := newFake()
	fr.res["doctor"] = runner.Result{Stdout: sb.String()}
	s := newScreen(fr)
	s.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	refresh(s)
	if !strings.Contains(view(s), "doctor — machine readiness") {
		t.Fatal("not at the top")
	}
	s.Update(key("ctrl+u")) // above the top
	s.Update(key("pgup"))
	if s.scroll.Offset != 0 {
		t.Errorf("scrolled above the top: %d", s.scroll.Offset)
	}
	for i := 0; i < 200; i++ {
		s.Update(key("pgdown"))
		s.Update(key("ctrl+d"))
	}
	if s.scroll.Offset != s.maxScroll() || !strings.Contains(view(s), "LASTLINE") {
		t.Errorf("scroll %d max %d:\n%s", s.scroll.Offset, s.maxScroll(), view(s))
	}
	if n := len(strings.Split(view(s), "\n")); n > 12 {
		t.Errorf("view is %d lines in a 12-line screen", n)
	}
	s.Update(key("ctrl+u"))
	if s.scroll.Offset >= s.maxScroll() {
		t.Error("ctrl+u did not scroll up")
	}
	// Shrinking the output re-clamps.
	fr.res["doctor"] = runner.Result{Stdout: "short\n"}
	refresh(s)
	if s.scroll.Offset > s.maxScroll() {
		t.Errorf("scroll %d beyond max %d", s.scroll.Offset, s.maxScroll())
	}
}

func TestRealFixtures(t *testing.T) {
	type c struct {
		name         string
		doc, chk     runner.Result
		wantDoc      string
		wantChk      string
		wantContains []string
	}
	cases := []c{
		{
			name:         "fresh",
			doc:          runner.Result{Stdout: fixture(t, "doctor-fresh.stdout"), Stderr: fixture(t, "doctor-fresh.stderr")},
			chk:          runner.Result{Stdout: fixture(t, "check-fresh.stdout"), Stderr: fixture(t, "check-fresh.stderr")},
			wantDoc:      "doctor: OK",
			wantChk:      "check: OK",
			wantContains: []string{"all checks passed", "schema valid"},
		},
		{
			name:         "broken",
			doc:          runner.Result{Stdout: fixture(t, "doctor-broken.stdout"), Stderr: fixture(t, "doctor-broken.stderr"), ExitCode: 1},
			chk:          runner.Result{Stdout: fixture(t, "check-broken.stdout"), Stderr: fixture(t, "check-broken.stderr"), ExitCode: 1},
			wantDoc:      "doctor: problems found (exit 1)",
			wantChk:      "check: failed (exit 1)",
			wantContains: []string{"schema     invalid", "basic strings cannot have new lines"},
		},
	}
	for _, tc := range cases {
		fr := newFake()
		fr.res["doctor"], fr.res["check"] = tc.doc, tc.chk
		s := newScreen(fr)
		refresh(s)
		v := view(s)
		for _, w := range append([]string{tc.wantDoc, tc.wantChk}, tc.wantContains...) {
			if !strings.Contains(v, w) {
				t.Errorf("%s: view lacks %q:\n%s", tc.name, w, v)
			}
		}
	}
}

// A secret whose identifying context comes AFTER it (URL userinfo needs the
// "@") is only recognisable before the cut: capping first would leave the
// bare secret behind.
func TestCapAfterMaskingKeepsTrailingContextSecrets(t *testing.T) {
	head := "https://user:" + canary
	for _, back := range []int{0, 3, 6} { // the cap lands this many bytes before the secret's end
		n := ui.MaxOutput - len(head) + back
		prefix := strings.Repeat("é", n/2) + strings.Repeat("x", n%2)
		fr := newFake()
		fr.res["doctor"] = runner.Result{Stdout: prefix + head + "@host/path\n" + strings.Repeat("é", 50)}
		s := newScreen(fr)
		refresh(s)
		if len(s.doctor.text) > ui.MaxOutput || !s.doctor.trunc || !utf8.ValidString(s.doctor.text) {
			t.Errorf("back %d: len %d trunc %v", back, len(s.doctor.text), s.doctor.trunc)
		}
		if strings.Contains(s.doctor.text, "CANARY") {
			t.Errorf("back %d: secret survived the cut: ...%q", back, s.doctor.text[len(s.doctor.text)-30:])
		}
	}
}
