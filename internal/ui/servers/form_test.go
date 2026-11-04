package servers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/actions"
)

const literalWarning = "literal value will be stored in plain text in mcp/srv.toml — prefer ${env:NAME} or ${secret:NAME}"

func TestAddFormBuildsAddSpec(t *testing.T) {
	t.Run("stdio", func(t *testing.T) {
		s := newLoaded(t, fixtureHome(t), okRunner())
		press(s, "a")
		if !s.CapturesKeys() {
			t.Fatal("form open but keys not captured")
		}
		typeText(s, "ctx7")
		press(s, "tab", "tab") // type stays stdio
		typeText(s, "npx")
		press(s, "tab")
		typeText(s, `-y "@upstash/context7-mcp" 'a b'`)
		press(s, "tab")
		typeText(s, "CONTEXT7_API_KEY=${env:CTX}")
		press(s, "enter", "tab", "tab")
		typeText(s, "claude, kiro")
		want := actions.AddSpec{
			ID: "ctx7", Type: "stdio", Command: "npx",
			Args:   []string{"-y", "@upstash/context7-mcp", "a b"},
			Env:    map[string]string{"CONTEXT7_API_KEY": "${env:CTX}"},
			Agents: []string{"claude", "kiro"},
		}
		got, err := s.form.spec()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("spec %#v, %v; want %#v", got, err, want)
		}
		act := onlyDo(t, press(s, "enter"))
		if !reflect.DeepEqual(act, mustAct(actions.McpAdd(want))) {
			t.Errorf("action %#v", act)
		}
		if s.form != nil || s.CapturesKeys() {
			t.Error("form still open after submit")
		}
	})
	t.Run("http", func(t *testing.T) {
		s := newLoaded(t, fixtureHome(t), okRunner())
		press(s, "a")
		typeText(s, "remote")
		press(s, "tab", "space", "tab") // stdio -> http
		typeText(s, "https://example.com/mcp")
		press(s, "tab", "tab", "tab")
		typeText(s, "Authorization: Bearer ${secret:TOK}")
		press(s, "enter")
		want := actions.AddSpec{
			ID: "remote", Type: "http", URL: "https://example.com/mcp",
			Headers: []string{"Authorization: Bearer ${secret:TOK}"},
		}
		got, err := s.form.spec()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("spec %#v, %v; want %#v", got, err, want)
		}
		if act := onlyDo(t, press(s, "ctrl+s")); !reflect.DeepEqual(act, mustAct(actions.McpAdd(want))) {
			t.Errorf("action %#v", act)
		}
	})
	t.Run("invalid id blocks submit", func(t *testing.T) {
		s := newLoaded(t, fixtureHome(t), okRunner())
		press(s, "a")
		typeText(s, "bad id!")
		press(s, "tab", "tab")
		typeText(s, "npx")
		if msgs := press(s, "ctrl+s"); len(msgs) != 0 {
			t.Fatalf("submitted: %#v", msgs)
		}
		if s.form == nil || !strings.Contains(view(s), "invalid server id") {
			t.Errorf("view:\n%s", view(s))
		}
	})
	t.Run("unterminated quote blocks submit", func(t *testing.T) {
		s := newLoaded(t, fixtureHome(t), okRunner())
		press(s, "a")
		typeText(s, "ok")
		press(s, "tab", "tab")
		typeText(s, "npx")
		press(s, "tab")
		typeText(s, `"oops`)
		if msgs := press(s, "ctrl+s"); len(msgs) != 0 {
			t.Fatalf("submitted: %#v", msgs)
		}
		if !strings.Contains(view(s), "unterminated") {
			t.Errorf("view:\n%s", view(s))
		}
	})
	t.Run("esc cancels", func(t *testing.T) {
		s := newLoaded(t, fixtureHome(t), okRunner())
		press(s, "a")
		typeText(s, "x")
		if msgs := press(s, "esc"); len(msgs) != 0 || s.form != nil || s.CapturesKeys() {
			t.Errorf("esc: msgs %#v form %v", msgs, s.form)
		}
	})
}

