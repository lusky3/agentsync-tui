package ui

import (
	"errors"
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

// errCacheClosed is Open's error after Close.
var errCacheClosed = errors.New("discovery cache closed")

// LazyCache opens a cache directory on first use, so starting the TUI
// creates nothing. A successful open is kept; a failed one is retried on
// the next call (a refused directory is re-checked and refused again). It
// is safe for concurrent use; a nil *LazyCache means no cache.
type LazyCache struct {
	dir    string
	mu     sync.Mutex
	c      *cache.Cache
	closed bool
}

// NewLazyCache returns a LazyCache over dir without touching it.
func NewLazyCache(dir string) *LazyCache { return &LazyCache{dir: dir} }

// Open returns the cache, opening (creating, mode 0700) the directory if
// no earlier call succeeded. A nil receiver returns nil, nil; after Close
// it returns an error.
func (l *LazyCache) Open() (*cache.Cache, error) {
	if l == nil {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, errCacheClosed
	}
	if l.c == nil {
		c, err := cache.Open(l.dir)
		if err != nil {
			return nil, err
		}
		l.c = c
	}
	return l.c, nil
}

// Close releases the cache if it was opened (it creates nothing) and makes
// later Opens fail. A nil receiver is a no-op.
func (l *LazyCache) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.c == nil {
		return nil
	}
	err := l.c.Close()
	l.c = nil
	return err
}
