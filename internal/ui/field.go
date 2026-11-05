package ui

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

// Field is the one-line text input every screen edits with, so the editing
// rules are the same everywhere: backspace removes one rune, ctrl+u clears,
// typed text is sanitised and line breaks and tabs fold to spaces, keys
// with ctrl or alt held insert nothing, and a byte cap (Max) and a rune
// policy (Accept) are applied on insert. Rendering is the screen's: a
// secret-bearing field echoes bullets, a key-name field locks on a bad rune.
type Field struct {
	Value string
	// Max caps the value in bytes; 0 means no cap. Text that would exceed it
	// is refused whole.
	Max int
	// Accept, when set, is asked about every rune before it is inserted;
	// Insert stops at the first refusal.
	Accept func(r rune) bool
}

// Key applies an editing key. handled reports whether k was one (backspace,
// ctrl+u or typed text); changed whether the value is different now.
func (f *Field) Key(k tea.KeyPressMsg) (handled, changed bool) {
	switch k.String() {
	case "backspace":
		if f.Value == "" {
			return true, false
		}
		_, n := utf8.DecodeLastRuneInString(f.Value)
		f.Value = f.Value[:len(f.Value)-n]
		return true, true
	case "ctrl+u":
		changed = f.Value != ""
		f.Value = ""
		return true, changed
	}
	if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
		return false, false
	}
	changed, _ = f.Insert(k.Text)
	return true, changed
}

// Insert appends s (sanitised, line breaks and tabs folded to spaces) one
// rune at a time. It stops at the first rune Accept refuses, and refuses
// everything when the whole would exceed Max. refused reports a rune Accept
// turned down; changed whether anything was added.
func (f *Field) Insert(s string) (changed, refused bool) {
	s = strings.NewReplacer("\n", " ", "\t", " ").Replace(mask.Sanitize(s))
	if f.Max > 0 && len(f.Value)+len(s) > f.Max {
		return false, false
	}
	for _, r := range s {
		if f.Accept != nil && !f.Accept(r) {
			return changed, true
		}
		f.Value += string(r)
		changed = true
	}
	return changed, false
}

// Clear empties the field.
func (f *Field) Clear() { f.Value = "" }
