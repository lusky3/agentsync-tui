package ui

import (
	"sync"

	"github.com/lusky3/agentsync-tui/internal/discover/cache"
	"github.com/lusky3/agentsync-tui/internal/netx"
)

// Discover is what the discovery screens (MCP discover, Skill search) need.
// Building it touches neither the network nor the disk: requests run only
// on an explicit search or install, the cache directory is created on the
// cache's first Open, and the staging directory by the first fetch.
type Discover struct {
	// Offline is --offline: every discovery screen says so and makes no
	// request. HTTP, Cache and Stage are then unset.
	Offline bool
	// HTTP is the sandboxed client (the four discovery hosts, bounded by
	// --read-timeout); nil when offline.
	HTTP *netx.Client
	// Cache holds discovery responses; nil means search uncached.
	Cache *LazyCache
	// Stage is the directory skill archives are extracted under (made on
	// first use, mode 0700); empty means installs are unavailable.
	Stage string
}

// DiscoverHosts are the only hosts discovery may contact: the MCP Registry,
// the Docker MCP Catalog, skills.sh search, and GitHub archive downloads.
func DiscoverHosts() []string {
	return []string{
		"registry.modelcontextprotocol.io",
		"desktop.docker.com",
		"skills.sh",
		"codeload.github.com",
	}
}

// LazyCache opens a cache directory on first use, so starting the TUI
// creates nothing. It is safe for concurrent use and lives as long as the
// process; a nil *LazyCache means no cache.
type LazyCache struct {
	dir  string
	once sync.Once
	c    *cache.Cache
	err  error
}

// NewLazyCache returns a LazyCache over dir without touching it.
func NewLazyCache(dir string) *LazyCache { return &LazyCache{dir: dir} }

// Open opens (creating, mode 0700) the directory on the first call and
// returns the same cache, or the same error, on every call. A nil receiver
// returns nil, nil.
func (l *LazyCache) Open() (*cache.Cache, error) {
	if l == nil {
		return nil, nil
	}
	l.once.Do(func() { l.c, l.err = cache.Open(l.dir) })
	return l.c, l.err
}
