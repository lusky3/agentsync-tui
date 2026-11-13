// Package netx is the TUI's only HTTP client: a read-only, sandboxed GET
// for the discovery sources (the MCP Registry, the Docker MCP Catalog,
// skills.sh search and GitHub codeload archives).
//
// Every request is https, GET, to a host on the caller's allowlist, with no
// cookie jar and an honest User-Agent. Redirects are followed only between
// allowed hosts, at most three. Every request is bounded twice: by an
// overall timeout (never unbounded) and by a cap on the decompressed body.
// Errors name the masked URL and the HTTP status, mask any server text
// net/http quotes (a malformed Location header), and never carry the
// response body.
package netx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

const (
	// DefaultTimeout bounds a request whose Options.Timeout is not positive,
	// the same default the TUI uses for network git.
	DefaultTimeout = 2 * time.Minute
	// DefaultMaxBytes caps a body when the caller passes a non-positive cap.
	DefaultMaxBytes int64 = 32 << 20
	// maxRedirects is the most redirects one request follows.
	maxRedirects = 3
	// drainBytes is how much of a refused response is read before closing,
	// so its connection can be reused.
	drainBytes = 4 << 10
	projectURL = "https://github.com/lusky3/agentsync-tui"
)

// Sentinel errors; match them with errors.Is.
var (
	ErrScheme   = errors.New("netx: only https URLs are allowed")
	ErrHost     = errors.New("netx: host not allowed")
	ErrTooLarge = errors.New("netx: response too large")
	ErrRedirect = errors.New("netx: redirect refused")
	ErrStatus   = errors.New("netx: unexpected HTTP status")
)

// StatusError is a response whose status is not 200, 203 or 204. It matches ErrStatus, and its message
// holds only the status, never the response body.
type StatusError struct{ Code int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("netx: HTTP %d %s", e.Code, http.StatusText(e.Code))
}

// Unwrap lets errors.Is(err, ErrStatus) match.
func (e *StatusError) Unwrap() error { return ErrStatus }

// Options configures a Client.
type Options struct {
	// AllowedHosts are the only hosts requested or redirected to, matched
	// case-insensitively on the default https port.
	AllowedHosts []string
	// UserAgent is sent with every request; empty means DefaultUserAgent
	// for the build's module version.
	UserAgent string
	// Timeout bounds each request from dial to the last body byte; zero or
	// negative means DefaultTimeout, never unbounded.
	Timeout time.Duration
	// Transport replaces the network transport (tests: netxtest.Transport).
	Transport http.RoundTripper
}

// Client is a sandboxed read-only HTTP client. It is safe for concurrent
// use.
type Client struct {
	hc      *http.Client
	allowed map[string]bool
	ua      string
}

// DefaultUserAgent is the honest User-Agent for a version: the program, its
// version and where to find it.
func DefaultUserAgent(version string) string {
	if version == "" {
		version = "dev"
	}
	return "agentsync-tui/" + version + " (+" + projectURL + ")"
}

// New returns a Client for opts.
func New(opts Options) *Client {
	c := &Client{allowed: make(map[string]bool, len(opts.AllowedHosts)), ua: opts.UserAgent}
	for _, h := range opts.AllowedHosts {
		c.allowed[strings.ToLower(h)] = true
	}
	if c.ua == "" {
		c.ua = DefaultUserAgent(buildVersion())
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	tr := opts.Transport
	if tr == nil {
		tr = defaultTransport(c.ua)
	}
	c.hc = &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		Jar:           nil, // no cookies, ever
		CheckRedirect: c.checkRedirect,
	}
	return c
}

