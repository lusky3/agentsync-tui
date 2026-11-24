package mcpreg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/lusky3/agentsync-tui/internal/discover/cache"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/netx"
)

// Host is the registry's host name; it is one of ui.DiscoverHosts.
const Host = "registry.modelcontextprotocol.io"

const (
	serversURL = "https://" + Host + "/v0.1/servers"
	pageSize   = "30"
	// cacheTTL is how long an identical search is answered from disk.
	cacheTTL = 10 * time.Minute
	// cachePrefix namespaces this source's cache keys.
	cachePrefix = "mcpreg:"
	// maxBody caps one page; 30 records are normally well under 1 MiB.
	maxBody int64 = 4 << 20
)

// MaxPageRecords is the most records decoded from one page (pageSize are
// asked for); the rest are counted in Page.Skipped.
const MaxPageRecords = 100

// Errors Search returns besides those from netx. Match them with errors.Is.
var (
	// ErrBlocked is a refusal rather than an answer: HTTP 401, 403 or 429
	// (the netx *StatusError stays in the chain, so errors.As still finds
	// the code), or an HTML page where JSON was expected (a Cloudflare
	// challenge served with 200).
	ErrBlocked = errors.New("mcpreg: the registry refused the request (blocked or rate-limited)")
	// ErrBadResponse is a body that is not a v0.1 server list: truncated
	// or otherwise invalid JSON, or JSON of the wrong shape.
	ErrBadResponse = errors.New("mcpreg: the registry sent an unreadable response")

	errNoHTTP = errors.New("mcpreg: no HTTP client (offline)")
)

// blockedStatus are the statuses reported as ErrBlocked.
var blockedStatus = map[int]bool{
	http.StatusUnauthorized:    true,
	http.StatusForbidden:       true,
	http.StatusTooManyRequests: true,
}

// Client searches the registry through a sandboxed netx client and an
// optional disk cache. It is safe for concurrent use.
type Client struct {
	hc    *netx.Client
	cache *cache.Cache
}

// NewClient returns a Client over h. c may be nil: every search then goes
// to the network.
func NewClient(h *netx.Client, c *cache.Cache) *Client {
	return &Client{hc: h, cache: c}
}

// Search returns one page of the latest versions of servers matching query
// (all servers when query is empty), starting at cursor (empty: the first
// page). An identical search within 10 minutes is answered from the cache;
// only a response that decoded cleanly is cached.
//
// Errors: ErrBlocked for 401/403/429 or an HTML page; ErrBadResponse for
// a body that is not a server list, or a page whose every record was
// unreadable (a page with some unreadable records returns the rest and
// counts them in Page.Skipped); any other non-2xx is the netx error as is
// (errors.Is netx.ErrStatus, errors.As *netx.StatusError); network,
// timeout and size errors are netx's. There is no retry.
//
// Search is stateless, so it can only catch a cursor that repeats
// immediately: when the registry answers cursor with the same cursor,
// Page.Next is empty rather than inviting an endless loop.
func (c *Client) Search(ctx context.Context, query, cursor string) (Page, error) {
	u := searchURL(query, cursor)
	key := cachePrefix + u
	if raw, ok := c.cache.Get(key, cacheTTL); ok {
		if p, err := decode(raw, cursor); err == nil {
			return p, nil
		}
	}
	if c.hc == nil {
		return Page{}, errNoHTTP
	}
	raw, err := c.hc.Get(ctx, u, maxBody)
	if err != nil {
		return Page{}, classify(err)
	}
	if looksHTML(raw) {
		return Page{}, fmt.Errorf("%w: %s sent an HTML page", ErrBlocked, Host)
	}
	p, err := decode(raw, cursor)
	if err != nil {
		return Page{}, err
	}
	_ = c.cache.Put(key, raw) // best effort: a failed write only costs a refetch
	return p, nil
}

// searchURL is the request URL; url.Values escapes and orders the
// parameters, so equal searches give equal URLs (and cache keys).
func searchURL(query, cursor string) string {
	v := url.Values{}
	v.Set("limit", pageSize)
	v.Set("version", "latest")
	if query != "" {
		v.Set("search", query)
	}
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	return serversURL + "?" + v.Encode()
}

// classify marks a refusal status as ErrBlocked, keeping err in the chain.
func classify(err error) error {
	var se *netx.StatusError
	if errors.As(err, &se) && blockedStatus[se.Code] {
		return fmt.Errorf("%w: %w", ErrBlocked, err)
	}
	return err
}

// looksHTML reports a body that starts like markup; JSON never does.
func looksHTML(b []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(b, " \t\r\n"), []byte("<"))
}

