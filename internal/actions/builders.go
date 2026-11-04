package actions

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

var (
	envKeyRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	revisionRe = regexp.MustCompile(`^(HEAD(~[0-9]{1,3})?|[0-9a-f]{7,40})$`)
)

func checkID(kind, s string) error {
	if !ValidID(s) {
		return fmt.Errorf("invalid %s %q", kind, s)
	}
	return nil
}

// checkText rejects empty or over-long values, a leading dash (it would
// read as a flag) and control characters, for free-form values that land in
// argv.
func checkText(kind, s string, max int) error {
	if s == "" || len(s) > max || strings.HasPrefix(s, "-") {
		return fmt.Errorf("invalid %s", kind)
	}
	return checkNoControl(kind, s)
}

func checkNoControl(kind, s string) error {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid %s: control character", kind)
		}
	}
	return nil
}

func checkAgents(agents []string) error {
	for _, a := range agents {
		if err := checkID("agent", a); err != nil {
			return err
		}
	}
	return nil
}

// sub builds "<group> <verb> <id>" actions after validating id.
func sub(title, kind, id string, destructive bool, argv ...string) (Action, error) {
	if err := checkID(kind, id); err != nil {
		return Action{}, err
	}
	return Action{Title: title + " " + id, Argv: append(argv, id), Destructive: destructive}, nil
}

// McpEnable builds `mcp enable <id>`.
func McpEnable(id string) (Action, error) {
	return sub("Enable MCP server", "server id", id, false, "mcp", "enable")
}

// McpDisable builds `mcp disable <id>`.
func McpDisable(id string) (Action, error) {
	return sub("Disable MCP server", "server id", id, false, "mcp", "disable")
}

// McpRemove builds the destructive `mcp remove <id>`.
func McpRemove(id string) (Action, error) {
	return sub("Remove MCP server", "server id", id, true, "mcp", "remove")
}

// AgentAdd builds `agent add <name>`.
func AgentAdd(name string) (Action, error) {
	return sub("Add agent", "agent name", name, false, "agent", "add")
}

// AgentEnable builds `agent enable <name>`.
func AgentEnable(name string) (Action, error) {
	return sub("Enable agent", "agent name", name, false, "agent", "enable")
}

// AgentDisable builds `agent disable <name>` (destination files are kept).
func AgentDisable(name string) (Action, error) {
	return sub("Disable agent", "agent name", name, false, "agent", "disable")
}

// AgentDisablePurge disables an agent and deletes the destination files
// agentsync owns for it (`agent disable <name> --purge`).
func AgentDisablePurge(name string) (Action, error) {
	a, err := sub("Disable agent and delete destination files agentsync owns for", "agent name", name, true, "agent", "disable")
	if err != nil {
		return Action{}, err
	}
	a.Argv = append(a.Argv, "--purge")
	a.Warning = "Deletes the destination files agentsync owns for this agent. This cannot be undone."
	return a, nil
}

// AgentRemove: `agentsync agent remove` has no --purge flag, so none is exposed.
func AgentRemove(name string) (Action, error) {
	return sub("Remove agent", "agent name", name, true, "agent", "remove")
}

// PluginAdd accepts "id" or "id@marketplace", as the CLI does.
func PluginAdd(name string) (Action, error) {
	id, mkt, hasMkt := strings.Cut(name, "@")
	if err := checkID("plugin id", id); err != nil {
		return Action{}, err
	}
	if hasMkt {
		if err := checkID("marketplace", mkt); err != nil {
			return Action{}, err
		}
	}
	return Action{Title: "Add plugin " + name, Argv: []string{"plugin", "add", name}, Network: true}, nil
}

// PluginEnable builds `plugin enable <id>`.
func PluginEnable(name string) (Action, error) {
	return sub("Enable plugin", "plugin id", name, false, "plugin", "enable")
}

