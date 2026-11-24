package servers

import (
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/ident"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

// runsCode ends the provenance banner of a prefilled form.
const runsCode = ": the command below runs code on your machine"

// OpenPrefilled opens the add form with its fields set from a discovered
// entry (the shell forwards a ui.ComposeMcpMsg to this screen only). It is
// the same form as `a` opens: submitting it builds the action through
// spec and submit and hands it to the confirm modal.
func (s *Screen) OpenPrefilled(m ui.ComposeMcpMsg) {
	s.form = newPrefilledForm(s.ids(), m)
	s.detail, s.note = false, ""
}

// newPrefilledForm is newForm with the fields set from m: the spec's
// values, a field per prompt (prefilled with its default), the provenance
// and trust banner, and the generated references to follow the id.
func newPrefilledForm(existing map[string]bool, m ui.ComposeMcpMsg) *form {
	f := newForm(existing)
	sp := m.Spec
	set := func(i int, v string) {
		f.vals[i].Max = max(maxField, len(v)) // never drop a long draft value
		f.vals[i].Insert(v)
	}
	set(fID, sp.ID)
	if t := slices.Index(serverTypes, sp.Type); t >= 0 {
		f.typ = t
	}
	if sp.Type == stdio {
		set(fTarget, sp.Command)
	} else {
		set(fTarget, sp.URL)
	}
	set(fArgs, joinArgs(sp.Args))
	for _, k := range slices.Sorted(maps.Keys(sp.Env)) {
		f.env = append(f.env, kv{k, sp.Env[k]})
	}
	for _, h := range sp.Headers {
		f.headers, _ = addKV(f.headers, h, ":") // a malformed one is refused by McpAdd
	}
	set(fAgents, strings.Join(sp.Agents, ","))
	for _, p := range m.Prompts {
		pr := prompt{p: p, val: ui.Field{Max: maxField}}
		pr.val.Insert(p.Default)
		f.prompts = append(f.prompts, pr)
	}
	f.banner = []string{"from " + mask.OneLine(m.Provenance) + runsCode}
	for _, t := range m.Trust {
		f.banner = append(f.banner, "! "+mask.OneLine(t))
	}
	f.refs = recordRefs(f, m.RefID)
	if id := strings.TrimSpace(sp.ID); existing[id] {
		f.err = mask.OneLine(id) + " already exists — change the id"
	}
	return f
}

// joinArgs renders args for the args field so that shellwords.Split gives
// them back unchanged: a word with no space, tab, quote or backslash as
// is, else single-quoted, else (it holds a single quote) double-quoted
// with '"' and '\' escaped.
func joinArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		switch {
		case a != "" && !strings.ContainsAny(a, " \t'\"\\"):
			out[i] = a
		case !strings.Contains(a, "'"):
			out[i] = "'" + a + "'"
		default:
			out[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a) + `"`
		}
	}
	return strings.Join(out, " ")
}

// prompt is one value a prefilled form still needs, as an env entry.
type prompt struct {
	p   ui.FieldPrompt
	val ui.Field
}

// secret reports whether the value is masked as it is typed: the prompt
// says so, or its name reads like a credential (as an env key would).
func (p prompt) secret() bool { return p.p.Secret || mask.SensitiveKey(p.p.Name) }

// live renders the prompt's value: masked like an env value when secret.
func (p prompt) live() string {
	if p.secret() {
		return echo(p.val.Value)
	}
	return mask.OneLine(mask.Text(p.val.Value))
}

