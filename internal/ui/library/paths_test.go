package library

import (
	"path/filepath"
	"testing"
)

func TestPathForKind(t *testing.T) {
	home := "/h/.agentsync"
	cases := []struct {
		kind Kind
		name string
		want string
	}{
		{Skill, "greet", "skills/greet/SKILL.md"},
		{Subagent, "reviewer", "subagents/reviewer.md"},
		{Command, "test-all", "commands/test-all.md"},
		{Hook, "PreToolUse", "hooks/PreToolUse.toml"},
		{LSP, "gopls", "lsp/gopls.toml"},
		{Memory, "AGENTS", "memory/AGENTS.md"},
		{Memory, "style", "memory/fragments/style.md"},
		{Memory, "a.b-c_d", "memory/fragments/a.b-c_d.md"},
	}
	for _, c := range cases {
		got, err := PathFor(home, c.kind, c.name)
		if err != nil || got != filepath.Join(home, c.want) {
			t.Errorf("%v %q: %q %v", c.kind, c.name, got, err)
		}
	}
}

func TestPathRejectsTraversal(t *testing.T) {
	bad := []string{"../x", "a/b", "", "a\nb", "-rf", "a\x00b", "/etc/passwd", "..", ".", ".hidden", `a\b`, "a b",
		string(make([]byte, 0)) + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for k := Skill; k <= Memory; k++ {
		for _, n := range bad {
			if p, err := PathFor("/h/.agentsync", k, n); err == nil {
				t.Errorf("kind %v name %q accepted: %q", k, n, p)
			}
		}
	}
	if _, err := PathFor("", Skill, "x"); err == nil {
		t.Error("empty home accepted")
	}
	if _, err := PathFor("/h", Kind(99), "x"); err == nil {
		t.Error("unknown kind accepted")
	}
}
