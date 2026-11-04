package mask

import (
	"strings"
	"testing"
)

// Secret-named key/value shapes in free text must lose the value but keep
// the key and structure.
func TestTextSecretKeyValueShapes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"password: CANARYp1", "password: " + Mask},
		{"Password:   CANARYp1 and more", "Password:   " + Mask + " and more"},
		{"secret: CANARYs1", "secret: " + Mask},
		{"api_key: CANARYk1", "api_key: " + Mask},
		{"apiKey: CANARYk1", "apiKey: " + Mask},
		{"API-KEY: CANARYk1", "API-KEY: " + Mask},
		{"client_secret: CANARYs2", "client_secret: " + Mask},
		{"token=CANARYx1", "token=" + Mask},
		{"wrote /x/.claude.json (token=CANARYx1)", "wrote /x/.claude.json (token=" + Mask + ")"},
		{"GITHUB_TOKEN=CANARYg1 next", "GITHUB_TOKEN=" + Mask + " next"},
		{`{"token": "CANARYj4"}`, `{"token": "` + Mask + `"}`},
		{`"API_KEY": "CANARYj5",`, `"API_KEY": "` + Mask + `",`},
		{`{"password":"CANARYj6","user":"bob"}`, `{"password":"` + Mask + `","user":"bob"}`},
		{`password: "CANARYy1 with space"`, `password: "` + Mask + `"`},
		{`secret: 'CANARYy2'`, `secret: '` + Mask + `'`},
		{`"apiKey": "a\"CANARYj7"`, `"apiKey": "` + Mask + `"`},
		{"X-Goog-Api-Key: CANARYh1", "X-Goog-Api-Key: " + Mask},
	}
	for _, c := range cases {
		got := Text(c.in)
		if got != c.want {
			t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "CANARY") {
			t.Errorf("Text(%q) leaked: %q", c.in, got)
		}
		if again := Text(got); again != got {
			t.Errorf("Text not idempotent on %q: %q -> %q", c.in, got, again)
		}
	}
}

// References stay visible, and ordinary text, paths and non-secret words are
// untouched. Decisions: a secret word needs a following ':' or '=' with a
// value; plurals ("passwords: 3") and prose ("token expired") are left alone.
func TestTextSecretKeyValueUnchanged(t *testing.T) {
	for _, in := range []string{
		"password: ${env:P}",
		"password: ${secret:DB}",
		`"token": "${GITHUB_TOKEN}"`,
		"API_KEY=${env:K}",
		"X-Goog-Api-Key: ${GOOGLE_KEY}",
		"→ write  /home/user/a=b/mcp.json",
		"/home/it's/a=b/mcp.json",
		"the password was rejected",
		"enter your password",
		"token expired",
		"passwords: 3 entries",
		"tokens: 3",
		"mode=fast",
		"--port=8080",
		"key=value",
		"password:",
		"password: \nnext line",
		`"token": ""`,
		"Plan: 1 ops total across 1 agent(s)",
		"https://example.com/path?mode=fast",
	} {
		if got := Text(in); got != in {
			t.Errorf("Text(%q) = %q, want unchanged", in, got)
		}
	}
}
