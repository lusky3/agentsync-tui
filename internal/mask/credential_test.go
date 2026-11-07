package mask

import "testing"

// TestCredentialHardKinds: every R5 hard credential kind is found and named.
func TestCredentialHardKinds(t *testing.T) {
	cases := []struct{ in, kind string }{
		{"add ghp_abcdefghijklmnop", "token-shape"},
		{"github_pat_11ABCDEFG0abcdefghij", "token-shape"},
		{"gg_pat_abcdefghijklmnop", "token-shape"},
		{"key sk-abcdefghijklmnop", "token-shape"},
		{"slack xoxb-1234-abcdef", "token-shape"},
		{"use xoxs-12345678901234", "token-shape"},
		{"use xoxr-12345678901234", "token-shape"},
		{"use xoxe-12345678901234", "token-shape"},
		{"aws AKIAABCDEFGHIJKLMNOP", "token-shape"},
		{"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig", "token-shape"},
		// A token shape inside a reference's name is still token text.
		{"${env:ghp_abcdefghijklmnop}", "token-shape"},
		{"Authorization: Basic dXNlcjpwYXNz", "auth-header"},
		{"Proxy-Authorization: Digest abc", "auth-header"},
		{"curl -H 'Bearer hunter2'", "auth-header"},
		{"x-api-key: hunter2", "auth-header"},
		{"Cookie: session=abc", "auth-header"},
		{"clone https://user:hunter2@example.com/r.git", "url-userinfo"},
		{"https://tok@github.com/o/r", "url-userinfo"},
		{"-----BEGIN OPENSSH PRIVATE KEY-----", "private-key"},
		{"-----BEGIN RSA PRIVATE KEY-----", "private-key"},
		{"-----BEGIN PRIVATE KEY-----", "private-key"},
		{"AGE-SECRET-KEY-1QQQQQQQQ", "age-identity"},
		{"password=hunter2", "secret-assignment"},
		{`api_key: "abc"`, "secret-assignment"},
		{"GITHUB_PAT=abc", "secret-assignment"},
	}
	for _, c := range cases {
		kind, ok := Credential(c.in)
		if !ok || kind != c.kind {
			t.Errorf("Credential(%q) = %q, %v; want %q, true", c.in, kind, ok, c.kind)
		}
	}
}

// TestCredentialIgnoresReferences: references, plain URLs with query strings
// and ordinary prose are not credentials.
func TestCredentialIgnoresReferences(t *testing.T) {
	for _, in := range []string{
		"Bearer ${secret:x}",
		"Authorization: Bearer ${secret:x}",
		"${env:T}",
		"token=${env:T}",
		"x-api-key: ${secret:K}",
		"https://example.com/search?q=go&page=2",
		"see https://api.example.com/v1/items?limit=10#top",
		"feat(mcp): add the brave server",
		"fix: refresh the token cache",
		"",
	} {
		if kind, ok := Credential(in); ok {
			t.Errorf("Credential(%q) = %q, true; want false", in, kind)
		}
	}
}

// TestCredentialSanitisesFirst: a control character cannot split a token
// past the rules.
func TestCredentialSanitisesFirst(t *testing.T) {
	if _, ok := Credential("gh\x1b[0mp_abcdefghijk"); !ok {
		t.Error("an escape sequence split a token past Credential")
	}
}

// TestShellAndTemplateRefsAreRefs: an unbraced upper-case shell variable
// ($NAME) and a backtick-quoted template reference are references in the
// header and value rules, so they are neither a finding nor masked.
func TestShellAndTemplateRefsAreRefs(t *testing.T) {
	for _, in := range []string{
		"Authorization: `Bearer ${token}`",
		"Authorization: Bearer $MEM0_API_KEY",
		"curl -H 'Authorization: Bearer $GITHUB_TOKEN'",
		"x-api-key: `${secret:K}`",
		"Bearer `${API_KEY}`",
		"API_KEY=$MEM0_API_KEY",
		"password: $DB_PASSWORD",
	} {
		if kind, ok := Credential(in); ok {
			t.Errorf("Credential(%q) = %q, true; want false", in, kind)
		}
		if got := Text(in); got != in {
			t.Errorf("Text(%q) = %q, want unchanged", in, got)
		}
	}
	// A literal next to a shell variable, or a lower-case $word, is still
	// a literal.
	for _, in := range []string{
		"Authorization: `Bearer abc123`",
		"Authorization: Bearer $MEM0_API_KEY-literal",
		"password: $ecret99",
	} {
		if _, ok := Credential(in); !ok {
			t.Errorf("Credential(%q) not found", in)
		}
	}
}

// TestValueKeepsShellRefs: Value uses the same reference rule as Text, so
// a header value that is only a shell variable or a template stays visible.
func TestValueKeepsShellRefs(t *testing.T) {
	for _, in := range []string{"Bearer $GH_TOKEN", "$GH_TOKEN", "`${GH_TOKEN}`", "Bearer `${X}`"} {
		if got := Value(in); got != in {
			t.Errorf("Value(%q) = %q, want unchanged", in, got)
		}
	}
	for _, in := range []string{"Bearer $GH_TOKEN-lit", "$lower", "x $GH_TOKEN"} {
		if got := Value(in); got != Mask {
			t.Errorf("Value(%q) = %q, want masked", in, got)
		}
	}
}
