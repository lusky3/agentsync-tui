// Package discover holds the discovery screens. MCPScreen searches the
// official MCP Registry and the Docker MCP Catalog and hands a chosen
// install option to the MCP servers tab's add form (ui.ComposeMcpMsg);
// nothing is installed from here.
//
// The network is reached only when the user asks: enter in the search
// field runs a search and n fetches the next page. Opening the tab, typing,
// switching the source and a refresh make no request, and with --offline
// (or no HTTP client) none is ever made. The disk cache is opened inside
// the search command, never in Update, so the screen creates nothing on
// disk until the first search.
package discover

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/discover/cache"
	"github.com/lusky3/agentsync-tui/internal/discover/dockercat"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpreg"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpsrc"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/netx"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// Title is the tab's title.
const Title = "MCP discover"

// Notes shown instead of making a request.
const (
	NoteOffline     = "discovery is off: --offline"
	NoteUnavailable = "discovery unavailable"
	NoteRateLimited = "rate limited; try again later"
	noteInterrupted = "search interrupted; press / then enter to run it again"
	noteNoInstall   = "not installable: "
	noteRefused     = "not opened: "
	noteChoose      = "several install options: choose one with j/k, then i"
	noteNoMore      = "no more results to load"
	maxQuery        = 200
)

// sourceInfo names one backend: its Source name, the host it contacts (for
// error notes) and how to build it over a client and an optional cache.
type sourceInfo struct {
	name  string
	host  string
	build func(h *netx.Client, c *cache.Cache) mcpsrc.Source
}

// sources are the backends in toggle order.
var sources = []sourceInfo{
	{mcpsrc.OfficialName, mcpreg.Host, func(h *netx.Client, c *cache.Cache) mcpsrc.Source {
		return mcpsrc.Official(mcpreg.NewClient(h, c))
	}},
	{dockercat.Name, dockercat.Host, func(h *netx.Client, c *cache.Cache) mcpsrc.Source {
		return dockercat.NewClient(h, c)
	}},
}

// results is one source's current search: what it answered, the cursors
// whose page has arrived (so a cursor that comes back is not offered
// again, whatever the cycle length), and the notes to show. A cursor
// counts only once its page arrives, so a failed or interrupted request
// can be made again.
type results struct {
	query    string // the query these results answer
	searched bool
	loading  bool
	entries  []mcpsrc.Entry
	next     string // cursor of the next page; "" when there is none
	used     map[string]bool
	skipped  int
	err      string // display-safe
}

// pageMsg carries one finished request. gen is from the screen's Rounds,
// so a result of a superseded search (a newer search, a source or scope
// switch, quit) is recognised and dropped; the type is unexported, so no
// other screen acts on it when the shell broadcasts it.
type pageMsg struct {
	gen    int
	src    int
	cursor string
	page   mcpsrc.Page
	err    error
}

// MCPScreen is the MCP discover tab.
type MCPScreen struct {
	base   context.Context
	disc   ui.Discover
	rounds ui.Rounds

	src   int // index into sources
	res   []results
	query ui.Field
	focus bool // the search field takes the keys

	list   ui.List
	detail string  // ID of the entry shown in detail; "" for the list
	opt    ui.List // the chosen install option in detail
	note   string  // display-safe

	width, height int
}

// NewMCP returns the MCP discover screen. It builds nothing that touches
// the network or the disk.
func NewMCP(d ui.Deps) *MCPScreen {
	return &MCPScreen{
		base:   ui.BaseCtx(d),
		disc:   d.Discover,
		res:    make([]results, len(sources)),
		query:  ui.Field{Max: maxQuery},
		width:  80,
		height: 20,
	}
}

// Title implements ui.Screen.
func (s *MCPScreen) Title() string { return Title }

// Init makes no request.
func (s *MCPScreen) Init() tea.Cmd { return nil }

// Refresh makes no request: the network is reached only on enter in the
// search field (or n for the next page).
func (s *MCPScreen) Refresh() tea.Cmd { return nil }

// CapturesKeys implements ui.KeyCapturer: while the search field has the
// focus every key goes to it.
func (s *MCPScreen) CapturesKeys() bool { return s.focus }

// SetScope implements ui.ScopeSetter. Discovery does not depend on the
// scope, so the results stay; a search in flight is dropped, as for any
// other switch. The install target is the MCP servers tab, which adds
// under the scope current when its form is submitted (and closes an open
// form on a scope switch).
func (s *MCPScreen) SetScope(runner.Runner, runner.Scope, string) { s.interrupt() }

// Shutdown implements ui.Shutdowner: it cancels a search in flight and
// makes its result stale.
func (s *MCPScreen) Shutdown() { s.rounds.Supersede() }

// interrupt supersedes the round in flight; its results will be ignored.
func (s *MCPScreen) interrupt() {
	s.rounds.Supersede()
	for i := range s.res {
		if s.res[i].loading {
			s.res[i].loading = false
			s.res[i].err = noteInterrupted
		}
	}
}

func (s *MCPScreen) cur() *results { return &s.res[s.src] }

