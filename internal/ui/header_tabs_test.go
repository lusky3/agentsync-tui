package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

func manyTabs(n int) *App {
	var screens []Screen
	for i := 1; i <= n; i++ {
		screens = append(screens, newScreen(fmt.Sprintf("Screen%d", i), "b"))
	}
	return NewApp(Deps{Scope: runner.ScopeUser}, screens...)
}

func TestHeaderAlwaysShowsActiveTab(t *testing.T) {
	const n = 15
	reached := 0
	for _, w := range []int{20, 40, 60, 80, 120} {
		for i := 0; i < n; i++ {
			a := manyTabs(n)
			a.Update(tea.WindowSizeMsg{Width: w, Height: 12})
			a.active = i
			a.Init() // no runner: the chip settles on "? unavailable"
			reached++
			h := ansi.Strip(header(a))
			num := i + 1
			whole := fmt.Sprintf(" %d Screen%d", num, num)
			short := fmt.Sprintf("%d/", num) // the "n/N Title" fallback at narrow widths
			switch {
			case w >= 60 && !strings.Contains(h, whole):
				t.Errorf("w=%d active=%d: active tab missing: %q", w, num, h)
			case w < 60 && !strings.Contains(h, whole) && !strings.HasPrefix(h, short):
				t.Errorf("w=%d active=%d: neither tab nor n/N shown: %q", w, num, h)
			}
			for _, l := range strings.Split(ansi.Strip(a.View().Content), "\n") {
				if ansi.StringWidth(l) > w {
					t.Errorf("w=%d active=%d: line %q is %d wide", w, num, l, ansi.StringWidth(l))
				}
			}
			if w == 80 && !(strings.Contains(h, "? unavailable") && strings.Contains(h, "scope: user")) {
				t.Errorf("w=80 active=%d: chip or scope lost: %q", num, h)
			}
			if (w == 80 || w == 60) && !strings.HasPrefix(h, " 1 Screen1 ") && !strings.Contains(h, "‹") {
				t.Errorf("w=%d active=%d: no marker for hidden left tabs: %q", w, num, h)
			}
		}
	}
	if reached != 5*n {
		t.Fatalf("reached %d", reached)
	}
	a := manyTabs(n)
	a.Update(tea.WindowSizeMsg{Width: 0, Height: 12})
	a.active = 14
	_ = a.View() // width 0 must not panic
}
