package servers

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/shellwords"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

type formResult int

const (
	formNone formResult = iota
	formCancel
	formSubmit
)

// Form fields, in focus order.
const (
	fID = iota
	fType
	fTarget
	fArgs
	fEnv
	fHeader
	fAgents
	nFields
)

var serverTypes = []string{"stdio", "http", "sse"}

// maxField caps one input's length (bytes).
const maxField = 4096

const formKeys = "tab/↑↓ move • ←/→ type • enter add entry / next • ctrl+d drop last entry\n" +
	"ctrl+s add server • esc cancel"

// freeTextWarning stands under the args field (wrapped for 80 columns).
var freeTextWarning = []string{
	"! arguments and command are free text: put secrets in env/headers as",
	"  ${env:NAME} or ${secret:NAME}; literal secrets in args are stored in",
	"  plain text and can't always be masked in the preview",
}

type kv struct{ k, v string }

// form is the "add MCP server" form. Values typed into it may be secrets:
// it renders env/header values masked and never echoes a literal value.
// Every text input is a ui.Field, so the editing rules are the shared ones.
type form struct {
	vals     [nFields]ui.Field
	focus    int
	typ      int
	env      []kv
	headers  []kv
	existing map[string]bool
	err      string // display-safe
}

func newForm(existing map[string]bool) *form {
	f := &form{existing: existing}
	for i := range f.vals {
		f.vals[i].Max = maxField
	}
	return f
}

// update applies one key press and reports whether to cancel or submit.
func (f *form) update(k tea.KeyPressMsg) formResult {
	switch k.String() {
	case "esc":
		return formCancel
	case "ctrl+s":
		return formSubmit
	case "tab", "down":
		f.focus = (f.focus + 1) % nFields
	case "shift+tab", "up":
		f.focus = (f.focus + nFields - 1) % nFields
	case "enter":
		switch {
		case (f.focus == fEnv || f.focus == fHeader) && strings.TrimSpace(f.vals[f.focus].Value) != "":
			f.addEntry()
		case f.focus == fAgents:
			return formSubmit
		default:
			f.focus++
		}
	case "left":
		if f.focus == fType {
			f.typ = (f.typ + len(serverTypes) - 1) % len(serverTypes)
		}
	case "right":
		if f.focus == fType {
			f.typ = (f.typ + 1) % len(serverTypes)
		}
	case "space":
		if f.focus == fType {
			f.typ = (f.typ + 1) % len(serverTypes)
		} else {
			f.insert(" ")
		}
	case "ctrl+d":
		if f.focus == fEnv && len(f.env) > 0 {
			f.env = f.env[:len(f.env)-1]
		} else if f.focus == fHeader && len(f.headers) > 0 {
			f.headers = f.headers[:len(f.headers)-1]
		}
	default:
		if f.focus != fType {
			f.vals[f.focus].Key(k)
		}
	}
	return formNone
}

// paste inserts bracketed-paste content into the focused input.
func (f *form) paste(s string) { f.insert(s) }

func (f *form) insert(s string) {
	if f.focus == fType {
		return
	}
	f.vals[f.focus].Insert(s)
}

// addEntry moves the focused env/header input into its list.
func (f *form) addEntry() {
	var err error
	if f.focus == fEnv {
		f.env, err = addKV(f.env, f.vals[fEnv].Value, "=")
	} else {
		f.headers, err = addKV(f.headers, f.vals[fHeader].Value, ":")
	}
	if err != nil {
		f.err = err.Error()
		return
	}
	f.vals[f.focus].Clear()
	f.err = ""
}

// addKV parses "K<sep>V" and adds it to list, replacing an existing key.
func addKV(list []kv, in, sep string) ([]kv, error) {
	k, v, ok := strings.Cut(in, sep)
	k = strings.TrimSpace(k)
	if !ok || k == "" {
		if sep == "=" {
			return list, errors.New("env entry must be KEY=VALUE")
		}
		return list, errors.New("header must be Name: Value")
	}
	if sep == ":" {
		v = strings.TrimSpace(v)
	}
	out := append([]kv(nil), list...)
	for i := range out {
		if out[i].k == k {
			out[i].v = v
			return out, nil
		}
	}
	return append(out, kv{k, v}), nil
}

