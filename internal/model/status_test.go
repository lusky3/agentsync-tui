package model

import (
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseStatusDrift(t *testing.T) {
	r, err := ParseStatus(fixture(t, "status-drift.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := r.Drifted()
	if len(d) != 1 || d[0].Pointer != "/mcpServers/stdio-srv" || d[0].State != "drift" {
		t.Fatalf("Drifted() = %+v", d)
	}
	if !strings.HasSuffix(d[0].Path, "mcp_config.json") {
		t.Fatalf("path = %q", d[0].Path)
	}
	clean, err := ParseStatus(fixture(t, "status-clean.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(clean.Drifted()); n != 0 {
		t.Fatalf("clean drifted = %d", n)
	}
}

func TestParseStatusToleratesLeadingInfoLines(t *testing.T) {
	in := "ℹ INFO   scope: user\n" + fixture(t, "status-drift.json")
	r, err := ParseStatus(in)
	if err != nil || len(r.Agents) != 1 {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}

func TestParseStatusIgnoresUnknownFields(t *testing.T) {
	in := `{"agents":[{"agent":"a","extra":1,"items":[{"path":"p","pointer":"/x","class":"drift","new":true}]}],"future":{}}`
	r, err := ParseStatus(in)
	if err != nil || len(r.Drifted()) != 1 {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}

func TestParseDiffHunks(t *testing.T) {
	h, err := ParseDiff(fixture(t, "diff-drift.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 1 {
		t.Fatalf("hunks = %d", len(h))
	}
	x := h[0]
	if x.Pointer != "/mcpServers/stdio-srv" || x.Path == "" ||
		strings.Contains(x.Source, "disabled") || !strings.Contains(x.Dest, `"disabled": true`) {
		t.Fatalf("hunk = %+v", x)
	}
}

func TestParseExplain(t *testing.T) {
	p, err := ParseExplain(fixture(t, "explain-drift.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Pointer != "/mcpServers/stdio-srv" || len(p.Owners) != 1 {
		t.Fatalf("p = %+v", p)
	}
	it := p.Owners[0].Items[0]
	if p.Owners[0].Agent != "antigravity" || it.Source.File != "mcp/stdio-srv.toml" || it.Drift != "drift" || it.Component != "mcp" {
		t.Fatalf("item = %+v", it)
	}
}

func TestParseErrors(t *testing.T) {
	for name, f := range map[string]func(string) error{
		"status":  func(s string) error { _, e := ParseStatus(s); return e },
		"diff":    func(s string) error { _, e := ParseDiff(s); return e },
		"explain": func(s string) error { _, e := ParseExplain(s); return e },
	} {
		if f("") == nil || f("   \n") == nil {
			t.Errorf("%s: empty input should error", name)
		}
	}
	trunc := `ℹ INFO hello` + "\x1b[31m\n" + `{"agents":[{"agent":"a"`
	err := func() error { _, e := ParseStatus(trunc); return e }()
	if err == nil || !strings.Contains(err.Error(), "INFO hello") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("status err = %v", err)
	}
	// diff errors must never echo content.
	secret := `{"hunks":[{"source":"SUPERSECRET`
	_, err = ParseDiff(secret)
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("diff err = %v", err)
	}
}
