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

// ID drops the "@marketplace" qualifier and leaves a bare name alone.
func TestPluginAndOutdatedID(t *testing.T) {
	for name, want := range map[string]string{"hello@localmkt": "hello", "hello": "hello"} {
		if got := (Plugin{Name: name}).ID(); got != want {
			t.Errorf("Plugin{%q}.ID() = %q, want %q", name, got, want)
		}
		if got := (Outdated{Name: name}).ID(); got != want {
			t.Errorf("Outdated{%q}.ID() = %q, want %q", name, got, want)
		}
	}
}

// The url runs to the last " sha=" field; a sha field directly after
// "url=" (no space) means an empty url. Only the first word after sha= is
// the sha, and only when it is a safe token. None of these may panic.
func TestParseMarketplacesURLAndSHAFields(t *testing.T) {
	for _, tc := range []struct{ line, url, sha string }{
		{"m url=sha=", "", ""},                      // empty url, empty sha: used to panic
		{"m url=sha=abc", "", "abc"},                // used to read the sha as "bc"
		{"m url=sha=a", "", "a"},                    // used to drop the sha
		{"m url=x sha=abc", "x", "abc"},             //
		{"m url=a b sha=c d", "a b", "c"},           // spaces in the url; first word of the sha
		{"m url= sha=", "", ""},                     //
		{"m url=  sha=abc", "", "abc"},              //
		{"m url=a sha=b sha=c", "a sha=b", "c"},     // the last sha field wins
		{"m url=sha=abc sha=def", "sha=abc", "def"}, // a url that starts with sha=
		{"m url=x sha=!!", "x", ""},                 // unsafe sha dropped, row kept
		{"m url=x", "x", ""},                        // no sha field
		{"m url=sha", "sha", ""},                    // no '=' after sha
		{"m url=xsha=abc", "xsha=abc", ""},          // sha= inside a word is not a field
	} {
		got, err := ParseMarketplaces(tc.line)
		want := []Marketplace{{Name: "m", URL: tc.url, SHA: tc.sha}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseMarketplaces(%q) = %+v, %v; want %+v", tc.line, got, err, want)
		}
	}
}

// ParseMarketplaces never panics, and what it keeps is display-safe: a
// clean url and a sha that is empty or a safe token.
func FuzzParseMarketplaces(f *testing.F) {
	for _, s := range []string{"m url=sha=", "m url=sha=abc", "m url=a b sha=c d", "m url= sha=", "(none)", "m url=x\x1b sha=y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, _ := ParseMarketplaces(s)
		for _, m := range got {
			if !cleanText(m.URL) || (m.SHA != "" && !tokenRe.MatchString(m.SHA)) {
				t.Fatalf("ParseMarketplaces(%q) kept %+v", s, m)
			}
		}
	})
}

// A version= field makes a line a plugin row even when its value is not a
// safe token; the version is then left empty.
func TestParsePluginListUnsafeVersionStillARow(t *testing.T) {
	got, err := ParsePluginList("p version=!!")
	if want := []Plugin{{Name: "p"}}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v; want %+v", got, err, want)
	}
}
