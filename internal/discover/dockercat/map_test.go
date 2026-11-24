package dockercat_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/discover/dockercat"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpreg"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpsrc"
)

const (
	testID     = "srv"
	testKey    = "srv"
	image      = "mcp/x@sha256:" + digest64
	toolsAll   = "--tools=all"
	tokenName  = "TOKEN"
	authHeader = "Authorization"
	remoteURL  = "https://mcp.example.com/mcp"
	draftFail  = "ToDraft: %v"
	runFlag    = "run"
	rmFlag     = "--rm"
	envFlag    = "-e"
	iFlag      = "-i"
	stdioWord  = "stdio"
)

// fixtureDigest is the made-up digest testdata/catalog.yaml gives key.
func fixtureDigest(key string) string {
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:])
}

func draftOf(t *testing.T, e mcpsrc.Entry) *mcpreg.Draft {
	t.Helper()
	if len(e.Installs) != 1 || e.Installs[0].Draft == nil {
		t.Fatalf("%s: installs %+v, want one installable", e.ID, e.Installs)
	}
	return e.Installs[0].Draft
}

func reasonOf(t *testing.T, e mcpsrc.Entry) string {
	t.Helper()
	if len(e.Installs) != 1 || e.Installs[0].Draft != nil {
		t.Fatalf("%s: installs %+v, want one unsupported", e.ID, e.Installs)
	}
	return e.Installs[0].Unsupported
}

func server(mod func(*dockercat.Entry)) dockercat.Entry {
	e := dockercat.Entry{Kind: dockercat.KindServer, Image: image}
	if mod != nil {
		mod(&e)
	}
	return e
}

func remote(mod func(*dockercat.Entry)) dockercat.Entry {
	e := dockercat.Entry{Kind: dockercat.KindRemote, Remote: dockercat.Remote{Transport: "streamable-http", URL: remoteURL}}
	if mod != nil {
		mod(&e)
	}
	return e
}

// refuse asserts ToDraft refuses e with reason want.
func refuse(t *testing.T, name string, e dockercat.Entry, want string) {
	t.Helper()
	d, err := dockercat.ToDraft(testKey, testID, e)
	if !errors.Is(err, mcpreg.ErrUnsupported) || err.Error() != want {
		t.Errorf("%s: err = %v, want %q (draft %+v)", name, err, want, d)
	}
}

func accept(t *testing.T, e dockercat.Entry) mcpreg.Draft {
	t.Helper()
	d, err := dockercat.ToDraft(testKey, testID, e)
	if err != nil {
		t.Fatalf(draftFail, err)
	}
	return d
}

func TestBraveMapsToDockerRunWithSecretRef(t *testing.T) {
	d := draftOf(t, find(t, searchSample(t), keyBrave))
	img := "mcp/brave-search@sha256:" + fixtureDigest(keyBrave)
	want := []string{runFlag, iFlag, rmFlag, envFlag, "BRAVE_API_KEY", img}
	if d.Spec.Type != stdioWord || d.Spec.Command != "docker" || !slices.Equal(d.Spec.Args, want) {
		t.Errorf("spec = %+v, want docker %q", d.Spec, want)
	}
	if got := d.Spec.Env["BRAVE_API_KEY"]; got != "${secret:brave.brave_api_key}" || len(d.Spec.Env) != 1 {
		t.Errorf("env = %v", d.Spec.Env)
	}
	if d.Spec.ID != keyBrave || len(d.Prompts) != 0 || len(d.Trust) != 0 {
		t.Errorf("id %q, prompts %v, draft trust %v", d.Spec.ID, d.Prompts, d.Trust)
	}
	if want := "docker catalog: brave (digest " + fixtureDigest(keyBrave)[:12] + ")"; d.Provenance != want {
		t.Errorf("provenance = %q, want %q", d.Provenance, want)
	}
}

func TestDisableNetworkAddsNetworkNone(t *testing.T) {
	d := draftOf(t, find(t, searchSample(t), keyTime))
	want := []string{runFlag, iFlag, rmFlag, "--network", "none", envFlag, "LOCAL_TIMEZONE", "mcp/time@sha256:" + fixtureDigest(keyTime)}
	if !slices.Equal(d.Spec.Args, want) || d.Spec.Env["LOCAL_TIMEZONE"] != "UTC" {
		t.Errorf("args %q env %v", d.Spec.Args, d.Spec.Env)
	}
}

