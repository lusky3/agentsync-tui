package mcpreg

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/discover/argtok"
	"github.com/lusky3/agentsync-tui/internal/ident"
)

// Choice is one way to install a server: exactly one of Package or Remote
// is set, pointing into the Server it came from.
type Choice struct {
	Label   string
	Package *Package
	Remote  *Remote
}

// Draft is an install spec derived from a registry record, ready to
// prefill the add form. Prompts are values the user must still supply;
// secrets are never prompted for or carried as values, only as
// ${secret:...} references in Spec. Trust lines describe risks of this
// particular choice (the source adds its own).
type Draft struct {
	Spec       actions.AddSpec
	Prompts    []Prompt
	Provenance string
	Trust      []string
}

// Prompt is a non-secret value the user must supply (an environment
// variable the registry marks required but gives no fixed value for).
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

	trustMutableTag = "image tag is mutable (not digest-pinned)"
	provenanceHead  = "official registry: "
	httpsPrefix     = "https://"
	typeStdio       = "stdio"
	registryOCI     = "oci"
	// maxSuffix is the highest de-duplication suffix SuggestID tries.
	maxSuffix = 100
	// shortNameLen bounds a name quoted in a refusal reason.
	shortNameLen = 32
)

var (
	envNameRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// npm package names: lowercase, optionally @scope/.
	npmNameRe = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]{0,213}$`)
	// PyPI project names (PEP 508 / 503): letters, digits, ._- inside.
	pypiNameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,198}[A-Za-z0-9])?$`)
	// versionRe covers semver and PEP 440 release strings without epochs.
	versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,63}$`)
	// OCI repository: an optional registry host (with a dot, or localhost,
	// optional port), then lowercase path components. No tag or digest:
	// the version supplies the tag.
	ociNameRe = regexp.MustCompile(`^((localhost|[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+)(:[0-9]{1,5})?/)?` +
		`[a-z0-9]+([._-]+[a-z0-9]+)*(/[a-z0-9]+([._-]+[a-z0-9]+)*)*$`)
	ociTagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

// pkgRuntime is how a supported registryType runs: the command is fixed per
// type and never taken from the record.
type pkgRuntime struct {
	command       string
	name, version *regexp.Regexp
	args          func(name, version string) []string
}

var runtimes = map[string]pkgRuntime{
	"npm": {command: "npx", name: npmNameRe, version: versionRe,
		args: func(n, v string) []string { return []string{"-y", n + "@" + v} }},
	"pypi": {command: "uvx", name: pypiNameRe, version: versionRe,
		args: func(n, v string) []string { return []string{n + "==" + v} }},
	registryOCI: {command: "docker", name: ociNameRe, version: ociTagRe,
		args: func(n, v string) []string { return []string{"run", "-i", "--rm", n + ":" + v} }},
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
	rt := runtimes[p.RegistryType]
	if rt.command == "" {
		return unsupported(reasonNoChoice)
	}
	if !rt.name.MatchString(p.Identifier) || !rt.version.MatchString(p.Version) {
		return ErrUnsafeIdentifier
	}
	if err := checkTransport(p.Transport); err != nil {
		return err
	}
	// Runtime options would go before the package token and can widen what
	// the runtime does (a docker volume, an npm registry): never applied.
	if len(p.RuntimeArgs) > 0 {
		return unsupported(reasonRuntimeArgs)
	}
	extra, err := packageArgs(p.PackageArgs)
	if err != nil {
		return err
	}
	if err := b.env(p.Env); err != nil {
		return err
	}
	b.d.Spec.Type = typeStdio
	b.d.Spec.Command = rt.command
	b.d.Spec.Args = append(rt.args(p.Identifier, p.Version), extra...)
	if p.RegistryType == registryOCI {
		b.d.Trust = append(b.d.Trust, trustMutableTag)
	}
	return nil
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
// Prompt, and an optional plain one is omitted.
func (b *builder) env(vars []EnvVar) error {
	seen := make(map[string]bool, len(vars))
	for _, v := range vars {
		if !envNameRe.MatchString(v.Name) || seen[v.Name] {
			return unsupported(reasonEnvName)
		}
		seen[v.Name] = true
		if err := b.envVar(v); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) envVar(v EnvVar) error {
	switch {
	case v.Value != "":
		// addEnv refuses ',' and '\'; '{' and '$' could be a template or a
		// reference to another server's secret.
		if strings.ContainsAny(v.Value, `,\{$`) {
			return unsupported(reasonEnvValue)
		}
		b.setEnv(v.Name, v.Value)
	case v.Secret:
		ref, err := b.ref(v.Name)
		if err != nil {
			return err
		}
		b.setEnv(v.Name, ref)
	case v.Required:
		b.d.Prompts = append(b.d.Prompts, Prompt{
			Name: v.Name, Required: true, Default: v.Default, Description: v.Description,
		})
	}
	return nil
}

func (b *builder) setEnv(name, value string) {
	if b.d.Spec.Env == nil {
		b.d.Spec.Env = map[string]string{}
	}
	b.d.Spec.Env[name] = value
}

// ref returns SecretRef(b.id, name), refusing a name with no lower_snake
// form or one whose form another name of this draft already took.
func (b *builder) ref(name string) (string, error) {
	key := lowerSnake(name)
	if key == "" {
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
	if !strings.HasPrefix(r.URL, httpsPrefix) || len(r.URL) == len(httpsPrefix) || hasSpace(r.URL) {
		return unsupported(reasonNotHTTPS)
	}
	u, err := b.fill(r.URL, r.Variables)
	if err != nil {
		return err
	}
	var headers []string
	for _, h := range r.Headers {
		line, err := b.header(h)
		if err != nil {
			return err
		}
		if line != "" {
			headers = append(headers, line)
		}
	}
	b.d.Spec.Type = typ
	b.d.Spec.URL = u
	b.d.Spec.Headers = headers
	return nil
}

// header returns "Name: value", or "" for an optional header with nothing
// to send. A header with no value that is secret becomes a reference
// named after the header itself.
func (b *builder) header(h Header) (string, error) {
	if !headerNameRe.MatchString(h.Name) {
		return "", unsupported(reasonHeaderName)
	}
	var v string
	var err error
	switch {
	case h.Value != "":
		v, err = b.fill(h.Value, h.Variables)
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
	return h.Name + ": " + v, nil
}

// fill replaces each {name} placeholder in tmpl with the declared
// variable's secret reference or default. The template itself may hold no
// '$' (so it can never write a reference of its own); a placeholder that is
// undeclared, unbalanced, nested or neither secret nor defaulted refuses.
func (b *builder) fill(tmpl string, vars []EnvVar) (string, error) {
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
		val, err := b.resolve(rest[i+1:i+1+j], vars)
		if err != nil {
			return "", err
		}
		out.WriteString(rest[:i])
		out.WriteString(val)
		rest = rest[i+2+j:]
	}
}

// resolve fills one placeholder from its declared variable.
func (b *builder) resolve(name string, vars []EnvVar) (string, error) {
	for _, v := range vars {
		if v.Name != name {
			continue
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
// or digit. lower_snake lowercases ASCII letters, turns every run of other
// characters into one '_' and trims '_' from both ends ("X-Api-Key" →
// "x_api_key", "apiKey" → "apikey"). The key has no '.', so the last '.'
// always separates the server id from the name.
func SecretRef(id, name string) string {
	key := lowerSnake(name)
	if key == "" {
		return ""
	}
	return "${secret:" + id + "." + key + "}"
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
