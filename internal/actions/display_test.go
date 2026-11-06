package actions

import (
	"strings"
	"testing"
)

const (
	dCanary  = "CANARYc4n4ry0123456789abcdef"
	dCanary2 = "CANARYtwo"
	dShort   = "CANARYs"
)

// TestMcpAddDisplayMasksLiterals: every literal typed into the add form must
// be masked in Display() (confirm modal + activity log), refs stay visible.
func TestMcpAddDisplayMasksLiterals(t *testing.T) {
	tests := []struct {
		name string
		spec AddSpec
		keep []string // must stay visible in Display()
	}{
		{"env DB_PASS", AddSpec{Env: map[string]string{"DB_PASS": dCanary}}, []string{"DB_PASS="}},
		{"env short secret after harmless", AddSpec{Env: map[string]string{"DEBUG": "1", "SECRET_TOKEN": dShort}}, []string{"SECRET_TOKEN="}},
		{"env harmless first then MY_PAT", AddSpec{Env: map[string]string{"AAA": "plain", "MY_PAT": dCanary}}, []string{"MY_PAT="}},
		{"env ref visible", AddSpec{Env: map[string]string{"API_KEY": "${env:A}"}}, []string{"API_KEY=${env:A}"}},
		{"args --api-key", AddSpec{Args: []string{"-y", "pkg", "--api-key", dCanary}}, []string{"-y", "pkg", "--api-key"}},
		{"args --token=", AddSpec{Args: []string{"--token=" + dCanary}}, []string{"--token="}},
		{"args header bearer", AddSpec{Args: []string{"--header", "Authorization: Bearer " + dCanary}}, []string{"--header"}},
		{"header literal", AddSpec{Headers: []string{"X-Custom: " + dCanary}}, []string{"X-Custom: "}},
		{"header bearer ref", AddSpec{Headers: []string{"Authorization: Bearer ${env:T}"}}, []string{"Authorization: Bearer ${env:T}"}},
		{"command literal token", AddSpec{Command: "gg_pat_" + dCanary}, nil},
		// Round 2: free-form args/command.
		{"args -e DB_PASS=", AddSpec{Args: []string{"-e", "DB_PASS=" + dCanary}}, []string{"-e", "DB_PASS="}},
		{"args -e MY_PAT=", AddSpec{Args: []string{"-e", "MY_PAT=" + dCanary}}, []string{"MY_PAT="}},
		{"args --pat next word", AddSpec{Args: []string{"--pat", dCanary}}, []string{"--pat"}},
		{"args --api-key=", AddSpec{Args: []string{"--api-key=" + dCanary}}, []string{"--api-key="}},
		{"args --mode= (fail-safe)", AddSpec{Args: []string{"--mode=" + dCanary}}, []string{"--mode="}},
		{"args env ref visible", AddSpec{Args: []string{"-e", "API_KEY=${env:A}"}}, []string{"API_KEY=${env:A}"}},
		{"command --password", AddSpec{Command: "mytool --password " + dCanary}, []string{"mytool", "--password"}},
		{"command --api-key=", AddSpec{Command: "mytool --api-key=" + dCanary}, []string{"mytool", "--api-key="}},
		{"command sh -c password", AddSpec{Command: "sh -c 'mytool --password " + dCanary + "'"}, []string{"sh", "-c", "mytool", "--password"}},
		{"command sh -c api-key=", AddSpec{Command: "sh -c 'mytool --api-key=" + dShort + "'"}, []string{"--api-key="}},
		{"command bash -c env", AddSpec{Command: `bash -c "DB_PASS=` + dCanary + ` mytool"`}, []string{"DB_PASS="}},
		{"command nested twice", AddSpec{Command: `sh -c "sh -c 'mytool --password ` + dCanary + `'"`}, nil},
		{"command unparseable", AddSpec{Command: `mytool --password "` + dCanary}, nil},
		{"command nested ref", AddSpec{Command: "sh -c 'mytool --token ${env:T}'"}, []string{"${env:T}"}},
		{"args camelCase key", AddSpec{Args: []string{"--accessKey", dCanary, "--privateKey", dCanary}}, []string{"--accessKey"}},
		{"command sh -c bearer", AddSpec{Command: `sh -c 'curl -H "Authorization: Bearer ` + dCanary + `"'`}, []string{"sh", "-c"}},
		// Round 4: a NAME=value / --flag=value word whose value holds spaces is masked whole.
		{"command env quoted assignment", AddSpec{Command: `env "DB_PASS=correct horse ` + dCanary + `" mytool`}, []string{"env", "DB_PASS=", "mytool"}},
		{"command --password= with space", AddSpec{Command: `mytool --password="correct ` + dCanary + `"`}, []string{"mytool", "--password="}},
		{"command --header= with space", AddSpec{Command: `mytool --header="X-Api-Key: ` + dCanary + `"`}, []string{"mytool", "--header="}},
		{"command sh -c export quoted", AddSpec{Command: `sh -c "export DB_PASS='a ` + dCanary + `' && run"`}, []string{"export", "DB_PASS=", "run"}},
		{"command --header= bearer ref", AddSpec{Command: `mytool --header="Authorization: Bearer ${env:T}"`}, []string{"--header=", "${env:T}"}},
		{"args -e DB_PASS= with spaces", AddSpec{Args: []string{"-e", "DB_PASS=correct horse " + dCanary}}, []string{"-e", "DB_PASS="}},
		{"args --header= with space", AddSpec{Args: []string{"--header=X-Api-Key: " + dCanary}}, []string{"--header="}},
		{"args --header= bearer ref", AddSpec{Args: []string{"--header=Authorization: Bearer ${env:T}"}}, []string{"--header=", "${env:T}"}},
		{"args -e literal plus ref", AddSpec{Args: []string{"-e", "A=" + dShort + " ${env:B}"}}, []string{"-e", "A="}},
		{"args --note= over-masked", AddSpec{Args: []string{"--note=hello " + dCanary}}, []string{"--note="}},
	}
	for _, tt := range tests {
		checkAddDisplay(t, tt.name, tt.spec, tt.keep)
	}
	a, err := McpAdd(AddSpec{ID: "r", Type: "http", URL: "https://user:" + dCanary + "@host/mcp?token=" + dCanary2})
	if err != nil {
		t.Fatal(err)
	}
	if d := a.Display(); strings.Contains(d, "CANARY") || !strings.Contains(d, "host/mcp") {
		t.Errorf("url: %s", d)
	}
}

