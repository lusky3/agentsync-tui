package mcpreg_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpreg"
)

const (
	mapID        = "demo"
	mapName      = "io.example/demo"
	mapVersion   = "1.2.3"
	npmPkg       = "@scope/demo-mcp"
	pypiPkg      = "demo-mcp"
	ociImage     = "ghcr.io/example/demo"
	stdio        = "stdio"
	typeNPM      = "npm"
	typePyPI     = "pypi"
	typeOCI      = "oci"
	streamable   = "streamable-http"
	typeSSE      = "sse"
	positional   = "positional"
	named        = "named"
	apiKey       = "API_KEY"
	apiKeyVar    = "api_key"
	authHeader   = "Authorization"
	remoteURL    = "https://mcp.example.com/mcp"
	demoRef      = "${secret:demo.api_key}"
	mutableLine  = "image tag is mutable (not digest-pinned)"
	wantUnsup    = "err = %v, want ErrUnsupported"
	wantUnsafe   = "err = %v, want ErrUnsafeIdentifier"
	refusedFmt   = "ToDraft(%q) = %+v, want a refusal"
	regionVar    = "region"
	modeFlag     = "--mode"
	modeEnv      = "MODE"
	vFlag        = "-v"
	npx          = "npx"
	commaVal     = "a,b"
	readOnly     = "read-only"
	backslashVal = `a\b`
	spaceVal     = "a b"
	stealRef     = "${secret:github.token}"
	headersFmt   = "Headers = %q, want %q"
	logLevel     = "LOG_LEVEL"
	newlineVal   = "a\nb"
	tabVal       = "a\tb"
	braceVal     = "{x}"
)

// pkgOf is a stdio package of the given registry type.
func pkgOf(registryType, identifier string) mcpreg.Package {
	return mcpreg.Package{RegistryType: registryType, Identifier: identifier, Version: mapVersion, Transport: stdio}
}

func serverWith(p ...mcpreg.Package) mcpreg.Server {
	return mcpreg.Server{Name: mapName, Version: mapVersion, Packages: p}
}

func remoteServer(r mcpreg.Remote) mcpreg.Server {
	return mcpreg.Server{Name: mapName, Version: mapVersion, Remotes: []mcpreg.Remote{r}}
}

// only returns the single choice s offers.
func only(t *testing.T, s mcpreg.Server) mcpreg.Choice {
	t.Helper()
	cs := mcpreg.Choices(s)
	if len(cs) != 1 {
		t.Fatalf("Choices = %+v, want exactly one", cs)
	}
	return cs[0]
}

// draft maps s's only choice with id mapID and checks that the spec is
// one actions.McpAdd accepts.
func draft(t *testing.T, s mcpreg.Server) mcpreg.Draft {
	t.Helper()
	return draftID(t, s, mapID)
}

func draftID(t *testing.T, s mcpreg.Server, id string) mcpreg.Draft {
	t.Helper()
	d, err := mcpreg.ToDraft(s, only(t, s), id)
	if err != nil {
		t.Fatalf("ToDraft: %v", err)
	}
	if _, err := actions.McpAdd(d.Spec); err != nil {
		t.Fatalf("McpAdd(%+v): %v", d.Spec, err)
	}
	return d
}

// refused maps s's only choice and returns the error it must produce.
func refused(t *testing.T, s mcpreg.Server) error {
	t.Helper()
	d, err := mcpreg.ToDraft(s, only(t, s), mapID)
	if err == nil {
		t.Fatalf(refusedFmt, s.Name, d)
	}
	return err
}

func unsupported(t *testing.T, s mcpreg.Server, reason string) {
	t.Helper()
	err := refused(t, s)
	if !errors.Is(err, mcpreg.ErrUnsupported) {
		t.Fatalf(wantUnsup, err)
	}
	if reason != "" && !strings.Contains(err.Error(), reason) {
		t.Errorf("err = %q, want it to say %q", err, reason)
	}
}

func checkStdio(t *testing.T, d mcpreg.Draft, command string, args ...string) {
	t.Helper()
	s := d.Spec
	if s.ID != mapID || s.Type != stdio || s.Command != command || s.URL != "" || !slices.Equal(s.Args, args) {
		t.Errorf("spec = %+v, want stdio %s %q", s, command, args)
	}
}

func TestNPMMapping(t *testing.T) {
	for _, id := range []string{npmPkg, "demo-mcp", "a.b_c"} {
		d := draft(t, serverWith(pkgOf(typeNPM, id)))
		checkStdio(t, d, npx, "-y", id+"@"+mapVersion)
		if d.Provenance != "official registry: "+mapName+"@"+mapVersion {
			t.Errorf("Provenance = %q", d.Provenance)
		}
		if slices.Contains(d.Trust, mutableLine) {
			t.Errorf("npm draft has the OCI trust line: %q", d.Trust)
		}
	}
}

func TestPyPIMapping(t *testing.T) {
	for _, id := range []string{pypiPkg, "Demo_MCP.x", "a"} {
		checkStdio(t, draft(t, serverWith(pkgOf(typePyPI, id))), "uvx", id+"=="+mapVersion)
	}
}

