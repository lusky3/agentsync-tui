package netxtest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransportMapsNamedHostsToServer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "host="+r.Host)
	}))
	t.Cleanup(srv.Close)
	hc := &http.Client{Transport: Transport(srv, "registry.test", "skills.sh")}

	for _, host := range []string{"registry.test", "skills.sh"} {
		resp, err := hc.Get("https://" + host + "/x")
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if got := string(b); got != "host="+host {
			t.Errorf("%s: body %q", host, got)
		}
	}
}

func TestTransportRefusesUnmappedHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unmapped host reached the server")
	}))
	t.Cleanup(srv.Close)
	hc := &http.Client{Transport: Transport(srv, "registry.test")}

	resp, err := hc.Get("https://other.test/x")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("unmapped host: want error")
	}
	if !strings.Contains(err.Error(), "netxtest") {
		t.Errorf("error %q does not name netxtest", err)
	}
}
