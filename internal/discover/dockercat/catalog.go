// Package dockercat is the Docker MCP Catalog as a discovery source: one
// YAML file listing Docker-reviewed servers (digest-pinned container
// images and remote endpoints). The whole catalog is downloaded, cached
// for an hour, and searched on the client.
package dockercat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lusky3/agentsync-tui/internal/discover/cache"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpsrc"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/netx"
)

// Host is the catalog's host name; it is one of ui.DiscoverHosts.
const Host = "desktop.docker.com"

// Name is the source's stable Name.
const Name = "docker"

// CatalogURL is the one file this source reads.
const CatalogURL = "https://" + Host + "/mcp/catalog/v3/catalog.yaml"

const (
	// PageSize is how many entries one Search returns.
	PageSize = 30
	// cacheTTL is how long the downloaded catalog is reused.
	cacheTTL = time.Hour
	// cachePrefix namespaces this source's cache keys.
	cachePrefix = "dockercat:"
	// MaxBody caps the catalog; it is about 570 KB today, and parsing
	// costs about 100 times the body in memory (D34).
	MaxBody int64 = 2 << 20
)

// Errors Search returns besides those from netx. Match them with errors.Is.
var (
	// ErrBlocked is a refusal rather than an answer: HTTP 401, 403 or 429
	// (the netx *StatusError stays in the chain), or an HTML page where
	// YAML was expected.
	ErrBlocked = errors.New("dockercat: the catalog server refused the request (blocked or rate-limited)")
	// ErrBadResponse is a body that is not a catalog: not YAML, no
	// "registry" mapping, or a catalog whose every entry was unreadable.
	ErrBadResponse = errors.New("dockercat: the catalog server sent an unreadable response")
	// ErrBadCursor is a cursor this source never returned.
	ErrBadCursor = errors.New("dockercat: invalid page cursor")

	errNoHTTP = errors.New("dockercat: no HTTP client (offline)")
)

var blockedStatus = map[int]bool{
	http.StatusUnauthorized:    true,
	http.StatusForbidden:       true,
	http.StatusTooManyRequests: true,
}

// Client searches the catalog through a sandboxed netx client and an
// optional disk cache. It is safe for concurrent use.
type Client struct {
	hc    *netx.Client
	cache *cache.Cache
}

// NewClient returns a Client over h. c may be nil: every search then
// downloads the catalog.
func NewClient(h *netx.Client, c *cache.Cache) *Client {
	return &Client{hc: h, cache: c}
}

// Name is "docker".
func (*Client) Name() string { return Name }

// item is one decoded catalog entry under its key.
type item struct {
	key string
	e   Entry
}

// catalog is a decoded catalog: entries sorted by key, and the number of
// entries left out as unreadable.
type catalog struct {
	items   []item
	skipped int
}

// Search returns one page of the entries whose key, title or description
// contains query (case-insensitive; all entries when query is empty),
// sorted by key, PageSize at a time. cursor is empty for the first page,
// otherwise a Page.Next from an earlier search. Page.Skipped (the
// catalog's unreadable entries) is set on the first page only, so it is
// counted once per search.
//
// The catalog is downloaded once and answered from the cache for an
// hour; only a catalog that decoded without error is cached.
//
// Errors: ErrBadCursor; ErrBlocked for 401/403/429 or an HTML page;
// ErrBadResponse for a body that is not a catalog; any other non-2xx,
// network, timeout and size errors are netx's. There is no retry.
func (c *Client) Search(ctx context.Context, query, cursor string) (mcpsrc.Page, error) {
	off, err := parseCursor(cursor)
	if err != nil {
		return mcpsrc.Page{}, err
	}
	cat, err := c.load(ctx)
	if err != nil {
		return mcpsrc.Page{}, err
	}
	matches := filter(cat.items, query)
	if off > len(matches) {
		return mcpsrc.Page{}, ErrBadCursor
	}
	end := min(off+PageSize, len(matches))
	p := mcpsrc.Page{Entries: make([]mcpsrc.Entry, 0, end-off)}
	for _, it := range matches[off:end] {
		p.Entries = append(p.Entries, toEntry(it.key, it.e))
	}
	if end < len(matches) {
		p.Next = strconv.Itoa(end)
	}
	if off == 0 {
		p.Skipped = cat.skipped
	}
	return p, nil
}

func parseCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n <= 0 || strconv.Itoa(n) != cursor {
		return 0, ErrBadCursor
	}
	return n, nil
}

func filter(items []item, query string) []item {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return items
	}
	var out []item
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.key), q) ||
			strings.Contains(strings.ToLower(it.e.Title), q) ||
			strings.Contains(strings.ToLower(it.e.Description), q) {
			out = append(out, it)
		}
	}
	return out
}

// load returns the catalog from the cache or the network.
func (c *Client) load(ctx context.Context) (catalog, error) {
	key := cachePrefix + CatalogURL
	if raw, ok := c.cache.Get(key, cacheTTL); ok {
		cat, err := decode(ctx, raw)
		if err == nil {
			return cat, nil
		}
		if ctx.Err() != nil {
			return catalog{}, ctx.Err()
		}
	}
	if c.hc == nil {
		return catalog{}, errNoHTTP
	}
	raw, err := c.hc.Get(ctx, CatalogURL, MaxBody)
	if err != nil {
		return catalog{}, classify(err)
	}
	if looksHTML(raw) {
		return catalog{}, fmt.Errorf("%w: %s sent an HTML page", ErrBlocked, Host)
	}
	cat, err := decode(ctx, raw)
	if err != nil {
		return catalog{}, err
	}
	_ = c.cache.Put(key, raw) // best effort: a failed write only costs a refetch
	return cat, nil
}

