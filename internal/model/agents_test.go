package model

import (
	"reflect"
	"testing"
)

func TestParseAgentList(t *testing.T) {
	got, err := ParseAgentList(fixture(t, "agent-list.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Agent{
		{Name: "antigravity", Enabled: false, Scope: "user", Registered: true},
		{Name: "claude", Enabled: true, Scope: "user", Registered: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	// the real "nothing registered" output is a note, not an agent and not an error
	got, err = ParseAgentList(fixture(t, "agent-list-empty.txt"))
	if err != nil || len(got) != 0 {
		t.Fatalf("empty list: %+v, %v", got, err)
	}
	got, err = ParseAgentList("")
	if err != nil || len(got) != 0 {
		t.Fatalf("blank input: %+v, %v", got, err)
	}
}

func TestParseAgentListAllMarksUnregistered(t *testing.T) {
	got, err := ParseAgentList(fixture(t, "agent-list-all.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 32 {
		t.Fatalf("got %d agents, want 32", len(got))
	}
	by := map[string]Agent{}
	for _, a := range got {
		by[a.Name] = a
	}
	if a := by["claude"]; !a.Registered || !a.Enabled || a.Scope != "user" {
		t.Fatalf("claude = %+v", a)
	}
	if a := by["antigravity"]; !a.Registered || a.Enabled || a.Scope != "user" {
		t.Fatalf("antigravity = %+v", a)
	}
	if a := by["amp"]; a.Registered || a.Enabled || a.Scope != "" || a.Name != "amp" {
		t.Fatalf("amp = %+v", a)
	}
	if a := by["copilot-cli"]; a.Registered || a.Name != "copilot-cli" {
		t.Fatalf("copilot-cli = %+v", a)
	}
}

func TestParseAgentListTolerantOfJunk(t *testing.T) {
	in := "\n\nclaude     enabled=true scope=user future=1 extra\n" +
		"✗ ERROR  something odd\n" +
		"-- weird --\n" +
		"bad;name enabled=true scope=user\n" +
		"codex  enabled=false scope=project\n\n" +
		"amp available maybe-more\n" +
		"zed registered enabled=true scope=user$(id)\n"
	got, err := ParseAgentList(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []Agent{
		{Name: "claude", Enabled: true, Scope: "user", Registered: true},
		{Name: "codex", Enabled: false, Scope: "project", Registered: true},
		{Name: "amp"},
		{Name: "zed", Enabled: true, Registered: true}, // unsafe scope value dropped
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for _, junk := range []string{"✗ ERROR  no agentsync config at /x; run `agentsync init` first\n", "garbage\n", "claude\n"} {
		if got, err := ParseAgentList(junk); err == nil {
			t.Fatalf("%q: want error, got %+v", junk, got)
		}
	}
}
