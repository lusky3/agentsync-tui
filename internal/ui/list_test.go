package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func rowsN(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("row%03d", i)
	}
	return out
}

// A list taller than the body keeps the cursor row and the footer visible
// at every height, and the viewport follows the cursor both ways.
func TestListFrameKeepsCursorAndFooter(t *testing.T) {
	rows := rowsN(50)
	header := []string{"HEAD1", "HEAD2"}
	footer := []string{"note", "keys"}
	for _, height := range []int{5, 8, 12, 24, 60} {
		var l List
		for _, cursor := range []int{0, 7, 49, 25, 0, 49} {
			l.Cursor = cursor
			out := l.Frame(header, rows, footer, height)
			if len(out) > max(height, len(header)+len(footer)+1) {
				t.Errorf("h=%d c=%d: %d lines", height, cursor, len(out))
			}
			if out[0] != "HEAD1" || out[len(out)-1] != "keys" || out[len(out)-2] != "note" {
				t.Errorf("h=%d c=%d: header or footer cut: %q", height, cursor, out)
			}
			if !slices.Contains(out, rows[cursor]) {
				t.Errorf("h=%d c=%d: cursor row not visible: %q", height, cursor, out)
			}
		}
	}
}

func TestListWindowMovesLeast(t *testing.T) {
	var l List
	if s, e := l.Window(50, 10); s != 0 || e != 10 {
		t.Fatalf("top: %d %d", s, e)
	}
	l.Cursor = 9
	if s, _ := l.Window(50, 10); s != 0 {
		t.Fatalf("cursor at the bottom edge must not scroll: %d", s)
	}
	l.Cursor = 10
	if s, e := l.Window(50, 10); s != 1 || e != 11 {
		t.Fatalf("one past the edge scrolls one: %d %d", s, e)
	}
	l.Cursor = 5
	if s, _ := l.Window(50, 10); s != 1 {
		t.Fatalf("moving up inside the window must not scroll: %d", s)
	}
	l.Cursor = 0
	if s, _ := l.Window(50, 10); s != 0 {
		t.Fatalf("home: %d", s)
	}
	l.Cursor = 49
	if s, e := l.Window(50, 10); s != 40 || e != 50 {
		t.Fatalf("end: %d %d", s, e)
	}
	// The list shrank under the viewport.
	if s, e := l.Window(5, 10); s != 0 || e != 5 {
		t.Fatalf("shrunk: %d %d", s, e)
	}
}

func TestListKeys(t *testing.T) {
	var l List
	l.Window(100, 10) // page = 10
	steps := []struct {
		k    string
		want int
	}{{"j", 1}, {"down", 2}, {"ctrl+d", 12}, {"pgdown", 22}, {"k", 21}, {"up", 20}, {"ctrl+u", 10}, {"pgup", 0}, {"up", 0},
		{"end", 99}, {"down", 99}, {"home", 0}}
	for _, st := range steps {
		if !l.Key(st.k, 100) || l.Cursor != st.want {
			t.Fatalf("%s: cursor %d, want %d", st.k, l.Cursor, st.want)
		}
	}
	if l.Key("x", 100) || l.Key("r", 100) || l.Key("enter", 100) {
		t.Fatal("non-scroll key handled")
	}
	l.Cursor = 5
	l.Key("j", 0)
	if l.Cursor != 0 {
		t.Fatalf("empty list cursor %d", l.Cursor)
	}
}

func TestScrollFrameAndKeys(t *testing.T) {
	body := rowsN(30)
	var s Scroll
	out := s.Frame([]string{"H"}, body, []string{"F"}, 12)
	if len(out) != 12 || out[0] != "H" || out[11] != "F" || out[1] != "row000" {
		t.Fatalf("top frame: %q", out)
	}
	s.Key("ctrl+d", 30)
	out = s.Frame([]string{"H"}, body, []string{"F"}, 12)
	if out[1] != "row010" {
		t.Fatalf("page down: %q", out[1])
	}
	if !s.Key("space", 30) || s.Offset != 20 { // space is a page-down alias
		t.Fatalf("space did not page down: %d", s.Offset)
	}
	s.Key("ctrl+u", 30)
	s.Key("end", 30)
	out = s.Frame([]string{"H"}, body, []string{"F"}, 12)
	if out[1] != "row020" || out[10] != "row029" || out[11] != "F" {
		t.Fatalf("end must show the last page and the footer: %q", out)
	}
	s.Key("j", 30)
	if s.Offset != 20 {
		t.Fatalf("scrolled past the end: %d", s.Offset)
	}
	s.Key("home", 30)
	s.Key("k", 30)
	if s.Offset != 0 {
		t.Fatalf("scrolled above the top: %d", s.Offset)
	}
	if s.Key("q", 30) {
		t.Fatal("non-scroll key handled")
	}
	// Short body: everything fits, the frame is padded by the shell, not here.
	out = s.Frame([]string{"H"}, body[:3], []string{"F"}, 12)
	if strings.Join(out, ",") != "H,row000,row001,row002,F" {
		t.Fatalf("short frame: %q", out)
	}
}

// When the terminal is shorter than header plus footer, one row still shows
// and the footer is kept rather than the rows.
func TestFrameTinyHeight(t *testing.T) {
	var l List
	out := l.Frame([]string{"H1", "H2"}, rowsN(5), []string{"F1", "F2"}, 3)
	if len(out) != 5 || out[2] != "row000" || out[4] != "F2" {
		t.Fatalf("tiny: %q", out)
	}
	if out := l.Frame([]string{"H1"}, nil, []string{"F1"}, 1); len(out) != 2 {
		t.Fatalf("no rows: %q", out)
	}
}

// KeyLines wraps a key hint at its " • " separators so every line fits the
// width and no hint (the scroll keys come last) is cut off; a hint that
// fits is one line, and a width too small for one item still yields it.
func TestKeyLinesWrap(t *testing.T) {
	hint := "a add • e enable/disable • x remove • r refresh • " + ScrollKeys
	one := KeyLines(hint, 200)
	if len(one) != 1 {
		t.Fatalf("fits: %q", one)
	}
	lines := KeyLines(hint, 40)
	if len(lines) < 2 {
		t.Fatalf("not wrapped: %q", lines)
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w > 40 {
			t.Errorf("line %q is %d wide", l, w)
		}
	}
	joined := strings.Join(lines, " • ")
	for _, want := range []string{"a add", "x remove", "home/end"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q lost: %q", want, lines)
		}
	}
	if got := KeyLines("home/end", 3); len(got) != 1 {
		t.Errorf("tiny width: %q", got)
	}
}
