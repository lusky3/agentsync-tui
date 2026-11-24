package source

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// home builds a temp agentsync home whose mcp dir holds the named fixtures
// (fixture file -> installed base name).
func home(t *testing.T, files map[string]string) string {
	t.Helper()
	h := t.TempDir()
	dir := filepath.Join(h, "mcp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for fixture, name := range files {
		b, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func loadOne(t *testing.T, fixture string) Server {
	t.Helper()
	s, err := LoadServers(home(t, map[string]string{fixture: "x.toml"}))
	if err != nil || len(s) != 1 {
		t.Fatalf("got %v, %v", s, err)
	}
	return s[0]
}

func TestLoadStdioServer(t *testing.T) {
	s := loadOne(t, "stdio.toml")
	if s.ID != "x" || s.Type != "stdio" || s.Command != "npx" || s.Disabled {
		t.Fatalf("bad server: %+v", s)
	}
	if !reflect.DeepEqual(s.Args, []string{"-y", "@upstash/context7-mcp"}) {
		t.Fatalf("args: %v", s.Args)
	}
	if s.Env["CONTEXT7_API_KEY"] != "${CONTEXT7_API_TOKEN}" {
		t.Fatalf("env: %v", s.Env)
	}
}

func TestLoadHTTPServer(t *testing.T) {
	s := loadOne(t, "http.toml")
	if s.Type != "http" || s.URL != "https://developerknowledge.googleapis.com/mcp" {
		t.Fatalf("bad server: %+v", s)
	}
	if s.Headers["X-Goog-Api-Key"] != "${GOOGLE_DEVELOPER_KNOWLEDGE_API_KEY}" {
		t.Fatalf("headers: %v", s.Headers)
	}
}

func TestLoadDisabledServer(t *testing.T) {
	if s := loadOne(t, "disabled.toml"); !s.Disabled {
		t.Fatalf("want disabled: %+v", s)
	}
	if s := loadOne(t, "stdio.toml"); s.Disabled {
		t.Fatal("absent enabled key must mean enabled")
	}
}

func TestUnknownKeysLandInExtra(t *testing.T) {
	s := loadOne(t, "extra.toml")
	if s.Extra["future_setting"] != "x" || s.Extra["timeout"] != int64(30) {
		t.Fatalf("extra: %v", s.Extra)
	}
	if _, ok := s.Extra["command"]; ok {
		t.Fatal("modelled key leaked into Extra")
	}
	if _, ok := s.Extra["enabled"]; ok {
		t.Fatal("enabled leaked into Extra")
	}
	// Extra is never nil, even with nothing unknown.
	if s := loadOne(t, "http.toml"); s.Extra == nil {
		t.Fatal("Extra must be non-nil")
	}
}

func TestServerPathRejectsTraversal(t *testing.T) {
	for _, id := range []string{"../x", "a/b", "", "a\nb", ".x", "a b"} {
		if _, err := ServerPath("/h", id); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
	p, err := ServerPath("/h", "ctx.7_a-b")
	if err != nil || p != filepath.Join("/h", "mcp", "ctx.7_a-b.toml") {
		t.Fatalf("got %q, %v", p, err)
	}
}

func TestLoadEmptyDirReturnsEmptySlice(t *testing.T) {
	s, err := LoadServers(home(t, nil))
	if err != nil || s == nil || len(s) != 0 {
		t.Fatalf("got %#v, %v", s, err)
	}
	// Missing mcp dir is also empty, not an error.
	s, err = LoadServers(t.TempDir())
	if err != nil || s == nil || len(s) != 0 {
		t.Fatalf("missing dir: %#v, %v", s, err)
	}
}

func TestLoadServersSortedAndSkipsNonToml(t *testing.T) {
	h := home(t, map[string]string{"stdio.toml": "b.toml", "http.toml": "a.toml"})
	dir := filepath.Join(h, "mcp")
	mustWrite(t, filepath.Join(dir, "notes.txt"), "hi")
	if err := os.Mkdir(filepath.Join(dir, "sub.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := LoadServers(h)
	if err != nil || len(s) != 2 || s[0].ID != "a" || s[1].ID != "b" {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestLoadServersSkipsMalformedFile(t *testing.T) {
	h := home(t, map[string]string{"stdio.toml": "good.toml"})
	dir := filepath.Join(h, "mcp")
	mustWrite(t, filepath.Join(dir, "bad.toml"), "[server\nsecret = 'hunter2'")
	mustWrite(t, filepath.Join(dir, "bad id.toml"), "[server]\n")
	s, err := LoadServers(h)
	if len(s) != 1 || s[0].ID != "good" {
		t.Fatalf("good server hidden: %+v", s)
	}
	if err == nil {
		t.Fatal("want joined error")
	}
	msg := err.Error()
	for _, want := range []string{"bad.toml", "bad id.toml"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "hunter2") {
		t.Errorf("file contents leaked: %q", msg)
	}
}

func TestLoadServersDoesNotFollowSymlinkOutside(t *testing.T) {
	h := home(t, map[string]string{"stdio.toml": "good.toml"})
	outside := filepath.Join(t.TempDir(), "evil.toml")
	mustWrite(t, outside, "[server]\ntype='stdio'\ncommand='evil'\n")
	if err := os.Symlink(outside, filepath.Join(h, "mcp", "x.toml")); err != nil {
		t.Skip("symlinks unsupported")
	}
	s, err := LoadServers(h)
	if len(s) != 1 || s[0].ID != "good" {
		t.Fatalf("symlink followed: %+v", s)
	}
	if err == nil || !strings.Contains(err.Error(), "x.toml") {
		t.Fatalf("want symlink reported, got %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