// PluginDisable builds `plugin disable <id>`.
func PluginDisable(name string) (Action, error) {
	return sub("Disable plugin", "plugin id", name, false, "plugin", "disable")
}

// PluginRemove builds the destructive `plugin remove <id>`.
func PluginRemove(name string) (Action, error) {
	return sub("Remove plugin", "plugin id", name, true, "plugin", "remove")
}

// PluginUpgrade builds `plugin upgrade <id>` (network).
func PluginUpgrade(name string) (Action, error) {
	a, err := sub("Upgrade plugin", "plugin id", name, false, "plugin", "upgrade")
	if err != nil {
		return Action{}, err
	}
	a.Network = true
	return a, nil
}

// PluginUpgradeAll upgrades every plugin with a pending version bump and
// re-applies once (`plugin upgrade --all`, network). Like every builder
// without an input to validate it cannot fail.
func PluginUpgradeAll() Action {
	return Action{
		Title:   "Upgrade every plugin with a pending bump, then re-apply",
		Argv:    []string{"plugin", "upgrade", "--all"},
		Network: true,
	}
}

// MarketplaceAdd takes a URL or local path.
func MarketplaceAdd(src string) (Action, error) {
	if err := checkText("marketplace source", src, 2048); err != nil {
		return Action{}, err
	}
	return Action{Title: "Add marketplace", Argv: []string{"marketplace", "add", src}, Network: true}, nil
}

// MarketplaceRemove builds the destructive `marketplace remove <name>`.
func MarketplaceRemove(name string) (Action, error) {
	return sub("Remove marketplace", "marketplace name", name, true, "marketplace", "remove")
}

// SecretSet hands over to agentsync's own prompt; no value is ever in argv.
func SecretSet(key string) (Action, error) {
	a, err := sub("Set secret", "secret key", key, false, "secret", "set")
	if err != nil {
		return Action{}, err
	}
	a.Interactive = true
	return a, nil
}

// SecretEdit hands the terminal to `secret edit` (agentsync opens the
// editor; the TUI never sees the values).
func SecretEdit() Action {
	return Action{Title: "Edit secrets", Argv: []string{"secret", "edit"}, Interactive: true}
}

// SecretRemove builds the destructive `secret remove <key>`.
func SecretRemove(key string) (Action, error) {
	return sub("Remove secret", "secret key", key, true, "secret", "remove")
}

// Reconcile is the interactive per-item resolver; it never carries --auto-*.
func Reconcile() Action {
	return Action{Title: "Reconcile drift", Argv: []string{"reconcile"}, Interactive: true}
}

// ReconcileReport resolves nothing and reports what needs a human.
func ReconcileReport() Action {
	return Action{Title: "Report drift needing reconcile", Argv: []string{"reconcile", "--auto-safe"}, ReadOnly: true}
}

// Check builds the read-only `check`.
func Check() Action {
	return Action{Title: "Check configuration", Argv: []string{"check"}, ReadOnly: true}
}

// Doctor builds the read-only `doctor`.
func Doctor() Action { return Action{Title: "Run doctor", Argv: []string{"doctor"}, ReadOnly: true} }

// Apply renders canonical config and writes destination files (unless dry).
func Apply(dry bool, agents ...string) (Action, error) {
	if err := checkAgents(agents); err != nil {
		return Action{}, err
	}
	argv := []string{"apply"}
	title := "Apply (writes destination files)"
	if dry {
		argv = append(argv, "--dry-run")
		title = "Preview apply (dry run)"
	}
	if len(agents) > 0 {
		argv = append(argv, "--agents", strings.Join(agents, ","))
	}
	return Action{Title: title, Argv: argv, ReadOnly: dry}, nil
}

// ApplyPair returns the apply dry run and the real apply for agents.
func ApplyPair(agents ...string) (Previewed, error) {
	return pair(func(dry bool) (Action, error) { return Apply(dry, agents...) })
}

