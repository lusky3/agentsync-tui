// Package e2e drives the real app against the real agentsync binary inside a
// hermetic home.
package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/app"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/testenv"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const waitFor = 15 * time.Second

func TestE2ESmoke(t *testing.T) {
	if _, err := exec.LookPath("agentsync"); err != nil {
		t.Skip("agentsync not on PATH")
	}
	env := testenv.New(t)
	env.Init(t)
	if _, se, code := env.Agentsync(t, "--no-input", "mcp", "add", "smoke", "--command", "echo"); code != 0 {
		t.Fatalf("mcp add exit %d: %s", code, se)
	}
	toml := filepath.Join(env.AgentsyncHome, "mcp", "smoke.toml")
	if b, err := os.ReadFile(toml); err != nil || strings.Contains(string(b), "enabled = false") {
		t.Fatalf("precondition: %v\n%s", err, b)
	}

	// The hermetic home is the only home this process's agentsync children see.
	ex := runner.NewExec("agentsync", runner.ScopeUser, "")
	ex.Env = env.Env
	deps := ui.Deps{
		Runner:        ex,
		Interactive:   ex.Interactive,
		AgentsyncHome: env.AgentsyncHome,
		Scope:         runner.ScopeUser,
		LogPath:       filepath.Join(env.Home, "activity.log"),
	}
	tm := teatest.NewTestModel(t, ui.NewApp(deps, app.Screens(deps)...),
		teatest.WithInitialTermSize(160, 40))

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("smoke"))
	}, teatest.WithDuration(waitFor))

	tm.Type("e")
	// Wait for the confirm modal before answering it.
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("mcp disable smoke")) && bytes.Contains(b, []byte("Run it?"))
	}, teatest.WithDuration(waitFor))
	tm.Send(tea.KeyPressMsg{Code: 'y', Text: "y"})

	deadline := time.Now().Add(waitFor)
	for {
		b, _ := os.ReadFile(toml)
		if strings.Contains(string(b), "enabled = false") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server was not disabled; canonical TOML:\n%s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}

	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(waitFor))
}