func TestOCIMapping(t *testing.T) {
	for _, id := range []string{ociImage, "example/demo", "localhost:5000/a/b-c", "demo"} {
		checkStdio(t, draft(t, serverWith(pkgOf(typeOCI, id))), "docker", "run", "-i", "--rm", id+":"+mapVersion)
	}
}

func TestOCITagGetsMutableTrustLine(t *testing.T) {
	d := draft(t, serverWith(pkgOf(typeOCI, ociImage)))
	if !slices.Contains(d.Trust, mutableLine) {
		t.Errorf("Trust = %q, want %q", d.Trust, mutableLine)
	}
}

func TestRemoteHTTPAndSSE(t *testing.T) {
	for wire, want := range map[string]string{streamable: "http", typeSSE: typeSSE} {
		d := draft(t, remoteServer(mcpreg.Remote{Type: wire, URL: remoteURL}))
		s := d.Spec
		if s.Type != want || s.URL != remoteURL || s.Command != "" || len(s.Args) != 0 || len(s.Headers) != 0 {
			t.Errorf("%s: spec = %+v, want type %s url %s", wire, s, want, remoteURL)
		}
	}
}

func TestRemoteWithoutHTTPSUnsupported(t *testing.T) {
	for _, u := range []string{"http://mcp.example.com/mcp", "ftp://x", "HTTPS://x", "mcp.example.com", "https://a b/c"} {
		unsupported(t, remoteServer(mcpreg.Remote{Type: streamable, URL: u}), "")
	}
}

func TestSecretEnvBecomesSecretRef(t *testing.T) {
	p := pkgOf(typeNPM, npmPkg)
	p.Env = []mcpreg.EnvVar{{Name: apiKey, Secret: true, Required: true, Default: "leaked-default"}}
	d := draft(t, serverWith(p))
	if got := d.Spec.Env[apiKey]; got != demoRef {
		t.Errorf("Env[%s] = %q, want %q", apiKey, got, demoRef)
	}
	if len(d.Prompts) != 0 {
		t.Errorf("Prompts = %+v, want none (the secret is a reference)", d.Prompts)
	}
	a, _ := actions.McpAdd(d.Spec)
	if strings.Contains(strings.Join(a.Argv, " "), "leaked-default") {
		t.Errorf("argv %q carries a value for the secret", a.Argv)
	}
}

func TestRequiredPlainEnvBecomesPrompt(t *testing.T) {
	p := pkgOf(typeNPM, npmPkg)
	p.Env = []mcpreg.EnvVar{
		{Name: "BUCKET", Required: true, Description: "bucket name"},
		{Name: "OPTIONAL_THING"},
	}
	d := draft(t, serverWith(p))
	want := []mcpreg.Prompt{{Name: "BUCKET", Required: true, Description: "bucket name"}}
	if !reflect.DeepEqual(d.Prompts, want) || len(d.Spec.Env) != 0 {
		t.Errorf("Prompts = %+v, Env = %v; want %+v and no env", d.Prompts, d.Spec.Env, want)
	}
}

func TestEnvDefaultBecomesPromptDefault(t *testing.T) {
	p := pkgOf(typeNPM, npmPkg)
	p.Env = []mcpreg.EnvVar{{Name: logLevel, Required: true, Default: "info"}}
	d := draft(t, serverWith(p))
	if len(d.Prompts) != 1 || d.Prompts[0].Default != "info" || !d.Prompts[0].Required {
		t.Errorf("Prompts = %+v, want LOG_LEVEL required with default info", d.Prompts)
	}
}

func TestFixedEnvValueIsLiteral(t *testing.T) {
	p := pkgOf(typeNPM, npmPkg)
	p.Env = []mcpreg.EnvVar{{Name: modeEnv, Value: "strict"}, {Name: "TOKEN", Value: "public", Secret: true}}
	d := draft(t, serverWith(p))
	if d.Spec.Env[modeEnv] != "strict" || d.Spec.Env["TOKEN"] != "public" {
		t.Errorf("Env = %v, want the fixed values", d.Spec.Env)
	}
}

func TestFixedEnvValueWithCommaBackslashOrBraceUnsupported(t *testing.T) {
	for _, v := range []string{commaVal, backslashVal, braceVal, stealRef, "$HOME"} {
		p := pkgOf(typeNPM, npmPkg)
		p.Env = []mcpreg.EnvVar{{Name: modeEnv, Value: v}}
		unsupported(t, serverWith(p), "")
	}
}

func TestInvalidEnvNameUnsupported(t *testing.T) {
	for _, name := range []string{"A-B", "1X", "X Y", ""} {
		p := pkgOf(typeNPM, npmPkg)
		p.Env = []mcpreg.EnvVar{{Name: name, Secret: true}}
		unsupported(t, serverWith(p), "")
	}
}