func TestFixedCommandAppendedAfterImage(t *testing.T) {
	d := draftOf(t, find(t, searchSample(t), keyStripe))
	img := "mcp/stripe@sha256:" + fixtureDigest(keyStripe)
	if n := len(d.Spec.Args); n < 2 || d.Spec.Args[n-2] != img || d.Spec.Args[n-1] != toolsAll {
		t.Errorf("args = %q, want … %s %s", d.Spec.Args, img, toolsAll)
	}
	for _, cmd := range [][]string{{"-v"}, {stdioWord}, {toolsAll, "a,b", "--port=8080"}} {
		d := accept(t, server(func(e *dockercat.Entry) { e.Command = cmd }))
		if got := d.Spec.Args[len(d.Spec.Args)-len(cmd):]; !slices.Equal(got, cmd) {
			t.Errorf("args = %q, want suffix %q", d.Spec.Args, cmd)
		}
	}
}

func TestUnsafeFixedCommandUnsupported(t *testing.T) {
	for _, c := range []string{toolsAll + ";id", "$(id)", "--", "-", "a b", "", "`id`", "a\x00b"} {
		refuse(t, c, server(func(e *dockercat.Entry) { e.Command = []string{toolsAll, c} }), dockercat.ReasonCommand)
	}
}

func TestContext7RemoteMapsToHTTPWithSecretHeader(t *testing.T) {
	d := draftOf(t, find(t, searchSample(t), keyContext7))
	s := d.Spec
	if s.Type != "http" || s.URL != "https://mcp.context7.com/mcp" || s.Command != "" || len(s.Args) != 0 || len(s.Env) != 0 {
		t.Errorf("spec = %+v", s)
	}
	want := []string{"CONTEXT7_API_KEY: ${secret:context7.context7_api_key}"}
	if !slices.Equal(s.Headers, want) {
		t.Errorf("headers = %q, want %q", s.Headers, want)
	}
	if d.Provenance != "docker catalog: context7" {
		t.Errorf("provenance = %q", d.Provenance)
	}
}

func TestSSERemoteAndBearerPrefixKept(t *testing.T) {
	d := accept(t, remote(func(e *dockercat.Entry) {
		e.Remote.Transport = "sse"
		e.Remote.Headers = []dockercat.Header{
			{Name: authHeader, Value: "Bearer ${" + tokenName + "}"},
			{Name: "X-Plain", Value: "v1"},
			{Name: "X-Empty"},
		}
	}))
	want := []string{authHeader + ": Bearer ${secret:srv.token}", "X-Plain: v1"}
	if d.Spec.Type != "sse" || !slices.Equal(d.Spec.Headers, want) {
		t.Errorf("type %q headers %q, want %q", d.Spec.Type, d.Spec.Headers, want)
	}
}

func TestUnsafeHeaderUnsupported(t *testing.T) {
	for _, v := range []string{"${A-B}", "$X", "${X", "Bearer {X}", "${X}}", "a $" + tokenName, "${}", "x\x01"} {
		refuse(t, v, remote(func(e *dockercat.Entry) {
			e.Remote.Headers = []dockercat.Header{{Name: authHeader, Value: v}}
		}), dockercat.ReasonHeader)
	}
	refuse(t, "template", remote(func(e *dockercat.Entry) {
		e.Remote.Headers = []dockercat.Header{{Name: authHeader, Value: "{{x.token}}"}}
	}), dockercat.ReasonTemplate)
	refuse(t, "header name", remote(func(e *dockercat.Entry) {
		e.Remote.Headers = []dockercat.Header{{Name: "-H", Value: "v"}}
	}), dockercat.ReasonHeader)
	refuse(t, "collision", remote(func(e *dockercat.Entry) {
		e.Remote.Headers = []dockercat.Header{{Name: "A", Value: "${API_KEY}"}, {Name: "B", Value: "${api_key}"}}
	}), dockercat.ReasonCollision)
}

func TestFilesystemTemplateIsUnsupportedNotRendered(t *testing.T) {
	e := find(t, searchSample(t), keyFS)
	if r := reasonOf(t, e); r != dockercat.ReasonTemplate {
		t.Errorf("reason = %q", r)
	}
	for name, mod := range map[string]func(*dockercat.Entry){
		"env value":  func(e *dockercat.Entry) { e.Env = []dockercat.EnvVar{{Name: "P", Value: "{{x.y}}"}} },
		"secret env": func(e *dockercat.Entry) { e.Secrets = []dockercat.Secret{{Name: "s", Env: "{{x}}"}} },
		"command":    func(e *dockercat.Entry) { e.Command = []string{"{{filesystem.paths|volume|into}}"} },
	} {
		refuse(t, name, server(mod), dockercat.ReasonTemplate)
	}
	refuse(t, "url", remote(func(e *dockercat.Entry) { e.Remote.URL = "https://{{x.host}}/mcp" }), dockercat.ReasonTemplate)
}