// Update implements tea.Model.
func (s *MCPScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case pageMsg:
		s.page(msg)
	case tea.PasteMsg:
		if s.focus {
			// One line: line breaks are dropped, not folded to spaces.
			s.query.Insert(strings.NewReplacer("\r", "", "\n", "").Replace(msg.Content))
		}
	case tea.KeyPressMsg:
		return s, s.key(msg)
	}
	return s, nil
}

// page applies a finished request of the current round.
func (s *MCPScreen) page(m pageMsg) {
	if !s.rounds.Current(m.gen) || m.src < 0 || m.src >= len(s.res) {
		return
	}
	r := &s.res[m.src]
	r.loading = false
	if m.err != nil {
		if errors.Is(m.err, context.Canceled) {
			r.err = noteInterrupted
			return
		}
		r.err = errNote(sources[m.src].host, m.err)
		return
	}
	r.err = ""
	if r.used == nil {
		r.used = map[string]bool{}
	}
	r.used[m.cursor] = true
	r.skipped += m.page.Skipped
	r.entries = appendNew(r.entries, m.page.Entries)
	r.next = m.page.Next
	if r.used[r.next] {
		r.next = "" // a cursor seen before: the source is going round in a cycle
	}
}

// appendNew appends the entries of page whose ID is not listed yet: row
// identity is (source, ID), and a source that repeats a record across
// pages lists it once.
func appendNew(have, page []mcpsrc.Entry) []mcpsrc.Entry {
	seen := make(map[string]bool, len(have))
	for _, e := range have {
		seen[e.ID] = true
	}
	for _, e := range page {
		if !seen[e.ID] {
			seen[e.ID] = true
			have = append(have, e)
		}
	}
	return have
}

// errNote is the display-safe note for a failed request to host. It never
// quotes a response body; only the fallback quotes the error itself, whose
// URLs netx has masked, masked again and kept to one inert row.
func errNote(host string, err error) string {
	var se *netx.StatusError
	var ne net.Error
	switch {
	case errors.As(err, &se) && se.Code == http.StatusTooManyRequests:
		return host + ": " + NoteRateLimited
	case errors.As(err, &se):
		return host + " refused the request (HTTP " + strconv.Itoa(se.Code) + ")"
	case errors.Is(err, mcpreg.ErrBlocked), errors.Is(err, dockercat.ErrBlocked):
		return host + " refused the request"
	case errors.Is(err, mcpreg.ErrBadResponse), errors.Is(err, dockercat.ErrBadResponse):
		return "unreadable response from " + host
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timed out reaching " + host
	case errors.As(err, &ne):
		return "could not reach " + host
	}
	return "request to " + host + " failed: " + mask.FirstLine(err.Error())
}

func (s *MCPScreen) key(k tea.KeyPressMsg) tea.Cmd {
	if s.focus {
		return s.fieldKey(k)
	}
	if s.detail != "" {
		return s.detailKey(k.String())
	}
	return s.listKey(k.String())
}

// fieldKey edits the query; enter runs the search, esc leaves the field.
func (s *MCPScreen) fieldKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		s.focus = false
		return s.search()
	case "esc":
		s.focus = false
		return nil
	}
	s.query.Key(k)
	return nil
}

func (s *MCPScreen) listKey(k string) tea.Cmd {
	r := s.cur()
	if s.list.Key(k, len(r.entries)) {
		s.note = "" // it was about the row left behind
		return nil
	}
	switch k {
	case "/":
		s.focus = true
	case "m", "[", "]":
		s.toggleSource()
	case "n":
		return s.nextPage()
	case "enter":
		s.openDetail()
	case "i":
		return s.installFromList()
	}
	return nil
}

func (s *MCPScreen) detailKey(k string) tea.Cmd {
	e, ok := s.shown()
	if !ok {
		s.detail = ""
		return nil
	}
	if s.opt.Key(k, len(e.Installs)) {
		s.note = "" // it was about the option left behind
		return nil
	}
	switch k {
	case "esc":
		s.detail, s.note = "", ""
	case "i":
		if s.opt.Cursor >= len(e.Installs) {
			return nil // a source broke its promise of at least one option
		}
		return s.install(e, e.Installs[s.opt.Cursor])
	}
	return nil
}

// shown is the entry in detail, found again by its ID in the current
// source's results.
func (s *MCPScreen) shown() (mcpsrc.Entry, bool) {
	for _, e := range s.cur().entries {
		if e.ID == s.detail {
			return e, true
		}
	}
	return mcpsrc.Entry{}, false
}

func (s *MCPScreen) selected() (mcpsrc.Entry, bool) {
	r := s.cur()
	if len(r.entries) == 0 {
		return mcpsrc.Entry{}, false
	}
	s.list.Clamp(len(r.entries))
	return r.entries[s.list.Cursor], true
}