func TestSuggestIDCases(t *testing.T) {
	none := func(string) bool { return false }
	cases := map[string]string{
		"com.pulsemcp/remote-filesystem": "remote-filesystem",
		"io.github.a/B C":                "b-c",
		"../evil":                        "evil",
		"a/ -x- ":                        "x",
		"io.example/Ünïcode":             "n-code",
		"a/..":                           "",
		"a/":                             "",
		"":                               "",
		"a/---":                          "",
		"plain.name":                     "plain.name",
	}
	for in, want := range cases {
		if got := mcpreg.SuggestID(in, none); got != want {
			t.Errorf("SuggestID(%q) = %q, want %q", in, got, want)
		}
	}
	if got := mcpreg.SuggestID("a/x", nil); got != "x" {
		t.Errorf("SuggestID with nil taken = %q, want x", got)
	}
}

func TestSuggestIDCollisions(t *testing.T) {
	taken := map[string]bool{"x": true, "x-2": true}
	if got := mcpreg.SuggestID("io.example/x", func(s string) bool { return taken[s] }); got != "x-3" {
		t.Errorf("SuggestID = %q, want x-3", got)
	}
	if got := mcpreg.SuggestID("io.example/x", func(string) bool { return true }); got != "" {
		t.Errorf("SuggestID with every id taken = %q, want empty", got)
	}
}

func TestSuggestIDIsAlwaysValidAndBounded(t *testing.T) {
	long := "io.example/" + strings.Repeat("ab", 200)
	got := mcpreg.SuggestID(long, func(s string) bool { return !strings.HasSuffix(s, "-99") })
	if !actions.ValidID(got) || !strings.HasSuffix(got, "-99") {
		t.Errorf("SuggestID(long) = %q, want a valid id ending -99", got)
	}
}

func TestSecretRefLowerSnake(t *testing.T) {
	cases := map[string]string{
		apiKey:       demoRef,
		"X-Api-Key":  "${secret:demo.x_api_key}",
		"a__b":       "${secret:demo.a_b}",
		"_x_":        "${secret:demo.x}",
		"apiKey":     "${secret:demo.apikey}",
		"Bearer.Tok": "${secret:demo.bearer_tok}",
		"___":        "",
		"":           "",
	}
	for in, want := range cases {
		if got := mcpreg.SecretRef(mapID, in); got != want {
			t.Errorf("SecretRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSecretRefsAreNamespacedByServer(t *testing.T) {
	p := pkgOf(typeNPM, npmPkg)
	p.Env = []mcpreg.EnvVar{{Name: apiKey, Secret: true}}
	s := serverWith(p)
	a, b := draftID(t, s, "a"), draftID(t, s, "b")
	if a.Spec.Env[apiKey] != "${secret:a.api_key}" || b.Spec.Env[apiKey] != "${secret:b.api_key}" {
		t.Errorf("refs = %q, %q; want one per server", a.Spec.Env[apiKey], b.Spec.Env[apiKey])
	}
}

func TestSecretNameCollisionUnsupported(t *testing.T) {
	p := pkgOf(typeNPM, npmPkg)
	p.Env = []mcpreg.EnvVar{{Name: apiKey, Secret: true}, {Name: "API__KEY", Secret: true}}
	unsupported(t, serverWith(p), "")

	r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{
		{Name: "X-Api-Key", Secret: true},
		{Name: "Other", Value: "{x_api_key}", Variables: []mcpreg.EnvVar{{Name: "x_api_key", Secret: true}}},
	}}
	unsupported(t, remoteServer(r), "")
}

func TestSameSecretNameReusedIsOneRef(t *testing.T) {
	v := []mcpreg.EnvVar{{Name: apiKeyVar, Secret: true}}
	r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{
		{Name: authHeader, Value: "Bearer {api_key}", Variables: v},
		{Name: "X-Key", Value: "{api_key}", Variables: v},
	}}
	d := draft(t, remoteServer(r))
	want := []string{"Authorization: Bearer " + demoRef, "X-Key: " + demoRef}
	if !slices.Equal(d.Spec.Headers, want) {
		t.Errorf(headersFmt, d.Spec.Headers, want)
	}
}

// D27: secrets go only in headers, never in a URL path or query.
func TestSecretURLVariableUnsupported(t *testing.T) {
	v := []mcpreg.EnvVar{{Name: apiKeyVar, Secret: true}}
	for _, u := range []string{"https://mcp.example.com/{api_key}", "https://mcp.example.com/mcp?key={api_key}"} {
		unsupported(t, remoteServer(mcpreg.Remote{Type: streamable, URL: u, Variables: v}), "")
	}
}

func TestRemoteURLShapesUnsupported(t *testing.T) {
	urls := []string{"https:///etc/passwd", "https://@evil", "https://evil.com@x/x", "https://user:pw@x.example/",
		"https://", "https://x.example/" + strings.Repeat("a", 3000)}
	for _, u := range urls {
		unsupported(t, remoteServer(mcpreg.Remote{Type: streamable, URL: u}), "")
	}
	// A defaulted variable cannot build a userinfo or empty host either.
	v := []mcpreg.EnvVar{{Name: regionVar, Default: "evil.com@x"}}
	unsupported(t, remoteServer(mcpreg.Remote{Type: streamable, URL: "https://{region}/mcp", Variables: v}), "")
}

func TestBearerTemplateHeaderBecomesSecretRef(t *testing.T) {
	r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{{
		Name: authHeader, Value: "Bearer {api_key}", Required: true, Secret: true,
		Variables: []mcpreg.EnvVar{{Name: apiKeyVar, Secret: true, Required: true}},
	}}}
	d := draft(t, remoteServer(r))
	want := []string{"Authorization: Bearer " + demoRef}
	if !slices.Equal(d.Spec.Headers, want) {
		t.Errorf(headersFmt, d.Spec.Headers, want)
	}
}

