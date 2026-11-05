package mask

import (
	"strings"
	"testing"
)

// Credentials carried by a URL are masked wherever Text runs: userinfo,
// every query value (the keys stay), fragment values, and path segments that
// look like tokens. Hosts, ordinary path segments and query keys stay.
func TestURLMasksCredentialBearingParts(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"https://user:CANARYu1@host/x", "https://••••@host/x"},
		{"https://mcp.example.com/sse?session=CANARYq1", "https://mcp.example.com/sse?session=••••"},
		{"https://h/x?a=1&b=two#frag=CANARYf1", "https://h/x?a=••••&b=••••#frag=••••"},
		{"https://h/x?flag", "https://h/x?flag"},
		{"https://mcp.zapier.com/api/mcp/s/CANARYx1f3a9c2d7e8b1x1f3a9/mcp", "https://mcp.zapier.com/api/mcp/s/••••/mcp"},
		{"https://hooks.example/services/T0A1B2C3D4E5F6/B0A1B2C3D4E5F6G7/CANARYw1abcdefghij0123456789", "https://hooks.example/services/••••/••••/••••"},
		{"https://github.com/lusky3/agentsync-tui/releases/download/v0.1.0/agentsync-tui_0.1.0_linux_amd64.tar.gz",
			"https://github.com/lusky3/agentsync-tui/releases/download/v0.1.0/agentsync-tui_0.1.0_linux_amd64.tar.gz"},
		{"https://api.example.com/v1/users/me", "https://api.example.com/v1/users/me"},
		{"http://localhost:8080/mcp", "http://localhost:8080/mcp"},
		{"https://${U}:${P}@host/x?token=${secret:T}", "https://${U}:${P}@host/x?token=${secret:T}"},
		{"https://h/x?k=${env:A}&other=lit", "https://h/x?k=${env:A}&other=••••"},
		{"see https://h/p?s=CANARYs1, then https://h/q#CANARYg1h2i3j4k5l6m7n8o9p0.", "see https://h/p?s=••••, then https://h/q#••••."},
		{"ssh://git@github.com/x/y.git", "ssh://••••@github.com/x/y.git"},
		// a bare token-shaped query (no key) is masked like a bare fragment
		{"https://h/x?CANARYbare1234567890abcd", "https://h/x?••••"},
		{"https://h/x?CANARYbare1234567890abcd&k=v", "https://h/x?••••&k=••••"},
		{"https://h/x?flag&other", "https://h/x?flag&other"},
	} {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// TokenShaped is the heuristic the secrets key field and the URL path rule
// share: it must refuse common secret shapes and accept ordinary names.
func TestTokenShaped(t *testing.T) {
	for _, s := range []string{
		"CANARYx1f3a9c2d7e8b1",                 // 20 mixed chars, no word structure
		"a1b2c3d4e5f60718293a4b5c6d7e8f90",     // 32 hex
		"CANARYx1-AbCdEf_GhIjKlMnOp",           // camelCase words with a digit
		"550e8400-e29b-41d4-a716-446655440000", // uuid
		"ya29.a0AfH6SMBx2CANARY9q8d7ag",        // oauth-style with a dot
		"Zm9vYmFyYmF6cXV4MTIzNA",               // base64 without digits in words? (mixed case, long)
		"xoxb-not-a-shape-but-9Q8d7Ag6F5e4D3c2B1a0",
		"T0A1B2C3D4E5F6", // 14 chars alternating letter/digit
		"CANARYbare1234567890abcd",
		"x1f3a9c2d7e8b1",
	} {
		if !TokenShaped(s) {
			t.Errorf("TokenShaped(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"GITHUB_TOKEN", "OPENAI_API_KEY", "db.password", "slack-bot-token",
		"AWS_ACCESS_KEY_ID", "KEY2", "s3_bucket_2024", "OAUTH2_CLIENT_ID", "GPT4_KEY",
		"my.key.v2", "api", "mcp", "v1", "users", "sse", "index.html", "2024-01-01",
		"agentsync-tui_0.1.0_linux_amd64.tar.gz", "releases", "download", "v0.1.0",
		"Slack-Bot-Token-2024", "ANTHROPIC_API_KEY_PROD", "", "a",
		// CamelCase names with a year or version are names, not tokens
		"MyServiceApiKey2024Prod", "S3Bucket2024Backup", "myApiKey2024ProdBackup", "OpenAIKey2", "GitHubPat2025",
	} {
		if TokenShaped(s) {
			t.Errorf("TokenShaped(%q) = true, want false", s)
		}
	}
}

// Cookie headers, and auth/pat as whole-word keys, are masked in free text
// and as flag names.
func TestCookieAndPatAuthKeys(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Cookie: session=CANARYc1; other=CANARYc2", "Cookie: ••••"},
		{"set-cookie: id=CANARYc3; Path=/", "set-cookie: ••••"},
		{`"Cookie": "a=CANARYc4"`, `"Cookie": "••••"`},
		{"AUTH=CANARYa1", "AUTH=••••"},
		{"github_pat=CANARYp1", "github_pat=••••"},
		{"PAT: CANARYp2", "PAT: ••••"},
		{"auth: ${secret:A}", "auth: ${secret:A}"},
		{"compat=true", "compat=true"}, // not a whole word
		{"oauth=flow", "oauth=flow"},   // not a whole word
		{"format=json", "format=json"}, // "pat" is not a suffix here
		{"path=/tmp/x", "path=/tmp/x"}, // "pat" is not a whole word
	} {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	got := Args([]string{"--pat", "CANARYp3", "--auth", "CANARYa2", "--path", "/tmp/x", "-p", "8080"})
	if strings.Contains(strings.Join(got, " "), "CANARY") || got[5] != "/tmp/x" || got[7] != "8080" {
		t.Errorf("Args = %q", got)
	}
}
