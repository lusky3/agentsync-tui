// Package cache is a small TTL disk cache for discovery responses. Keys are
// hashed so callers cannot influence file names; reads go through gitrepo's
// symlink- and FIFO-safe readers and writes through os.Root, so a planted
// symlink is never followed.
package cache

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lusky3/agentsync-tui/internal/gitrepo"
)

const (
	// maxEntry caps the payload of one entry.
	maxEntry = 8 << 20
	// header is the magic that starts every entry; the first line is
	// "<header> <unix-nano> <sha256-hex of payload>".
	header = "agsc1"
	// headerMax bounds the first line, so a read limit of
	// maxEntry+headerMax+1 detects an oversize file.
	headerMax = 128

	dirMode  = 0o700
	fileMode = 0o600
	appDir   = "agentsync-tui"
)

// Cache is a directory of hashed, timestamped entries.
type Cache struct {
	dir string
	now func() time.Time
}

// DefaultDir is the per-user cache directory for this program. It is
// computed, not created.
func DefaultDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appDir), nil
}

// Open creates dir (0700) if needed and returns a Cache over it. A dir that
// is a symlink is refused.
func Open(dir string) (*Cache, error) {
	if err := requireNotSymlink(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, err
	}
	if err := requireNotSymlink(dir); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(dir); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("cache: %s is not a directory", dir)
	}
	return &Cache{dir: dir, now: time.Now}, nil
}

func requireNotSymlink(dir string) error {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("cache: %s is a symlink", dir)
	}
	return nil
}

func entryName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Get returns the entry for key if it exists, is intact and is younger than
// ttl. Anything else (missing, corrupt, expired, oversize, symlink, FIFO) is
// a miss.
func (c *Cache) Get(key string, ttl time.Duration) ([]byte, bool) {
	raw, err := gitrepo.ReadRegularIn(c.dir, entryName(key), maxEntry+headerMax+1)
	if err != nil {
		return nil, false
	}
	stamp, data, ok := parseEntry(raw)
	if !ok || c.now().Sub(stamp) >= ttl {
		return nil, false
	}
	return data, true
}

func parseEntry(raw []byte) (time.Time, []byte, bool) {
	line, data, found := bytes.Cut(raw, []byte("\n"))
	if !found || len(line) > headerMax || len(data) > maxEntry {
		return time.Time{}, nil, false
	}
	fields := strings.Fields(string(line))
	if len(fields) != 3 || fields[0] != header {
		return time.Time{}, nil, false
	}
	nano, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || fields[2] != digest(data) {
		return time.Time{}, nil, false
	}
	return time.Unix(0, nano), data, true
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Put stores data under key, atomically replacing any previous entry. An
// entry over 8 MiB is refused.
func (c *Cache) Put(key string, data []byte) error {
	if len(data) > maxEntry {
		return fmt.Errorf("cache: entry of %d bytes exceeds %d", len(data), maxEntry)
	}
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".tmp-" + hex.EncodeToString(nonce[:])
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("%s %d %s\n", header, c.now().UnixNano(), digest(data))
	_, werr := f.Write(append([]byte(body), data...))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Rename(tmp, entryName(key))
	}
	if werr != nil {
		_ = root.Remove(tmp)
	}
	return werr
}