func TestSecretHeaderWithoutValueBecomesSecretRef(t *testing.T) {
	r := mcpreg.Remote{Type: typeSSE, URL: remoteURL, Headers: []mcpreg.Header{
		{Name: "X-API-Key", Secret: true, Required: true},
		{Name: "X-Optional"},
		{Name: "X-Fixed", Value: "v1, v2"},
	}}
	d := draft(t, remoteServer(r))
	want := []string{"X-API-Key: ${secret:demo.x_api_key}", "X-Fixed: v1, v2"}
	if !slices.Equal(d.Spec.Headers, want) {
		t.Errorf(headersFmt, d.Spec.Headers, want)
	}
}

func TestPlainRequiredHeaderWithoutValueUnsupported(t *testing.T) {
	r := mcpreg.Remote{Type: typeSSE, URL: remoteURL, Headers: []mcpreg.Header{{Name: "X-Tenant", Required: true}}}
	unsupported(t, remoteServer(r), "")
}

func TestInvalidHeaderNameUnsupported(t *testing.T) {
	for _, name := range []string{"", "-X", "X Y", "X:Y", ":x"} {
		r := mcpreg.Remote{Type: typeSSE, URL: remoteURL, Headers: []mcpreg.Header{{Name: name, Value: "v"}}}
		unsupported(t, remoteServer(r), "")
	}
}

func TestUndeclaredPlaceholderUnsupported(t *testing.T) {
	vars := []mcpreg.EnvVar{{Name: regionVar}} // declared, but no default and not secret
	urls := []string{
		"https://{tenant}.example.com/mcp", "https://{region}.example.com/mcp",
		"https://x/{", "https://x/}", "https://x/{a{b}}", "https://x/{}",
	}
	for _, u := range urls {
		unsupported(t, remoteServer(mcpreg.Remote{Type: streamable, URL: u, Variables: vars}), "")
	}
	r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{{Name: authHeader, Value: "Bearer {tok}"}}}
	unsupported(t, remoteServer(r), "")
}

func TestLiteralSecretReferenceFromRegistryRefused(t *testing.T) {
	// A record must not be able to point a new server at another server's
	// vault entry by writing a reference itself.
	steal := stealRef
	r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{{Name: authHeader, Value: "Bearer " + steal}}}
	unsupported(t, remoteServer(r), "")
	unsupported(t, remoteServer(mcpreg.Remote{Type: streamable, URL: remoteURL + "?k=" + steal}), "")
	// No braces at all: only the '$' rule stops these.
	for _, v := range []string{"Bearer $GITHUB_TOKEN", "$" + steal[1:len(steal)-1]} {
		r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{{Name: authHeader, Value: v}}}
		unsupported(t, remoteServer(r), "")
	}
	unsupported(t, remoteServer(mcpreg.Remote{Type: streamable, URL: remoteURL + "?k=$HOME"}), "")
	v := []mcpreg.EnvVar{{Name: regionVar, Default: steal}}
	unsupported(t, remoteServer(mcpreg.Remote{Type: streamable, URL: "https://x/{region}", Variables: v}), "")
}

func TestDeclaredURLVariableWithDefaultResolved(t *testing.T) {
	r := mcpreg.Remote{Type: streamable, URL: "https://{region}.weather.example/mcp",
		Variables: []mcpreg.EnvVar{{Name: regionVar, Required: true, Default: "us"}}}
	if got := draft(t, remoteServer(r)).Spec.URL; got != "https://us.weather.example/mcp" {
		t.Errorf("URL = %q", got)
	}
}

func TestUnsafeVariableDefaultUnsupported(t *testing.T) {
	for _, def := range []string{spaceVal, braceVal, "a}", tabVal} {
		r := mcpreg.Remote{Type: streamable, URL: "https://{region}.example/mcp",
			Variables: []mcpreg.EnvVar{{Name: regionVar, Default: def}}}
		unsupported(t, remoteServer(r), "")
	}
}