// TestAddEnvBackslashBlocksSubmit: an env value ending in a backslash would
// merge with the next entry in the CLI's comma splitter, so what the dialog
// shows would not be what is written. The form refuses it inline.
func TestAddEnvBackslashBlocksSubmit(t *testing.T) {
	s := newLoaded(t, fixtureHome(t), okRunner())
	press(s, "a")
	typeText(s, "srv")
	press(s, "tab", "tab")
	typeText(s, "npx")
	press(s, "tab", "tab")
	typeText(s, `A_OPT=xx\`)
	press(s, "enter")
	typeText(s, "B_TOKEN=${secret:B}")
	press(s, "enter")
	if msgs := press(s, "ctrl+s"); len(msgs) != 0 {
		t.Fatalf("submitted: %#v", msgs)
	}
	if s.form == nil || !strings.Contains(view(s), "env value for A_OPT contains a backslash") {
		t.Errorf("view:\n%s", view(s))
	}
}

func TestAddExistingIDIsRefused(t *testing.T) {
	s := newLoaded(t, fixtureHome(t), okRunner())
	press(s, "a")
	typeText(s, "http-srv")
	press(s, "tab", "tab")
	typeText(s, "npx")
	if msgs := press(s, "ctrl+s"); len(msgs) != 0 {
		t.Fatalf("submitted an existing id: %#v", msgs)
	}
	if s.form == nil || !strings.Contains(view(s), "already exists — press E on it to edit") {
		t.Errorf("view:\n%s", view(s))
	}
}

func TestFormMasksEnvValueAndWarnsOnLiteral(t *testing.T) {
	s := newLoaded(t, fixtureHome(t), okRunner())
	press(s, "a")
	typeText(s, "srv")
	press(s, "tab", "tab")
	typeText(s, "npx")
	press(s, "tab", "tab")
	// Live echo: the literal value is never shown while typing.
	typeText(s, "API_KEY="+canary)
	v := view(s)
	if strings.Contains(v, "CANARY") || !strings.Contains(v, "API_KEY=•") {
		t.Fatalf("live env input:\n%s", v)
	}
	press(s, "enter")
	typeText(s, "TOKEN=${env:TOK}")
	if v := view(s); !strings.Contains(v, "TOKEN=${env:TOK}") {
		t.Errorf("ref not echoed:\n%s", v)
	}
	press(s, "enter", "tab")
	typeText(s, "X-Api-Key: "+canary)
	if v := view(s); strings.Contains(v, "CANARY") || !strings.Contains(v, "X-Api-Key: •") {
		t.Fatalf("live header input:\n%s", v)
	}
	press(s, "enter")
	typeText(s, "Authorization: Bearer ${secret:GH}")
	press(s, "enter")
	v = view(s)
	if strings.Contains(v, "CANARY") {
		t.Fatalf("form leaks canary:\n%s", v)
	}
	for _, want := range []string{"API_KEY = ••••", "TOKEN = ${env:TOK}", "X-Api-Key = ••••", "Authorization = Bearer ${secret:GH}"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if n := strings.Count(v, literalWarning); n != 2 {
		t.Errorf("want 2 literal warnings, got %d:\n%s", n, v)
	}
	// ctrl+d removes the last entry of the focused list.
	press(s, "ctrl+d")
	if v := view(s); strings.Contains(v, "Authorization = ") || !strings.Contains(v, "X-Api-Key = ••••") {
		t.Errorf("ctrl+d did not remove the last header:\n%s", v)
	}
	// The submitted action carries the literal (the CLI stores it), but its
	// masked display does not.
	act := onlyDo(t, press(s, "ctrl+s"))
	if strings.Contains(act.Display(), "CANARY") {
		t.Errorf("display leaks: %s", act.Display())
	}
}
