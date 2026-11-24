// Package mcpsrc is the common shape of an MCP discovery source: the
// official MCP Registry (Official, here) and the Docker MCP Catalog
// (package dockercat) both answer a search with the same Page of Entry
// values, so the discover screen handles them alike.
package mcpsrc

import (
	"context"
	"errors"

	"github.com/lusky3/agentsync-tui/internal/discover/mcpreg"
)

// Entry is one server as one source lists it. The same server found in
// two sources is two entries, never merged.
//
// ID is the source's own stable key for the record (the registry name
// for the official registry, the catalog key for Docker): unique within
// one source, unchanged between pages and searches, so (Source, ID) is
// the identity the screen uses to find a row again and to drop a stale
// one. It is not the suggested agentsync server id; that is in each
// installable Draft's Spec.ID (also the id its secret references were
// built with).
//
// Trust holds the lines that apply to the entry whatever is installed
// (for the official registry, the two lines in OfficialTrust); lines
// about one install option stay on that option's Draft.Trust (such as
// the OCI "image tag is mutable" line). InstallTrust joins the two for
// one option. Title, Description and every Trust line are sanitized but
// may hold \n or \t: render them with mask.OneLine.
//
// Deprecated is true when the source marks the record deprecated or
// deleted; a record that does not say is not deprecated. Installs lists
// every install option the source offers that this tool knows, the
// unsupported ones included with their reason; an entry with no such
// option has one unsupported Install saying so, so an entry never has
// an empty Installs.
type Entry struct {
	Source      string
	ID          string
	Title       string
	Description string
	Trust       []string
	Deprecated  bool
	Installs    []Install
}

// Install is one way to install an entry. Exactly one of Draft and
// Unsupported is set: Unsupported non-empty means the option is shown
// with that reason and is not installable. Label may hold \n or \t:
// render it with mask.OneLine.
type Install struct {
	Label       string
	Draft       *mcpreg.Draft
	Unsupported string
}

// Page is one page of a search. Next is the cursor for the following
// page, empty on the last one. Skipped counts records the source could
// not read and left out of Entries (the screen shows it).
type Page struct {
	Entries []Entry
	Next    string
	Skipped int
}

// Source is a searchable MCP server catalog. Name is a short, stable
// label ("official", "docker"). Search returns one page of entries
// matching query (all when empty) starting at cursor (empty: the first
// page). It never retries; the caller decides whether to ask again.
type Source interface {
	Name() string
	Search(ctx context.Context, query, cursor string) (Page, error)
}

// OfficialName is the official registry source's Name.
const OfficialName = "official"

// Entry-level trust lines of the official registry.
const (
	TrustUnreviewed = "unreviewed: the registry does not scan entries"
	TrustNamespace  = "namespace ownership is verified at publish time"
)

// Refusal reasons the adapter adds to those mcpreg.ToDraft gives.
const (
	ReasonNoID      = "the registry name gives no usable server id"
	ReasonNoInstall = "offers no package or remote this tool can install"
	// noInstallLabel labels the single Install of an entry that has none.
	noInstallLabel = "no install option"
)

// ErrNoClient is Search's error when Official was given a nil client.
var ErrNoClient = errors.New("mcpsrc: no registry client")

// OfficialTrust returns the official registry's entry-level trust lines,
// a fresh slice each call.
func OfficialTrust() []string {
	return []string{TrustUnreviewed, TrustNamespace}
}

// InstallTrust is every trust line for installing in from e: e's own
// lines, then those of in's draft (none for an unsupported option). It
// is what the screen shows beside one install option and what the add
// form receives with it.
func InstallTrust(e Entry, in Install) []string {
	out := append([]string(nil), e.Trust...)
	if in.Draft != nil {
		out = append(out, in.Draft.Trust...)
	}
	return out
}

// official adapts an mcpreg.Client. It holds the client only; the
// context comes with each Search.
type official struct {
	c *mcpreg.Client
}

// Official returns the official MCP Registry as a Source over c.
func Official(c *mcpreg.Client) Source {
	return official{c: c}
}

func (official) Name() string { return OfficialName }

// Search runs c.Search and maps its page. A client error is returned as
// is, so errors.Is(err, mcpreg.ErrBlocked) and errors.As(err,
// **netx.StatusError) still hold; a nil client is ErrNoClient.
func (o official) Search(ctx context.Context, query, cursor string) (Page, error) {
	if o.c == nil {
		return Page{}, ErrNoClient
	}
	p, err := o.c.Search(ctx, query, cursor)
	if err != nil {
		return Page{}, err
	}
	out := Page{Next: p.Next, Skipped: p.Skipped, Entries: make([]Entry, 0, len(p.Servers))}
	for _, s := range p.Servers {
		out.Entries = append(out.Entries, officialEntry(s))
	}
	return out, nil
}

func officialEntry(s mcpreg.Server) Entry {
	return Entry{
		Source:      OfficialName,
		ID:          s.Name,
		Title:       s.Name,
		Description: s.Description,
		Trust:       OfficialTrust(),
		Deprecated:  s.Status == "deprecated" || s.Status == "deleted",
		Installs:    officialInstalls(s),
	}
}

// officialInstalls maps every choice of s. The server id is suggested
// with nothing taken; the add form checks it against the real servers.
func officialInstalls(s mcpreg.Server) []Install {
	choices := mcpreg.Choices(s)
	if len(choices) == 0 {
		return []Install{{Label: noInstallLabel, Unsupported: ReasonNoInstall}}
	}
	id := mcpreg.SuggestID(s.Name, nil)
	out := make([]Install, 0, len(choices))
	for _, c := range choices {
		in := Install{Label: c.Label}
		if id == "" {
			in.Unsupported = ReasonNoID
			out = append(out, in)
			continue
		}
		d, err := mcpreg.ToDraft(s, c, id)
		if err != nil {
			in.Unsupported = err.Error()
		} else {
			in.Draft = &d
		}
		out = append(out, in)
	}
	return out
}