func TestFixedPackageArgumentsAppended(t *testing.T) {
	p := pkgOf(typeNPM, npmPkg)
	p.PackageArgs = []mcpreg.Argument{
		{Type: positional, Name: "root_dir", Value: "/data"},
		{Type: named, Name: modeFlag, Value: readOnly, Required: true},
		{Type: named, Name: vFlag, Value: "--level=2"},
		{Type: positional, Name: "list", Value: commaVal},
		{Type: named, Name: "--skipped", Default: "x"},
	}
	d := draft(t, serverWith(p))
	checkStdio(t, d, npx, "-y", npmPkg+"@"+mapVersion, "/data", modeFlag, readOnly, vFlag, "--level=2", commaVal)
	a, _ := actions.McpAdd(d.Spec)
	if !slices.Contains(a.Argv, `-y,`+npmPkg+"@"+mapVersion+`,/data,--mode,read-only,-v,--level=2,a\,b`) {
		t.Errorf("argv = %q, want the comma in a,b escaped", a.Argv)
	}
}

func TestUnsafePackageArgumentUnsupported(t *testing.T) {
	cases := []mcpreg.Argument{
		{Type: positional, Value: spaceVal},
		{Type: positional, Value: "$(id)"},
		{Type: positional, Value: "x;y"},
		{Type: positional, Value: "--"},
		{Type: positional, Value: backslashVal},
		{Type: positional, Value: newlineVal},
		{Type: named, Name: "mode", Value: "x"},
		{Type: named, Name: "--mode=x", Value: "y"},
		{Type: named, Name: modeFlag, Value: "x;id"},
		{Type: "other", Name: modeFlag, Value: "x"},
	}
	for _, a := range cases {
		p := pkgOf(typeNPM, npmPkg)
		p.PackageArgs = []mcpreg.Argument{a}
		unsupported(t, serverWith(p), "argument")
	}
}

func TestTemplatedPackageArgumentUnsupported(t *testing.T) {
	for _, v := range []string{"{dir}", "--root={dir}", "{"} {
		p := pkgOf(typeNPM, npmPkg)
		p.PackageArgs = []mcpreg.Argument{{Type: positional, Value: v}}
		unsupported(t, serverWith(p), "argument variables")
	}
}

func TestRequiredArgumentWithoutValueUnsupported(t *testing.T) {
	p := pkgOf(typePyPI, pypiPkg)
	p.PackageArgs = []mcpreg.Argument{
		{Type: positional, Name: "root_dir", Required: true},
		{Type: named, Name: "--token", Required: true, Default: "x"},
	}
	unsupported(t, serverWith(p), "requires arguments the registry leaves to you: root_dir, --token")
}

func TestOptionalArgumentWithoutValueOmitted(t *testing.T) {
	p := pkgOf(typePyPI, pypiPkg)
	p.PackageArgs = []mcpreg.Argument{{Type: named, Name: modeFlag, Default: readOnly}}
	checkStdio(t, draft(t, serverWith(p)), "uvx", pypiPkg+"=="+mapVersion)
}

func TestRuntimeArgumentsUnsupported(t *testing.T) {
	for _, typ := range []string{typeNPM, typePyPI, typeOCI} {
		id := map[string]string{typeNPM: npmPkg, typePyPI: pypiPkg, typeOCI: ociImage}[typ]
		p := pkgOf(typ, id)
		p.RuntimeArgs = []mcpreg.Argument{{Type: named, Name: vFlag, Value: "/:/host"}}
		unsupported(t, serverWith(p), "sets runtime options this tool does not apply")
	}
}

func TestNonStdioPackageTransportUnsupported(t *testing.T) {
	for _, tr := range []string{streamable, typeSSE, ""} {
		p := pkgOf(typeNPM, npmPkg)
		p.Transport = tr
		unsupported(t, serverWith(p), "transport")
	}
}

// refuseIdentifier asserts each identifier is refused as unsafe for typ.
func refuseIdentifier(t *testing.T, typ string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		err := refused(t, serverWith(pkgOf(typ, id)))
		if !errors.Is(err, mcpreg.ErrUnsafeIdentifier) {
			t.Errorf("%s %q: "+wantUnsafe, typ, id, err)
		}
		if id != "" && strings.Contains(err.Error(), id) {
			t.Errorf("err %q echoes the identifier", err)
		}
	}
}

func TestIdentifierWithLeadingDashRefused(t *testing.T) {
	for _, typ := range []string{typeNPM, typePyPI, typeOCI} {
		refuseIdentifier(t, typ, "--registry=evil", "-x", "-p.x")
	}
	refuseIdentifier(t, typeOCI, "-Pde.x/y", "--privileged.io/img", "a.-b/c", "a-.b/c")
}

// TestDockerOptionSmuggledThroughHostRefused is the review's case: a host
// label starting with '-' made `docker run -i --rm -Pde.x/y:t alpine`.
func TestDockerOptionSmuggledThroughHostRefused(t *testing.T) {
	p := pkgOf(typeOCI, "-Pde.x/y")
	p.Version = "t"
	p.PackageArgs = []mcpreg.Argument{{Type: positional, Value: "alpine"}}
	if err := refused(t, serverWith(p)); !errors.Is(err, mcpreg.ErrUnsafeIdentifier) {
		t.Errorf(wantUnsafe, err)
	}
}

// versionOK reports whether ToDraft accepts version v for registry type typ.
func versionOK(typ, id, v string) bool {
	p := pkgOf(typ, id)
	p.Version = v
	s := serverWith(p)
	_, err := mcpreg.ToDraft(s, mcpreg.Choices(s)[0], mapID)
	return err == nil
}