// decode parses a page and sanitizes it. Each record decodes on its own,
// so one bad record is skipped (and counted) instead of losing the page.
// ErrBadResponse means the envelope is not a server list (not JSON, not an
// object, no "servers" key, "servers" not an array) or every record on a
// non-empty page was skipped. Records past MaxPageRecords are not decoded,
// only counted as skipped. "servers": null reads as an empty page. JSON
// errors are not wrapped: their text can quote what the server sent.
func decode(raw []byte, cursor string) (Page, error) {
	bad := fmt.Errorf("%w from %s", ErrBadResponse, Host)
	var w wirePage
	if err := json.Unmarshal(raw, &w); err != nil || w.Servers == nil {
		return Page{}, bad
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(w.Servers, &entries); err != nil {
		return Page{}, bad
	}
	p := Page{Next: clean(w.Metadata.NextCursor)}
	if p.Next == cursor {
		p.Next = ""
	}
	if len(entries) > MaxPageRecords {
		p.Skipped = len(entries) - MaxPageRecords
		entries = entries[:MaxPageRecords]
	}
	p.Servers = make([]Server, 0, len(entries))
	for _, e := range entries {
		if s, ok := decodeEntry(e); ok {
			p.Servers = append(p.Servers, s)
		} else {
			p.Skipped++
		}
	}
	if len(entries) > 0 && len(p.Servers) == 0 {
		return Page{}, bad
	}
	return p, nil
}

// decodeEntry decodes one record strictly: any ill-typed field (which
// json would otherwise zero, turning "isSecret": "true" into false) or
// an unreadable input name rejects the whole record.
func decodeEntry(raw json.RawMessage) (Server, bool) {
	var e wireEntry
	if err := json.Unmarshal(raw, &e); err != nil || !namesOK(e.Server) {
		return Server{}, false
	}
	return toServer(e), true
}

// namesOK reports whether every input name in s (package env names,
// remote and header variable keys) is plain text that sanitizing leaves
// unchanged and is unique within its list or map. Otherwise two names
// could clean to one, and a secret could resolve to another input's
// plain default.
func namesOK(s wireServer) bool {
	for _, p := range s.Packages {
		names := make([]string, 0, len(p.Env))
		for _, e := range p.Env {
			names = append(names, e.Name)
		}
		if !distinctClean(names) {
			return false
		}
	}
	for _, r := range s.Remotes {
		if !remoteNamesOK(r) {
			return false
		}
	}
	return true
}

func remoteNamesOK(r wireRemote) bool {
	if !distinctClean(slices.Collect(maps.Keys(r.Variables))) {
		return false
	}
	for _, h := range r.Headers {
		if !distinctClean(slices.Collect(maps.Keys(h.Variables))) {
			return false
		}
	}
	return true
}

// distinctClean reports whether names are unchanged by clean and unique.
func distinctClean(names []string) bool {
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if clean(n) != n || seen[n] {
			return false
		}
		seen[n] = true
	}
	return true
}

// clean is the boundary every server-supplied string crosses.
func clean(s string) string { return mask.Sanitize(s) }

func toServer(e wireEntry) Server {
	s := e.Server
	out := Server{
		Name:        clean(s.Name),
		Description: clean(s.Description),
		Version:     clean(s.Version),
		RepoURL:     clean(s.Repository.URL),
		Status:      clean(e.Meta.Official.Status),
	}
	for _, p := range s.Packages {
		out.Packages = append(out.Packages, toPackage(p))
	}
	for _, r := range s.Remotes {
		out.Remotes = append(out.Remotes, toRemote(r))
	}
	return out
}

func toPackage(p wirePackage) Package {
	out := Package{
		RegistryType:    clean(p.RegistryType),
		RegistryBaseURL: clean(p.RegistryBaseURL),
		Identifier:      clean(p.Identifier),
		Version:         clean(p.Version),
		RuntimeHint:     clean(p.RuntimeHint),
		FileSHA256:      clean(p.FileSHA256),
		Transport:       clean(p.Transport.Type),
		RuntimeArgs:     toArgs(p.RuntimeArgs),
		PackageArgs:     toArgs(p.PackageArgs),
	}
	for _, e := range p.Env {
		out.Env = append(out.Env, toEnv(e.Name, e.Value, e.wireInput))
	}
	return out
}

func toArgs(in []wireArgument) []Argument {
	var out []Argument
	for _, a := range in {
		name := a.Name
		if name == "" {
			name = a.ValueHint
		}
		out = append(out, Argument{
			Type:     clean(a.Type),
			Name:     clean(name),
			Value:    clean(a.Value),
			Default:  clean(a.Default),
			Required: a.IsRequired,
			Secret:   a.IsSecret,
		})
	}
	return out
}

func toEnv(name, value string, in wireInput) EnvVar {
	return EnvVar{
		Name:        clean(name),
		Description: clean(in.Description),
		Default:     clean(in.Default),
		Value:       clean(value),
		Required:    in.IsRequired,
		Secret:      in.IsSecret,
	}
}

// toVars turns a variables object into a slice sorted by (raw) name, so
// the order is stable.
func toVars(m map[string]wireInput) []EnvVar {
	if len(m) == 0 {
		return nil
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	slices.Sort(names)
	out := make([]EnvVar, 0, len(names))
	for _, k := range names {
		out = append(out, toEnv(k, "", m[k]))
	}
	return out
}

func toRemote(r wireRemote) Remote {
	out := Remote{Type: clean(r.Type), URL: clean(r.URL), Variables: toVars(r.Variables)}
	for _, h := range r.Headers {
		out.Headers = append(out.Headers, Header{
			Name:      clean(h.Name),
			Value:     clean(h.Value),
			Required:  h.IsRequired,
			Secret:    h.IsSecret,
			Variables: toVars(h.Variables),
		})
	}
	return out
}