// withPrompts adds each filled prompt to env; an empty required one is an
// error.
func (f *form) withPrompts(env []kv) ([]kv, error) {
	for _, p := range f.prompts {
		if strings.TrimSpace(p.val.Value) == "" {
			if p.p.Required {
				return nil, errors.New(mask.OneLine(p.p.Name) + " is required")
			}
			continue
		}
		var err error
		if env, err = addKV(env, p.p.Name+"="+p.val.Value, "="); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// promptLines renders the prompt fields after the fixed ones, each with
// its description (one line) and, for a literal in a secret one, warn.
func (f *form) promptLines(warn string) []string {
	if len(f.prompts) == 0 {
		return nil
	}
	out := []string{"", theme.Bold.Render("  values this server needs")}
	for i, p := range f.prompts {
		out = append(out, f.fieldLine(nFields+i, mask.OneLine(p.p.Name)))
		note := mask.OneLine(p.p.Description)
		if p.p.Required {
			note = strings.TrimSpace("(required) " + note)
		}
		if note != "" {
			out = append(out, theme.Dim.Render("           "+note))
		}
		if p.secret() && strings.TrimSpace(p.val.Value) != "" && !refVisible(p.val.Value) {
			out = append(out, theme.Warn.Render("           ! "+warn))
		}
	}
	return out
}

// site is where a generated reference sits: the command/url or args
// field, or the value of the env or header entry named key.
type site struct {
	field int // fTarget, fArgs, fEnv or fHeader
	key   string
}

// genRefs tracks the secret references the draft generated, so they follow
// the id (Decision 7: a renamed server must not reuse another server's
// vault entry). id is the server id they name now; seen counts, per site,
// the occurrences of each generated ${secret:<id>.<name>} still there.
// References the user typed are never in seen, so never rewritten.
type genRefs struct {
	id   string
	seen map[site]map[string]int
}

// secretRef is the generated reference form (mcpreg.SecretRef's).
func secretRef(id, name string) string { return "${secret:" + id + "." + name + "}" }

// recordRefs finds the references to id's vault entries in the freshly
// prefilled form: ${secret:<id>.<lower_snake>}, mcpreg.SecretRef's form.
func recordRefs(f *form, id string) genRefs {
	g := genRefs{id: id, seen: map[site]map[string]int{}}
	if !ident.Valid(id) {
		return g
	}
	re := regexp.MustCompile(`\$\{secret:` + regexp.QuoteMeta(id) + `\.([a-z0-9]+(?:_[a-z0-9]+)*)\}`)
	for _, st := range f.sites() {
		v, _ := f.siteValue(st)
		for _, m := range re.FindAllStringSubmatch(v, -1) {
			if g.seen[st] == nil {
				g.seen[st] = map[string]int{}
			}
			g.seen[st][m[1]]++
		}
	}
	return g
}

// sites lists every place a reference can sit in the form.
func (f *form) sites() []site {
	out := []site{{field: fTarget}, {field: fArgs}}
	for _, e := range f.env {
		out = append(out, site{fEnv, e.k})
	}
	for _, h := range f.headers {
		out = append(out, site{fHeader, h.k})
	}
	return out
}

// entries is the env or header list a site's field names.
func (f *form) entries(field int) []kv {
	if field == fEnv {
		return f.env
	}
	return f.headers
}

// siteValue is the current value at st; false when the entry is gone.
func (f *form) siteValue(st site) (string, bool) {
	if st.field == fTarget || st.field == fArgs {
		return f.vals[st.field].Value, true
	}
	for _, e := range f.entries(st.field) {
		if e.k == st.key {
			return e.v, true
		}
	}
	return "", false
}

// setSite replaces the value at st (entries are copied, never shared).
func (f *form) setSite(st site, v string) {
	if st.field == fTarget || st.field == fArgs {
		f.vals[st.field].Value = v
		return
	}
	list := slices.Clone(f.entries(st.field))
	for i := range list {
		if list[i].k == st.key {
			list[i].v = v
		}
	}
	if st.field == fEnv {
		f.env = list
	} else {
		f.headers = list
	}
}

// followRefs runs after every edit: it stops tracking generated references
// the user removed, then, when the id now differs from the one they name
// and is a valid id no other server has, rewrites each tracked occurrence
// to the new id. An invalid or taken id rewrites nothing yet (submit
// refuses a taken one).
func (f *form) followRefs() {
	if len(f.refs.seen) == 0 {
		return
	}
	f.pruneRefs()
	id := strings.TrimSpace(f.vals[fID].Value)
	if id == f.refs.id || !actions.ValidID(id) || f.existing[id] {
		return
	}
	for st, names := range f.refs.seen {
		v, _ := f.siteValue(st)
		for name, n := range names {
			v = strings.Replace(v, secretRef(f.refs.id, name), secretRef(id, name), n)
		}
		f.setSite(st, v)
	}
	f.refs.id = id
}

// pruneRefs lowers each tracked count to the occurrences still present,
// forgetting what is gone. The command and args fields are edited only at
// their end and entries are replaced whole, so the generated occurrences
// are the first ones in a value; Replace's first-n rewrite keeps to them.
func (f *form) pruneRefs() {
	for st, names := range f.refs.seen {
		v, ok := f.siteValue(st)
		for name, n := range names {
			c := 0
			if ok {
				c = strings.Count(v, secretRef(f.refs.id, name))
			}
			if c == 0 {
				delete(names, name)
			} else {
				names[name] = min(n, c)
			}
		}
		if len(names) == 0 {
			delete(f.refs.seen, st)
		}
	}
}