func TestNPMVersionShapes(t *testing.T) {
	for _, v := range []string{mapVersion, "0.0.1", "1.0.0-beta.1+build.5", "2.0.0-rc-1", "latest", "next", "beta.2"} {
		if !versionOK(typeNPM, npmPkg, v) {
			t.Errorf("npm version %q refused", v)
		}
	}
	bad := []string{"1.0.0.tgz", "1.0.0+x.tgz", "1.0.0-x.tgz", "1.0.0+b.tar", "1.0.0+x.TGZ", "1.0.0-x.tar.gz", "1.0.tar", "1.0.tar.gz", "LATEST.TGZ", "next.Tar.Gz", "1.0", "01.0.0", "1.0.0;x",
		"x/y", "file:x", "~1.0.0", "^1.0.0", ">=1", "1.x", ".hidden", strings.Repeat("a", 129)}
	for _, v := range bad {
		if versionOK(typeNPM, npmPkg, v) {
			t.Errorf("npm version %q accepted", v)
		}
	}
}

func TestPyPIVersionShapes(t *testing.T) {
	for _, v := range []string{mapVersion, "1.0rc1", "2.0.post1", "1.0.dev3", "1.0a1", "1.0+local.1", "2024.10"} {
		if !versionOK(typePyPI, pypiPkg, v) {
			t.Errorf("pypi version %q refused", v)
		}
	}
	bad := []string{"1.0;python_version", "1.0@x", "==1.0", "1.0==2", "https://x", "1.0 ", "latest", "1!2.0", "v1.0", ">=1.0"}
	for _, v := range bad {
		if versionOK(typePyPI, pypiPkg, v) {
			t.Errorf("pypi version %q accepted", v)
		}
	}
}

func TestIdentifierAndVersionCaps(t *testing.T) {
	refuseIdentifier(t, typeOCI, "a/"+strings.Repeat("b", 254))
	if versionOK(typeOCI, ociImage, strings.Repeat("t", 129)) {
		t.Error("OCI tag over 128 bytes accepted")
	}
}

func TestInputCountCaps(t *testing.T) {
	p := pkgOf(typeNPM, npmPkg)
	for range 65 {
		p.PackageArgs = append(p.PackageArgs, mcpreg.Argument{Type: positional, Value: "x"})
	}
	unsupported(t, serverWith(p), "")
	p = pkgOf(typeNPM, npmPkg)
	for i := range 65 {
		p.Env = append(p.Env, mcpreg.EnvVar{Name: "E" + strconv.Itoa(i), Value: "v"})
	}
	unsupported(t, serverWith(p), "")
	r := mcpreg.Remote{Type: streamable, URL: remoteURL}
	for i := range 33 {
		r.Headers = append(r.Headers, mcpreg.Header{Name: "H" + strconv.Itoa(i), Value: "v"})
	}
	unsupported(t, remoteServer(r), "")
}

func TestOverlongValuesUnsupported(t *testing.T) {
	long := strings.Repeat("v", 4097)
	p := pkgOf(typeNPM, npmPkg)
	p.Env = []mcpreg.EnvVar{{Name: modeEnv, Value: long}}
	unsupported(t, serverWith(p), "")
	r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{{Name: authHeader, Value: long}}}
	unsupported(t, remoteServer(r), "")
}

func TestControlCharactersUnsupported(t *testing.T) {
	p := pkgOf(typeNPM, npmPkg)
	p.Env = []mcpreg.EnvVar{{Name: modeEnv, Value: newlineVal}}
	unsupported(t, serverWith(p), "")
	for _, v := range []string{tabVal, newlineVal, "a\x7fb"} {
		r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{{Name: authHeader, Value: v}}}
		unsupported(t, remoteServer(r), "")
	}
}

func TestUnsafePromptDefaultDropped(t *testing.T) {
	for _, def := range []string{stealRef, commaVal, backslashVal, braceVal, newlineVal, tabVal, "a\x00b"} {
		p := pkgOf(typeNPM, npmPkg)
		p.Env = []mcpreg.EnvVar{{Name: logLevel, Required: true, Default: def}}
		d := draft(t, serverWith(p))
		if len(d.Prompts) != 1 || d.Prompts[0].Name != logLevel || d.Prompts[0].Default != "" {
			t.Errorf("default %q: Prompts = %+v, want the prompt kept with no default", def, d.Prompts)
		}
	}
}

func TestIdentifierWithCommaRefused(t *testing.T) {
	for _, typ := range []string{typeNPM, typePyPI, typeOCI} {
		refuseIdentifier(t, typ, commaVal)
	}
}

func TestIdentifierWithSpaceOrBackslashRefused(t *testing.T) {
	for _, typ := range []string{typeNPM, typePyPI, typeOCI} {
		refuseIdentifier(t, typ, spaceVal, backslashVal, newlineVal, "a\n", tabVal, "\na", "")
	}
	refuseIdentifier(t, typeOCI, "img:1.0", "img@sha256:abc", "Upper/case")
	refuseIdentifier(t, typeNPM, "pkg@1.0", "@scope", "Upper")
	refuseIdentifier(t, typePyPI, "pkg==1.0", "pkg-")
}