// RevertPair returns the revert dry run and the real revert.
func RevertPair(agent, to string, all bool) (Previewed, error) {
	return pair(func(dry bool) (Action, error) { return Revert(agent, to, all, dry) })
}

// ImportPair returns the import dry run and the real import.
func ImportPair(agent, component, name string) (Previewed, error) {
	return pair(func(dry bool) (Action, error) { return Import(agent, component, name, dry) })
}

func pair(build func(dry bool) (Action, error)) (Previewed, error) {
	dry, err := build(true)
	if err != nil {
		return Previewed{}, err
	}
	real, err := build(false)
	if err != nil {
		return Previewed{}, err
	}
	return Previewed{Preview: dry, Real: real}, nil
}

// Revert restores an agent's destination to a prior checkpoint. Exactly one of
// agent or all must be set.
func Revert(agent, to string, all, dry bool) (Action, error) {
	if (agent == "") == !all {
		return Action{}, errors.New("revert needs exactly one of agent or all")
	}
	argv := []string{"revert"}
	if all {
		argv = append(argv, "--all")
	} else {
		if err := checkID("agent", agent); err != nil {
			return Action{}, err
		}
		argv = append(argv, agent)
	}
	if to != "" {
		if !revisionRe.MatchString(to) {
			return Action{}, fmt.Errorf("invalid revision %q", to)
		}
		argv = append(argv, "--to", to)
	}
	title := "Revert"
	if dry {
		argv = append(argv, "--dry-run")
		title = "Preview revert (dry run)"
	}
	return Action{Title: title, Argv: argv, Destructive: !dry, ReadOnly: dry}, nil
}

var importComponents = map[string]bool{
	"mcp": true, "skill": true, "subagent": true, "command": true,
	"hook": true, "lsp": true, "memory": true, "plugin": true,
}

// Import adopts native agent config into the canonical source. The selector
// grammar is agent[:component[:name]].
func Import(agent, component, name string, dry bool) (Action, error) {
	if err := checkID("agent", agent); err != nil {
		return Action{}, err
	}
	sel := agent
	if component != "" {
		if !importComponents[component] {
			return Action{}, fmt.Errorf("invalid component %q", component)
		}
		sel += ":" + component
		if name != "" {
			if err := checkID("name", name); err != nil {
				return Action{}, err
			}
			sel += ":" + name
		}
	} else if name != "" {
		return Action{}, errors.New("import name requires a component")
	}
	argv := []string{"import", sel}
	title := "Import " + sel
	if dry {
		argv = append(argv, "--dry-run")
		title = "Preview import " + sel + " (dry run)"
	}
	return Action{
		Title: title, Argv: argv, ReadOnly: dry,
		Network: component == "" || component == "plugin",
	}, nil
}

// AddSpec describes an MCP server to add.
type AddSpec struct {
	ID, Type, Command, URL string
	Args                   []string
	Env                    map[string]string
	Headers                []string
	Agents                 []string
}

// cmdline builds argv and its masked display copy in lockstep: every token
// is added through add, so the two can never drift.
type cmdline struct{ argv, disp []string }

func (c *cmdline) add(flag, real, shown string) {
	c.argv = append(c.argv, flag, real)
	c.disp = append(c.disp, flag, shown)
}

// McpAdd always passes --type explicitly: the CLI defaults to stdio, and an
// http add without it fails.
func McpAdd(s AddSpec) (Action, error) {
	if err := checkID("server id", s.ID); err != nil {
		return Action{}, err
	}
	head := []string{"mcp", "add", s.ID, "--type", s.Type}
	c := &cmdline{argv: head, disp: append([]string(nil), head...)}
	for _, step := range []func(*cmdline, AddSpec) error{addTarget, addArgs, addEnv, addHeaders, addAgents} {
		if err := step(c, s); err != nil {
			return Action{}, err
		}
	}
	return Action{Title: "Add MCP server " + s.ID, Argv: c.argv, display: c.disp}, nil
}

