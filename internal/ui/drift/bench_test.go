package drift

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// bigStatus is a status report with n items spread over ten agents, every
// path and pointer carrying something mask.Text has to look at.
func bigStatus(n int) string {
	var b strings.Builder
	b.WriteString(`{"agents":[`)
	for a := 0; a < 10; a++ {
		if a > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"agent":"agent%d","items":[`, a)
		for i := 0; i < n/10; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			class := "clean"
			if i%7 == 0 {
				class = "drift"
			}
			fmt.Fprintf(&b, `{"path":"/home/u/.agent%d/config-%d.json","pointer":"/mcpServers/srv-%d-token","class":"%s"}`, a, i, i, class)
		}
		b.WriteString("]}")
	}
	b.WriteString("]}")
	return b.String()
}

func benchList(b *testing.B, n int) {
	b.Helper()
	s := New(ui.Deps{Runner: statusRunner(bigStatus(n)), Scope: runner.ScopeUser})
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	drive(s, s.Refresh())
	if !strings.Contains(view(s), "agent0") {
		b.Fatal("report not loaded")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, cmd := s.Update(key("down"))
		drive(s, cmd)
		_ = s.View()
	}
}

// BenchmarkListView5000 is one key press plus one render of the list at
// 5,000 items (the production audit's 174 ms case).
func BenchmarkListView5000(b *testing.B) { benchList(b, 5000) }

func BenchmarkListView500(b *testing.B) { benchList(b, 500) }