func classify(err error) error {
	var se *netx.StatusError
	if errors.As(err, &se) && blockedStatus[se.Code] {
		return fmt.Errorf("%w: %w", ErrBlocked, err)
	}
	return err
}

// looksHTML reports a body that starts like markup; the catalog never does.
func looksHTML(b []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(b, " \t\r\n"), []byte("<"))
}

// Limits on the decode work one catalog may cause. yaml.v3's Decode checks
// every mapping for duplicate keys in time quadratic in its size and
// expands aliases, so only small, alias-free entries are ever decoded.
const (
	// maxEntries caps the registry; the catalog has about 320 entries.
	maxEntries = 5000
	// maxPairs caps the pairs of any one mapping inside an entry.
	maxPairs = 128
	// maxNodes caps the nodes of one entry.
	maxNodes = 10000
)

// decode parses a catalog. The document is parsed into a yaml.Node tree,
// which neither expands aliases nor checks keys, and the envelope and the
// registry mapping are walked by hand: nothing above an entry is passed
// to Decode. Each entry is then checked (budget, no alias) and decoded on
// its own into typed structs, so one bad entry is skipped (and counted)
// instead of losing the catalog. A repeated key keeps the first entry;
// later ones are skipped undecoded. ctx is checked between entries. YAML
// errors are not wrapped: their text can quote what the server sent.
func decode(ctx context.Context, raw []byte) (catalog, error) {
	bad := fmt.Errorf("%w from %s", ErrBadResponse, Host)
	if int64(len(raw)) > MaxBody {
		return catalog{}, bad
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return catalog{}, bad
	}
	reg, ok := registryNode(&doc)
	if !ok || len(reg.Content) > 2*maxEntries {
		return catalog{}, bad
	}
	var cat catalog
	seen := map[string]bool{}
	for i := 0; i+1 < len(reg.Content); i += 2 {
		if err := ctx.Err(); err != nil {
			return catalog{}, err
		}
		if seen[reg.Content[i].Value] {
			cat.skipped++
			continue
		}
		key, e, ok := decodeItem(reg.Content[i], reg.Content[i+1])
		if !ok {
			cat.skipped++
			continue
		}
		seen[key] = true
		cat.items = append(cat.items, item{key: key, e: e})
	}
	if len(reg.Content) > 0 && len(cat.items) == 0 {
		return catalog{}, bad
	}
	slices.SortFunc(cat.items, func(a, b item) int { return strings.Compare(a.key, b.key) })
	return cat, nil
}

// registryNode finds the top-level "registry" mapping. A null registry
// is an empty one; a missing, repeated or ill-typed one is not a catalog
// (with two, which one counts would be a guess).
func registryNode(doc *yaml.Node) (*yaml.Node, bool) {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, false
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, false
	}
	var reg *yaml.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		if k := top.Content[i]; k.Kind != yaml.ScalarNode || k.Value != "registry" {
			continue
		}
		if reg != nil {
			return nil, false
		}
		reg = top.Content[i+1]
	}
	switch {
	case reg == nil:
		return nil, false
	case reg.Kind == yaml.MappingNode:
		return reg, true
	case reg.Kind == yaml.ScalarNode && reg.Tag == "!!null":
		return &yaml.Node{Kind: yaml.MappingNode}, true
	}
	return nil, false
}

// decodeItem decodes one entry. The key must be plain text that
// sanitizing leaves unchanged (it is the entry's identity); the value
// must be a mapping within budget (see withinBudget) that decodes into
// wireEntry without a type error.
func decodeItem(k, v *yaml.Node) (string, Entry, bool) {
	if k.Kind != yaml.ScalarNode || k.Value == "" || clean(k.Value) != k.Value ||
		v.Kind != yaml.MappingNode || !withinBudget(v) {
		return "", Entry{}, false
	}
	var w wireEntry
	if err := v.Decode(&w); err != nil {
		return "", Entry{}, false
	}
	return k.Value, toCatalogEntry(w), true
}

// withinBudget reports whether the tree at n holds no alias, no mapping
// that keysOK refuses, and at most maxNodes nodes. It walks
// iteratively and stops as soon as visited plus pending nodes pass
// maxNodes, so its own work is bounded by that.
func withinBudget(n *yaml.Node) bool {
	stack := []*yaml.Node{n}
	for count := 0; len(stack) > 0; count++ {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.Kind == yaml.AliasNode || (cur.Kind == yaml.MappingNode && !keysOK(cur)) {
			return false
		}
		stack = append(stack, cur.Content...)
		if count+1+len(stack) > maxNodes { // visited plus pending
			return false
		}
	}
	return true
}

// keysOK reports whether mapping m has at most maxPairs pairs whose keys
// are distinct plain scalars and none a merge key ("<<" or tagged
// !!merge; the catalog uses none). yaml.v3's Decode compares every key
// pair of a mapping and formats an error for each duplicate, and a merge
// key multiplies that work, so neither may reach it.
func keysOK(m *yaml.Node) bool {
	if len(m.Content) > 2*maxPairs {
		return false
	}
	seen := make(map[string]bool, len(m.Content)/2)
	for i := 0; i < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Kind != yaml.ScalarNode || k.Value == "<<" || k.Tag == "!!merge" || seen[k.Value] {
			return false
		}
		seen[k.Value] = true
	}
	return true
}

// clean is the boundary every catalog string crosses.
func clean(s string) string { return mask.Sanitize(s) }
