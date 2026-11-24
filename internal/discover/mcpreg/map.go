package mcpreg

import (
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/discover/argtok"
	"github.com/lusky3/agentsync-tui/internal/discover/runenv"
	"github.com/lusky3/agentsync-tui/internal/ident"
	"github.com/lusky3/agentsync-tui/internal/mask"
)

// Choice is one way to install a server: exactly one of Package or Remote
// is set, pointing into the Server it came from. Label is built from
// sanitized registry text that may still hold \n or \t: render it with
// mask.OneLine.
type Choice struct {
	Label   string
	Package *Package
	Remote  *Remote
}

// Draft is an install spec derived from a registry record, ready to
// prefill the add form. Prompts are values the user must still supply;
// secrets are never prompted for or carried as values, only as
// ${secret:...} references in Spec. Trust lines describe risks of this
// particular choice (the source adds its own). Provenance and Trust may
// hold \n or \t from registry text: render them with mask.OneLine.
type Draft struct {
	Spec       actions.AddSpec
	Prompts    []Prompt
	Provenance string
	Trust      []string
}

// Prompt is a non-secret value the user must supply (an environment
// variable the registry marks required but gives no fixed value for).
// Description is registry text that may hold \n or \t: render it with
// mask.OneLine. Default is empty when the registry's default is not one
// the add form could carry safely (see ToDraft).
type Prompt struct {
	Name        string
	Secret      bool
	Required    bool
	Default     string
	Description string
}

var (
	// ErrUnsafeIdentifier means the package name or version does not match
	// its registry's naming rules; it is refused, never escaped.
	ErrUnsafeIdentifier = errors.New("the package name or version is not safe to pass to its runtime")
	// ErrInvalidID means the server id given to ToDraft is not an identifier.
	ErrInvalidID = errors.New("the server id is not a valid identifier")
	// ErrUnsupported is matched (errors.Is) by every refusal whose text is
	// the user-facing reason the choice cannot be installed.
	ErrUnsupported = errors.New("this install option is not supported")
)

// unsupported is a refusal reason. Its text is shown to the user as is, so
// it never quotes a raw server string (at most sanitized, shortened names).
type unsupported string

func (u unsupported) Error() string        { return string(u) }
func (u unsupported) Is(target error) bool { return target == ErrUnsupported }

const (
	reasonNoChoice        = "no package or remote was chosen"
	reasonRuntimeArgs     = "sets runtime options this tool does not apply"
	reasonNoTransport     = "does not say which transport the package uses"
	reasonNetTransport    = "the package uses a network transport this tool does not set up"
	reasonArgVariables    = "a package argument uses argument variables this tool does not fill in"
	reasonUnsafeArg       = "a package argument is not safe to pass"
	reasonArgKind         = "a package argument is of an unknown kind"
	reasonMissingArgs     = "requires arguments the registry leaves to you: "
	reasonEnvName         = "declares an environment variable name agentsync cannot pass"
	reasonEnvValue        = "sets an environment value agentsync cannot pass safely"
	reasonSecretName      = "declares a secret whose name cannot form a vault key"
	reasonSecretCollision = "declares two secrets that would share one vault key"
	reasonNotHTTPS        = "the remote URL is not a plain https URL"
	reasonPlaceholder     = "uses a placeholder with no default or secret to fill it"
	reasonTemplateText    = "contains a reference or default this tool will not pass"
	reasonHeaderName      = "declares a header name agentsync cannot pass"
	reasonHeaderValue     = "requires a header value the registry leaves to you"
	reasonHeaderText      = "sets a header value agentsync cannot pass safely"
	reasonSecretInURL     = "puts a secret in the remote URL; this tool passes secrets only in headers"
	reasonTooMany         = "declares more arguments, variables or headers than this tool accepts"
	reasonSpecRejected    = "agentsync would not accept the resulting server definition"
	reasonCredentialName  = "the package name reads like a credential and could not be shown"
	reasonHeaderTwice     = "declares the header "
	reasonChecksum        = "declares a file checksum its runtime cannot verify before running it"
	reasonRegistryBase    = "is published to a package registry other than the one its runtime installs from"
	reasonSecretLiteral   = "gives a fixed value for a secret; this tool never stores a secret as plain text"

	trustMutableTag = "image tag is mutable (not digest-pinned)"
	provenanceHead  = "official registry: "
	httpsPrefix     = "https://"
	secretRefHead   = "${secret:"
	typeStdio       = "stdio"
	registryOCI     = "oci"
	// maxSuffix is the highest de-duplication suffix SuggestID tries.
	maxSuffix = 100
	// shortNameLen bounds a name quoted in a refusal reason.
	shortNameLen = 32

	// Caps on what one record may ask for; beyond them the choice is
	// unsupported.
	maxIdentifier = 255
	maxVersion    = 128
	maxValue      = 4096
	maxArgs       = 64
	maxEnv        = 64
	maxHeaders    = 32
)

