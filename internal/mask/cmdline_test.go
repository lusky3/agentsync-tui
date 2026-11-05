package mask

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCommandLine(t *testing.T) {
	c := "plainCANARY"
	tests := []struct{ in, want []string }{
		{[]string{"npx", "-y", "@upstash/context7-mcp", "--port", "8080", "-p", "3000"}, []string{"npx", "-y", "@upstash/context7-mcp", "--port", "8080", "-p", "3000"}},
		{[]string{"-e", "DB_PASS=" + c}, []string{"-e", "DB_PASS=" + Mask}},
		{[]string{"-e", "API_KEY=${env:A}"}, []string{"-e", "API_KEY=${env:A}"}},
		{[]string{"--mode=" + c}, []string{"--mode=" + Mask}},
		{[]string{"--pat", c, "--path", "/x"}, []string{"--pat", Mask, "--path", "/x"}},
		{[]string{"--private-key", c}, []string{"--private-key", Mask}},
		{[]string{"--token", "--verbose"}, []string{"--token", "--verbose"}},
		{[]string{"-H", "X-Custom: " + c}, []string{"-H", "X-Custom: " + Mask}},
	}
	for _, tt := range tests {
		if got := CommandLine(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("CommandLine(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFreeText(t *testing.T) {
	for _, in := range []string{
		"mytool --password plainCANARY", "mytool --api-key=plainCANARY", `mytool --password "plainCANARY`,
		`sh -c 'curl -H "Authorization: Bearer plainCANARY"'`, `x --token plainCANARY\`,
	} {
		if got := FreeText(in); strings.Contains(got, "CANARY") {
			t.Errorf("FreeText(%q) = %q", in, got)
		}
	}
	if got := FreeText(`npx -y "@scope/pkg"`); got != `npx -y "@scope/pkg"` {
		t.Errorf("benign input not echoed as typed: %q", got)
	}
}

func TestFreeTextRescansNestedStrings(t *testing.T) {
	for _, in := range []string{
		`sh -c 'mytool --password plainCANARY'`,
		`sh -c 'mytool --api-key=plainCANARY'`,
		`bash -c "DB_PASS=plainCANARY mytool"`,
		`sh -c "sh -c 'mytool --password plainCANARY'"`,
		`sh -c 'curl -H "Authorization: Bearer plainCANARY"'`,
	} {
		for name, f := range map[string]func(string) string{"FreeText": FreeText, "FreeTextLive": FreeTextLive} {
			if got := f(in); strings.Contains(got, "CANARY") {
				t.Errorf("%s(%q) = %q", name, in, got)
			}
		}
	}
	for _, w := range [][]string{{"sh", "-c", "mytool --password plainCANARY"}, {"--password plainCANARY"}} {
		if got := strings.Join(CommandLine(w), " "); strings.Contains(got, "CANARY") {
			t.Errorf("CommandLine(%q) = %q", w, got)
		}
	}
	if got := FreeText(`sh -c 'mytool --token ${env:T}'`); !strings.Contains(got, "--token ${env:T}") {
		t.Errorf("nested ref hidden: %q", got)
	}
	for _, in := range []string{`npx -y @upstash/context7-mcp --port 8080`, `sh -c 'echo hello world'`} {
		if got := FreeText(in); got != in {
			t.Errorf("FreeText(%q) = %q, want unchanged", in, got)
		}
	}
}

// TestCommandLineMasksWholeValueAfterEquals: for a NAME=value or
// --flag=value word, everything after the first '=' is the value, spaces
// included. A word is only re-split when the name itself holds whitespace
// (a nested script) or there is no '='.
func TestCommandLineMasksWholeValueAfterEquals(t *testing.T) {
	c := "plainCANARY"
	tests := []struct{ in, want []string }{
		{[]string{"-e", "DB_PASS=correct horse " + c}, []string{"-e", "DB_PASS=" + Mask}},
		{[]string{"--header=X-Api-Key: " + c}, []string{"--header=" + Mask}},
		{[]string{"--password=correct " + c}, []string{"--password=" + Mask}},
		{[]string{"--header=Authorization: Bearer " + c}, []string{"--header=" + Mask}},
		{[]string{"-e", "DB_PASS=" + c + " trailing words"}, []string{"-e", "DB_PASS=" + Mask}},
		{[]string{"-e", "A=x ${env:B}"}, []string{"-e", "A=" + Mask}}, // literal mixed with a ref
		{[]string{"--note=hello world"}, []string{"--note=" + Mask}},  // over-masked by the ruling
		{[]string{"-e", "A=${env:B} ${env:C}"}, []string{"-e", "A=${env:B} ${env:C}"}},
		{[]string{"-e", "A=Bearer ${env:B}"}, []string{"-e", "A=Bearer ${env:B}"}},
		{[]string{"--header=Authorization: Bearer ${env:T}"}, []string{"--header=Authorization: Bearer ${env:T}"}},
		{[]string{"--header=X-Secret: lit ${env:T}"}, []string{"--header=" + Mask}},
		{[]string{"--header=ghp_" + c + ": Bearer ${env:T}"}, []string{"--header=" + Mask + " Bearer ${env:T}"}}, // token-shaped "name"
		// A nested script is still re-split: '=' belongs to an inner word.
		{[]string{"mytool --api-key=" + c + " --port 8080"}, []string{"mytool --api-key=" + Mask + " --port 8080"}},
		{[]string{"export DB_PASS='a " + c + "' && run"}, []string{"export DB_PASS=" + Mask + " && run"}},
		{[]string{"mytool --note='a=b' --password " + c}, []string{"mytool --note=" + Mask + " --password " + Mask}},
		// Leading assignment: the rest of the script is hidden with the value (fail-safe).
		{[]string{"DB_PASS=" + c + " mytool"}, []string{"DB_PASS=" + Mask}},
	}
	for _, tt := range tests {
		if got := CommandLine(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("CommandLine(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	free := []struct{ in, want string }{
		{`env "DB_PASS=correct horse ` + c + `" mytool`, "env DB_PASS=" + Mask + " mytool"},
		{`mytool --password="correct ` + c + `"`, "mytool --password=" + Mask},
		{`mytool --header="X-Api-Key: ` + c + `"`, "mytool --header=" + Mask},
		{`mytool --password "correct horse ` + c + `"`, "mytool --password " + Mask},
		{`sh -c "export DB_PASS='a b' && run"`, "sh -c 'export DB_PASS=" + Mask + " && run'"},
		{`sh -c 'mytool --header="Authorization: Bearer ${env:T}"'`, `sh -c 'mytool --header="Authorization: Bearer ${env:T}"'`}, // nothing to mask: as typed
		{`sh -c 'A=1 mytool --password ` + c + `'`, "sh -c A=" + Mask},
		{`sh -c "DB_PASS=` + c + ` mytool"`, "sh -c DB_PASS=" + Mask},
	}
	for _, tt := range free {
		got := FreeText(tt.in)
		if got != tt.want {
			t.Errorf("FreeText(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if again := FreeText(got); again != got {
			t.Errorf("FreeText not idempotent: %q -> %q", got, again)
		}
	}
	for _, in := range []string{
		`mytool --password="correct ` + c, `mytool --header="X-Api-Key: ` + c, `-e "DB_PASS=correct horse ` + c,
		`env "DB_PASS=correct horse ` + c + `" mytool`, `mytool --password="correct ` + c + `"`,
	} {
		if got := FreeTextLive(in); strings.Contains(got, "CANARY") {
			t.Errorf("FreeTextLive(%q) = %q", in, got)
		}
	}
}

func TestFreeTextUnparseableFailsSafe(t *testing.T) {
	for _, in := range []string{`mytool --password "plainCANARY`, `"--password plainCANARY`, `sh -c "mytool --password plainCANARY`, `harmless "open`} {
		if got := FreeText(in); got != Mask {
			t.Errorf("FreeText(%q) = %q, want %q", in, got, Mask)
		}
	}
}

func TestFreeTextLiveMasksQuoteOpenedBeforeFlag(t *testing.T) {
	for _, in := range []string{`"--password plainCANARY`, `sh -c "mytool --password plainCANARY`, `sh -c 'mytool --api-key=plainCANARY`} {
		if got := FreeTextLive(in); strings.Contains(got, "CANARY") {
			t.Errorf("FreeTextLive(%q) = %q", in, got)
		}
	}
}

func TestFreeTextPathologicalInputIsBounded(t *testing.T) {
	// Deeply nested quoting and a long input must finish quickly and fail safe.
	deep := "mytool --password plainCANARY"
	for i := 0; i < 10; i++ {
		deep = `sh -c "` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(deep) + `"`
	}
	if got := FreeText(deep); !strings.Contains(got, Mask) || strings.Contains(got, "mytool") {
		t.Errorf("script nested beyond maxNest not masked whole: %q", got)
	}
	long := strings.Repeat(`a "b c" 'd e' `, 1100) + "--password plainCANARY" // just under maxFreeText
	if FreeText(strings.Repeat("x ", maxFreeText)) != Mask {
		t.Error("input over maxFreeText not masked whole")
	}
	start := time.Now()
	for _, in := range []string{deep, long} {
		for _, f := range []func(string) string{FreeText, FreeTextLive} {
			if got := f(in); strings.Contains(got, "CANARY") {
				t.Errorf("leak in pathological input (len %d)", len(in))
			}
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}

// TestCommandLineCoversOldSecretFlags: everything mask.Args' secretFlagRe
// masked (f7c7501) must still be masked by CommandLine.
func TestCommandLineCoversOldSecretFlags(t *testing.T) {
	names := []string{
		"accessKey", "privateKey", "authkey", "licenseKey", "apiKey", "api_key", "API-KEY", "clientSecret",
		"client-secret", "access-token", "accessToken", "db-password", "dbPassword", "signingKey", "sshKey",
		"passwd", "pwd", "pat", "bearer", "credentials", "secretKey", "token", "auth", "authorization", "key",
		"header", "h", "H", "github-pat", "githubPat", "author",
	}
	for _, n := range names {
		for _, flag := range []string{"--" + n, "-" + n} {
			got := CommandLine([]string{flag, "plainCANARY"})
			if strings.Contains(got[1], "CANARY") {
				old := secretFlagRe.MatchString(flag)
				t.Errorf("CommandLine(%q, value) leaked (old secretFlagRe matched: %v)", flag, old)
			}
		}
	}
	// Generic flags stay readable.
	for _, n := range []string{"-y", "-p", "--port", "--path", "-v"} {
		if got := CommandLine([]string{n, "8080"}); got[1] != "8080" {
			t.Errorf("%s value masked: %q", n, got)
		}
	}
}

// A word split only by newlines is a nested script too: the value after a
// secret flag on its own line must not survive.
func TestCommandLineNewlineSplitScript(t *testing.T) {
	got := CommandLine([]string{"-c", "mytool\n--password\nCANARYx1\nX=1"})
	for _, w := range got {
		if strings.Contains(w, "CANARYx1") {
			t.Errorf("leaked: %q", got)
		}
	}
	// shellwords rejects a bare newline, so the script is masked whole
	// (fail-safe); the first word keeps its shape.
	if len(got) != 2 || got[0] != "-c" || got[1] != Mask {
		t.Errorf("shape: %q", got)
	}
}
