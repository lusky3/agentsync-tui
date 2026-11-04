package mask

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
		if got := Value(r); got != r {
			t.Errorf("Value(%q)=%q", r, got)
		}
		if !IsRef(r) {
			t.Errorf("IsRef(%q) false", r)
		}
	}
	if got := Value("abcdef123456"); got != "••••" {
		t.Errorf("got %q", got)
	}
	if got := Value("Bearer ${GG_TOKEN}"); got != "Bearer ${GG_TOKEN}" {
		t.Errorf("mixed ref changed: %q", got)
	}
	if got := Value("Basic ${USER}:${PASS}"); got != "Basic ${USER}:${PASS}" {
		t.Errorf("scheme+refs changed: %q", got)
	}
	for _, s := range []string{"Basic ${USER}:FAKElit5", "${X} FAKElit6", "prefix-${X}"} {
		if got := Value(s); got != "••••" {
			t.Errorf("Value(%q)=%q", s, got)
		}
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
	if !strings.Contains(got.Dest, "Bearer ${secret:GG}") {
		t.Errorf("Dest ref lost: %q", got.Dest)
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
		"https://example.com/a/b?x&y", // query keys stay; values are masked (TestURLMasksCredentialBearingParts)
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
		noCanary(t, "Value", Value(c))
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

const realisticHunk = `{
  "command": "npx",
  "args": ["-y", "pkg", "--token", "FAKEopaque1", "-e", "API_KEY=FAKEshort2", "--header", "X-Custom: FAKEhdr3", "--password=FAKEeq9"],
  "env": {"MY_VAR": "FAKEenv4", "PGPASSWORD": "FAKEpw5short", "API_TOKEN": "FAKEabcdefghijklmnopqrstuvwx+yz/FAKEtail6", "REF": "${secret:R}"},
  "headers": {"X-Custom": "FAKEhdr7", "Authorization": "Bearer ${GG_TOKEN}"}
}`

func TestHunkMasksOpaqueEnvHeadersArgs(t *testing.T) {
	h := Hunk(model.Hunk{Path: "p", Pointer: "/mcpServers/gg", Source: realisticHunk, Dest: realisticHunk})
	for _, side := range []string{h.Source, h.Dest} {
		if strings.Contains(side, "FAKE") {
			t.Errorf("leak: %s", side)
		}
		for _, keep := range []string{"npx", `"pkg"`, "${secret:R}", "Bearer ${GG_TOKEN}", "MY_VAR", "X-Custom"} {
			if !strings.Contains(side, keep) {
				t.Errorf("lost %q in %s", keep, side)
			}
		}
	}
	if h.Source != h.Dest {
		t.Errorf("sides not comparable")
	}
}

func TestArgsMasksShortSensitiveNameValue(t *testing.T) {
	for _, a := range Args([]string{"-e", "API_KEY=FAKEshort", "DB_PASSWORD=FAKEshort2"}) {
		if strings.Contains(a, "FAKE") {
			t.Errorf("leak %q", a)
		}
	}
	if got := Args([]string{"-e", "API_KEY=${secret:K}", "MODE=fast"}); got[1] != "API_KEY=${secret:K}" || got[2] != "MODE=fast" {
		t.Errorf("got %q", got)
	}
}

func TestTextSanitisesFirst(t *testing.T) {
	for _, in := range []string{"Bea\u200brer FAKEzw2", "gh\x1b[0mp_FAKEesc4", "Bearer\x07 FAKEbel"} {
		if got := Sanitize(Text(in)); strings.Contains(got, "FAKE") {
			t.Errorf("Text(%q) leaked: %q", in, got)
		}
	}
	if got := Text("ok \x1b]0;title\x07 done"); got != "ok  done" {
		t.Errorf("got %q", got)
	}
}

func TestNoOpaqueCanaryInHunk(t *testing.T) {
	for _, c := range []string{"CANARYa1", "CANARY+/=z", "CANARY value with spaces"} {
		src := `{"env":{"ANY":"` + c + `"},"headers":{"H":"` + c + `"},"args":["--token","` + c + `","-e","TOKEN=` + c + `"]}`
		h := Hunk(model.Hunk{Pointer: "/mcpServers/x", Source: src, Dest: src})
		noCanary(t, "Hunk.Source", h.Source)
		noCanary(t, "Hunk.Dest", h.Dest)
	}
}

func TestHunkPointerContext(t *testing.T) {
	cases := []struct{ ptr, side string }{
		{"/mcpServers/gg/env/MY_VAR", `"FAKEenv4"`},
		{"/mcpServers/gg/env", `{"MY_VAR":"FAKEenv4"}`},
		{"/mcpServers/gg/headers", `{"X-Custom":"FAKEhdr7"}`},
		{"/mcpServers/gg/args", `["--token","FAKEopaque1","-e","API_KEY=FAKEs2"]`},
		{"/mcpServers/gg/headers/Authorization", `"Bearer FAKEauth"`},
	}
	for _, c := range cases {
		h := Hunk(model.Hunk{Pointer: c.ptr, Source: c.side, Dest: c.side})
		if strings.Contains(h.Source, "FAKE") || strings.Contains(h.Dest, "FAKE") {
			t.Errorf("%s leaked: %q", c.ptr, h.Source)
		}
	}
	h := Hunk(model.Hunk{Pointer: "/mcpServers/gg/env/K", Source: `"${secret:K}"`})
	if h.Source != `"${secret:K}"` {
		t.Errorf("ref lost: %q", h.Source)
	}
}

func TestHunkKeysSanitisedAndScrubbed(t *testing.T) {
	h := Hunk(model.Hunk{Pointer: "/x", Source: "{\"a\u202eb\":1,\"c\u009bd\":2,\"Bearer FAKEkey9\":3}"})
	if strings.ContainsRune(h.Source, '\u202e') || strings.ContainsRune(h.Source, '\u009b') || strings.Contains(h.Source, "FAKE") {
		t.Errorf("key leak: %q", h.Source)
	}
}

func TestHunkKeyCollisionKeepsBothValues(t *testing.T) {
	h := Hunk(model.Hunk{Pointer: "/x", Source: `{"Bearer FAKEa":"one","Bearer FAKEb":"two"}`})
	if strings.Count(h.Source, "Bearer") != 2 || strings.Contains(h.Source, "FAKE") {
		t.Errorf("collision: %q", h.Source)
	}
	if again := Hunk(model.Hunk{Pointer: "/x", Source: `{"Bearer FAKEa":"one","Bearer FAKEb":"two"}`}); again.Source != h.Source {
		t.Errorf("unstable")
	}
}

func TestHunkUnparseableSides(t *testing.T) {
	for _, c := range []struct{ ptr, side string }{
		{"/env/API_TOKEN", `FAKEraw1`},
		{"/mcpServers/gg", `{"env":{"A":"FAKEtg"}} x`},
		{"/mcpServers/gg/env", `{"A":"FAKEtg"} {}`},
	} {
		h := Hunk(model.Hunk{Pointer: c.ptr, Source: c.side})
		if strings.Contains(h.Source, "FAKE") {
			t.Errorf("%q leaked: %q", c.side, h.Source)
		}
	}
	if h := Hunk(model.Hunk{Pointer: "/env/T", Source: `${secret:T}`}); h.Source != "${secret:T}" {
		t.Errorf("raw ref changed: %q", h.Source)
	}
	if h := Hunk(model.Hunk{Pointer: "/a", Source: `plain text`}); h.Source != "plain text" {
		t.Errorf("plain changed: %q", h.Source)
	}
}

func TestArgsFlagValueRefDoesNotShieldLiteral(t *testing.T) {
	// `mcp add --env` joins pairs with commas: a ref in a later pair must not
	// keep an earlier literal visible.
	// A plain (not token-shaped) literal, so only the ref rule is exercised.
	const lit = "plainCANARYliteral"
	for _, in := range [][]string{
		{"--env", "API_KEY=" + lit + ",TOKEN=${env:TOK}"},
		{"--token", "prefix-" + lit + "-${X}"},
		{"--header", "X-Custom: " + lit + " ${X}"},
	} {
		for _, a := range Args(in) {
			noCanary(t, "Args", a)
		}
	}
	if got := Args([]string{"--header", "X-Api-Key: ${secret:K}"}); got[1] != "X-Api-Key: ${secret:K}" {
		t.Errorf("header ref masked: %q", got)
	}
}

// Probe shapes from the whole-branch review: an args element addressed by
// index, numbers and bools under forced keys, a non-string element in args,
// and unparseable or TOML-rendered sides under an mcpServers pointer.
func TestHunkCanaryShapes(t *testing.T) {
	cases := []struct{ ptr, side, leak string }{
		{"/mcpServers/x/args/1", `"CANARYx1"`, "CANARY"},
		{"/mcpServers/x/ARGS/0", `"CANARYx1"`, "CANARY"},
		{"/mcpServers/x/env/API_TOKEN", `31415926`, "31415926"},
		{"/mcpServers/x/env/PIN", `true`, "true"},
		{"/mcpServers/x", `{"env":{"PIN":31415926,"ON":true}}`, "31415926"},
		{"/mcpServers/x", `{"env":{"PIN":31415926,"ON":true}}`, "true"},
		{"/mcpServers/x", `{"args":["--token",31415926]}`, "31415926"},
		{"/mcpServers/x", `{"args":["--port",8080,"--token","CANARYx2"]}`, "CANARY"},
		{"/mcpServers/x", `{"env":{"A":"CANARYx3"}`, "CANARY"},
		{"/mcpServers/x", "DB_PASS = \"CANARYx4\"\n", "CANARY"},
		{"/mcpServers/x/env", `{"A":"${secret:A}"} CANARYx5`, "CANARY"},
		{"/mcp_servers/x", `[mcp_servers.x.env]` + "\nA = \"CANARYx6\"\n", "CANARY"},
	}
	for _, c := range cases {
		h := Hunk(model.Hunk{Pointer: c.ptr, Source: c.side, Dest: c.side})
		for _, s := range []string{h.Source, h.Dest} {
			if strings.Contains(s, c.leak) {
				t.Errorf("%s %q leaked %q: %q", c.ptr, c.side, c.leak, s)
			}
		}
		if again := Hunk(h); again != h {
			t.Errorf("Hunk not idempotent for %s %q: %q -> %q", c.ptr, c.side, h.Source, again.Source)
		}
	}
}

// A hunk under a pointer that is neither forced nor mcpServers keeps its
// structure, and an unparseable side there is only scrubbed, not masked.
func TestHunkNonForcedKeepsStructure(t *testing.T) {
	h := Hunk(model.Hunk{Pointer: "/settings/theme", Source: `{"mode":"dark","size":12}`})
	for _, keep := range []string{`"mode": "dark"`, `"size": 12`} {
		if !strings.Contains(h.Source, keep) {
			t.Errorf("lost %q: %q", keep, h.Source)
		}
	}
	if h := Hunk(model.Hunk{Pointer: "/settings", Source: "theme = dark"}); h.Source != "theme = dark" {
		t.Errorf("non-mcp unparseable side changed: %q", h.Source)
	}
	if h := Hunk(model.Hunk{Pointer: "/mcpServers/x", Source: `{"command":"npx","args":["-y","pkg","--port","8080"]}`}); !strings.Contains(h.Source, "npx") || !strings.Contains(h.Source, `"pkg"`) {
		t.Errorf("args readability lost: %q", h.Source)
	}
}

func TestOneLineAndFirstLine(t *testing.T) {
	if got := OneLine("a\x1b[31m\tb\nc"); got != "a b c" {
		t.Errorf("OneLine = %q", got)
	}
	cases := map[string]string{"": "", "\n \n": "", "\n  first\x07 line \nsecond": "first line", "x\ty": "x y"}
	for in, want := range cases {
		if got := FirstLine(in); got != want {
			t.Errorf("FirstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// FirstLine is how error text and stderr become status notes: it must mask
// as well as sanitise, so a secret on the first line of an error never
// reaches the status line.
func TestFirstLineMasks(t *testing.T) {
	in := "\n token=CANARYfl0123456789abcdefghij \nghp_CANARYfl2"
	got := FirstLine(in)
	if strings.Contains(got, "CANARY") || got != "token="+Mask {
		t.Errorf("FirstLine = %q", got)
	}
	if got := FirstLine("ghp_CANARYfl3 first"); strings.Contains(got, "CANARY") {
		t.Errorf("token shape leaked: %q", got)
	}
}

func TestCapped(t *testing.T) {
	if got, tr := Capped("short", 100); got != "short" || tr {
		t.Errorf("no cap: %q %v", got, tr)
	}
	// Masking happens before the cut: a secret straddling the cap leaves no prefix.
	secret := "password=CANARYcap0123456789abcdefghijklmnop"
	got, tr := Capped("a\n"+secret, 12)
	if !tr || strings.Contains(got, "CANARY") || got != "a" {
		t.Errorf("cut before masking or wrong boundary: %q %v", got, tr)
	}
	// With a newline inside the budget the cut lands on it.
	got, tr = Capped("line1\nline2\nline3", 9)
	if !tr || got != "line1" {
		t.Errorf("line boundary: %q %v", got, tr)
	}
	// Without one the cut is rune-safe.
	got, tr = Capped("ééééé", 5)
	if !tr || !utf8.ValidString(got) || got != "éé" {
		t.Errorf("rune boundary: %q %v", got, tr)
	}
	if got, tr := Capped("abc", 0); got != "abc" || tr {
		t.Errorf("zero cap must mean no cap: %q %v", got, tr)
	}
}

// An exact-boundary cut keeps the last whole rune; a cut inside a rune drops
// only that rune, whatever its width.
func TestCappedExactRuneBoundary(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"ééx", 4, "éé"}, // the cut lands exactly after the second é
		{"ééx", 3, "é"},  // inside the second é
		{"a😀b", 5, "a😀"}, // exactly after a 4-byte rune
		{"a😀b", 4, "a"},  // inside it
		{"abcdef", 3, "abc"},
	} {
		got, tr := Capped(c.in, c.n)
		if !tr || got != c.want {
			t.Errorf("Capped(%q, %d) = %q, %v; want %q", c.in, c.n, got, tr, c.want)
		}
	}
}

// A side made of many concatenated JSON values is masked in linear time:
// every value is decoded by one decoder, not by a fresh decoder (and a fresh
// Sanitize) per remaining value.
func TestHunkManyValuesBoundedTime(t *testing.T) {
	side := strings.Repeat("1 ", 20000) // 40 KB
	start := time.Now()
	h := Hunk(model.Hunk{Pointer: "/x", Source: side, Dest: side})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Hunk took %v on 40 KB of values", d)
	}
	if n := strings.Count(h.Source, "\n"); n != 19999 {
		t.Errorf("want one value per line, got %d line breaks", n)
	}
}

// Every value of a multi-document side is walked, and a trailing remainder
// that is not JSON is scrubbed, not dropped.
func TestHunkMultiValueSides(t *testing.T) {
	src := `{"token":"CANARYm1"} {"a":1} not json token=CANARYm2`
	h := Hunk(model.Hunk{Pointer: "/x", Source: src})
	if strings.Contains(h.Source, "CANARY") {
		t.Fatalf("leak: %q", h.Source)
	}
	for _, want := range []string{"\"token\": \"••••\"", "\"a\": 1", "not json token=••••"} {
		if !strings.Contains(h.Source, want) {
			t.Errorf("missing %q in %q", want, h.Source)
		}
	}
	// Under an mcpServers pointer the unparseable remainder is masked whole.
	h = Hunk(model.Hunk{Pointer: "/mcpServers/s", Source: `{"a":1} CANARYm3 junk`})
	if strings.Contains(h.Source, "CANARY") || strings.Contains(h.Source, "junk") {
		t.Errorf("remainder not masked whole: %q", h.Source)
	}
}