// checkAddDisplay builds `mcp add` for spec as a stdio server "srv" (npx
// when no command is given) and checks its Display and Title leak no
// canary while every string in keep stays visible.
func checkAddDisplay(t *testing.T, name string, spec AddSpec, keep []string) {
	t.Helper()
	s := spec
	s.ID, s.Type = "srv", "stdio"
	if s.Command == "" {
		s.Command = "npx"
	}
	a, err := McpAdd(s)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	d := a.Display()
	if strings.Contains(d, "CANARY") || strings.Contains(a.Title, "CANARY") {
		t.Errorf("%s: leaked: %s", name, d)
	}
	for _, k := range keep {
		if !strings.Contains(d, k) {
			t.Errorf("%s: %q not visible in %s", name, k, d)
		}
	}
}

// TestMcpAddDisplayDoesNotChangeArgv: masking is display-only.
func TestMcpAddDisplayDoesNotChangeArgv(t *testing.T) {
	a, err := McpAdd(AddSpec{ID: "s", Type: "stdio", Command: "npx", Env: map[string]string{"DB_PASS": dCanary}})
	if err != nil {
		t.Fatal(err)
	}
	if argAfter(t, a.Argv, "--env") != "DB_PASS="+dCanary {
		t.Errorf("argv changed: %q", a.Argv)
	}
}

// TestMcpAddDisplayKeepsBenignArgsReadable: generic flags keep their values.
func TestMcpAddDisplayKeepsBenignArgsReadable(t *testing.T) {
	a, err := McpAdd(AddSpec{ID: "s", Type: "stdio", Command: "npx", Args: []string{"-y", "@upstash/context7-mcp", "--port", "8080", "-p", "3000"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := a.Display(); !strings.Contains(d, "--args -y,@upstash/context7-mcp,--port,8080,-p,3000") {
		t.Errorf("benign args masked: %s", d)
	}
}

// TestMcpAddCommandArgvIsNotMasked pins what runs: the real command reaches
// Argv; only Display() is masked.
func TestMcpAddCommandArgvIsNotMasked(t *testing.T) {
	cmd := "sh -c 'mytool --password " + dCanary + "'"
	a, err := McpAdd(AddSpec{ID: "s", Type: "stdio", Command: cmd, Args: []string{"--token", dCanary}})
	if err != nil {
		t.Fatal(err)
	}
	if got := argAfter(t, a.Argv, "--command"); got != cmd {
		t.Errorf("argv command changed: %q", got)
	}
	if got := argAfter(t, a.Argv, "--args"); got != "--token,"+dCanary {
		t.Errorf("argv args changed: %q", got)
	}
	if strings.Contains(a.Display(), "CANARY") {
		t.Errorf("display leaks: %s", a.Display())
	}
}

// A vault key that is shaped like a value (no known token prefix, so Text
// leaves it alone) is masked in the display of secret set and remove, and
// the Warning never carries it.
func TestSecretActionsMaskValueShapedKeys(t *testing.T) {
	const key = "CANARYx1f3a9c2d7e8b1"
	set, err := SecretSet(key)
	if err != nil {
		t.Fatal(err)
	}
	rm, err := SecretRemove(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []Action{set, rm} {
		if a.Argv[2] != key {
			t.Errorf("argv %q", a.Argv)
		}
		if strings.Contains(a.Display(), "CANARY") || strings.Contains(a.Warning, "CANARY") {
			t.Errorf("display %q warning %q", a.Display(), a.Warning)
		}
	}
	plain, _ := SecretSet("GITHUB_TOKEN")
	if plain.Display() != "agentsync secret set GITHUB_TOKEN" {
		t.Errorf("plain name changed: %q", plain.Display())
	}
}