// openDetail shows the selected entry with its first installable option
// chosen.
func (s *MCPScreen) openDetail() {
	e, ok := s.selected()
	if !ok {
		return
	}
	s.detail, s.note = e.ID, ""
	s.opt = ui.List{}
	for i, in := range e.Installs {
		if in.Draft != nil {
			s.opt.Cursor = i
			break
		}
	}
}

// installFromList installs the selected entry's only installable option;
// with several it opens the detail to choose one.
func (s *MCPScreen) installFromList() tea.Cmd {
	e, ok := s.selected()
	if !ok {
		return nil
	}
	var installable []int
	for i, in := range e.Installs {
		if in.Draft != nil {
			installable = append(installable, i)
		}
	}
	switch len(installable) {
	case 0:
		s.note = noteNoInstall + mask.OneLine(unsupportedOnly(e))
		return nil
	case 1:
		return s.install(e, e.Installs[installable[0]])
	}
	s.openDetail()
	s.note = noteChoose
	return nil
}

// install hands the chosen option to the MCP servers tab's add form.
func (s *MCPScreen) install(e mcpsrc.Entry, in mcpsrc.Install) tea.Cmd {
	msg, why := composeFor(e, in)
	if why != "" {
		s.note = why
		return nil
	}
	s.note = ""
	return func() tea.Msg { return msg }
}

// composeFor builds the add-form message for in, or the display-safe
// reason it is not offered: an unsupported option, a prompt that asks for
// a secret value (discovery carries secrets as references only), or a
// message the form would refuse (ComposeMcpMsg.Refusal).
func composeFor(e mcpsrc.Entry, in mcpsrc.Install) (ui.ComposeMcpMsg, string) {
	if in.Draft == nil {
		return ui.ComposeMcpMsg{}, noteNoInstall + mask.OneLine(in.Unsupported)
	}
	d := in.Draft
	msg := ui.ComposeMcpMsg{
		Spec:       cloneSpec(d.Spec),
		Provenance: d.Provenance,
		Trust:      mcpsrc.InstallTrust(e, in),
		RefID:      d.Spec.ID,
	}
	for _, p := range d.Prompts {
		// Secret is carried only so Refusal turns such a draft down: a
		// secret value would be typed into the form and stored as plain
		// text. No message with it set is ever emitted.
		msg.Prompts = append(msg.Prompts, ui.FieldPrompt{
			Name: p.Name, Secret: p.Secret, Required: p.Required, Default: p.Default, Description: p.Description,
		})
	}
	if why := msg.Refusal(); why != "" {
		return ui.ComposeMcpMsg{}, noteRefused + why
	}
	return msg, ""
}

// cloneSpec copies sp so the message shares no slice or map with the
// listed entry.
func cloneSpec(sp actions.AddSpec) actions.AddSpec {
	sp.Args = slices.Clone(sp.Args)
	sp.Headers = slices.Clone(sp.Headers)
	sp.Agents = slices.Clone(sp.Agents)
	sp.Env = maps.Clone(sp.Env)
	return sp
}

// toggleSource shows the other source; the query and each source's
// results stay. A search in flight is dropped.
func (s *MCPScreen) toggleSource() {
	s.interrupt()
	s.src = (s.src + 1) % len(sources)
	s.list, s.detail, s.note = ui.List{}, "", ""
}

// blocked is why no request may be made now, or "".
func (s *MCPScreen) blocked() string {
	switch {
	case s.disc.Offline:
		return NoteOffline
	case s.disc.HTTP == nil:
		return NoteUnavailable
	}
	return ""
}

// search starts a new search of the current source for the query; it
// resets that source's results, paging and notes.
func (s *MCPScreen) search() tea.Cmd {
	if why := s.blocked(); why != "" {
		s.note = why
		return nil
	}
	q := s.query.Value
	s.res[s.src] = results{query: q, searched: true}
	s.list, s.detail, s.note = ui.List{}, "", ""
	return s.request("")
}

// nextPage fetches the page after the last one, unless a request is in
// flight, no request may be made, or there is no page left (or its cursor
// already came back in this search), which the note says.
func (s *MCPScreen) nextPage() tea.Cmd {
	r := s.cur()
	if r.loading {
		return nil
	}
	if why := s.blocked(); why != "" {
		s.note = why
		return nil
	}
	if r.next == "" || r.used[r.next] {
		s.note = noteNoMore
		return nil
	}
	s.note = ""
	return s.request(r.next)
}

// request starts one request of the current source in a new round. The
// cache is opened and the client built inside the command: opening may
// create the directory and holds a lock, which Update must not wait on.
// A cache that cannot be opened only means the search runs uncached.
func (s *MCPScreen) request(cursor string) tea.Cmd {
	r := s.cur()
	r.loading, r.err = true, ""
	gen, ctx := s.rounds.Next(s.base)
	src, hc, lc, q := s.src, s.disc.HTTP, s.disc.Cache, r.query
	return func() tea.Msg {
		c, err := lc.Open()
		if err != nil {
			c = nil
		}
		p, err := sources[src].build(hc, c).Search(ctx, q, cursor)
		return pageMsg{gen: gen, src: src, cursor: cursor, page: p, err: err}
	}
}
