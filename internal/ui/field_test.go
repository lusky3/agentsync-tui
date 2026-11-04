package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func typed(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

func TestFieldEditingRules(t *testing.T) {
	var f Field
	if h, c := f.Key(typed("a")); !h || !c || f.Value != "a" {
		t.Fatalf("typed: %v %v %q", h, c, f.Value)
	}
	f.Insert("é\x1b[31mb\nc\td")
	if f.Value != "aéb c d" {
		t.Fatalf("sanitise/fold: %q", f.Value)
	}
	if h, c := f.Key(tea.KeyPressMsg{Code: tea.KeyBackspace}); !h || !c || f.Value != "aéb c " {
		t.Fatalf("backspace: %q", f.Value)
	}
	f.Value = "aé"
	f.Key(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if f.Value != "a" {
		t.Fatalf("backspace must remove one rune: %q", f.Value)
	}
	if h, c := f.Key(tea.KeyPressMsg{Code: 'x', Text: "x", Mod: tea.ModCtrl}); h || c || f.Value != "a" {
		t.Fatalf("ctrl+x inserted: %q", f.Value)
	}
	if h, c := f.Key(tea.KeyPressMsg{Code: 'x', Text: "x", Mod: tea.ModAlt}); h || c || f.Value != "a" {
		t.Fatalf("alt+x inserted: %q", f.Value)
	}
	if h, c := f.Key(tea.KeyPressMsg{Code: tea.KeyEnter}); h || c {
		t.Fatal("enter is not an editing key")
	}
	if h, c := f.Key(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}); !h || !c || f.Value != "" {
		t.Fatalf("ctrl+u: %v %v %q", h, c, f.Value)
	}
	if h, c := f.Key(tea.KeyPressMsg{Code: tea.KeyBackspace}); !h || c {
		t.Fatal("backspace on empty must be handled but change nothing")
	}
}

func TestFieldMaxAndAccept(t *testing.T) {
	f := Field{Max: 3}
	f.Insert("ab")
	if c, _ := f.Insert("cd"); c || f.Value != "ab" {
		t.Fatalf("over Max accepted: %q", f.Value)
	}
	if c, _ := f.Insert("c"); !c || f.Value != "abc" {
		t.Fatalf("at Max refused: %q", f.Value)
	}
	g := Field{Accept: func(r rune) bool { return r >= 'a' && r <= 'z' }}
	c, refused := g.Insert("ab=secret")
	if !c || !refused || g.Value != "ab" {
		t.Fatalf("Accept: changed=%v refused=%v %q", c, refused, g.Value)
	}
	if _, refused := g.Insert("ok"); refused || g.Value != "abok" {
		t.Fatalf("accepted text: %q", g.Value)
	}
}