var (
	envNameRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// npm package names: lowercase, optionally @scope/.
	npmNameRe = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]{0,213}$`)
	// PyPI project names (PEP 508 / 503): letters, digits, ._- inside.
	pypiNameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,198}[A-Za-z0-9])?$`)
	// npm versions: a strict semver (optional prerelease and build; a
	// numeric prerelease identifier has no leading zero), or a dist-tag
	// (letter first) that npm would not read as a version or range; see
	// npmVersionOK. Anything else (a range, a file or URL spec) is refused.
	semverRe  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-` + preID + `(\.` + preID + `)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	distTagRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*$`)
	// rangeLikeRe matches what npm-package-arg accepts as a (loose) semver
	// version or x-range before it falls back to a tag: v1, v1.2.3, x, 1.x.
	rangeLikeRe = regexp.MustCompile(`^[vV=]*(([xX*]|[0-9]+)(\.([xX*]|[0-9]+)(\.([xX*]|[0-9]+)(-?[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?)?)?)?$`)
	// pep440Re is a normalized PEP 440 public version (no epoch) with an
	// optional local part: release, a/b/rc, .postN, .devN, +local.
	pep440Re = regexp.MustCompile(`(?i)^[0-9]+(\.[0-9]+)*((a|b|rc)[0-9]+)?(\.post[0-9]+)?(\.dev[0-9]+)?(\+[a-z0-9]+(\.[a-z0-9]+)*)?$`)
	// OCI repository: an optional registry host (dotted labels that start
	// and end with a letter or digit, or localhost; optional port), then
	// lowercase path components joined by ., _, __ or dashes. The tag or
	// digest is split off first (see ociIdentity).
	ociNameRe   = regexp.MustCompile(`^` + OCIRepoPattern + `$`)
	ociTagRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	ociDigestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

const (
	// preID is one semver prerelease identifier: numeric without a leading
	// zero, or alphanumeric.
	preID        = `(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)`
	ociLabel     = `[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?`
	ociComponent = `[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*`
)

// OCIRepoPattern is the unanchored regexp source of an OCI repository
// reference as ToDraft accepts it (optional registry host whose labels
// start and end with a letter or digit, optional port, then lowercase path
// components; no tag or digest). Other sources build on it.
const OCIRepoPattern = `((localhost|` + ociLabel + `(\.` + ociLabel + `)+)(:[0-9]{1,5})?/)?` +
	ociComponent + `(/` + ociComponent + `)*`

// Caps other sources apply as ToDraft does: a longer value, or more
// arguments, environment variables or headers, is unsupported.
const (
	MaxValue   = maxValue
	MaxArgs    = maxArgs
	MaxEnv     = maxEnv
	MaxHeaders = maxHeaders
)

// ValidEnvName reports whether s is an environment variable name agentsync
// can pass: [A-Za-z_][A-Za-z0-9_]*.
func ValidEnvName(s string) bool { return envNameRe.MatchString(s) }

// ValidHeaderName reports whether s is a header name ToDraft accepts.
func ValidHeaderName(s string) bool { return headerNameRe.MatchString(s) }

// UnsafeValue reports whether a fixed or default value cannot be offered:
// too long, holding a control character, ',' or a backslash (addEnv refuses both),
// or '{' or '$' (a template, or a reference to another server's secret).
func UnsafeValue(s string) bool { return unsafeValue(s) }

// ValidRemoteURL reports whether u, with every placeholder already filled,
// is a plain https URL: scheme https, a host, no userinfo, no whitespace
// or control character.
func ValidRemoteURL(u string) bool { return checkURL(u) == nil }

// npmVersionOK accepts a strict semver or a dist-tag. Any version ending in
// .tgz, .tar or .tar.gz (any case) is refused: npm reads name@x.tgz as a
// local file. A letter-first word npm reads as a version or range (v1,
// x.x) is not a tag and is refused too.
func npmVersionOK(v string) bool {
	// Checked first: a semver prerelease or build part is dotted, so
	// 1.0.0+x.tgz is a valid semver that npm still reads as a file.
	l := strings.ToLower(v)
	if strings.HasSuffix(l, ".tgz") || strings.HasSuffix(l, ".tar") || strings.HasSuffix(l, ".tar.gz") {
		return false
	}
	return semverRe.MatchString(v) || (distTagRe.MatchString(v) && !rangeLikeRe.MatchString(v))
}

// pkgRuntime is how a supported registryType runs: the command is fixed per
// type and never taken from the record. The argv is prefix, then the one
// identity token that names the package (see identity).
type pkgRuntime struct {
	command string
	prefix  []string
	// identity checks the record's identifier and version and returns the
	// token the runtime is given, or false when either is unsafe.
	identity func(name, version string) (string, bool)
}

var runtimes = map[string]pkgRuntime{
	"npm":       {command: "npx", prefix: []string{"-y"}, identity: joined(npmNameRe, npmVersionOK, "@")},
	"pypi":      {command: "uvx", identity: joined(pypiNameRe, pep440Re.MatchString, "==")},
	registryOCI: {command: "docker", prefix: []string{"run", "-i", "--rm"}, identity: ociIdentity},
}

// joined is the identity of a name@version style runtime: name and
// version checked on their own, then joined by sep.
func joined(name *regexp.Regexp, version func(string) bool, sep string) func(string, string) (string, bool) {
	return func(n, v string) (string, bool) {
		if !name.MatchString(n) || !version(v) {
			return "", false
		}
		return n + sep + v, true
	}
}

// ociIdentity is the image reference docker runs. The v0.1 registry puts
// the tag (or a sha256 digest) in the identifier and leaves the version
// empty; older records give the repository with the tag as the version.
// A version beside a tagged identifier must equal the tag, and none may
// stand beside a digest. Without a tag or digest (docker would pull
// :latest), or with both, the identity is refused.
func ociIdentity(n, v string) (string, bool) {
	if repo, digest, ok := strings.Cut(n, "@"); ok {
		return n, v == "" && ociNameRe.MatchString(repo) && ociDigestRe.MatchString(digest)
	}
	repo, tag := n, v
	// A ':' before the last '/' is a registry port, not a tag.
	if i := strings.LastIndexByte(n, ':'); i > strings.LastIndexByte(n, '/') {
		repo, tag = n[:i], n[i+1:]
		if v != "" && v != tag {
			return "", false
		}
	}
	if !ociNameRe.MatchString(repo) || !ociTagRe.MatchString(tag) {
		return "", false
	}
	return repo + ":" + tag, true
}

// remoteTypes maps a registry remote type to the agentsync --type.
var remoteTypes = map[string]string{"streamable-http": "http", "sse": "sse"}

// Choices lists the install options of s: one per package with a
// supported registryType (npm, pypi, oci) and one per remote with a
// supported type (streamable-http, sse), packages first. Others are
// omitted. A listed choice can still be refused by ToDraft.
func Choices(s Server) []Choice {
	var out []Choice
	for i := range s.Packages {
		p := &s.Packages[i]
		if _, ok := runtimes[p.RegistryType]; ok {
			out = append(out, Choice{Label: p.RegistryType + " package " + p.Identifier, Package: p})
		}
	}
	for i := range s.Remotes {
		r := &s.Remotes[i]
		if _, ok := remoteTypes[r.Type]; ok {
			out = append(out, Choice{Label: r.Type + " remote " + r.URL, Remote: r})
		}
	}
	return out
}

// ToDraft maps one choice of s to an add spec for server id. id must be an
// identifier; it namespaces every generated secret reference (Decision 7).
// A refusal is ErrInvalidID, ErrUnsafeIdentifier, or an error matching
// ErrUnsupported whose text is the reason.
func ToDraft(s Server, c Choice, id string) (Draft, error) {
	if !ident.Valid(id) {
		return Draft{}, ErrInvalidID
	}
	b := &builder{
		id:    id,
		snake: map[string]string{},
		d:     Draft{Spec: actions.AddSpec{ID: id}, Provenance: provenance(s)},
	}
	var err error
	switch {
	case c.Package != nil:
		err = b.pkg(*c.Package)
	case c.Remote != nil:
		err = b.remote(*c.Remote)
	default:
		err = unsupported(reasonNoChoice)
	}
	if err != nil {
		return Draft{}, err
	}
	// Final gate: never hand out a draft the add command would refuse.
	if _, err := actions.McpAdd(b.d.Spec); err != nil {
		return Draft{}, unsupported(reasonSpecRejected)
	}
	return b.d, nil
}

func provenance(s Server) string {
	if s.Version == "" {
		return provenanceHead + s.Name
	}
	return provenanceHead + s.Name + "@" + s.Version
}

// builder accumulates one Draft. snake maps each generated reference's
// lower_snake key to the input name it came from, so two different names
// can never share one vault key.
type builder struct {
	id    string
	snake map[string]string
	d     Draft
}

func (b *builder) pkg(p Package) error {
	rt, ok := runtimes[p.RegistryType]
	if !ok {
		return unsupported(reasonNoChoice)
	}
	token, err := checkPackage(p, rt)
	if err != nil {
		return err
	}
	idArgs := append(slices.Clone(rt.prefix), token)
	// The confirm modal shows these words through mask.CommandLine: a
	// name it would mask could not be checked by the user.
	if !slices.Equal(mask.CommandLine(idArgs), idArgs) {
		return unsupported(reasonCredentialName)
	}
	extra, err := packageArgs(p.PackageArgs)
	if err != nil {
		return err
	}
	passed, err := b.env(p.Env)
	if err != nil {
		return err
	}
	if p.RegistryType == registryOCI {
		// docker run starts the container with none of its own
		// environment: each variable must be named with -e before the image.
		idArgs = slices.Insert(idArgs, len(rt.prefix), forwardFlags(passed)...)
	}
	b.d.Spec.Type = typeStdio
	b.d.Spec.Command = rt.command
	b.d.Spec.Args = append(idArgs, extra...)
	if p.RegistryType == registryOCI && !strings.Contains(token, "@") {
		b.d.Trust = append(b.d.Trust, trustMutableTag)
	}
	return nil
}

// checkPackage refuses an unsafe identity, a non-stdio transport, runtime
// options and oversized input lists. It returns the identity token.
func checkPackage(p Package, rt pkgRuntime) (string, error) {
	// The leading-dash refusal is independent of the per-type patterns: the
	// identifier is the first token after the runtime's own options.
	if strings.HasPrefix(p.Identifier, "-") || strings.HasPrefix(p.Version, "-") ||
		len(p.Identifier) > maxIdentifier || len(p.Version) > maxVersion {
		return "", ErrUnsafeIdentifier
	}
	token, ok := rt.identity(p.Identifier, p.Version)
	if !ok {
		return "", ErrUnsafeIdentifier
	}
	if !baseOK(p) {
		return "", unsupported(reasonRegistryBase)
	}
	if err := checkTransport(p.Transport); err != nil {
		return "", err
	}
	// Runtime options would go before the package token and can widen what
	// the runtime does (a docker volume, an npm registry): never applied.
	if len(p.RuntimeArgs) > 0 {
		return "", unsupported(reasonRuntimeArgs)
	}
	if len(p.PackageArgs) > maxArgs || len(p.Env) > maxEnv {
		return "", unsupported(reasonTooMany)
	}
	// The schema requires a client to check a declared digest before it
	// runs the package; npx, uvx and docker run have no way to.
	if p.FileSHA256 != "" {
		return "", unsupported(reasonChecksum)
	}
	return token, nil
}

// defaultBases are the registries npx and uvx install a bare name from.
var defaultBases = map[string]string{"npm": "https://registry.npmjs.org", "pypi": "https://pypi.org"}

// baseOK reports whether the package's registryBaseUrl, if any, is where
// its runtime will look: the default registry for npm and pypi (npx and
// uvx would fetch a bare name from there, whatever the record says), and
// for oci the registry host the image reference itself names.
func baseOK(p Package) bool {
	base := strings.TrimSuffix(p.RegistryBaseURL, "/")
	if base == "" {
		return true
	}
	want := defaultBases[p.RegistryType]
	if p.RegistryType == registryOCI {
		want = httpsPrefix + ociHost(p.Identifier)
	}
	return strings.EqualFold(base, want)
}

// ociHost is the registry an image reference pulls from: its first path
// component when that looks like a host, else Docker Hub.
func ociHost(ref string) string {
	if first, _, ok := strings.Cut(ref, "/"); ok && (first == "localhost" || strings.ContainsAny(first, ".:")) {
		return first
	}
	return "docker.io"
}

// checkTransport accepts only stdio: a package's own url/headers for a
// network transport are not modelled. An empty transport is refused too;
// the v0.1 schema requires one, so its absence is a malformed record.
func checkTransport(t string) error {
	switch t {
	case typeStdio:
		return nil
	case "":
		return unsupported(reasonNoTransport)
	default:
		return unsupported(reasonNetTransport)
	}
}

// packageArgs returns the tokens of every argument with a fixed value, in
// order. A required argument without one makes the choice unsupported;
// optional ones without a value are omitted.
func packageArgs(args []Argument) ([]string, error) {
	var out, missing []string
	for _, a := range args {
		if a.Value == "" {
			if a.Required {
				missing = append(missing, shortName(a.Name))
			}
			continue
		}
		toks, err := argTokens(a)
		if err != nil {
			return nil, err
		}
		out = append(out, toks...)
	}
	if len(missing) > 0 {
		return nil, unsupported(reasonMissingArgs + strings.Join(missing, ", "))
	}
	return out, nil
}

// argTokens turns one fixed argument into argv tokens: a positional one is
// its value, a named one is two tokens, the flag name (which the registry
// gives with its dashes, such as --port) then the value. Every token must
// pass argtok.Valid.
func argTokens(a Argument) ([]string, error) {
	// Decision 7: a secret is only ever a ${secret:...} reference in env or
	// headers, never a plain argv token.
	if a.Secret {
		return nil, unsupported(reasonSecretLiteral)
	}
	if strings.Contains(a.Value, "{") {
		return nil, unsupported(reasonArgVariables)
	}
	var toks []string
	switch a.Type {
	case "positional":
		toks = []string{a.Value}
	case "named":
		if !argtok.Flag(a.Name) {
			return nil, unsupported(reasonUnsafeArg)
		}
		toks = []string{a.Name, a.Value}
	default:
		return nil, unsupported(reasonArgKind)
	}
	for _, t := range toks {
		if !argtok.Valid(t) {
			return nil, unsupported(reasonUnsafeArg)
		}
	}
	return toks, nil
}

// env maps environment variables: a fixed value is passed literally, a
// secret becomes a namespaced reference, a required plain one becomes a
// Prompt, and an optional plain one is omitted. A fixed value marked
// secret refuses (Decision 7: never a literal). Setting a runtime or
// loader variable (runenv.Reserved) in any of those ways refuses (D40).
// It returns, in order, the names the server will be given: those set and
// those prompted for.
func (b *builder) env(vars []EnvVar) ([]string, error) {
	var passed []string
	seen := make(map[string]bool, len(vars))
	for _, v := range vars {
		if !envNameRe.MatchString(v.Name) || seen[v.Name] {
			return nil, unsupported(reasonEnvName)
		}
		seen[v.Name] = true
		if runenv.Reserved(v.Name) && (v.Value != "" || v.Secret || v.Required) {
			return nil, unsupported(runenv.Reason(v.Name))
		}
		ok, err := b.envVar(v)
		if err != nil {
			return nil, err
		}
		if ok {
			passed = append(passed, v.Name)
		}
	}
	return passed, nil
}

// envVar maps one variable and reports whether the server gets it.
func (b *builder) envVar(v EnvVar) (bool, error) {
	switch {
	case v.Value != "" && v.Secret:
		return false, unsupported(reasonSecretLiteral)
	case v.Value != "":
		if unsafeValue(v.Value) {
			return false, unsupported(reasonEnvValue)
		}
		b.setEnv(v.Name, v.Value)
	case v.Secret:
		ref, err := b.ref(v.Name)
		if err != nil {
			return false, err
		}
		b.setEnv(v.Name, ref)
	case v.Required:
		def := v.Default
		if unsafeValue(def) {
			def = "" // the user types the value; never suggest a reference
		}
		b.d.Prompts = append(b.d.Prompts, Prompt{
			Name: v.Name, Required: true, Default: def, Description: v.Description,
		})
	default:
		return false, nil
	}
	return true, nil
}

// forwardFlags is "-e NAME" for each name, as docker run takes them.
func forwardFlags(names []string) []string {
	out := make([]string, 0, 2*len(names))
	for _, n := range names {
		out = append(out, "-e", n)
	}
	return out
}

// unsafeValue reports whether a fixed or default value cannot be offered:
// too long, holding a control character (Sanitize keeps \n and \t), ',' or
// '\' (addEnv refuses both), or '{' or '$' (a template, or a reference to
// another server's secret).
func unsafeValue(s string) bool {
	return len(s) > maxValue || strings.ContainsAny(s, `,\{$`) || strings.IndexFunc(s, unicode.IsControl) >= 0
}

func (b *builder) setEnv(name, value string) {
	if b.d.Spec.Env == nil {
		b.d.Spec.Env = map[string]string{}
	}
	b.d.Spec.Env[name] = value
}

// ref returns SecretRef(b.id, name), refusing a name with no lower_snake
// form, one whose vault key would be too long, or one whose form another
// name of this draft already took.
func (b *builder) ref(name string) (string, error) {
	key := lowerSnake(name)
	if SecretRef(b.id, name) == "" {
		return "", unsupported(reasonSecretName)
	}
	if prev, ok := b.snake[key]; ok && prev != name {
		return "", unsupported(reasonSecretCollision)
	}
	b.snake[key] = name
	return SecretRef(b.id, name), nil
}

func (b *builder) remote(r Remote) error {
	typ, ok := remoteTypes[r.Type]
	if !ok {
		return unsupported(reasonNoChoice)
	}
	if !strings.HasPrefix(r.URL, httpsPrefix) {
		return unsupported(reasonNotHTTPS)
	}
	if len(r.Headers) > maxHeaders {
		return unsupported(reasonTooMany)
	}
	u, err := b.fill(r.URL, r.Variables, false)
	if err != nil {
		return err
	}
	if err := checkURL(u); err != nil {
		return err
	}
	headers, err := b.headers(r.Headers)
	if err != nil {
		return err
	}
	b.d.Spec.Type = typ
	b.d.Spec.URL = u
	b.d.Spec.Headers = headers
	return nil
}

// headers maps each header in order, refusing a name given twice in any
// case (the add form would keep only one).
func (b *builder) headers(hs []Header) ([]string, error) {
	var out []string
	seen := make(map[string]bool, len(hs))
	for _, h := range hs {
		k := strings.ToLower(h.Name)
		if seen[k] {
			return nil, unsupported(reasonHeaderTwice + shortName(h.Name) + " twice")
		}
		seen[k] = true
		line, err := b.header(h)
		if err != nil {
			return nil, err
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// checkURL takes the filled URL: https, a host, no userinfo, no
// whitespace or control character.
func checkURL(u string) error {
	pu, err := url.Parse(u)
	if err != nil || pu.Scheme != "https" || pu.Host == "" || pu.User != nil || hasSpace(u) {
		return unsupported(reasonNotHTTPS)
	}
	return nil
}

// header returns "Name: value", or "" for an optional header with nothing
// to send. A header with no value that is secret becomes a reference
// named after the header itself. A secret header's value must take its
// secret from a secret variable (Bearer {api_key}): one that fills to
// plain text refuses (Decision 7).
func (b *builder) header(h Header) (string, error) {
	if !headerNameRe.MatchString(h.Name) {
		return "", unsupported(reasonHeaderName)
	}
	var v string
	var err error
	switch {
	case h.Value != "":
		v, err = b.fill(h.Value, h.Variables, true)
	case h.Secret:
		v, err = b.ref(h.Name)
	case h.Required:
		err = unsupported(reasonHeaderValue)
	default:
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// fill refuses '$' in the template and in defaults, so a reference in
	// v can only be one ref generated.
	if h.Secret && !strings.Contains(v, secretRefHead) {
		return "", unsupported(reasonSecretLiteral)
	}
	// Trimmed as the add form trims a header value, so the prefill keeps it.
	v = strings.TrimSpace(v)
	if len(v) > maxValue || strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return "", unsupported(reasonHeaderText)
	}
	return h.Name + ": " + v, nil
}

// fill replaces each {name} placeholder in tmpl with the declared
// variable's secret reference (only when secrets is set: a URL never
// carries one) or default. The template itself may hold no '$' (so it can
// never write a reference of its own); a placeholder that is undeclared,
// unbalanced, nested or neither secret nor defaulted refuses.
func (b *builder) fill(tmpl string, vars []EnvVar, secrets bool) (string, error) {
	if strings.Contains(tmpl, "$") {
		return "", unsupported(reasonTemplateText)
	}
	var out strings.Builder
	rest := tmpl
	for {
		i := strings.IndexAny(rest, "{}")
		if i < 0 {
			out.WriteString(rest)
			return out.String(), nil
		}
		j := strings.IndexByte(rest[i+1:], '}')
		if rest[i] == '}' || j < 0 {
			return "", unsupported(reasonPlaceholder)
		}
		val, err := b.resolve(rest[i+1:i+1+j], vars, secrets)
		if err != nil {
			return "", err
		}
		out.WriteString(rest[:i])
		out.WriteString(val)
		rest = rest[i+2+j:]
	}
}

// resolve fills one placeholder from its declared variable.
func (b *builder) resolve(name string, vars []EnvVar, secrets bool) (string, error) {
	for _, v := range vars {
		if v.Name != name {
			continue
		}
		if v.Secret && !secrets {
			return "", unsupported(reasonSecretInURL)
		}
		if v.Secret {
			return b.ref(name)
		}
		if v.Default == "" {
			break
		}
		if strings.ContainsAny(v.Default, "{}$") || hasSpace(v.Default) {
			return "", unsupported(reasonTemplateText)
		}
		return v.Default, nil
	}
	return "", unsupported(reasonPlaceholder)
}

func hasSpace(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0
}

// shortName bounds a (sanitized) name quoted in a reason.
func shortName(s string) string {
	r := []rune(s)
	if len(r) <= shortNameLen {
		return s
	}
	return string(r[:shortNameLen]) + "…"
}

// SecretRef is the vault reference for input name of server id:
// ${secret:<id>.<lower_snake_name>}, or "" when name has no ASCII letter
// or digit or the key <id>.<lower_snake_name> would be longer than
// ident.MaxLen. lower_snake lowercases ASCII letters, turns every run of other
// characters into one '_' and trims '_' from both ends ("X-Api-Key" →
// "x_api_key", "apiKey" → "apikey"). The key has no '.', so the last '.'
// always separates the server id from the name.
func SecretRef(id, name string) string {
	key := lowerSnake(name)
	if key == "" || len(id)+1+len(key) > ident.MaxLen {
		return ""
	}
	return secretRefHead + id + "." + key + "}"
}

func lowerSnake(name string) string {
	var sb strings.Builder
	gap := false
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			r += 'a' - 'A'
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		default:
			gap = true
			continue
		}
		if gap && sb.Len() > 0 {
			sb.WriteByte('_')
		}
		gap = false
		sb.WriteRune(r)
	}
	return sb.String()
}

// SuggestID derives a server id from a registry name: the last
// '/'-separated segment, lowercased, every run of characters other than
// [a-z0-9._] (including '-') collapsed to one '-', '-', '.' and '_' trimmed
// from both ends, and cut short enough to take a suffix. When taken
// reports the id as used, "-2", "-3", ... up to "-100" are tried. It
// returns "" when no valid, free id results; taken may be nil.
func SuggestID(name string, taken func(string) bool) string {
	base := slug(name[strings.LastIndex(name, "/")+1:])
	if base == "" {
		return ""
	}
	for n := 1; n <= maxSuffix; n++ {
		id := base
		if n > 1 {
			id = base + "-" + strconv.Itoa(n)
		}
		if taken == nil || !taken(id) {
			return id
		}
	}
	return ""
}

func slug(seg string) string {
	var sb strings.Builder
	dash := false
	for _, r := range strings.ToLower(seg) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' {
			sb.WriteRune(r)
			dash = false
		} else if !dash {
			sb.WriteByte('-')
			dash = true
		}
	}
	const trim = "-._"
	out := strings.Trim(sb.String(), trim)
	if limit := ident.MaxLen - len("-"+strconv.Itoa(maxSuffix)); len(out) > limit {
		out = strings.Trim(out[:limit], trim)
	}
	if !ident.Valid(out) {
		return ""
	}
	return out
}
