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

const (
	// maxSkew is how far in the future an entry's stamp may be before it
	// is distrusted (a clock step backwards or a poisoned entry).
	maxSkew = time.Minute
	// staleTemp is the age after which an abandoned temp file is removed.
	staleTemp = time.Hour
	tmpPrefix = ".tmp-"
)

// Cache is a directory of hashed, timestamped entries. Its reads and writes
// all go through one os.Root opened by Open, so they stay bound to the
// directory that Open vetted. A non-empty directory planted at an entry path
// makes that key uncacheable (Get misses, Put fails); that is safe.
type Cache struct {
	root *os.Root
	now  func() time.Time
}

func errForeignDir(dir string) error {
	return fmt.Errorf("cache: %s is owned by another user", dir)
}

// Close releases the directory handle.
func (c *Cache) Close() error { return c.root.Close() }

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
// is a symlink or owned by another user is refused, a loose mode on our own
// dir is tightened to 0700, and temp files abandoned over an hour ago are
// removed. Call Close when done.
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
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("cache: %s is not a directory", dir)
	}
	if err := enforceDirPerm(dir, fi); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	c := &Cache{root: root, now: time.Now}
	c.removeStaleTemps()
	return c, nil
}

// removeStaleTemps deletes regular .tmp-* files older than staleTemp. It
// works inside the root and never follows a link.
func (c *Cache) removeStaleTemps() {
	d, err := c.root.Open(".")
	if err != nil {
		return
	}
	names, _ := d.Readdirnames(-1)
	_ = d.Close()
	for _, name := range names {
		if !strings.HasPrefix(name, tmpPrefix) {
			continue
		}
		fi, err := c.root.Lstat(name)
		if err == nil && fi.Mode().IsRegular() && c.now().Sub(fi.ModTime()) > staleTemp {
			_ = c.root.Remove(name)
		}
	}
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
	raw, err := gitrepo.ReadRegularInRoot(c.root, entryName(key), maxEntry+headerMax+1)
	if err != nil {
		return nil, false
	}
	stamp, data, ok := parseEntry(raw)
	age := c.now().Sub(stamp)
	if !ok || age >= ttl || -age > maxSkew {
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
	root := c.root
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := tmpPrefix + hex.EncodeToString(nonce[:])
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
