package actions

import (
	"reflect"
	"strings"
	"testing"
)

func argAfter(t *testing.T, argv []string, flag string) string {
	t.Helper()
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	t.Fatalf("flag %s not in %v", flag, argv)
	return ""
}

func TestMcpAddEscapesCommasInArgs(t *testing.T) {
	a, err := McpAdd(AddSpec{ID: "x", Type: "stdio", Command: "npx", Args: []string{"--list=a,b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := argAfter(t, a.Argv, "--args"); got != `--list=a\,b,c` {
		t.Fatalf("got %q", got)
	}
}

func TestMcpAddEnvAndHeaders(t *testing.T) {
	a, err := McpAdd(AddSpec{ID: "x", Type: "http", URL: "https://h/x",
		Env:     map[string]string{"B": "2", "A": "1"},
		Headers: []string{"X-A: 1", "X-B: 2"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := argAfter(t, a.Argv, "--env"); got != "A=1,B=2" {
		t.Fatalf("env %q", got)
	}
	var hs []string
	for i, v := range a.Argv {
		if v == "--header" {
			hs = append(hs, a.Argv[i+1])
		}
	}
	if !reflect.DeepEqual(hs, []string{"X-A: 1", "X-B: 2"}) {
		t.Fatalf("headers %v", hs)
	}
}

func TestMcpAddAlwaysPassesType(t *testing.T) {
	cases := []struct {
		s    AddSpec
		want string
	}{
		{AddSpec{ID: "a", Type: "stdio", Command: "c"}, "stdio"},
		{AddSpec{ID: "a", Type: "http", URL: "https://u"}, "http"},
		{AddSpec{ID: "a", Type: "sse", URL: "https://u"}, "sse"},
	}
	for _, c := range cases {
		a, err := McpAdd(c.s)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a.Argv[:5], []string{"mcp", "add", "a", "--type", c.want}) {
			t.Fatalf("argv %v", a.Argv)
		}
	}
	if _, err := McpAdd(AddSpec{ID: "a", Command: "c"}); err == nil {
		t.Fatal("empty type must error")
	}
}

func TestMcpAddValidation(t *testing.T) {
	bad := []AddSpec{
		{ID: "a", Type: "stdio"},                         // no command
		{ID: "a", Type: "stdio", Command: "c", URL: "u"}, // url on stdio
		{ID: "a", Type: "http"},                          // no url
		{ID: "a", Type: "http", URL: "u", Command: "c"},  // command on http
		{ID: "a", Type: "ws", URL: "u"},                  // bad type
		{ID: "a", Type: "stdio", Command: "c", Env: map[string]string{"1X": "v"}},
		{ID: "a", Type: "stdio", Command: "c", Env: map[string]string{"K": "a,b"}},
		{ID: "a", Type: "stdio", Command: "c", Agents: []string{"bad name"}},
		{ID: "-a", Type: "stdio", Command: "c"},
	}
	for i, s := range bad {
		if _, err := McpAdd(s); err == nil {
			t.Errorf("case %d should error", i)
		}
	}
}

func TestMcpAddHTTPRequiresURL(t *testing.T) {
	if _, err := McpAdd(AddSpec{ID: "a", Type: "http"}); err == nil {
		t.Fatal("want error")
	}
}

func TestMcpAddStdioRequiresCommand(t *testing.T) {
	if _, err := McpAdd(AddSpec{ID: "a", Type: "stdio"}); err == nil {
		t.Fatal("want error")
	}
}

func TestMcpAddAgents(t *testing.T) {
	a, _ := McpAdd(AddSpec{ID: "a", Type: "stdio", Command: "c"})
	for _, v := range a.Argv {
		if v == "--agents" {
			t.Fatal("--agents must be omitted when empty")
		}
	}
	a, _ = McpAdd(AddSpec{ID: "a", Type: "stdio", Command: "c", Agents: []string{"claude", "codex"}})
	if argAfter(t, a.Argv, "--agents") != "claude,codex" {
		t.Fatalf("%v", a.Argv)
	}
}

func TestValidID(t *testing.T) {
	for _, s := range []string{"a", "A1", "ctx.7_a-b", "0x"} {
		if !ValidID(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", "../x", "a b", "x\n", "-rf", ".a", "é", strings.Repeat("a", 129)} {
		if ValidID(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
	if !ValidID(strings.Repeat("a", 128)) {
		t.Error("128 should be valid")
	}
}

var idBuilders = map[string]func(string) (Action, error){
	"McpEnable": McpEnable, "McpDisable": McpDisable, "McpRemove": McpRemove,
	"AgentAdd": AgentAdd, "AgentEnable": AgentEnable, "AgentDisable": AgentDisable, "AgentRemove": AgentRemove,
	"PluginAdd": PluginAdd, "PluginEnable": PluginEnable, "PluginDisable": PluginDisable,
	"PluginRemove": PluginRemove, "PluginUpgrade": PluginUpgrade,
	"MarketplaceRemove": MarketplaceRemove, "SecretSet": SecretSet, "SecretRemove": SecretRemove,
}

func TestBuildersRejectBadIDs(t *testing.T) {
	for name, f := range idBuilders {
		for _, bad := range []string{"", "../x", "a b", "x\n", "-rf"} {
			if _, err := f(bad); err == nil {
				t.Errorf("%s(%q) should error", name, bad)
			}
		}
		if _, err := f("good-1"); err != nil {
			t.Errorf("%s(good-1): %v", name, err)
		}
	}
	if _, err := Apply(false, "ok", "bad name"); err == nil {
		t.Error("Apply bad agent")
	}
	if _, err := PluginAdd("p@bad name"); err == nil {
		t.Error("PluginAdd bad marketplace")
	}
	if a, err := PluginAdd("p@mkt"); err != nil || a.Argv[2] != "p@mkt" {
		t.Errorf("PluginAdd id@market: %v %v", a, err)
	}
}

func TestIDBuilderArgv(t *testing.T) {
	a, _ := McpEnable("context7")
	if !reflect.DeepEqual(a.Argv, []string{"mcp", "enable", "context7"}) {
		t.Fatal(a.Argv)
	}
	a, _ = PluginUpgrade("p")
	if !a.Network || a.Destructive {
		t.Fatal("upgrade flags")
	}
	a, _ = PluginAdd("p")
	if !a.Network {
		t.Fatal("add is network")
	}
}

func TestDestructiveFlags(t *testing.T) {
	d := map[string]bool{}
	a, _ := McpRemove("x")
	d["McpRemove"] = a.Destructive
	a, _ = AgentRemove("x")
	d["AgentRemove"] = a.Destructive
	a, _ = PluginRemove("x")
	d["PluginRemove"] = a.Destructive
	a, _ = MarketplaceRemove("x")
	d["MarketplaceRemove"] = a.Destructive
	a, _ = SecretRemove("x")
	d["SecretRemove"] = a.Destructive
	a, _ = Revert("claude", "", false, false)
	d["Revert"] = a.Destructive
	for k, v := range d {
		if !v {
			t.Errorf("%s should be destructive", k)
		}
	}
	a, _ = Revert("claude", "", false, true)
	if a.Destructive {
		t.Error("dry revert must not be destructive")
	}
	a, _ = Apply(false)
	if a.Destructive {
		t.Error("apply not destructive")
	}
}

func TestReadOnlyFlags(t *testing.T) {
	ap, _ := Apply(true)
	apw, _ := Apply(false)
	rv, _ := Revert("a", "", false, true)
	rvw, _ := Revert("a", "", false, false)
	im, _ := Import("a", "", "", true)
	imw, _ := Import("a", "", "", false)
	me, _ := McpEnable("x")
	cases := []struct {
		name string
		a    Action
		want bool
	}{
		{"apply dry", ap, true}, {"apply", apw, false},
		{"revert dry", rv, true}, {"revert", rvw, false},
		{"import dry", im, true}, {"import", imw, false},
		{"report", ReconcileReport(), true}, {"check", Check(), true}, {"doctor", Doctor(), true},
		{"reconcile", Reconcile(), false}, {"mcp enable", me, false},
	}
	for _, c := range cases {
		if c.a.ReadOnly != c.want {
			t.Errorf("%s ReadOnly=%v want %v", c.name, c.a.ReadOnly, c.want)
		}
	}
}

func TestReconcile(t *testing.T) {
	r := ReconcileReport()
	if !reflect.DeepEqual(r.Argv, []string{"reconcile", "--auto-safe"}) || r.Interactive {
		t.Fatalf("%+v", r)
	}
}

func TestReconcileInteractiveHasNoAutoFlags(t *testing.T) {
	r := Reconcile()
	if !r.Interactive || !reflect.DeepEqual(r.Argv, []string{"reconcile"}) {
		t.Fatalf("%+v", r)
	}
	for _, a := range r.Argv {
		if strings.HasPrefix(a, "--auto-") {
			t.Fatal(a)
		}
	}
}

func TestRevert(t *testing.T) {
	a, err := Revert("claude", "HEAD~2", false, true)
	if err != nil || !reflect.DeepEqual(a.Argv, []string{"revert", "claude", "--to", "HEAD~2", "--dry-run"}) || a.Network {
		t.Fatalf("%v %v", a, err)
	}
	a, _ = Revert("", "", true, false)
	if !reflect.DeepEqual(a.Argv, []string{"revert", "--all"}) {
		t.Fatal(a.Argv)
	}
	if _, err := Revert("", "", false, false); err == nil {
		t.Error("neither agent nor all")
	}
	if _, err := Revert("a", "", true, false); err == nil {
		t.Error("both agent and all")
	}
	for _, to := range []string{"--all", "; rm -rf", "a b", "-abc1234", "HEAD~", "HEAD~1234", "abc123", "HEAD\n", "ABCDEF1"} {
		if _, err := Revert("a", to, false, false); err == nil {
			t.Errorf("to=%q should error", to)
		}
	}
	for _, to := range []string{"HEAD", "HEAD~0", "HEAD~999", "abcdef1", strings.Repeat("a", 40)} {
		if _, err := Revert("a", to, false, false); err != nil {
			t.Errorf("to=%q: %v", to, err)
		}
	}
}

func TestImport(t *testing.T) {
	a, err := Import("claude", "mcp", "ctx", true)
	if err != nil || !reflect.DeepEqual(a.Argv, []string{"import", "claude:mcp:ctx", "--dry-run"}) || a.Network {
		t.Fatalf("%v %v", a, err)
	}
	a, _ = Import("claude", "plugin", "", false)
	if !a.Network || a.Argv[1] != "claude:plugin" {
		t.Fatalf("%v", a)
	}
	a, _ = Import("claude", "", "", false)
	if !a.Network || a.Argv[1] != "claude" {
		t.Fatalf("%v", a)
	}
	for _, c := range [][3]string{{"", "", ""}, {"c", "bogus", ""}, {"c", "", "n"}, {"c", "mcp", "bad name"}, {"c:x", "", ""}, {"-c", "", ""}} {
		if _, err := Import(c[0], c[1], c[2], false); err == nil {
			t.Errorf("%v should error", c)
		}
	}
}

func TestApply(t *testing.T) {
	a, _ := Apply(true, "a", "b")
	if !reflect.DeepEqual(a.Argv, []string{"apply", "--dry-run", "--agents", "a,b"}) {
		t.Fatal(a.Argv)
	}
	a, _ = Apply(false)
	if !reflect.DeepEqual(a.Argv, []string{"apply"}) {
		t.Fatal(a.Argv)
	}
}

func TestMarketplaceAdd(t *testing.T) {
	a, err := MarketplaceAdd("https://github.com/x/y")
	if err != nil || !a.Network || a.Argv[2] != "https://github.com/x/y" {
		t.Fatalf("%v %v", a, err)
	}
	for _, s := range []string{"", "-x", "a\nb", "a\x00b", "a\x7fb", strings.Repeat("a", 2049)} {
		if _, err := MarketplaceAdd(s); err == nil {
			t.Errorf("%q should error", s)
		}
	}
}

func TestSecretFlags(t *testing.T) {
	s, _ := SecretSet("K")
	e, _ := SecretEdit()
	if !s.Interactive || !e.Interactive || !reflect.DeepEqual(s.Argv, []string{"secret", "set", "K"}) || !reflect.DeepEqual(e.Argv, []string{"secret", "edit"}) {
		t.Fatal("secret interactive")
	}
	if len(s.Argv) != 3 {
		t.Fatal("SecretSet must never carry a value")
	}
}

func TestDisplayMasksSecretsInArgv(t *testing.T) {
	a, err := McpAdd(AddSpec{ID: "x", Type: "http", URL: "https://h", Headers: []string{"Authorization: Bearer CANARYSECRET1234567890"}})
	if err != nil {
		t.Fatal(err)
	}
	d := a.Display()
	if strings.Contains(d, "CANARY") {
		t.Fatalf("leak: %s", d)
	}
	if !strings.HasPrefix(d, "agentsync mcp add x --type http") {
		t.Fatal(d)
	}
}

func TestDisplayQuoting(t *testing.T) {
	a, _ := McpEnable("context7")
	if a.Display() != "agentsync mcp enable context7" {
		t.Fatal(a.Display())
	}
	b := Action{Argv: []string{"x", "a b", "it's", ""}}
	if got := b.Display(); got != `agentsync x 'a b' 'it'\''s' ''` {
		t.Fatal(got)
	}
}

func TestNoSecretGetBuilder(t *testing.T) {
	// Name check: every exported func in the package.
	for _, n := range exportedFuncNames() {
		if strings.Contains(strings.ToLower(n), "secretget") {
			t.Errorf("forbidden builder %s", n)
		}
	}
	// Behaviour check: no builder yields ["secret","get",...].
	var all []Action
	for _, f := range idBuilders {
		a, _ := f("k")
		all = append(all, a)
	}
	e, _ := SecretEdit()
	all = append(all, e, Reconcile(), ReconcileReport(), Check(), Doctor())
	for _, dry := range []bool{true, false} {
		a, _ := Apply(dry, "claude")
		all = append(all, a)
		a, _ = Revert("claude", "HEAD", false, dry)
		all = append(all, a)
		a, _ = Import("claude", "mcp", "x", dry)
		all = append(all, a)
	}
	a, _ := MarketplaceAdd("https://x")
	all = append(all, a)
	a, _ = McpAdd(AddSpec{ID: "k", Type: "stdio", Command: "c"})
	all = append(all, a)
	for _, a := range all {
		if len(a.Argv) >= 2 && a.Argv[0] == "secret" && a.Argv[1] == "get" {
			t.Errorf("secret get produced: %v", a.Argv)
		}
	}
}

func TestNoGlobalFlagsInArgv(t *testing.T) {
	for _, f := range idBuilders {
		a, _ := f("k")
		for _, v := range a.Argv {
			if v == "--no-input" || v == "--color" || v == "agentsync" {
				t.Fatal(a.Argv)
			}
		}
	}
}