func TestVersionWithLeadingDashRefused(t *testing.T) {
	for _, typ := range []string{typeNPM, typePyPI, typeOCI} {
		for _, v := range []string{"-1", "--registry=evil", "1.0\n", "1 0", "1,0", "", `1\0`} {
			p := pkgOf(typ, map[string]string{typeNPM: npmPkg, typePyPI: pypiPkg, typeOCI: ociImage}[typ])
			p.Version = v
			if err := refused(t, serverWith(p)); !errors.Is(err, mcpreg.ErrUnsafeIdentifier) {
				t.Errorf("%s version %q: "+wantUnsafe, typ, v, err)
			}
		}
	}
}

func TestUnknownRegistryTypeOmittedFromChoices(t *testing.T) {
	s := mcpreg.Server{Name: mapName,
		Packages: []mcpreg.Package{pkgOf("mcpb", "x"), pkgOf(typeNPM, npmPkg), pkgOf("nuget", "x"), pkgOf("NPM", "x")},
		Remotes:  []mcpreg.Remote{{Type: "websocket", URL: remoteURL}, {Type: typeSSE, URL: remoteURL}},
	}
	cs := mcpreg.Choices(s)
	if len(cs) != 2 || cs[0].Package == nil || cs[0].Package.RegistryType != typeNPM ||
		cs[1].Remote == nil || cs[1].Remote.Type != typeSSE {
		t.Fatalf("Choices = %+v, want the npm package then the sse remote", cs)
	}
	for _, c := range cs {
		if c.Label == "" {
			t.Errorf("choice %+v has no label", c)
		}
	}
}

func TestInvalidIDRefused(t *testing.T) {
	s := serverWith(pkgOf(typeNPM, npmPkg))
	for _, id := range []string{"", "-x", spaceVal, "../x"} {
		if _, err := mcpreg.ToDraft(s, only(t, s), id); !errors.Is(err, mcpreg.ErrInvalidID) {
			t.Errorf("id %q: err = %v, want ErrInvalidID", id, err)
		}
	}
}

func TestEmptyChoiceRefused(t *testing.T) {
	if _, err := mcpreg.ToDraft(serverWith(), mcpreg.Choice{}, mapID); !errors.Is(err, mcpreg.ErrUnsupported) {
		t.Errorf(wantUnsup, err)
	}
}

func TestDraftNeverCarriesRegistryCommand(t *testing.T) {
	entry := `{"server": {"name": "io.example/cmd", "version": "1.0.0", "packages": [{"registryType": "npm",
		"identifier": "cmd-mcp", "version": "1.0.0", "runtimeHint": "sh", "command": "rm",
		"args": ["-rf", "/"], "transport": {"type": "stdio", "command": "curl"}}]}}`
	c, _ := newClient(t, nil, serveBytes(pageOf(entry)))
	p, err := c.Search(context.Background(), anyQuery, "")
	if err != nil {
		t.Fatalf(searchFail, err)
	}
	d := draft(t, p.Servers[0])
	if d.Spec.Command != npx || !slices.Equal(d.Spec.Args, []string{"-y", "cmd-mcp@1.0.0"}) {
		t.Errorf("spec = %+v, want the command derived from registryType only", d.Spec)
	}
}

// TestSampleRecordsMap runs the sample page through Choices and ToDraft.
func TestSampleRecordsMap(t *testing.T) {
	p := searchSample(t)
	fs := draftID(t, find(t, p, fsName), "remote-filesystem")
	if fs.Spec.Env["GCS_PRIVATE_KEY"] != "${secret:remote-filesystem.gcs_private_key}" ||
		len(fs.Prompts) != 1 || fs.Prompts[0].Name != "GCS_BUCKET" {
		t.Errorf("filesystem draft = %+v", fs)
	}
	w := draftID(t, find(t, p, weatherName), "weather")
	if w.Spec.URL != "https://us.weather.example/mcp" ||
		!slices.Equal(w.Spec.Headers, []string{"Authorization: Bearer ${secret:weather.api_key}"}) {
		t.Errorf("weather draft = %+v", w.Spec)
	}
	unsupported(t, find(t, p, argsName), "runtime options")
	if old := draft(t, find(t, p, oldName)); !slices.Contains(old.Trust, mutableLine) {
		t.Errorf("old-server Trust = %q", old.Trust)
	}
	if cs := mcpreg.Choices(find(t, p, "io.example/bundle")); len(cs) != 0 {
		t.Errorf("mcpb bundle choices = %+v, want none", cs)
	}
}

func TestMissingArgumentNameIsShortened(t *testing.T) {
	p := pkgOf(typePyPI, pypiPkg)
	p.PackageArgs = []mcpreg.Argument{{Type: positional, Name: strings.Repeat("n", 100), Required: true}}
	err := refused(t, serverWith(p))
	if strings.Contains(err.Error(), strings.Repeat("n", 33)) || !strings.HasSuffix(err.Error(), strings.Repeat("n", 32)+"…") {
		t.Errorf("err = %q, want the name cut to 32 runes", err)
	}
}

