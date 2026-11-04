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

func shortNetworkTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := networkTimeout
	networkTimeout = d
	t.Cleanup(func() { networkTimeout = old })
}

func TestNetworkTimeoutDefault(t *testing.T) {
	if NetworkTimeout != 120*time.Second || networkTimeout != NetworkTimeout {
		t.Fatalf("NetworkTimeout = %v, networkTimeout = %v", NetworkTimeout, networkTimeout)
	}
}

func TestNetworkActionHasTimeout(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	act := mustAct(actions.PluginUpgrade("p"))
	if !act.Network {
		t.Fatal("precondition: network action")
	}
	h.tm.Send(DoMsg{Action: act})
	h.pressAndWait(key("y"))
	h.waitStarted()
	h.tm.Send(key("ctrl+c")) // end the blocked fake; only the deadline matters here
	h.waitRun()
	h.final()
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.deadlines) != 1 || !fr.deadlines[0] {
		t.Fatalf("network action ran without a deadline: %v", fr.deadlines)
	}
}

func TestNonNetworkActionHasNoTimeout(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: actions.Check()})
	h.waitStarted()
	h.tm.Send(key("ctrl+c"))
	h.waitRun()
	h.final()
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.deadlines) != 1 || fr.deadlines[0] {
		t.Fatalf("non-network action got a deadline: %v", fr.deadlines)
	}
}

func TestNetworkTimeoutIsLogged(t *testing.T) {
	shortNetworkTimeout(t, 50*time.Millisecond)
	fr := newFakeRunner()
	d := baseDeps(fr)
	d.LogPath = filepath.Join(t.TempDir(), "activity.log")
	h := start(t, d, newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: mustAct(actions.PluginUpgrade("p"))})
	h.pressAndWait(key("y"))
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

func TestTimeoutDescribed(t *testing.T) {
	if got := (timeoutError{120 * time.Second}).Error(); got != "timed out after 120s" {
		t.Errorf("got %q", got)
	}
	if !errors.Is(timeoutError{time.Second}, context.DeadlineExceeded) {
		t.Error("timeoutError must unwrap to DeadlineExceeded")
	}
}