func TestImageWithoutDigestUnsupported(t *testing.T) {
	for _, img := range []string{"mcp/brave-search", "mcp/brave-search:latest", "mcp/x@sha256:abc", "mcp/x@sha256:" + strings.ToUpper(digest64), ""} {
		refuse(t, img, server(func(e *dockercat.Entry) { e.Image = img }), dockercat.ReasonNoDigest)
	}
	e := searchBody(t, []byte("registry:\n  nodigest:\n    type: server\n    image: mcp/x:1\n"), "")
	if r := reasonOf(t, e.Entries[0]); r != dockercat.ReasonNoDigest {
		t.Errorf("reason = %q", r)
	}
}

func TestBadImageReferenceUnsupported(t *testing.T) {
	for _, img := range []string{"-v/x", "--privileged", "evil.-host.io/x", "Mcp/X", "mcp/x y", "mcp/x;id", "mcp//x"} {
		refuse(t, img, server(func(e *dockercat.Entry) { e.Image = img + "@sha256:" + digest64 }), dockercat.ReasonImage)
	}
	for _, img := range []string{"mcp/x", "docker.io/mcp/x:1.2", "localhost:5000/a/b_c"} {
		accept(t, server(func(e *dockercat.Entry) { e.Image = img + "@sha256:" + digest64 }))
	}
}

func TestPociUnsupported(t *testing.T) {
	if r := reasonOf(t, find(t, searchSample(t), keyCurl)); r != dockercat.ReasonPoci {
		t.Errorf("reason = %q", r)
	}
	refuse(t, "unknown kind", dockercat.Entry{Kind: "plugin", Image: image}, dockercat.ReasonKind)
}

func TestOAuthRemoteUnsupported(t *testing.T) {
	if r := reasonOf(t, find(t, searchSample(t), keyLinear)); r != dockercat.ReasonOAuth {
		t.Errorf("reason = %q", r)
	}
	for _, oauth := range []string{"oauth: {}", "oauth: [x]", "oauth: yes"} {
		body := "registry:\n  r:\n    type: remote\n    " + oauth + "\n    remote: {transport_type: sse, url: 'https://a.example.com'}\n"
		if r := reasonOf(t, searchBody(t, []byte(body), "").Entries[0]); r != dockercat.ReasonOAuth {
			t.Errorf("%s: reason = %q", oauth, r)
		}
	}
	body := "registry:\n  r:\n    type: remote\n    oauth: null\n    remote: {transport_type: sse, url: 'https://a.example.com'}\n"
	draftOf(t, searchBody(t, []byte(body), "").Entries[0])
}

func TestHttpURLRemoteUnsupported(t *testing.T) {
	for _, u := range []string{"http://mcp.example.com/mcp", "https://user:pw@mcp.example.com/", "https:///x", "https://a.example.com/${TOKEN}", "https://a.example.com/x y", "ftp://x", ""} {
		refuse(t, u, remote(func(e *dockercat.Entry) { e.Remote.URL = u }), dockercat.ReasonURL)
	}
	refuse(t, "transport", remote(func(e *dockercat.Entry) { e.Remote.Transport = stdioWord }), dockercat.ReasonTransport)
}

func TestTrustLines(t *testing.T) {
	p := searchSample(t)
	brave := find(t, p, keyBrave)
	want := []string{dockercat.TrustDigestPrefix + fixtureDigest(keyBrave)[:12], dockercat.TrustNotVerified, dockercat.TrustDocker}
	if !slices.Equal(brave.Trust, want) {
		t.Errorf("brave trust = %q, want %q", brave.Trust, want)
	}
	if got := mcpsrc.InstallTrust(brave, brave.Installs[0]); !slices.Equal(got, want) {
		t.Errorf("install trust = %q", got)
	}
	noDigest := dockercat.EntryTrust(server(func(e *dockercat.Entry) { e.Image = "mcp/x:1"; e.AllowHosts = []string{"a:443"} }))
	if want := []string{dockercat.TrustNotVerified, dockercat.TrustDocker, dockercat.TrustAllowHosts}; !slices.Equal(noDigest, want) {
		t.Errorf("unsupported image trust = %q, want %q", noDigest, want)
	}
	if got := find(t, p, keyLinear).Trust; !slices.Equal(got, []string{dockercat.TrustRemote}) {
		t.Errorf("remote trust = %q", got)
	}
}

func TestAllowHostsAddsTrustWarning(t *testing.T) {
	p := searchSample(t)
	if !slices.Contains(find(t, p, keyStripe).Trust, dockercat.TrustAllowHosts) {
		t.Error("stripe declares allowHosts but has no warning")
	}
	if slices.Contains(find(t, p, keyBrave).Trust, dockercat.TrustAllowHosts) {
		t.Error("brave declares no allowHosts but has the warning")
	}
}

