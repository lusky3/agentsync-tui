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
	// MaxBody caps the catalog; it is about 570 KB today.
	MaxBody int64 = 4 << 20
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
		if cat, err := decode(raw); err == nil {
			return cat, nil
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
	cat, err := decode(raw)
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

// decode parses a catalog. The document is first parsed into a yaml.Node
// tree, which never expands aliases; each entry is then checked and
// decoded on its own into typed structs, so one bad entry is skipped (and
// counted) instead of losing the catalog. An entry holding any alias
// (the catalog uses none) is skipped before it is decoded, so an alias
// bomb is never expanded. YAML errors are not wrapped: their text can
// quote what the server sent.
func decode(raw []byte) (catalog, error) {
	bad := fmt.Errorf("%w from %s", ErrBadResponse, Host)
	if int64(len(raw)) > MaxBody {
		return catalog{}, bad
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return catalog{}, bad
	}
	reg, ok := registryNode(&doc)
	if !ok {
		return catalog{}, bad
	}
	var cat catalog
	seen := map[string]bool{}
	for i := 0; i+1 < len(reg.Content); i += 2 {
		key, e, ok := decodeItem(reg.Content[i], reg.Content[i+1])
		if !ok || seen[key] {
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
// is an empty one; a missing or ill-typed one is not a catalog.
func registryNode(doc *yaml.Node) (*yaml.Node, bool) {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, false
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		k, v := top.Content[i], top.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Value != "registry" {
			continue
		}
		switch {
		case v.Kind == yaml.MappingNode:
			return v, true
		case v.Kind == yaml.ScalarNode && v.Tag == "!!null":
			return &yaml.Node{Kind: yaml.MappingNode}, true
		}
		return nil, false
	}
	return nil, false
}

// decodeItem decodes one entry. The key must be plain text that
// sanitizing leaves unchanged (it is the entry's identity); the value
// must be an alias-free mapping that decodes into wireEntry without a
// type error.
func decodeItem(k, v *yaml.Node) (string, Entry, bool) {
	if k.Kind != yaml.ScalarNode || k.Value == "" || clean(k.Value) != k.Value ||
		v.Kind != yaml.MappingNode || hasAlias(v) {
		return "", Entry{}, false
	}
	var w wireEntry
	if err := v.Decode(&w); err != nil {
		return "", Entry{}, false
	}
	return k.Value, toCatalogEntry(w), true
}

// hasAlias reports whether n or anything below it is an alias. The tree
// is the parser's, so its depth is already bounded by the YAML library.
func hasAlias(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode {
		return true
	}
	for _, c := range n.Content {
		if hasAlias(c) {
			return true
		}
	}
	return false
}

// clean is the boundary every catalog string crosses.
func clean(s string) string { return mask.Sanitize(s) }
