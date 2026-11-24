package dockercat

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/discover/argtok"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpreg"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpsrc"
	"github.com/lusky3/agentsync-tui/internal/discover/runenv"
	"github.com/lusky3/agentsync-tui/internal/ident"
	"github.com/lusky3/agentsync-tui/internal/mask"
)

// Entry kinds in the catalog.
const (
	KindServer = "server"
	KindRemote = "remote"
	KindPoci   = "poci"
)

// Entry is one catalog entry, every string sanitized. When sanitizing
// would have changed text that reaches argv or a variable name (the
// image, a command argument, an env or secret variable name, a header
// name), the cleaned text is kept but ToDraft refuses the entry.
type Entry struct {
	Kind           string
	Title          string
	Description    string
	Image          string
	Command        []string
	Env            []EnvVar
	Secrets        []Secret
	Volumes        []string
	DisableNetwork bool
	AllowHosts     []string
	Remote         Remote
	// OAuth is true when the entry declares an OAuth sign-in.
	OAuth bool

	// notPlain is set when argv text or a name changed under sanitizing.
	notPlain bool
}

// EnvVar is a plain environment variable with a fixed value.
type EnvVar struct{ Name, Value string }

// Secret is a secret the server reads from environment variable Env;
// Name is Docker's own secret-store key, unused here.
type Secret struct{ Name, Env string }

// Remote is a remote entry's endpoint. Headers are sorted by name.
type Remote struct {
	Transport string
	URL       string
	Headers   []Header
}

// Header is one remote header; Value may hold ${NAME} secret references.
type Header struct{ Name, Value string }

// Wire shapes of the v3 catalog. Unknown fields are ignored; config,
// tools and metadata are never decoded.
type wireEntry struct {
	Type           string     `yaml:"type"`
	Title          string     `yaml:"title"`
	Description    string     `yaml:"description"`
	Image          string     `yaml:"image"`
	Command        []string   `yaml:"command"`
	Env            []wireEnv  `yaml:"env"`
	Secrets        []wireEnv  `yaml:"secrets"`
	Volumes        []string   `yaml:"volumes"`
	DisableNetwork bool       `yaml:"disableNetwork"`
	AllowHosts     []string   `yaml:"allowHosts"`
	Remote         wireRemote `yaml:"remote"`
	OAuth          yaml.Node  `yaml:"oauth"` // only its presence is read
}

// wireEnv is both an env entry (name, value) and a secret (name, env).
type wireEnv struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
	Env   string `yaml:"env"`
}

type wireRemote struct {
	URL       string            `yaml:"url"`
	Transport string            `yaml:"transport_type"`
	Headers   map[string]string `yaml:"headers"`
}

func toCatalogEntry(w wireEntry) Entry {
	e := Entry{
		Kind:           clean(w.Type),
		Title:          clean(w.Title),
		Description:    clean(w.Description),
		Volumes:        cleanAll(w.Volumes),
		DisableNetwork: w.DisableNetwork,
		AllowHosts:     cleanAll(w.AllowHosts),
		OAuth:          present(w.OAuth),
		Remote:         Remote{Transport: clean(w.Remote.Transport), URL: clean(w.Remote.URL)},
	}
	e.Image = e.plain(w.Image)
	for _, c := range w.Command {
		e.Command = append(e.Command, e.plain(c))
	}
	for _, v := range w.Env {
		e.Env = append(e.Env, EnvVar{Name: e.plain(v.Name), Value: clean(v.Value)})
	}
	for _, s := range w.Secrets {
		e.Secrets = append(e.Secrets, Secret{Name: clean(s.Name), Env: e.plain(s.Env)})
	}
	for _, k := range slices.Sorted(maps.Keys(w.Remote.Headers)) {
		e.Remote.Headers = append(e.Remote.Headers, Header{Name: e.plain(k), Value: clean(w.Remote.Headers[k])})
	}
	return e
}

// plain cleans s and records whether cleaning changed it.
func (e *Entry) plain(s string) string {
	c := clean(s)
	if c != s {
		e.notPlain = true
	}
	return c
}

func cleanAll(in []string) []string {
	var out []string
	for _, s := range in {
		out = append(out, clean(s))
	}
	return out
}

// present reports whether an optional mapping was given at all (and not
// as null). Any shape counts: an OAuth requirement fails closed.
func present(n yaml.Node) bool {
	return n.Kind != 0 && (n.Kind != yaml.ScalarNode || n.Tag != "!!null")
}

