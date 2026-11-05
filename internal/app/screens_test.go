package app_test

import (
	"reflect"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/app"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

func TestScreensOrderAndTitles(t *testing.T) {
	want := []string{
		"MCP servers", "Drift", "Apply", "Reconcile", "Import", "Agents",
		"Plugins", "Secrets", "Skills", "Subagents", "Commands", "Hooks",
		"LSP", "Memory", "Health",
	}
	got := app.Screens(ui.Deps{})
	if len(got) != len(want) {
		t.Fatalf("got %d screens, want %d", len(got), len(want))
	}
	var titles []string
	for i, s := range got {
		if s == nil {
			t.Fatalf("screen %d is nil", i)
		}
		if _, ok := s.(ui.ScopeAware); !ok {
			t.Errorf("screen %d (%s) is not ui.ScopeAware", i, s.Title())
		}
		titles = append(titles, s.Title())
	}
	if !reflect.DeepEqual(titles, want) {
		t.Fatalf("titles/order\n got: %q\nwant: %q", titles, want)
	}
}
