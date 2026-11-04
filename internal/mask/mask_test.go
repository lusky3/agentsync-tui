package mask

import (
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/model"
)

const CANARY = "gg_pat_CANARYCANARYCANARYCANARY0123456789"

func noCanary(t *testing.T, where, s string) {
	t.Helper()
	if strings.Contains(s, "CANARY") {
		t.Errorf("%s leaked canary: %q", where, s)
	}
}

func TestArgsMasksInlineBearerToken(t *testing.T) {
	in := []string{"-y", "mcp-remote", "https://x", "--header", "Authorization: Bearer " + CANARY}
	orig := append([]string(nil), in...)
	out := Args(in)
	for _, a := range out {
		noCanary(t, "Args", a)
	}
	for i := range in {
		if in[i] != orig[i] {
			t.Fatalf("input mutated at %d", i)
		}
	}
	if out[0] != "-y" || out[1] != "mcp-remote" || out[2] != "https://x" || out[3] != "--header" {
		t.Errorf("non-secret args changed: %q", out)
	}
}

func TestArgsFlagForms(t *testing.T) {
	cases := [][]string{
		{"--token", "short"},
		{"--token=short"},
		{"--api-key", "short"},
		{"--password=short"},
		{"-H", "X-Custom: short"},
		{"--secret", "short"},
		{"--auth", "short"},
		{"Authorization: Basic short"},
		{"Bearer short"},
	}
	for _, c := range cases {
		for _, a := range Args(c) {
			if strings.Contains(a, "short") {
				t.Errorf("Args(%q) leaked: %q", c, a)
			}
		}
	}
	got := Args([]string{"--header", "Authorization: Bearer ${GG_TOKEN}", "--token", "${secret:GG}", "--token=${env:X}"})
	want := []string{"--header", "Authorization: Bearer ${GG_TOKEN}", "--token", "${secret:GG}", "--token=${env:X}"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ref arg changed: got %q want %q", got[i], want[i])
		}
	}
}

func TestArgsPassesPlainThrough(t *testing.T) {
	in := []string{"-y", "@scope/pkg@1.2.3", "https://mcp.example/mcp", "--verbose"}
	out := Args(in)
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("changed %q -> %q", in[i], out[i])
		}
	}
}

func TestValueKeepsRefs(t *testing.T) {
	for _, r := range []string{"${secret:GG}", "${env:HOME_TOKEN}", "${GG}"} {
		if got := Value("token", r); got != r {
			t.Errorf("Value(%q)=%q", r, got)
		}
		if !IsRef(r) {
			t.Errorf("IsRef(%q) false", r)
		}
	}
	if got := Value("token", "abcdef123456"); got != "••••" {
		t.Errorf("got %q", got)
	}
	if got := Value("h", "Bearer ${GG_TOKEN}"); got != "Bearer ${GG_TOKEN}" {
		t.Errorf("mixed ref changed: %q", got)
	}
	if got := Value("h", "prefix-${X}"); got != "prefix-${X}" {
		t.Errorf("mixed ref changed: %q", got)
	}
	for _, s := range []string{"", "plain", "${a} ${b}", "x${a}", "${}", "${secret:}"} {
		if IsRef(s) {
			t.Errorf("IsRef(%q) true", s)
		}
	}
}

func TestHunkMasksResolvedSecrets(t *testing.T) {
	h := model.Hunk{
		Path:    "mcp.json",
		Pointer: "/mcpServers/gg/headers/Authorization",
		Source:  `{"Authorization":"Bearer ` + CANARY + `"}`,
		Dest:    `{"Authorization":"Bearer ${secret:GG}"}`,
	}
	got := Hunk(h)
	noCanary(t, "Source", got.Source)
	if got.Dest != h.Dest {
		t.Errorf("Dest changed: %q", got.Dest)
	}
	if got.Path != h.Path || got.Pointer != h.Pointer {
		t.Errorf("structural fields changed")
	}
	g2 := Hunk(model.Hunk{Pointer: "/env/API_TOKEN", Source: `"hunter2"`, Dest: `"${secret:T}"`})
	if strings.Contains(g2.Source, "hunter2") || g2.Dest != `"${secret:T}"` {
		t.Errorf("sensitive pointer: %+v", g2)
	}
}

