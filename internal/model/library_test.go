package model

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func names(items []Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

func TestParseListHappyPath(t *testing.T) {
	cases := map[string][]string{
		"skill-list.txt":    {"greet", "plan-it"},
		"subagent-list.txt": {"reviewer"},
		"command-list.txt":  {"deploy", "test-all"},
		"hook-list.txt":     {"PreToolUse"},
		"lsp-list.txt":      {"gopls", "rust-analyzer"},
	}
	for f, want := range cases {
		t.Run(f, func(t *testing.T) {
			got, err := ParseList(readFixture(t, f))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(names(got), want) {
				t.Errorf("names %v, want %v", names(got), want)
			}
		})
	}
	got, err := ParseList("alpha  some detail   here\nbeta\n")
	if err != nil || len(got) != 2 || got[0].Detail != "some detail here" || got[1].Detail != "" {
		t.Errorf("detail: %+v %v", got, err)
	}
}

func TestParseListEmptyMarker(t *testing.T) {
	for _, f := range []string{"skill", "subagent", "command", "hook", "lsp"} {
		got, err := ParseList(readFixture(t, f+"-list-empty.txt"))
		if err != nil || len(got) != 0 {
			t.Errorf("%s: %v %v", f, got, err)
		}
	}
	if got, err := ParseList(""); err != nil || len(got) != 0 {
		t.Errorf("empty input: %v %v", got, err)
	}
	if got, err := ParseList("\n  \n"); err != nil || len(got) != 0 {
		t.Errorf("blank input: %v %v", got, err)
	}
}

func TestParseListTolerantOfJunk(t *testing.T) {
	in := "\n\n  a-one   x=1 y=2 \r\n\t\nb.two\n(a parenthesised note)\n  \n"
	got, err := ParseList(in)
	if err != nil || !reflect.DeepEqual(names(got), []string{"a-one", "b.two"}) {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Detail != "x=1 y=2" {
		t.Errorf("detail %q", got[0].Detail)
	}
	// duplicates keep the first
	got, _ = ParseList("x one\nx two\n")
	if len(got) != 1 || got[0].Detail != "one" {
		t.Errorf("dup: %+v", got)
	}
	// non-empty input with nothing recognisable is an error
	if _, err := ParseList("✗ ERROR  load source: boom\n"); err == nil {
		t.Error("error output must not parse as an empty list")
	}
}

func TestParseListHostileDropped(t *testing.T) {
	in := strings.Join([]string{
		"good",
		"evil\x1b[31mred",
		"bell\x07x",
		"nul\x00x",
		"bidi‮x",
		"zw​x",
		"-rf",
		"--flag detail",
		"../x",
		"a/b",
		".hidden",
		"ok2 detail\x1b]0;title\x07",
		"bad\xffutf",
		strings.Repeat("a", 129),
		"fine detail",
	}, "\n")
	got, err := ParseList(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names(got), []string{"good", "fine"}) {
		t.Errorf("kept %v", names(got))
	}
	if _, err := ParseList("-rf\n../x\n"); err == nil {
		t.Error("all-hostile input must be an error")
	}
}
