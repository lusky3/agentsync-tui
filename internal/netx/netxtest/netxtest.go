// Package netxtest is the host-mapping transport the network tests share:
// requests for each named host are dialled to one httptest TLS server whose
// certificate is trusted, so code that talks to real host names (the MCP
// Registry, skills.sh, codeload.github.com) runs against a local fake. Any
// other host fails to dial, so a test can never reach the real network.
// Only _test.go files import it.
package netxtest

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
)

// certName is a name the httptest certificate is issued for; every mapped
// host is verified against it, whatever name the request used.
const certName = "example.com"

// Transport returns an http.RoundTripper that dials srv (a TLS server from
// httptest.NewTLSServer) for each of hosts and trusts srv's certificate.
// Host names match case-insensitively; the port in the request is ignored.
// Response decompression stays on, as in http.DefaultTransport.
func Transport(srv *httptest.Server, hosts ...string) http.RoundTripper {
	mapped := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		mapped[strings.ToLower(h)] = true
	}
	addr := srv.Listener.Addr().String()
	var d net.Dialer
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DisableCompression = false
	tr.DialContext = func(ctx context.Context, network, hostport string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(hostport)
		if err != nil {
			return nil, err
		}
		if !mapped[strings.ToLower(host)] {
			return nil, fmt.Errorf("netxtest: no mapping for host %q", host)
		}
		return d.DialContext(ctx, network, addr)
	}
	cfg := tr.TLSClientConfig
	if cfg == nil {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cfg = cfg.Clone()
	cfg.ServerName = certName
	tr.TLSClientConfig = cfg
	return tr
}
