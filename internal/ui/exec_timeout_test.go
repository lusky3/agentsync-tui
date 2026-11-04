package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/actions"
)

// networkRead is a ReadOnly action with Network set: an import dry run of
// a whole agent.
func networkRead(t *testing.T) actions.Action {
	t.Helper()
	a := mustAct(actions.Import("claude", "", "", true))
	if !a.ReadOnly || !a.Network {
		t.Fatalf("precondition: a network read, got %+v", a)
	}
	return a
}

func TestDefaultTimeouts(t *testing.T) {
	if DefaultReadTimeout != 120*time.Second {
		t.Errorf("DefaultReadTimeout = %v", DefaultReadTimeout)
	}
	if got := (Deps{}).Limits(); got != (Timeouts{Read: DefaultReadTimeout}) {
		t.Errorf("nil Timeouts gives %+v, want reads bounded and writes not", got)
	}
	if got := (Deps{Timeouts: &Timeouts{}}).Limits(); got != (Timeouts{}) {
		t.Errorf("explicit zero Timeouts gives %+v, want no bounds", got)
	}
}

// deadlineOf runs act through the shell (confirming a write with y), lets
// it finish and reports whether its context had a deadline.
func deadlineOf(t *testing.T, d Deps, act actions.Action) bool {
	t.Helper()
	fr := d.Runner.(*fakeRunner)
	h := start(t, d, newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: act})
	if !act.ReadOnly {
		h.pressAndWait(key("y"))
	}
	h.waitStarted()
	fr.respond <- fakeResp{}
	h.waitRun()
	h.final()
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.deadlines) != 1 {
		t.Fatalf("runner calls %v", fr.calls)
	}
	return fr.deadlines[0]
}

// By default every read is bounded (network reads by Read, local ones by
// Local) and no write is: a write, even a network one such as `plugin
// upgrade --all`, runs to completion.
func TestDefaultBoundsNetworkReadsOnly(t *testing.T) {
	cases := []struct {
		name string
		act  actions.Action
		want bool
	}{
		{"network read", networkRead(t), true},
		{"local read", actions.Check(), true},
		{"network write", actions.PluginUpgradeAll(), false},
		{"local write", mustAct(actions.McpEnable("x")), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deadlineOf(t, baseDeps(newFakeRunner()), c.act); got != c.want {
				t.Errorf("deadline = %v, want %v", got, c.want)
			}
		})
	}
}

// --write-timeout bounds every write; --read-timeout 0 lifts the read bound.
func TestConfiguredTimeouts(t *testing.T) {
	for _, act := range []actions.Action{mustAct(actions.McpEnable("x")), actions.PluginUpgradeAll()} {
		d := baseDeps(newFakeRunner())
		d.Timeouts = &Timeouts{Write: time.Hour}
		if !deadlineOf(t, d, act) {
			t.Errorf("write timeout not applied to %q", act.Argv)
		}
	}
	d := baseDeps(newFakeRunner())
	d.Timeouts = &Timeouts{}
	if deadlineOf(t, d, networkRead(t)) {
		t.Error("read timeout 0 still bounded a network read")
	}
}

func TestReadTimeoutIsLogged(t *testing.T) {
	fr := newFakeRunner()
	d := baseDeps(fr)
	d.Timeouts = &Timeouts{Read: 50 * time.Millisecond}
	d.LogPath = filepath.Join(t.TempDir(), "activity.log")
	h := start(t, d, newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: networkRead(t)})
	h.waitRun() // the fake honours ctx, so only the deadline can end it
	app := h.final()
	if !strings.Contains(content(app), "timed out after") {
		t.Errorf("no timeout banner: %q", content(app))
	}
	b, err := os.ReadFile(d.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "timeout") {
		t.Errorf("log lacks timeout outcome: %q", b)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.ctxErrs) != 1 || !errors.Is(fr.ctxErrs[0], context.DeadlineExceeded) {
		t.Errorf("ctx errs %v", fr.ctxErrs)
	}
}

func TestWriteTimeoutIsLogged(t *testing.T) {
	fr := newFakeRunner()
	d := baseDeps(fr)
	d.Timeouts = &Timeouts{Write: 50 * time.Millisecond}
	d.LogPath = filepath.Join(t.TempDir(), "activity.log")
	h := start(t, d, newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: actions.PluginUpgradeAll()})
	h.pressAndWait(key("y"))
	h.waitRun()
	app := h.final()
	if !strings.Contains(content(app), "timed out after") {
		t.Errorf("no timeout banner: %q", content(app))
	}
	if b, err := os.ReadFile(d.LogPath); err != nil || !strings.Contains(string(b), "timeout") {
		t.Errorf("log %q err %v", b, err)
	}
}

func TestTimeoutDescribed(t *testing.T) {
	if got := (timeoutError{120 * time.Second}).Error(); got != "timed out after 120s" {
		t.Errorf("got %q", got)
	}
	if !errors.Is(timeoutError{time.Second}, context.DeadlineExceeded) {
		t.Error("timeoutError must unwrap to DeadlineExceeded")
	}
}

// Local reads (lists, status, diff, explain, doctor, check, the header
// chip) are bounded at min(LocalReadBound, Read): --read-timeout can lower
// the bound but not lift it, and 0 (no network bound) still bounds them.
func TestTimeoutsLocal(t *testing.T) {
	if LocalReadBound != 30*time.Second {
		t.Fatalf("LocalReadBound = %v", LocalReadBound)
	}
	for _, c := range []struct{ read, want time.Duration }{
		{0, LocalReadBound},
		{2 * time.Minute, LocalReadBound},
		{10 * time.Second, 10 * time.Second},
		{LocalReadBound, LocalReadBound},
	} {
		if got := (Timeouts{Read: c.read}).Local(); got != c.want {
			t.Errorf("Read %v: Local %v, want %v", c.read, got, c.want)
		}
	}
	if DefaultTimeouts.Local() != LocalReadBound {
		t.Errorf("default local bound %v", DefaultTimeouts.Local())
	}
}

// A ReadOnly action that is not a network one (the apply/revert/import
// dry-run previews, check) is a local read and gets the local bound,
// min(LocalReadBound, --read-timeout); a network read keeps --read-timeout.
func TestLocalReadActionsBoundedLocally(t *testing.T) {
	pair, err := actions.ApplyPair()
	if err != nil {
		t.Fatal(err)
	}
	preview := pair.Preview
	if !preview.ReadOnly || preview.Network {
		t.Fatalf("precondition: %+v", preview)
	}
	for _, c := range []struct {
		name string
		read time.Duration
		act  actions.Action
		max  time.Duration
	}{
		{"preview default", 0, preview, LocalReadBound},
		{"check default", 0, actions.Check(), LocalReadBound},
		{"preview lowered", 5 * time.Second, preview, 5 * time.Second},
		{"preview read-timeout 2m", 2 * time.Minute, preview, LocalReadBound},
	} {
		fr := newFakeRunner()
		d := baseDeps(fr)
		if c.read > 0 {
			d.Timeouts = &Timeouts{Read: c.read}
		}
		h := start(t, d, newScreen("A", "a"))
		h.tm.Send(DoMsg{Action: c.act})
		h.waitStarted()
		fr.mu.Lock()
		ctx := fr.ctxs[0]
		fr.mu.Unlock()
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) > c.max {
			t.Errorf("%s: deadline %v (%v), want one within %v", c.name, ok, time.Until(dl), c.max)
		}
		h.final()
	}
}
