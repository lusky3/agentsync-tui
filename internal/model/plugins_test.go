package model

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func fx(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const hostile = "\x1b[31mred\x1b[0m \u202eevil ../x -rf a/b \x07bell\n"

func TestParsePluginListFixture(t *testing.T) {
	got, err := ParsePluginList(fx(t, "plugin-list.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Plugin{
		{Name: "hello", Version: "1.0.0", SHA: "tree:v1:9995", Enabled: true},
		{Name: "other", Version: "1.0.0", SHA: "tree:v1:76cc", Enabled: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	empty, err := ParsePluginList(fx(t, "plugin-list-empty.txt"))
	if err != nil || len(empty) != 0 {
		t.Errorf("empty = %v, %v", empty, err)
	}
}

func TestParsePluginListTolerantOfJunk(t *testing.T) {
	in := "\n  \nnew-token-line frobnicate\nhello version=2 enabled extra=1 shiny\n\nok@mkt version=1.2 disabled\n"
	got, err := ParsePluginList(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "hello" || !got[0].Enabled || got[1].Name != "ok@mkt" || got[1].Enabled {
		t.Errorf("got %+v", got)
	}
	if _, err := ParsePluginList("totally unrelated words here\n"); err == nil {
		t.Error("nothing parsed from non-empty input must error")
	}
	if got, err := ParsePluginList(""); err != nil || got != nil {
		t.Errorf("empty input: %v %v", got, err)
	}
}

func TestParsePluginListHostileNamesRejected(t *testing.T) {
	for _, name := range []string{"\x1b[31mred", "-rf", "../x", "a/b", "evil\u202e", "a@-b", "a@", "@m", "a@b@c", "bell\x07", ".hidden"} {
		got, err := ParsePluginList("good version=1 enabled\n" + name + " version=1 enabled\n")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != "good" {
			t.Errorf("%q became actionable: %+v", name, got)
		}
	}
	got, _ := ParsePluginList(hostile)
	if len(got) != 0 {
		t.Errorf("hostile line parsed: %+v", got)
	}
	got, _ = ParsePluginList("p version=\x1b[31m1 enabled\n")
	if len(got) != 1 || strings.ContainsAny(got[0].Version, "\x1b") || got[0].Version != "" {
		t.Errorf("hostile version kept: %+v", got)
	}
}

func TestParseOutdatedFixture(t *testing.T) {
	got, err := ParseOutdated(fx(t, "plugin-outdated.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Outdated{{Name: "hello", From: "1.0.0", To: "1.1.0", Mode: "track"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	clean, err := ParseOutdated(fx(t, "plugin-outdated-clean.txt"))
	if err != nil || len(clean) != 0 {
		t.Errorf("clean = %v, %v", clean, err)
	}
}

func TestParseOutdatedTolerantOfJunk(t *testing.T) {
	in := "pending bumps (2):\n\n  a 1 -> 2\n  b@m 1.0 → 1.1 [pin] extra\n  garbage line\nRun it\n"
	got, err := ParseOutdated(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "a" || got[0].To != "2" || got[1].Name != "b@m" || got[1].Mode != "pin" {
		t.Errorf("got %+v", got)
	}
	if _, err := ParseOutdated("nothing recognisable\n"); err == nil {
		t.Error("nothing parsed from non-empty input must error")
	}
}

func TestParseOutdatedHostileNamesRejected(t *testing.T) {
	for _, name := range []string{"\x1b[31mred", "-rf", "../x", "a/b", "evil\u202e", "a@-b"} {
		got, err := ParseOutdated("pending bumps (2):\n  good 1 → 2 [track]\n  " + name + " 1 → 2 [track]\n")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != "good" {
			t.Errorf("%q became actionable: %+v", name, got)
		}
	}
}

func TestParseMarketplacesFixture(t *testing.T) {
	got, err := ParseMarketplaces(fx(t, "marketplace-list.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Marketplace{{Name: "localmkt", URL: "/tmp/hermetic/mkt"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	empty, err := ParseMarketplaces(fx(t, "marketplace-list-empty.txt"))
	if err != nil || len(empty) != 0 {
		t.Errorf("empty = %v, %v", empty, err)
	}
}

func TestParseMarketplacesTolerantOfJunk(t *testing.T) {
	in := "\nwhat is this\nm1 url=https://h/r.git sha=abc extra\nm2 url=/path with space/x sha=\nm3 url=/p\nm4 newtoken=1\n"
	got, err := ParseMarketplaces(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].URL != "https://h/r.git" || got[0].SHA != "abc" || got[1].URL != "/path with space/x" || got[2].URL != "/p" {
		t.Errorf("got %+v", got)
	}
	if _, err := ParseMarketplaces("nothing here\n"); err == nil {
		t.Error("nothing parsed from non-empty input must error")
	}
}

func TestParseMarketplacesHostileNamesRejected(t *testing.T) {
	for _, name := range []string{"\x1b[31mred", "-rf", "../x", "a/b", "evil\u202e", "a@b"} {
		got, err := ParseMarketplaces("good url=/p sha=\n" + name + " url=/p sha=\n")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != "good" {
			t.Errorf("%q became actionable: %+v", name, got)
		}
	}
	got, _ := ParseMarketplaces("m url=/p\x1b[31m sha=\n")
	if len(got) != 1 || got[0].URL != "" {
		t.Errorf("control chars in url kept: %+v", got)
	}
}
