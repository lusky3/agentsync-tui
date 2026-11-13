package netx

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/netx/netxtest"
)

const (
	hostA = "a.example"
	hostB = "b.example"
	hostC = "evil.example"
	capKB = 1 << 10

	httpsScheme = "https://"
	urlA        = httpsScheme + hostA + "/"
)

// fixture is a TLS test server reachable as hostA, hostB and hostC, with
// a client that allows only hostA and hostB.
type fixture struct {
	srv *httptest.Server
	c   *Client
	mu  sync.Mutex
	got []*http.Request
}

func newFixture(t *testing.T, timeout time.Duration, h http.HandlerFunc) *fixture {
	t.Helper()
	f := &fixture{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.got = append(f.got, r.Clone(context.Background()))
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	f.c = New(Options{
		AllowedHosts: []string{hostA, "B.Example"},
		UserAgent:    "agentsync-tui/test (+https://github.com/lusky3/agentsync-tui)",
		Timeout:      timeout,
		Transport:    netxtest.Transport(f.srv, hostA, hostB, hostC),
	})
	return f
}

func (f *fixture) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.got...)
}

func ok(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }
}

func TestRefusesHTTP(t *testing.T) {
	f := newFixture(t, time.Second, ok("x"))
	for _, u := range []string{"http://" + hostA + "/", "ftp://" + hostA + "/", "//" + hostA + "/", "file:///etc/passwd"} {
		if _, err := f.c.Get(context.Background(), u, capKB); !errors.Is(err, ErrScheme) {
			t.Errorf("Get(%q) = %v, want ErrScheme", u, err)
		}
		if _, err := f.c.Stream(context.Background(), u, capKB); !errors.Is(err, ErrScheme) {
			t.Errorf("Stream(%q) = %v, want ErrScheme", u, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("server saw %d requests, want 0", n)
	}
}

func TestRefusesHostOutsideAllowlist(t *testing.T) {
	f := newFixture(t, time.Second, ok("x"))
	for _, u := range []string{
		httpsScheme + hostC + "/",
		httpsScheme + hostA + ":8443/",
		"https://user:hunter2@" + hostA + "/",
		httpsScheme + hostA + ".evil.example/",
	} {
		_, err := f.c.Get(context.Background(), u, capKB)
		if !errors.Is(err, ErrHost) {
			t.Errorf("Get(%q) = %v, want ErrHost", u, err)
		}
		if err != nil && strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error leaks URL password: %v", err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("server saw %d requests, want 0", n)
	}
	// Host matching ignores case on both sides.
	if b, err := f.c.Get(context.Background(), "https://A.EXAMPLE/", capKB); err != nil || string(b) != "x" {
		t.Errorf("upper-case allowed host = %q, %v", b, err)
	}
}

func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Host == hostA {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "landed on "+r.Host)
	}
}

func TestRedirectToOtherAllowedHostFollowed(t *testing.T) {
	f := newFixture(t, time.Second, redirectTo(httpsScheme+hostB+"/next"))
	b, err := f.c.Get(context.Background(), urlA, capKB)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "landed on "+hostB {
		t.Errorf("body %q", b)
	}
}

func TestRedirectToDisallowedHostRefused(t *testing.T) {
	cases := map[string]string{
		"other host":     httpsScheme + hostC + "/steal",
		"https to http":  "http://" + hostB + "/plain",
		"secret in path": httpsScheme + hostC + "/x?token=ghp_abcdefghijklmnopqrstuvwxyz0123456789",
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, time.Second, redirectTo(target))
			_, err := f.c.Get(context.Background(), urlA, capKB)
			if !errors.Is(err, ErrRedirect) {
				t.Fatalf("err = %v, want ErrRedirect", err)
			}
			if strings.Contains(err.Error(), "ghp_abcdefghij") {
				t.Errorf("error leaks redirect token: %v", err)
			}
			for _, r := range f.requests() {
				if r.Host != hostA {
					t.Errorf("request reached %s", r.Host)
				}
			}
		})
	}
}

