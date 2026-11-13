package netx

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

// fakeProxy is a loopback listener that records each request line it is
// sent and answers 403, so no connection ever leaves the machine.
type fakeProxy struct {
	ln    net.Listener
	mu    sync.Mutex
	conns int
	reqs  []*http.Request
}

func newFakeProxy(t *testing.T) *fakeProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go p.serve()
	return p
}

func (p *fakeProxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.handle(conn)
	}
}

func (p *fakeProxy) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	p.mu.Lock()
	p.conns++
	p.mu.Unlock()
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	_, _ = conn.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
}

func (p *fakeProxy) snapshot() (int, []*http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns, append([]*http.Request(nil), p.reqs...)
}

// The default transport sends allowed requests through the configured
// proxy as CONNECT host:443 with the client's honest User-Agent, and a
// disallowed host never reaches the proxy at all.
func TestDefaultTransportHonoursProxy(t *testing.T) {
	p := newFakeProxy(t)
	const ua = "agentsync-tui/proxytest (+https://github.com/lusky3/agentsync-tui)"
	c := New(Options{AllowedHosts: []string{hostA}, UserAgent: ua, Timeout: 10 * time.Second})
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport is %T, want *http.Transport", c.hc.Transport)
	}
	tr.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: p.ln.Addr().String()})

	ctx := context.Background()
	if _, err := c.Get(ctx, httpsScheme+hostC+"/", 0); err == nil {
		t.Fatal("disallowed host fetched")
	}
	if n, _ := p.snapshot(); n != 0 {
		t.Fatalf("disallowed host reached the proxy (%d connections)", n)
	}

	if _, err := c.Get(ctx, urlA, 0); err == nil {
		t.Fatal("Get through a refusing proxy succeeded")
	}
	_, reqs := p.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("proxy saw %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodConnect || r.Host != hostA+":443" {
		t.Errorf("proxy saw %s %s, want CONNECT %s:443", r.Method, r.Host, hostA)
	}
	if got := r.Header.Get("User-Agent"); got != ua {
		t.Errorf("CONNECT User-Agent = %q, want %q", got, ua)
	}
}