func TestEnvRules(t *testing.T) {
	envs := func(v ...dockercat.EnvVar) dockercat.Entry {
		return server(func(e *dockercat.Entry) { e.Env = v })
	}
	secrets := func(s ...dockercat.Secret) dockercat.Entry {
		return server(func(e *dockercat.Entry) { e.Secrets = s })
	}
	for _, n := range []string{"A-B", "1X", "X Y", ""} {
		refuse(t, "env "+n, envs(dockercat.EnvVar{Name: n, Value: "v"}), dockercat.ReasonEnvName)
		refuse(t, "secret "+n, secrets(dockercat.Secret{Env: n}), dockercat.ReasonEnvName)
	}
	for _, v := range []string{"a,b", `a\b`, "${secret:other.key}", "a{b", "x\x01", strings.Repeat("v", mcpreg.MaxValue+1)} {
		refuse(t, v, envs(dockercat.EnvVar{Name: "V", Value: v}), dockercat.ReasonEnvValue)
	}
	refuse(t, "dup env", envs(dockercat.EnvVar{Name: "V"}, dockercat.EnvVar{Name: "V"}), dockercat.ReasonEnvName)
	refuse(t, "secret and env", server(func(e *dockercat.Entry) {
		e.Secrets = []dockercat.Secret{{Env: "V"}}
		e.Env = []dockercat.EnvVar{{Name: "V", Value: "x"}}
	}), dockercat.ReasonEnvName)
	refuse(t, "snake collision", secrets(dockercat.Secret{Env: "API_KEY"}, dockercat.Secret{Env: "api_key"}), dockercat.ReasonCollision)
	refuse(t, "no key", secrets(dockercat.Secret{Env: "_"}), dockercat.ReasonSecretName)
	d := accept(t, envs(dockercat.EnvVar{Name: "EMPTY"}))
	if v, ok := d.Spec.Env["EMPTY"]; !ok || v != "" {
		t.Errorf("env = %v", d.Spec.Env)
	}
}

func TestSecretsNeverInArgv(t *testing.T) {
	d := accept(t, server(func(e *dockercat.Entry) {
		e.Secrets = []dockercat.Secret{{Name: "x.token", Env: tokenName}}
		e.Env = []dockercat.EnvVar{{Name: "MODE", Value: "fast"}}
	}))
	want := []string{runFlag, iFlag, rmFlag, envFlag, tokenName, envFlag, "MODE", image}
	if !slices.Equal(d.Spec.Args, want) {
		t.Errorf("args = %q, want %q", d.Spec.Args, want)
	}
	for _, a := range d.Spec.Args {
		if strings.Contains(a, "secret") || strings.Contains(a, "=") {
			t.Errorf("argv token %q carries a value", a)
		}
	}
}

func TestVolumesAndCapsUnsupported(t *testing.T) {
	refuse(t, "volumes", server(func(e *dockercat.Entry) { e.Volumes = []string{"/data:/data"} }), dockercat.ReasonVolumes)
	refuse(t, "args", server(func(e *dockercat.Entry) { e.Command = make([]string, mcpreg.MaxArgs+1) }), dockercat.ReasonTooMany)
	refuse(t, "env", server(func(e *dockercat.Entry) { e.Env = make([]dockercat.EnvVar, mcpreg.MaxEnv+1) }), dockercat.ReasonTooMany)
	refuse(t, "headers", remote(func(e *dockercat.Entry) { e.Remote.Headers = make([]dockercat.Header, mcpreg.MaxHeaders+1) }), dockercat.ReasonTooMany)
}

func TestInvalidIDRefused(t *testing.T) {
	if _, err := dockercat.ToDraft(testKey, "-bad id", server(nil)); !errors.Is(err, mcpreg.ErrInvalidID) {
		t.Errorf(errWant, err, mcpreg.ErrInvalidID)
	}
	e := searchBody(t, []byte("registry:\n  '...':\n    type: server\n    image: "+image+"\n"), "").Entries[0]
	if r := reasonOf(t, e); r != dockercat.ReasonNoID {
		t.Errorf("reason = %q", r)
	}
}

func TestEveryInstallableDraftPassesMcpAdd(t *testing.T) {
	n := 0
	for _, e := range searchSample(t).Entries {
		for _, in := range e.Installs {
			if in.Draft == nil {
				continue
			}
			n++
			if _, err := actions.McpAdd(in.Draft.Spec); err != nil {
				t.Errorf("%s: McpAdd: %v", e.ID, err)
			}
			if strings.Contains(strings.Join(in.Draft.Spec.Args, " ")+strings.Join(in.Draft.Spec.Headers, " "), "{{") {
				t.Errorf("%s: template reached the draft", e.ID)
			}
		}
	}
	if n != 4 {
		t.Errorf("%d installable entries, want 4", n)
	}
}