// Refusal reasons; the text is shown to the user as is.
const (
	ReasonPoci       = "a poci entry runs tools in short-lived containers, which this tool does not set up"
	ReasonNoDigest   = "the image is not pinned by digest"
	ReasonImage      = "the image reference is not one this tool will pass to docker"
	ReasonVolumes    = "mounts volumes, which this tool does not set up"
	ReasonTemplate   = "uses Docker template values this tool does not fill in"
	ReasonCommand    = "a fixed command argument is not safe to pass"
	ReasonOAuth      = "requires an OAuth sign-in that agentsync cannot perform"
	ReasonTransport  = "the remote uses a transport this tool does not set up"
	ReasonURL        = "the remote URL is not a plain https URL"
	ReasonHeader     = "sets a header this tool cannot pass safely"
	ReasonNames      = "its image, command, variable or header names are not plain text"
	ReasonEnvName    = "declares an environment variable name agentsync cannot pass"
	ReasonEnvValue   = "sets an environment value agentsync cannot pass safely"
	ReasonSecretName = "declares a secret whose name cannot form a vault key"
	ReasonCollision  = "declares two secrets that would share one vault key"
	ReasonTooMany    = "declares more arguments, variables or headers than this tool accepts"
	ReasonKind       = "is of a kind this tool does not know"
	ReasonNoID       = "the catalog key gives no usable server id"
	ReasonRejected   = "agentsync would not accept the resulting server definition"
	// ReasonImageCredential: the confirm modal would mask the image.
	ReasonImageCredential = "the image reference reads like a credential and could not be shown"
	ReasonHeaderTwice     = "declares the same header name twice"
	// ReasonEnvCredential: the confirm modal would mask an -e NAME word.
	ReasonEnvCredential = "an environment variable name reads like a credential and could not be shown"
)

// Entry-level trust lines.
const (
	TrustDigestPrefix = "Docker-reviewed catalog; image pinned by digest "
	TrustRemote       = "Docker-reviewed catalog; the server is run by its provider, not in a local container"
	TrustNotVerified  = "image signatures and attestations are NOT verified by this tool"
	TrustAllowHosts   = "declares allowHosts, which plain docker run does not enforce"
	TrustDocker       = "requires Docker"
)

const (
	provenanceHead = "docker catalog: "
	digestShort    = 12
	maxImage       = 512
	templateMark   = "{{"
	typeStdio      = "stdio"
	httpsPrefix    = "https://"
)

// unsupported is a refusal reason; it matches mcpreg.ErrUnsupported.
type unsupported string

func (u unsupported) Error() string        { return string(u) }
func (u unsupported) Is(target error) bool { return target == mcpreg.ErrUnsupported }

var (
	// digestRe is the digest suffix every installable image carries.
	digestRe = regexp.MustCompile(`@sha256:([0-9a-f]{64})$`)
	// imageRe is a whole reference: repository, optional tag, digest.
	imageRe = regexp.MustCompile(`^` + mcpreg.OCIRepoPattern +
		`(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[0-9a-f]{64}$`)
)

var remoteTypes = map[string]string{"streamable-http": "http", "sse": "sse"}

// digest returns the first 12 hex digits of image's sha256 digest, or ""
// when image is not digest-pinned.
func digest(image string) string {
	m := digestRe.FindStringSubmatch(image)
	if m == nil {
		return ""
	}
	return m[1][:digestShort]
}

// ToDraft maps the entry under catalog key to an add spec for server id.
// id must be an identifier; it namespaces every secret reference. A
// refusal is mcpreg.ErrInvalidID or an error matching
// mcpreg.ErrUnsupported whose text is the reason. Every draft it returns
// passes actions.McpAdd. Trust lines are the entry's (see EntryTrust),
// so Draft.Trust is empty.
func ToDraft(key, id string, e Entry) (mcpreg.Draft, error) {
	if !ident.Valid(id) {
		return mcpreg.Draft{}, mcpreg.ErrInvalidID
	}
	if e.notPlain {
		return mcpreg.Draft{}, unsupported(ReasonNames)
	}
	b := &builder{id: id, refs: map[string]string{}}
	b.d.Spec.ID = id
	var err error
	switch e.Kind {
	case KindServer:
		err = b.server(e)
		b.d.Provenance = provenanceHead + key + " (digest " + digest(e.Image) + ")"
	case KindRemote:
		err = b.remote(e)
		b.d.Provenance = provenanceHead + key
	case KindPoci:
		err = unsupported(ReasonPoci)
	default:
		err = unsupported(ReasonKind)
	}
	if err != nil {
		return mcpreg.Draft{}, err
	}
	if _, err := actions.McpAdd(b.d.Spec); err != nil {
		return mcpreg.Draft{}, unsupported(ReasonRejected)
	}
	return b.d, nil
}