func TestMoreThanThreeRedirectsRefused(t *testing.T) {
	hops := func(n int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if strings.Count(r.URL.Path, "/r") < n {
				http.Redirect(w, r, strings.TrimSuffix(r.URL.Path, "/")+"/r", http.StatusFound)
				return
			}
			_, _ = io.WriteString(w, "done")
		}
	}
	f := newFixture(t, time.Second, hops(3))
	if b, err := f.c.Get(context.Background(), urlA, capKB); err != nil || string(b) != "done" {
		t.Fatalf("3 redirects = %q, %v; want done", b, err)
	}
	f = newFixture(t, time.Second, hops(4))
	_, err := f.c.Get(context.Background(), urlA, capKB)
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("4 redirects: err = %v, want ErrRedirect", err)
	}
	if n := len(f.requests()); n != 4 {
		t.Errorf("server saw %d requests, want 4", n)
	}
}

func gzipped(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(bytes.Repeat([]byte("a"), n)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBodyOverCapIsErrTooLarge(t *testing.T) {
	// Chunked (no Content-Length), so the cap is enforced while reading.
	f := newFixture(t, time.Second, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("p", capKB/2))
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, strings.Repeat("p", capKB/2+1))
	})
	if _, err := f.c.Get(context.Background(), urlA, capKB); !errors.Is(err, ErrTooLarge) {
		t.Errorf("plain over cap: err = %v, want ErrTooLarge", err)
	}
	if b, err := f.c.Get(context.Background(), urlA, capKB+1); err != nil || len(b) != capKB+1 {
		t.Errorf("plain at cap: len %d, err %v", len(b), err)
	}
	rc, err := f.c.Stream(context.Background(), urlA, capKB)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(rc)
	_ = rc.Close()
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("stream over cap: err = %v, want ErrTooLarge", err)
	}
	t.Run("declared length", func(t *testing.T) {
		f := newFixture(t, time.Second, ok(strings.Repeat("d", capKB+1)))
		if _, err := f.c.Stream(context.Background(), urlA, capKB); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Content-Length over cap: err = %v, want ErrTooLarge before reading", err)
		}
	})
	t.Run("gzip", gzipOverCap)
}

// gzipOverCap checks the cap counts decompressed bytes, not wire bytes.
func gzipOverCap(t *testing.T) {
	// Well under the cap on the wire, far over it once decompressed.
	gz := gzipped(t, 64*capKB)
	if len(gz) >= capKB {
		t.Fatalf("fixture: compressed size %d not under cap", len(gz))
	}
	f := newFixture(t, time.Second, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gz)
	})
	if _, err := f.c.Get(context.Background(), urlA, capKB); !errors.Is(err, ErrTooLarge) {
		t.Errorf("gzip bomb: err = %v, want ErrTooLarge", err)
	}
	b, err := f.c.Get(context.Background(), urlA, 64*capKB)
	if err != nil || len(b) != 64*capKB {
		t.Errorf("gzip at cap: len %d, err %v (want decompressed bytes)", len(b), err)
	}
}

func slowBody(release <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}
}

func TestSlowBodyHitsTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := newFixture(t, 200*time.Millisecond, slowBody(release))
	start := time.Now()
	_, err := f.c.Get(context.Background(), urlA, capKB)
	if err == nil {
		t.Fatal("slow body: want timeout error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Get took %v, want ~200ms", d)
	}

	rc, err := f.c.Stream(context.Background(), urlA, capKB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	start = time.Now()
	if _, err := io.ReadAll(rc); err == nil {
		t.Error("slow stream: want timeout error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("stream read took %v, want ~200ms", d)
	}
}

func TestCallerContextCancels(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := newFixture(t, time.Minute, slowBody(release))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := f.c.Get(ctx, urlA, capKB); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestZeroTimeoutStillBounded(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		c := New(Options{AllowedHosts: []string{hostA}, Timeout: d})
		if c.hc.Timeout != DefaultTimeout {
			t.Errorf("Timeout %v: client timeout %v, want %v", d, c.hc.Timeout, DefaultTimeout)
		}
	}
	if DefaultTimeout != 2*time.Minute {
		t.Errorf("DefaultTimeout = %v", DefaultTimeout)
	}
	c := New(Options{Timeout: 5 * time.Second})
	if c.hc.Timeout != 5*time.Second {
		t.Errorf("explicit timeout lost: %v", c.hc.Timeout)
	}
	// A non-positive byte cap is bounded too.
	f := newFixture(t, time.Second, ok(strings.Repeat("z", 10)))
	if b, err := f.c.Get(context.Background(), urlA, 0); err != nil || len(b) != 10 {
		t.Errorf("maxBytes 0: %q, %v", b, err)
	}
	if n := capOf(0); n != DefaultMaxBytes || capOf(-1) != DefaultMaxBytes || capOf(7) != 7 {
		t.Errorf("capOf: %d", n)
	}
}

func TestNon2xxIsStatusErrorWithoutBody(t *testing.T) {
	const body = "<html>cf-ray SECRET-BODY-CANARY Attention Required</html>"
	f := newFixture(t, time.Second, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, body)
	})
	for name, call := range map[string]func() error{
		"Get": func() error {
			_, err := f.c.Get(context.Background(), httpsScheme+hostA+"/?q=x", capKB)
			return err
		},
		"Stream": func() error {
			rc, err := f.c.Stream(context.Background(), httpsScheme+hostA+"/?q=x", capKB)
			if err == nil {
				_ = rc.Close()
			}
			return err
		},
	} {
		err := call()
		var se *StatusError
		if !errors.As(err, &se) || se.Code != http.StatusForbidden {
			t.Fatalf("%s: err = %v, want *StatusError 403", name, err)
		}
		if !errors.Is(err, ErrStatus) {
			t.Errorf("%s: err does not match ErrStatus", name)
		}
		if strings.Contains(err.Error(), "CANARY") || strings.Contains(err.Error(), "cf-ray") {
			t.Errorf("%s: error echoes the body: %v", name, err)
		}
		if !strings.Contains(err.Error(), "403") {
			t.Errorf("%s: error %q lacks the status", name, err)
		}
	}
}

func TestSetsUserAgentAndNoCookies(t *testing.T) {
	f := newFixture(t, time.Second, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sess", Value: "s3cr3t", Path: "/"})
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/again", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	for range 2 {
		if _, err := f.c.Get(context.Background(), urlA, capKB); err != nil {
			t.Fatal(err)
		}
	}
	reqs := f.requests()
	if len(reqs) != 4 {
		t.Fatalf("server saw %d requests, want 4", len(reqs))
	}
	for i, r := range reqs {
		if ua := r.UserAgent(); ua != "agentsync-tui/test (+https://github.com/lusky3/agentsync-tui)" {
			t.Errorf("request %d: User-Agent %q", i, ua)
		}
		if c := r.Header.Get("Cookie"); c != "" {
			t.Errorf("request %d: sent Cookie %q", i, c)
		}
		if a := r.Header.Get("Authorization"); a != "" {
			t.Errorf("request %d: sent Authorization", i)
		}
	}
}

func TestDefaultUserAgentIsHonest(t *testing.T) {
	ua := DefaultUserAgent("v1.2.3")
	if ua != "agentsync-tui/v1.2.3 (+https://github.com/lusky3/agentsync-tui)" {
		t.Errorf("DefaultUserAgent = %q", ua)
	}
	if !strings.HasPrefix(DefaultUserAgent(""), "agentsync-tui/dev ") {
		t.Errorf("empty version: %q", DefaultUserAgent(""))
	}
	f := newFixture(t, time.Second, ok("x"))
	c := New(Options{AllowedHosts: []string{hostA}, Transport: netxtest.Transport(f.srv, hostA)})
	if _, err := c.Get(context.Background(), urlA, capKB); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if ua := reqs[len(reqs)-1].UserAgent(); !strings.HasPrefix(ua, "agentsync-tui/") || !strings.Contains(ua, "github.com/lusky3/agentsync-tui") {
		t.Errorf("default User-Agent %q", ua)
	}
}