// spec builds the AddSpec from the form, including any pending env/header
// input that has not been added with enter yet.
func (f *form) spec() (actions.AddSpec, error) {
	s := actions.AddSpec{ID: strings.TrimSpace(f.vals[fID].Value), Type: serverTypes[f.typ]}
	if t := strings.TrimSpace(f.vals[fTarget].Value); s.Type == "stdio" {
		s.Command = t
	} else {
		s.URL = t
	}
	args, err := shellwords.Split(f.vals[fArgs].Value)
	if err != nil {
		return s, fmt.Errorf("args: %v", err)
	}
	if len(args) > 0 {
		s.Args = args
	}
	env, headers := f.env, f.headers
	if strings.TrimSpace(f.vals[fEnv].Value) != "" {
		if env, err = addKV(env, f.vals[fEnv].Value, "="); err != nil {
			return s, err
		}
	}
	if strings.TrimSpace(f.vals[fHeader].Value) != "" {
		if headers, err = addKV(headers, f.vals[fHeader].Value, ":"); err != nil {
			return s, err
		}
	}
	if len(env) > 0 {
		s.Env = make(map[string]string, len(env))
		for _, e := range env {
			s.Env[e.k] = e.v
		}
	}
	for _, h := range headers {
		s.Headers = append(s.Headers, h.k+": "+h.v)
	}
	for _, a := range strings.Split(f.vals[fAgents].Value, ",") {
		if a = strings.TrimSpace(a); a != "" {
			s.Agents = append(s.Agents, a)
		}
	}
	return s, nil
}

// submit validates the form and builds the add action. An existing ID is
// refused: `mcp add` errors on it, and remove+add would drop its allowlist.
func (f *form) submit() (actions.Action, error) {
	s, err := f.spec()
	if err == nil && f.existing[s.ID] {
		err = errors.New(s.ID + " already exists — press E on it to edit")
	}
	var act actions.Action
	if err == nil {
		act, err = actions.McpAdd(s)
	}
	if err != nil {
		f.err = mask.FirstLine(err.Error())
		return actions.Action{}, err
	}
	return act, nil
}

// refVisible: v is (or embeds) a ${...} reference and holds nothing that
// mask.Value would hide, so it is safe to echo and is not a stored literal.
func refVisible(v string) bool {
	v = strings.TrimSpace(v)
	return strings.Contains(v, "${") && mask.Value(v) != mask.Mask
}

// echo renders a value being typed: refs stay visible, anything else is
// one bullet per rune (leading spaces kept so "Name: " reads naturally).
func echo(v string) string {
	trimmed := strings.TrimLeft(v, " ")
	lead := v[:len(v)-len(trimmed)]
	if refVisible(trimmed) {
		return lead + mask.OneLine(mask.Value(trimmed))
	}
	return lead + strings.Repeat("•", utf8.RuneCountInString(trimmed))
}

// live renders the focused-or-not input text of field i, masked.
func (f *form) live(i int) string {
	v := f.vals[i].Value
	switch i {
	case fType:
		return "‹" + serverTypes[f.typ] + "›"
	case fTarget:
		if serverTypes[f.typ] == "stdio" {
			return mask.OneLine(mask.FreeTextLive(v))
		}
		return mask.OneLine(mask.Text(v))
	case fArgs:
		return mask.OneLine(mask.FreeTextLive(v))
	case fEnv, fHeader:
		sep := "="
		if i == fHeader {
			sep = ":"
		}
		k, val, ok := strings.Cut(v, sep)
		if !ok {
			return mask.OneLine(mask.Text(v))
		}
		return mask.OneLine(mask.Text(k)) + sep + echo(val)
	}
	return mask.OneLine(v)
}

func (f *form) view() string {
	labels := [nFields]string{"id", "type", "command", "args", "env", "headers", "agents"}
	if serverTypes[f.typ] != "stdio" {
		labels[fTarget] = "url"
	}
	id := strings.TrimSpace(f.vals[fID].Value)
	if !actions.ValidID(id) {
		id = "<id>"
	}
	warn := "literal value will be stored in plain text in mcp/" + mask.OneLine(id) +
		".toml — prefer ${env:NAME} or ${secret:NAME}"
	out := []string{theme.Bold.Render("Add MCP server"), ""}
	for i := 0; i < nFields; i++ {
		prefix, text := "  ", f.live(i)
		if i == f.focus {
			prefix = "> "
			if i != fType {
				text += "_"
			}
		}
		out = append(out, prefix+fmt.Sprintf("%-8s ", labels[i])+text)
		var list []kv
		switch i {
		case fEnv:
			list = f.env
		case fHeader:
			list = f.headers
		case fArgs:
			for _, l := range freeTextWarning {
				out = append(out, theme.Warn.Render("           "+l))
			}
		case fAgents:
			out = append(out, theme.Dim.Render("           comma-separated agent names; empty = agentsync default"))
		}
		for _, e := range list {
			out = append(out, "           "+mask.OneLine(mask.Text(e.k))+" = "+mask.OneLine(mask.Value(e.v)))
			if !refVisible(e.v) {
				out = append(out, theme.Warn.Render("           ! "+warn))
			}
		}
	}
	out = append(out, "")
	if f.err != "" {
		out = append(out, theme.Bad.Render(f.err))
	}
	for _, l := range strings.Split(formKeys, "\n") {
		out = append(out, theme.Dim.Render(l))
	}
	return strings.Join(out, "\n")
}