// The confirm modal shows the package identity verbatim: the runtime
// token survives actions.McpAdd(...).Display() for every package type.
func TestDisplayShowsPackageIdentity(t *testing.T) {
	for _, c := range []struct{ typ, id, token string }{
		{typeNPM, npmPkg, npmPkg + "@" + mapVersion},
		{typePyPI, "mcp-server-fetch", "mcp-server-fetch==" + mapVersion},
		{typeOCI, ociImage, ociImage + ":" + mapVersion},
	} {
		d := draft(t, serverWith(pkgOf(c.typ, c.id)))
		a, err := actions.McpAdd(d.Spec)
		if err != nil {
			t.Fatalf("McpAdd: %v", err)
		}
		if got := a.Display(); !strings.Contains(got, c.token) {
			t.Errorf("%s: Display() = %q, want it to show %q", c.typ, got, c.token)
		}
	}
}

const (
	credentialReason = "the package name reads like a credential and could not be shown"
	runtimeReason    = "which changes how the runtime itself behaves"
)

// A package name the confirm modal would mask cannot be checked by the
// user, so the choice is refused.
func TestCredentialShapedPackageNameUnsupported(t *testing.T) {
	for _, p := range []mcpreg.Package{
		pkgOf(typeNPM, "sk-"+strings.Repeat("a", 50)),
		pkgOf(typePyPI, "AKIAIOSFODNN7EXAMPLE"),
		pkgOf(typeOCI, "ghcr.io/x/sk-"+strings.Repeat("a", 50)),
	} {
		unsupported(t, serverWith(p), credentialReason)
	}
}

// D40: a fixed value, a prompt or a secret for a runtime or loader
// variable is refused.
func TestRuntimeEnvNamesUnsupported(t *testing.T) {
	for _, name := range []string{"NPM_CONFIG_REGISTRY", "npm_config_registry", "PIP_INDEX_URL", "PATH", "LD_PRELOAD", "https_proxy", "DOCKER_HOST"} {
		for _, v := range []mcpreg.EnvVar{
			{Name: name, Value: "x"},
			{Name: name, Required: true},
			{Name: name, Secret: true},
		} {
			p := pkgOf(typeNPM, npmPkg)
			p.Env = []mcpreg.EnvVar{v}
			unsupported(t, serverWith(p), "sets "+name+", "+runtimeReason)
		}
	}
	// The reviewer's case: an OCI package pointing docker at another daemon.
	p := pkgOf(typeOCI, ociImage)
	p.Env = []mcpreg.EnvVar{{Name: "DOCKER_HOST", Value: "tcp://evil.example:2375"}}
	unsupported(t, serverWith(p), runtimeReason)
	// An optional one with nothing to set is simply omitted.
	p = pkgOf(typeNPM, npmPkg)
	p.Env = []mcpreg.EnvVar{{Name: "NODE_OPTIONS"}}
	draft(t, serverWith(p))
}

// Header values are trimmed, as the add form trims them, so the prefill
// round-trip keeps them.
func TestHeaderValueTrimmed(t *testing.T) {
	v := []mcpreg.EnvVar{{Name: "a", Secret: true}, {Name: "b", Secret: true}}
	r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{
		{Name: "X-Pair", Value: "{a}:{b} ", Variables: v},
		{Name: "X-Fixed", Value: "  v1 "},
	}}
	want := []string{"X-Pair: ${secret:demo.a}:${secret:demo.b}", "X-Fixed: v1"}
	if got := draft(t, remoteServer(r)).Spec.Headers; !slices.Equal(got, want) {
		t.Errorf(headersFmt, got, want)
	}
}

func TestDuplicateHeaderNameUnsupported(t *testing.T) {
	for _, second := range []string{"X-A", "x-a"} {
		r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{
			{Name: "X-A", Value: "1"}, {Name: second, Value: "2"},
		}}
		unsupported(t, remoteServer(r), "declares the header "+second+" twice")
	}
}

// A vault key id.name longer than an identifier allows is refused: the
// after-add note could never find it set.
func TestOverlongSecretKeyUnsupported(t *testing.T) {
	p := pkgOf(typeNPM, npmPkg)
	p.Env = []mcpreg.EnvVar{{Name: strings.Repeat("K", 140), Secret: true}}
	unsupported(t, serverWith(p), "declares a secret whose name cannot form a vault key")
	r := mcpreg.Remote{Type: streamable, URL: remoteURL, Headers: []mcpreg.Header{{Name: strings.Repeat("H", 140), Secret: true}}}
	unsupported(t, remoteServer(r), "declares a secret whose name cannot form a vault key")
	// At the limit it is accepted.
	p.Env = []mcpreg.EnvVar{{Name: strings.Repeat("K", 128-len(mapID)-1), Secret: true}}
	draft(t, serverWith(p))
}