func TestOnlyGET(t *testing.T) {
	f := newFixture(t, time.Second, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			// 307/308 replay the original method: it must still be GET.
			http.Redirect(w, r, "/kept", http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	if _, err := f.c.Get(context.Background(), urlA, capKB); err != nil {
		t.Fatal(err)
	}
	rc, err := f.c.Stream(context.Background(), urlA, capKB)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(rc)
	_ = rc.Close()
	for _, r := range f.requests() {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s, want GET", r.Method, r.URL.Path)
		}
		if r.ContentLength > 0 {
			t.Errorf("%s carried a body", r.URL.Path)
		}
	}
	// The exported surface is read-only: Get and Stream, nothing else.
	var names []string
	typ := reflect.TypeFor[*Client]()
	for i := range typ.NumMethod() {
		names = append(names, typ.Method(i).Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"Get", "Stream"}) {
		t.Errorf("Client methods = %v, want [Get Stream]", names)
	}
}

func TestTransportErrorMasksURLSecrets(t *testing.T) {
	f := newFixture(t, time.Second, ok("x"))
	f.srv.Close() // connection refused from here on
	_, err := f.c.Get(context.Background(), httpsScheme+hostA+"/x?token=ghp_abcdefghijklmnopqrstuvwxyz0123456789", capKB)
	if err == nil {
		t.Fatal("want error from closed server")
	}
	if strings.Contains(err.Error(), "ghp_abcdefghij") {
		t.Errorf("error leaks URL token: %v", err)
	}
	if !strings.Contains(err.Error(), hostA) {
		t.Errorf("error %q does not name the host", err)
	}
}

func TestMaximumCapDoesNotOverflow(t *testing.T) {
	if n := capOf(math.MaxInt64); n != math.MaxInt64-1 {
		t.Errorf("capOf(MaxInt64) = %d", n)
	}
	f := newFixture(t, time.Second, ok("all of it"))
	b, err := f.c.Get(context.Background(), urlA, math.MaxInt64)
	if err != nil || string(b) != "all of it" {
		t.Fatalf("maxBytes MaxInt64 = %q, %v", b, err)
	}
	rc, err := f.c.Stream(context.Background(), urlA, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	if b, err := io.ReadAll(rc); err != nil || string(b) != "all of it" {
		t.Errorf("stream MaxInt64 = %q, %v", b, err)
	}
}

func TestTooLargeErrorNamesTheCap(t *testing.T) {
	f := newFixture(t, time.Second, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	})
	_, err := f.c.Get(context.Background(), urlA, 150)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if !strings.Contains(err.Error(), "over 150 bytes") {
		t.Errorf("error %q does not name the 150-byte cap", err)
	}
}

func TestOnlyFullContentStatusesAccepted(t *testing.T) {
	for code, want := range map[int]bool{
		http.StatusOK:                   true,
		http.StatusNonAuthoritativeInfo: true,
		http.StatusNoContent:            true,
		http.StatusCreated:              false,
		http.StatusAccepted:             false,
		http.StatusResetContent:         false,
		http.StatusPartialContent:       false,
	} {
		f := newFixture(t, time.Second, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			if code != http.StatusNoContent && code != http.StatusResetContent {
				_, _ = io.WriteString(w, `{"truncated":`)
			}
		})
		_, err := f.c.Get(context.Background(), urlA, capKB)
		var se *StatusError
		switch {
		case want && err != nil:
			t.Errorf("%d: err = %v, want accepted", code, err)
		case !want && (!errors.As(err, &se) || se.Code != code):
			t.Errorf("%d: err = %v, want *StatusError %d", code, err, code)
		}
	}
}