// Get fetches rawURL and returns its body, failing with ErrTooLarge when
// the decompressed body exceeds maxBytes (non-positive: DefaultMaxBytes).
func (c *Client) Get(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	rc, err := c.Stream(ctx, rawURL, maxBytes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Stream fetches rawURL and returns its body for reading. Reading past
// maxBytes decompressed bytes (non-positive: DefaultMaxBytes) fails with
// ErrTooLarge, and the overall timeout still applies while reading. The
// caller must Close it, which releases the connection.
func (c *Client) Stream(ctx context.Context, rawURL string, maxBytes int64) (io.ReadCloser, error) {
	u, err := c.check(rawURL)
	if err != nil {
		return nil, err
	}
	shown := display(u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fail(shown, errors.New("cannot build request"))
	}
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.hc.Do(req)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, fail(shown, masked(unwrapURLError(err)))
	}
	limit := capOf(maxBytes)
	if err := refuse(resp, limit); err != nil {
		_, _ = io.CopyN(io.Discard, resp.Body, drainBytes)
		_ = resp.Body.Close()
		return nil, fail(shown, err)
	}
	return &capped{rc: resp.Body, limit: limit, left: limit, shown: shown}, nil
}

// fullContent are the statuses whose body is the whole resource. Others
// in 2xx are refused too: 206 is a fragment (no Range is ever sent), and
// 201/202/205 are not answers to a read.
var fullContent = map[int]bool{
	http.StatusOK:                   true,
	http.StatusNonAuthoritativeInfo: true,
	http.StatusNoContent:            true,
}

// refuse reports why a response is not read: a status other than
// fullContent, or a declared length already over the cap.
func refuse(resp *http.Response, limit int64) error {
	if !fullContent[resp.StatusCode] {
		return &StatusError{Code: resp.StatusCode}
	}
	if resp.ContentLength > limit {
		return tooLarge(limit)
	}
	return nil
}

// check parses rawURL and enforces https, no credentials, the default port
// and the host allowlist.
func (c *Client) check(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Host == "" {
		// url.Parse's error echoes the URL; say only what is wrong.
		return nil, ErrScheme
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: credentials in URL", ErrHost)
	}
	host := strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" && p != "443" {
		return nil, fmt.Errorf("%w: %s port %s", ErrHost, mask.Text(host), mask.Text(p))
	}
	if !c.allowed[host] {
		return nil, fmt.Errorf("%w: %s", ErrHost, mask.Text(host))
	}
	return u, nil
}

// checkRedirect allows at most maxRedirects hops, each to a URL check
// accepts. No cookie jar is set, so none are carried along.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("%w: more than %d redirects", ErrRedirect, maxRedirects)
	}
	if _, err := c.check(req.URL.String()); err != nil {
		return fmt.Errorf("%w to %s: %w", ErrRedirect, display(req.URL), err)
	}
	return nil
}

// capped is a response body that fails once more than limit bytes arrive;
// left is what may still be read.
type capped struct {
	rc    io.ReadCloser
	limit int64
	left  int64
	shown string
	err   error
}

func (r *capped) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if int64(len(p)) > r.left+1 {
		p = p[:r.left+1]
	}
	n, err := r.rc.Read(p)
	if int64(n) > r.left {
		r.err = fail(r.shown, tooLarge(r.limit))
		return int(r.left), r.err
	}
	r.left -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = fail(r.shown, fmt.Errorf("reading body: %w", masked(err)))
		return n, r.err
	}
	return n, err
}

func (r *capped) Close() error { return r.rc.Close() }

// capOf is the byte cap for a caller's maxBytes: never unbounded, and at
// most math.MaxInt64-1 so the one-byte overshoot read cannot overflow.
func capOf(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return DefaultMaxBytes
	}
	return min(maxBytes, math.MaxInt64-1)
}

func tooLarge(limit int64) error {
	return fmt.Errorf("%w: over %d bytes", ErrTooLarge, limit)
}

// fail prefixes err with the request it belongs to.
func fail(shown string, err error) error {
	return fmt.Errorf("GET %s: %w", shown, err)
}

// maskedError is an error from below this package (net/http, the
// transport, a body read) whose text may quote what a server sent, such as
// a Location header net/http could not parse. Its message is masked; Unwrap
// keeps errors.Is and errors.As working on the original.
type maskedError struct{ inner error }

func (e *maskedError) Error() string { return mask.Text(e.inner.Error()) }

func (e *maskedError) Unwrap() error { return e.inner }

// masked wraps err so its message is masked; nil stays nil.
func masked(err error) error {
	if err == nil {
		return nil
	}
	return &maskedError{inner: err}
}

// display is a URL as an error may show it: credentials masked.
func display(u *url.URL) string { return mask.Text(u.Redacted()) }

// unwrapURLError drops net/http's *url.Error wrapper, whose message repeats
// the unmasked URL (for a refused redirect, the redirect target).
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// defaultTransport is http.DefaultTransport (HTTPS_PROXY and friends
// honoured) with TLS 1.2 or later and ua sent on a proxy CONNECT too, in
// place of Go's default User-Agent.
func defaultTransport(ua string) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ProxyConnectHeader = http.Header{"User-Agent": []string{ua}}
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	tr.DisableCompression = false // decompressed bytes are what the cap counts
	return tr
}

// buildVersion is the main module's version when built by `go install
// ...@vX.Y.Z`, else "dev".
func buildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