// builder accumulates one draft. refs maps each generated reference to
// the name it came from, so two names never share one vault key.
type builder struct {
	id   string
	refs map[string]string
	d    mcpreg.Draft
}

func (b *builder) ref(name string) (string, error) {
	r := mcpreg.SecretRef(b.id, name)
	if r == "" {
		return "", unsupported(ReasonSecretName)
	}
	if prev, ok := b.refs[r]; ok && prev != name {
		return "", unsupported(ReasonCollision)
	}
	b.refs[r] = name
	return r, nil
}

func (b *builder) server(e Entry) error {
	if err := checkImage(e.Image); err != nil {
		return err
	}
	if hasTemplate(e) {
		return unsupported(ReasonTemplate)
	}
	if len(e.Volumes) > 0 {
		return unsupported(ReasonVolumes)
	}
	if len(e.Command) > mcpreg.MaxArgs || len(e.Env)+len(e.Secrets) > mcpreg.MaxEnv {
		return unsupported(ReasonTooMany)
	}
	for _, c := range e.Command {
		if !argtok.Valid(c) {
			return unsupported(ReasonCommand)
		}
	}
	names, err := b.env(e)
	if err != nil {
		return err
	}
	args := []string{"run", "-i", "--rm"}
	if e.DisableNetwork {
		args = append(args, "--network", "none")
	}
	for _, n := range names {
		args = append(args, "-e", n)
	}
	// The confirm modal shows argv through mask.CommandLine; checkImage
	// already holds the image to that, and the same goes for each -e NAME.
	// Only a name masking would change refuses (GITHUB_TOKEN is shown).
	if !slices.Equal(mask.CommandLine(args), args) {
		return unsupported(ReasonEnvCredential)
	}
	args = append(args, e.Image)
	b.d.Spec.Type = typeStdio
	b.d.Spec.Command = "docker"
	b.d.Spec.Args = append(args, e.Command...)
	return nil
}

// checkImage requires a digest-pinned, well-formed reference that the
// confirm modal (mask.CommandLine) shows as it is.
func checkImage(image string) error {
	if digest(image) == "" {
		return unsupported(ReasonNoDigest)
	}
	if strings.HasPrefix(image, "-") || len(image) > maxImage || !imageRe.MatchString(image) {
		return unsupported(ReasonImage)
	}
	if mask.CommandLine([]string{image})[0] != image {
		return unsupported(ReasonImageCredential)
	}
	return nil
}

// hasTemplate reports a Docker template anywhere that would reach argv
// or the environment.
func hasTemplate(e Entry) bool {
	texts := append([]string{e.Image}, e.Command...)
	for _, v := range e.Env {
		texts = append(texts, v.Name, v.Value)
	}
	for _, s := range e.Secrets {
		texts = append(texts, s.Name, s.Env)
	}
	return slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, templateMark) })
}

// env sets the spec's environment: each secret as its namespaced
// reference, each plain variable as its literal value. It returns the
// names in order (secrets first) for the -e flags. A runtime or loader
// variable (runenv.Reserved) refuses either way (D40).
func (b *builder) env(e Entry) ([]string, error) {
	var names []string
	add := func(name, value string) {
		if b.d.Spec.Env == nil {
			b.d.Spec.Env = map[string]string{}
		}
		b.d.Spec.Env[name] = value
		names = append(names, name)
	}
	seen := map[string]bool{}
	for _, s := range e.Secrets {
		if err := checkEnvName(s.Env, seen); err != nil {
			return nil, err
		}
		r, err := b.ref(s.Env)
		if err != nil {
			return nil, err
		}
		add(s.Env, r)
	}
	for _, v := range e.Env {
		if err := checkEnvName(v.Name, seen); err != nil {
			return nil, err
		}
		if mcpreg.UnsafeValue(v.Value) {
			return nil, unsupported(ReasonEnvValue)
		}
		add(v.Name, v.Value)
	}
	return names, nil
}

// checkEnvName refuses an invalid, repeated or reserved variable name and
// records a good one in seen.
func checkEnvName(name string, seen map[string]bool) error {
	if !mcpreg.ValidEnvName(name) || seen[name] {
		return unsupported(ReasonEnvName)
	}
	seen[name] = true
	if runenv.Reserved(name) {
		return unsupported(runenv.Reason(name))
	}
	return nil
}