func TestSanitizeStripsAnsiAndControl(t *testing.T) {
	cases := map[string]string{
		"a\x1b[31mb\x07c\r\nd":   "abc\nd",
		"x\x1b]0;evil\x07y":      "xy",
		"x\x1b]0;evil\x1b\\y":    "xy",
		"a\rb":                   "ab",
		"a\tb\nc":                "a\tb\nc",
		"a\x7fb\u009bc":          "ab",
		"a\u202eb\u200bc\ufeffd": "abcd",
		"a\xffb":                 "a�b",
		"a\x1b[1;31;mb":          "ab",
		"a\u009d0;evil\u009cb":   "ab",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q)=%q want %q", in, got, want)
		}
	}
}

func TestTextLeavesPlainURLsAlone(t *testing.T) {
	for _, s := range []string{
		"https://mcp.example/mcp",
		"https://example.com/a/b?x=1&y=2",
		"npx -y mcp-remote task-runner risk-free",
		"git@github.com:lusky3/repo.git",
	} {
		if got := Text(s); got != s {
			t.Errorf("Text(%q)=%q", s, got)
		}
	}
}

func TestTextKeepsBearerWithRef(t *testing.T) {
	s := "Authorization: Bearer ${GG_TOKEN}"
	if got := Text(s); got != s {
		t.Errorf("got %q", got)
	}
}

func TestTextMasksBearerLiteralButKeepsSurroundingText(t *testing.T) {
	got := Text("Authorization: Bearer " + CANARY)
	if got != "Authorization: Bearer ••••" {
		t.Errorf("got %q", got)
	}
}

func TestTextShapes(t *testing.T) {
	for _, s := range []string{
		"ghp_abcdef123", "github_pat_abc123", "xoxb-1234-abcd", "AKIAABCDEFGHIJKLMNOP",
		"sk-abc123", "https://user:pw0rd@host/x", "https://h/x?token=short",
		"https://h/x?a=1&api_key=short", "token=abcdefghijklmnopqrstuvwxyz",
		`"password": "abcdefghijklmnopqrstuvwxyz"`,
	} {
		if got := Text("pre " + s + " post"); got == "pre "+s+" post" || strings.Contains(got, "pw0rd") ||
			strings.Contains(got, "short") || strings.Contains(got, "abcdefghijklmnop") {
			t.Errorf("Text(%q)=%q", s, got)
		}
	}
	if got := Text("https://${U}:${P}@host/x?token=${secret:T}"); got != "https://${U}:${P}@host/x?token=${secret:T}" {
		t.Errorf("refs changed: %q", got)
	}
}

func TestNoCanarySurvivesAnyEntryPoint(t *testing.T) {
	shapes := []string{
		CANARY,
		"ghp_CANARYCANARY0123456789",
		"sk-CANARYCANARY0123456789",
		"eyJCANARYhdr.eyJCANARYpay.CANARYsig",
		"https://user:CANARYPASS@host/mcp",
		"https://host/mcp?token=CANARYQ",
		"https://host/mcp?x=1&api_key=CANARYQ",
	}
	for _, c := range shapes {
		noCanary(t, "Value", Value("anything", c))
		noCanary(t, "Text", Text("pre "+c+" post"))
		noCanary(t, "Text-bearer", Text("Authorization: Bearer "+c))
		for _, a := range Args([]string{"-y", c, "--header", "Authorization: Bearer " + c, "--token", c, "--token=" + c}) {
			noCanary(t, "Args", a)
		}
		h := Hunk(model.Hunk{
			Path: "p", Pointer: "/a/b",
			Source: `{"v":"` + c + `","u":"x ` + c + `"}`,
			Dest:   `["` + c + `"]`,
		})
		noCanary(t, "Hunk.Source", h.Source)
		noCanary(t, "Hunk.Dest", h.Dest)
	}
}
