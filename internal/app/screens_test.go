package app_test

import (
	"reflect"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/app"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// baseTitles are the tabs before the Source repo tab, in order. A plan that
// adds tabs inserts them here; Source repo stays last.
var baseTitles = []string{
	"MCP servers", "Drift", "Apply", "Reconcile", "Import", "Agents",
	"Plugins", "Secrets", "Skills", "Subagents", "Commands", "Hooks",
	"LSP", "Memory", "Health",
}

func TestScreensOrderAndTitles(t *testing.T) {
	got := app.Screens(ui.Deps{})
	if len(got) != len(baseTitles)+1 {
		t.Fatalf("got %d screens, want %d (the base tabs plus Source repo)", len(got), len(baseTitles)+1)
	}
	var titles []string
	for i, s := range got {
		if s == nil {
			t.Fatalf("screen %d is nil", i)
		}
		titles = append(titles, s.Title())
	}
	if last := titles[len(titles)-1]; last != "Source repo" {
		t.Errorf("last tab is %q, want %q", last, "Source repo")
	}
	if !reflect.DeepEqual(titles[:len(baseTitles)], baseTitles) {
		t.Fatalf("titles/order\n got: %q\nwant: %q", titles[:len(baseTitles)], baseTitles)
	}
}

func TestEveryScreenImplementsScopeSetter(t *testing.T) {
	for i, s := range app.Screens(ui.Deps{}) {
		if _, ok := s.(ui.ScopeSetter); !ok {
			t.Errorf("screen %d (%s) is not ui.ScopeSetter", i, s.Title())
		}
	}
}
