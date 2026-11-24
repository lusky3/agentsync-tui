package actions

import (
	"testing"

	"github.com/lusky3/agentsync-tui/internal/source"
	"github.com/lusky3/agentsync-tui/internal/testenv"
)

const (
	exampleMCPURL = "https://mcp.example.com/mcp"
	kindHTTP      = "http"
)

// McpAdd passes every header as its own --header token, commas untouched.
func TestMcpAddHeaderCommaIsOneToken(t *testing.T) {
	h := "Accept: application/json, text/event-stream"
	a, err := McpAdd(AddSpec{ID: "x", Type: kindHTTP, URL: exampleMCPURL, Headers: []string{h}})
	if err != nil {
		t.Fatal(err)
	}
	if got := argAfter(t, a.Argv, "--header"); got != h {
		t.Fatalf("got %q", got)
	}
}

// The real agentsync CLI declares --header as a string array (repeatable,
// never split on ','), so a header value with commas is stored whole and
// the confirm dialog's one entry per --header is what lands in the file.
// Pins that: if a CLI release starts splitting on ',', this fails and the
// add form must refuse commas in header values.
func TestCLIKeepsCommasInHeaderValues(t *testing.T) {
	env := testenv.New(t)
	env.Chdir(t)
	env.Init(t)
	headers := []string{"Accept: application/json, text/event-stream", "Authorization: Bearer ${secret:hx.api_key}", "X-List: a,b,,c"}
	a, err := McpAdd(AddSpec{ID: "hx", Type: kindHTTP, URL: exampleMCPURL, Headers: headers})
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := env.Agentsync(t, append([]string{"--no-input"}, a.Argv...)...); code != 0 {
		t.Fatalf("mcp add exit %d: %s", code, errOut)
	}
	srv, err := source.LoadServers(env.AgentsyncHome)
	if err != nil || len(srv) != 1 {
		t.Fatalf("load: %v %#v", err, srv)
	}
	want := map[string]string{
		"Accept":        "application/json, text/event-stream",
		"Authorization": "Bearer ${secret:hx.api_key}",
		"X-List":        "a,b,,c",
	}
	if got := srv[0].Headers; len(got) != len(want) {
		t.Fatalf("headers %#v, want %#v", got, want)
	}
	for k, v := range want {
		if got, ok := srv[0].Headers[k]; !ok || got != v {
			t.Errorf("header %s = %q, want %q (all: %#v)", k, got, v, srv[0].Headers)
		}
	}
}