// addTarget adds --command (stdio) or --url (http, sse).
func addTarget(c *cmdline, s AddSpec) error {
	switch s.Type {
	case "stdio":
		if s.URL != "" {
			return errors.New("stdio server must not have a URL")
		}
		if err := checkText("command", s.Command, 4096); err != nil {
			return err
		}
		c.add("--command", s.Command, mask.FreeText(s.Command))
	case "http", "sse":
		if s.Command != "" {
			return fmt.Errorf("%s server must not have a command", s.Type)
		}
		if err := checkText("url", s.URL, 2048); err != nil {
			return err
		}
		c.add("--url", s.URL, mask.Text(s.URL))
	default:
		return fmt.Errorf("invalid server type %q", s.Type)
	}
	return nil
}

// addArgs adds --args as one comma-separated token, commas escaped.
func addArgs(c *cmdline, s AddSpec) error {
	if len(s.Args) == 0 {
		return nil
	}
	esc := make([]string, len(s.Args))
	for i, a := range s.Args {
		if err := checkNoControl("arg", a); err != nil {
			return err
		}
		// The CLI's backslash escaping is undocumented: refuse rather than
		// let a literal backslash change argument boundaries.
		if strings.Contains(a, `\`) {
			return errors.New("arg contains a backslash, which the CLI cannot express safely")
		}
		esc[i] = strings.ReplaceAll(a, ",", `\,`)
	}
	masked := mask.CommandLine(s.Args)
	for i := range masked {
		masked[i] = strings.ReplaceAll(masked[i], ",", `\,`)
	}
	c.add("--args", strings.Join(esc, ","), strings.Join(masked, ","))
	return nil
}

// addEnv adds --env as sorted KEY=VALUE pairs, values masked for display.
func addEnv(c *cmdline, s AddSpec) error {
	if len(s.Env) == 0 {
		return nil
	}
	keys := slices.Sorted(maps.Keys(s.Env))
	pairs := make([]string, len(keys))
	shown := make([]string, len(keys))
	for i, k := range keys {
		v := s.Env[k]
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("invalid env key %q", k)
		}
		// The CLI's escaping for env values is undocumented: refuse rather than guess.
		if strings.Contains(v, ",") {
			return fmt.Errorf("env value for %s contains a comma, which the CLI cannot express", k)
		}
		// The CLI's splitter reads `\,` as a literal comma, so a value ending
		// in a backslash would swallow the separator and merge it with the
		// next entry: the confirm dialog would show two entries while the
		// file stores one. Refuse every backslash, as for --args.
		if strings.Contains(v, `\`) {
			return fmt.Errorf("env value for %s contains a backslash, which the CLI cannot express safely", k)
		}
		if err := checkNoControl("env value", v); err != nil {
			return err
		}
		pairs[i] = k + "=" + v
		shown[i] = k + "=" + mask.Value(v)
	}
	c.add("--env", strings.Join(pairs, ","), strings.Join(shown, ","))
	return nil
}

// addHeaders adds one --header per "Name: Value", values masked for display.
func addHeaders(c *cmdline, s AddSpec) error {
	for _, h := range s.Headers {
		if !strings.Contains(h, ":") || strings.HasPrefix(h, "-") || strings.HasPrefix(h, ":") {
			return fmt.Errorf("invalid header (want \"Name: Value\")")
		}
		if err := checkNoControl("header", h); err != nil {
			return err
		}
		name, val, _ := strings.Cut(h, ":")
		c.add("--header", h, name+": "+mask.Value(strings.TrimSpace(val)))
	}
	return nil
}

// addAgents adds --agents as a comma-separated allowlist.
func addAgents(c *cmdline, s AddSpec) error {
	if len(s.Agents) == 0 {
		return nil
	}
	if err := checkAgents(s.Agents); err != nil {
		return err
	}
	list := strings.Join(s.Agents, ",")
	c.add("--agents", list, list)
	return nil
}