func (b *builder) remote(e Entry) error {
	if e.OAuth {
		return unsupported(ReasonOAuth)
	}
	typ, ok := remoteTypes[e.Remote.Transport]
	if !ok {
		return unsupported(ReasonTransport)
	}
	u := e.Remote.URL
	if strings.Contains(u, templateMark) {
		return unsupported(ReasonTemplate)
	}
	// No '$', '{' or '}': a URL never carries a secret (D27) or a template.
	if !strings.HasPrefix(u, httpsPrefix) || strings.ContainsAny(u, "${}") || !mcpreg.ValidRemoteURL(u) {
		return unsupported(ReasonURL)
	}
	if len(e.Remote.Headers) > mcpreg.MaxHeaders {
		return unsupported(ReasonTooMany)
	}
	headers, err := b.headers(e.Remote.Headers)
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
			return nil, unsupported(ReasonHeaderTwice)
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

// header returns "Name: value" with each ${NAME} replaced by its secret
// reference, or "" for a header with no value. Any other '$', '{' or '}'
// (a template this tool does not fill) refuses.
func (b *builder) header(h Header) (string, error) {
	if !mcpreg.ValidHeaderName(h.Name) {
		return "", unsupported(ReasonHeader)
	}
	if h.Value == "" {
		return "", nil
	}
	if strings.Contains(h.Value, templateMark) {
		return "", unsupported(ReasonTemplate)
	}
	v, err := b.fill(h.Value)
	if err != nil {
		return "", err
	}
	// Trimmed as the add form trims a header value, so the prefill keeps it.
	v = strings.TrimSpace(v)
	if len(v) > mcpreg.MaxValue || strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return "", unsupported(ReasonHeader)
	}
	return h.Name + ": " + v, nil
}

func (b *builder) fill(v string) (string, error) {
	var out strings.Builder
	rest := v
	for {
		i := strings.Index(rest, "${")
		if i < 0 {
			break
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 || strings.ContainsAny(rest[:i], "${}") {
			return "", unsupported(ReasonHeader)
		}
		name := rest[i+2 : i+j]
		if !mcpreg.ValidEnvName(name) {
			return "", unsupported(ReasonHeader)
		}
		r, err := b.ref(name)
		if err != nil {
			return "", err
		}
		out.WriteString(rest[:i])
		out.WriteString(r)
		rest = rest[i+j+1:]
	}
	if strings.ContainsAny(rest, "${}") {
		return "", unsupported(ReasonHeader)
	}
	out.WriteString(rest)
	return out.String(), nil
}

// EntryTrust is the entry-level trust lines of e: the review-and-digest
// line when the image is digest-pinned (a remote gets its own review
// line), the not-verified and requires-Docker lines for container kinds,
// and the allowHosts warning when the entry declares hosts.
func EntryTrust(e Entry) []string {
	var out []string
	switch e.Kind {
	case KindRemote:
		out = append(out, TrustRemote)
	default:
		if d := digest(e.Image); d != "" {
			out = append(out, TrustDigestPrefix+d)
		}
		out = append(out, TrustNotVerified, TrustDocker)
	}
	if len(e.AllowHosts) > 0 {
		out = append(out, TrustAllowHosts)
	}
	return out
}

// label describes the entry's single install option.
func label(e Entry) string {
	switch e.Kind {
	case KindRemote:
		return e.Remote.Transport + " remote " + e.Remote.URL
	case KindServer, KindPoci:
		return e.Kind + " image " + e.Image
	}
	return "entry of kind " + e.Kind
}

// toEntry is the entry as the discover screen lists it: one install
// option, suggested id from the catalog key.
func toEntry(key string, e Entry) mcpsrc.Entry {
	title := e.Title
	if title == "" {
		title = key
	}
	in := mcpsrc.Install{Label: label(e)}
	if id := mcpreg.SuggestID(key, nil); id == "" {
		in.Unsupported = ReasonNoID
	} else if d, err := ToDraft(key, id, e); err != nil {
		in.Unsupported = err.Error()
	} else {
		in.Draft = &d
	}
	return mcpsrc.Entry{
		Source:      Name,
		ID:          key,
		Title:       title,
		Description: e.Description,
		Trust:       EntryTrust(e),
		Installs:    []mcpsrc.Install{in},
	}
}
