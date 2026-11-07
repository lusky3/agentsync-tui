# Discover & Install (MCP Registry + skills.sh) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user search the official MCP Registry, the Docker MCP Catalog and skills.sh from inside agentsync-tui, review a result, and install it into their agentsync home through the same confirm-before-run discipline as every other change.

**Architecture:** A small, sandboxed, read-only HTTP layer (`internal/netx`) feeds three source clients (`mcpreg` and `dockercat` behind one `mcpsrc.Source` interface, plus `skillsh`). MCP results are mapped to the existing `actions.AddSpec` and opened in the existing add form, prefilled, so the mutation stays an `agentsync mcp add` argv behind the confirm modal. Skills have no install command in agentsync, so a skill is downloaded to a staging directory, shown for review (manifest-hashed), then copied into `<home>/skills/<name>` by one narrowly-scoped installer behind the same confirm modal.

**Tech Stack:** Go 1.26 stdlib (`net/http`, `archive/tar`, `compress/gzip`, `crypto/sha256`); Bubble Tea v2 (existing); **one new module dependency**, a YAML parser, needed only for the Docker catalog (Decision 5).

**Spec:** this document plus the research findings in "Decisions" below (verified 2026-11-07 against live endpoints). Existing contracts: `docs/superpowers/plans/2026-11-03-agentsync-tui.md` (Global Constraints) and `README.md` (safety model).

## Decisions made (read first)

**Status: final (reviewed 2026-11-07; the maintainer delegated these calls).** Each was decided from verified facts; change one and the affected tasks are named. Execution method: subagent-driven development (Decision 6).

1. **MCP sources = the official MCP Registry v0.1 plus the Docker MCP Catalog.** They are complementary, not mirrors.
   - **Official registry** (`https://registry.modelcontextprotocol.io/v0.1/servers`): no auth, versioned (frozen since 2025-10-24, still labelled preview), about 58k records, install-ready schema (packages, `runtimeHint`, transport, `environmentVariables` with `isRequired`/`isSecret`, `remotes`). Breadth, no vetting and no popularity data.
   - **Docker MCP Catalog** (`https://desktop.docker.com/mcp/catalog/v3/catalog.yaml`, one ~570 KB YAML file, no auth, MIT source repo `docker/mcp-registry`): about 318 curated entries (237 container servers, 78 remote, 3 `poci`). Only 92 match an official-registry entry by exact URL, so it adds vetted entries in a different shape. Every container image is pinned by `sha256` digest and built from a pinned commit, Docker publishes attestations and SBOMs for its `mcp/*` images, and entries go through a PR review. Installing one is safer than a raw `npx`/`uvx` fetch because the digest is immutable and the server runs inside a container. Costs: the user needs Docker, some entries use Docker template syntax that this plan treats as unsupported (Task 5B), and the TUI does **not** verify image signatures (it says so).
   - *Skipped, with reasons:* **mcpservers.org** (no API, Cloudflare blocks clients, robots disallows `/api/`); **mcp.directory** (robots disallows `/api/`, no ToS, malformed install configs); **glama.ai** (API key required and its ToS forbids shipping a key in a public client; mandatory attribution on every shown record; no command/args snippet); **Smithery** (registry search returns HTTP 500, entries are almost all remote servers behind Smithery OAuth, and its own 2026-10-31 notice shut down server URLs, API keys, CLI and SDKs); **PulseMCP** (v0beta sunset with 410, `v0.1` requires a partner API key); **GitHub's MCP registry** (runner-up: same schema as the official registry, no auth, 10-request rate-limit header; 742 of 771 entries are already in the official registry, so it is a quality filter, not new coverage; revisit as a "curated" filter later); **npm/PyPI keyword search** (nothing vetted, no env/transport data, real typosquat history such as `postmark-mcp`). *Affects: Tasks 4-8.*
2. **skills.sh discovery = the unauthenticated `GET https://skills.sh/api/search?q=&limit=`** that Vercel's own `skills` CLI calls. The documented `/api/v1/*` API needs a Vercel OIDC token, unusable from a desktop app. Caveats, stated in the README: the endpoint is undocumented and `robots.txt` disallows `/api/`; skills.sh's ToS permits "reasonable use, including caching". Mitigations: only on an explicit Enter in the search box (no background or startup calls), 10-minute disk cache, honest User-Agent, `--offline` kill switch, and a **paste `owner/repo` path that never touches skills.sh**, so the feature survives being blocked. *Affects: Tasks 8, 12.*
3. **skills install = stage, review, copy into `<home>/skills/<name>/`.** agentsync deliberately has no `skill add` (upstream issue #200) and only imports from an agent's native directory; `plugin add` cannot install a skills-only repo. Shelling out to `npx skills add` was rejected: it needs Node, sends telemetry, and writes into the real `~/.claude/skills`, outside agentsync's canonical model. Creating `skills/<name>/SKILL.md` is the documented authoring path, so this is the same effect as the user making the directory; it is still a **deliberate exception** to the README rule "the TUI writes no agentsync files directly" (the only prior exception is `E`, which opens the editor). The exception is confined to one function (`install.Skill`) with the safeguards in Task 10. *Affects: Tasks 9-12.*
4. **New HTTP client is a deliberate first for this codebase** (today there is no `net/http`; the README says the CLI is the API). It is read-only, allowlisted to four hosts, and every mutation remains an agentsync argv or the Task 10 copy. *Affects: Task 1, 3, 13.*
5. **One new dependency: a YAML library** (the Docker catalog is YAML; the module graph has TOML but no YAML). Use `go.yaml.in/yaml/v3` (the maintained continuation of the archived `gopkg.in/yaml.v3`); Task 5B verifies the module path, licence and maintenance status before adding it, runs `govulncheck`, and records it in the README. Decode only into typed structs with a size cap; no `interface{}` decoding. Alternative: have the plan fetch `docker/mcp-registry` per-server files instead, which is still YAML, so there is no way around the parser. *Affects: Task 5B.*
6. **Execution: subagent-driven, in three PRs.** PR A = Tasks 1-3 (foundation, no user-visible feature). PR B = Tasks 4-7 (MCP discover, including registering its tab in Task 6, so the screen is reachable even if PR C is delayed). PR C = Tasks 8-13 (skills, the Skill search registration, e2e). Each PR ends with an independent whole-branch review. Reason: the extractor, installer and identifier mapping are the security-sensitive parts and deserve a fresh reviewer per task; splitting PRs keeps each review package small.
7. **Credentials are namespaced per server.** A generated reference is `${secret:<id>.<lower_snake_name>}` (for example `${secret:brave-search.brave_api_key}`), never a bare `${secret:api_key}`. Two unrelated servers that both declare `API_KEY` must not silently share one vault entry and so send one service's credential to another. The user can still edit the reference in the form to share deliberately. The pattern satisfies the vault's key rule `[A-Za-z0-9][A-Za-z0-9._-]*` and is not `mask.TokenShaped` for normal names. *Affects: Tasks 5, 5B, 7.*

## Relationship to the Source repo plan

`/home/cody/tmp/agentsync-tui-source-repo-plan.md` is executed **first**. Build Task 11's install confirm on its shell changes (`Action.Program`, `DoChain`, one confirm path) rather than adding a parallel path, rebase shared files (`internal/ui/app.go`, `exec.go`, `modal.go`, `internal/app/screens.go`, `main.go`, README safety model) onto it, and keep its `TestImageDigestInArgsNotAFinding` green: the digest-pinned Docker installs from Task 5B must stay committable from the Source repo tab.

## Global Constraints

- Go `1.26.0` (go.mod), toolchain pinned `go1.26.10`; Bubble Tea `charm.land/bubbletea/v2`; **exactly one new third-party dependency (the YAML parser, Task 5B)**.
- Network is **user-initiated only**: no calls at startup, on tab switch, or on a timer. `--offline` (flag) disables all discovery; screens then say so and stay usable for local data.
- Allowed hosts, https only: `registry.modelcontextprotocol.io`, `desktop.docker.com`, `skills.sh`, `codeload.github.com`. GET only. No cookies. Redirects only between allowed hosts, max 3.
- Every string that came from the network passes `mask.Sanitize` before it enters a model, and `mask.Text`/`mask.CommandLine` before it is rendered inside a command or snippet. Fetched content is never written to the activity log (log: source, host, byte count, sha256, outcome).
- Mutations: `agentsync` argv `Action`s through the confirm modal, except the Task 10 skill copy (confirm modal, `y`).
- A registry, catalog or skills.sh entry never supplies a `command`: the command is derived from the package `registryType` / entry kind (Tasks 5, 5B). Docker entries install only when the image is digest-pinned (`@sha256:`) and no template syntax (`{{`) appears anywhere that would reach argv, env or headers. Identifiers/versions starting with `-` or containing whitespace, `,` or `\` are refused, not escaped.
- Tests are hermetic (`httptest`); one opt-in live smoke test behind build tag `live` and env `AGENTSYNC_TUI_LIVE=1`, never in CI.
- Commit messages: conventional commits, **no `Co-Authored-By`, `Claude-Session` or "Generated with" lines**. Never push to `main`, never force-push, merge only when asked.
- Gate per commit: `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run && go test -race -count=1 ./...` (if `golangci-lint` is not on PATH use `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run`). Run each check separately or under `set -e`; a chained `&&` inside a script without `set -e` has hidden a gofmt failure before.

## Review Focus

Failure modes the spec implies but no obvious task would test; each has its test in the owning task.

1. **Hostile text from the network** (ANSI/OSC/bidi/zero-width in names, descriptions, SKILL.md): rendered inert. Tests in Tasks 4, 8, 12.
2. **Argv/flag injection and unrendered templates** (`--registry=evil`, `a,b`, `x y`, leading `-`, `\`, a leftover `{{filesystem.paths|volume|into}}` from a Docker entry): refused or marked unsupported before an `AddSpec` exists, never half-rendered into a command. Tasks 5, 5B.
3. **Malicious archives and YAML** (path traversal, absolute paths, symlinks, hardlinks, devices, case-fold collisions, decompression bombs, file-count bombs, `.git` content; YAML alias bombs and oversized catalogs): rejected during extract/decode. Tasks 5B, 9.
4. **Non-JSON, blocked, or slow responses** (Cloudflare HTML 403, 401, 429, truncated JSON, slow-loris body, redirect to another host, pagination cursor that repeats): clear single error, no retry loop, no hang. Tasks 1, 4, 8.
5. **Name collisions and TOCTOU** (registry name normalises to an existing id; two servers declaring the same env name would share one vault secret; skill dir already exists; staged files altered between review and install; planted symlink after review; case-insensitive filesystems): refused or namespaced, nothing partial left behind. Tasks 5, 5B, 10.

## File Structure

| Path | Responsibility |
|---|---|
| `internal/netx/client.go` | Sandboxed GET client (allowlist, caps, timeouts, redirects, UA) |
| `internal/discover/cache/cache.go` | Disk cache (0700/0600, TTL, symlink-safe) |
| `internal/discover/mcpreg/{types,client,map}.go` | Registry client + `Server`→`AddSpec` mapping |
| `internal/discover/mcpsrc/source.go` | Common `Entry`/`Install`/`Page` types, the `Source` interface, and the adapter that makes the official registry a `Source` |
| `internal/discover/dockercat/{catalog,map}.go` | Docker MCP Catalog fetch, client-side search, entry → `Draft` mapping |
| `internal/discover/skillsh/client.go` | skills.sh search client |
| `internal/discover/stage/{fetch,extract,find}.go` | Archive download, safe extract, skill discovery, manifest |
| `internal/install/skill.go` | The single permitted direct write: copy a reviewed skill into the home |
| `internal/ui/discover/{mcp,skills,review}.go` | Two screens (MCP discover with a source toggle, Skill search) + skill review view (shared `ui.List`/`ui.Scroll`/`ui.Field`) |
| `internal/ui/compose.go` | `ComposeMcpMsg`, `InstallSkillMsg` + shell handling |
| `internal/ui/servers/form.go` (modify) | `OpenPrefilled(AddSpec, provenance string)` |
| `internal/app/screens.go` (modify) | Register the two screens at the END of the tab order |
| `main.go` (modify) | `--offline` flag, build `Deps.Discover` |

Dependency order: 1 → 2 → 3; 4,5 need 1-3; 5A needs 4,5; 5B needs 5A; 6,7 need 5A,5B; 8 needs 1-3; 9 needs 1; 10 needs 9; 11 needs 10; 12 needs 8-11; 13 last. Parts 2 (Tasks 4-7 including 5A and 5B, MCP) and 3 (Tasks 8-12, skills) are independently shippable as separate PRs after Task 3.

---

## Part 1: Foundation

### Task 1: Sandboxed HTTP client

**Files:** Create `internal/netx/client.go`, `internal/netx/client_test.go`.

**Interfaces:**
- Produces:
  - `type Client struct{ ... }`
  - `func New(opts Options) *Client`; `type Options struct{ AllowedHosts []string; UserAgent string; Timeout time.Duration; Transport http.RoundTripper }` (Transport injectable for tests)
  - `func (c *Client) Get(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error)`
  - `func (c *Client) Stream(ctx context.Context, rawURL string, maxBytes int64) (io.ReadCloser, error)` (body capped; closing releases the connection)
  - sentinel errors: `ErrScheme`, `ErrHost`, `ErrTooLarge`, `ErrRedirect`, `ErrStatus` (wrapped in `*StatusError{Code int}` carrying the status; its message never includes the response body)

- [ ] **Step 1: Write failing tests** (`httptest.NewTLSServer`, host mapping via the injected Transport): `TestRefusesHTTP`, `TestRefusesHostOutsideAllowlist`, `TestRedirectToOtherAllowedHostFollowed`, `TestRedirectToDisallowedHostRefused`, `TestMoreThanThreeRedirectsRefused`, `TestBodyOverCapIsErrTooLarge` (also for a gzip response that expands past the cap), `TestSlowBodyHitsTimeout`, `TestNon2xxIsStatusErrorWithoutBody` (assert `StatusError.Code==403` and that the error string does not contain the body), `TestSetsUserAgentAndNoCookies`, `TestOnlyGET`.
- [ ] **Step 2: Run** `go test ./internal/netx/... -run . -v`. Expected: FAIL (package missing).
- [ ] **Step 3: Implement** `New`/`Get`/`Stream` over `net/http` with `CheckRedirect` enforcing the allowlist and hop count, `Jar=nil`, `DisableCompression:false` but the cap applies to **decompressed** bytes (wrap the body in `io.LimitReader(cap+1)` and fail if exceeded). `UserAgent` default `agentsync-tui/<version> (+https://github.com/lusky3/agentsync-tui)`.
- [ ] **Step 4: Run** the same command. Expected: PASS. Run `go test -race -count=3 ./internal/netx/...`.
- [ ] **Step 5: Commit** `feat(netx): sandboxed read-only HTTP client for discovery`.

### Task 2: Disk cache

**Files:** Create `internal/discover/cache/cache.go`, `cache_test.go`.

**Interfaces:**
- Produces: `func Open(dir string) (*Cache, error)` (creates `0700`; refuses a symlinked dir); `func (c *Cache) Get(key string, ttl time.Duration) ([]byte, bool)`; `func (c *Cache) Put(key string, data []byte) error`; `func DefaultDir() (string, error)` = `os.UserCacheDir()+"/agentsync-tui"`. Keys are hashed (`sha256` hex) so callers cannot influence file names.

- [ ] **Step 1: Write failing tests:** `TestPutGetRoundTrip`, `TestExpiredEntryIsMiss`, `TestFilesAre0600DirIs0700`, `TestSymlinkedCacheDirRefused`, `TestSymlinkedEntryNotFollowed` (pre-plant a symlink at the hashed path; `Put` must not write through it), `TestOversizeEntryRefused` (>8 MiB), `TestCorruptEntryIsMiss`.
- [ ] **Step 2: Run** `go test ./internal/discover/cache/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement** with write-to-temp + atomic rename, `O_NOFOLLOW`-style checks via `os.Lstat`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(cache): symlink-safe TTL disk cache for discovery results`.

### Task 3: `--offline`, `Deps.Discover`, and the documented exception

**Files:** Modify `main.go`, `internal/ui/app.go` (Deps), `README.md`, `docs/superpowers/plans/2026-11-03-agentsync-tui.md` (Global Constraints note pointing here); Test `main_test.go`, `internal/ui/app_test.go`.

**Interfaces:**
- Produces: `type ui.Discover struct{ Offline bool; HTTP *netx.Client; Cache *cache.Cache; Stage string }` as `Deps.Discover`; main builds it from `--offline` (default false) and `cache.DefaultDir()`. When `Offline`, `HTTP` is nil.

- [ ] **Step 1: Write failing tests:** `TestOfflineFlagLeavesHTTPNil`, `TestDiscoverDefaultsOnline`, `TestHelpMentionsOffline`.
- [ ] **Step 2: Run** `go test . ./internal/ui/ -run 'Offline|Discover' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the flag and Deps wiring. README: new "Discovery and network access" section stating the four hosts (and the Docker catalog URL and its MIT source repository), that calls happen only on explicit search/install, what is cached and where, `--offline`, the skills.sh caveat from Decision 2, and the `install.Skill` exception from Decision 3 (reword the existing "writes no agentsync files" sentence to name the two exceptions).
- [ ] **Step 4: Run** the full gate. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add --offline and the discovery dependency wiring; document the network exception`.

---

## Part 2: MCP registry (independently shippable)

### Task 4: Registry client

**Files:** Create `internal/discover/mcpreg/types.go`, `client.go`, `testdata/page.json` (trimmed real sample from the research), `client_test.go`.

**Interfaces:**
- Consumes: `netx.Client.Get`, `cache.Cache`, `mask.Sanitize`.
- Produces:
  - `type Server struct{ Name, Description, Version, RepoURL, Status string; Packages []Package; Remotes []Remote }`
  - `type Package struct{ RegistryType, Identifier, Version, RuntimeHint, Transport string; Env []EnvVar; RuntimeArgs, PackageArgs []Argument }` (`runtimeArguments` and `packageArguments`, kept so Task 5 can tell "no arguments" from "an argument you must supply")
  - `type Argument struct{ Type, Name, Value, Default string; Required bool }` (`Type` is `positional` or `named`)
  - `type EnvVar struct{ Name, Description, Default string; Required, Secret bool }`
  - `type Remote struct{ Type, URL string; Headers []Header; Variables []EnvVar }` (`Variables` are the remote's declared URL-template variables)
  - `type Header struct{ Name, Value string; Required, Secret bool; Variables []EnvVar }`
  - `type Page struct{ Servers []Server; Next string }`
  - `func NewClient(h *netx.Client, c *cache.Cache) *Client`
  - `func (c *Client) Search(ctx context.Context, query, cursor string) (Page, error)` → `GET /v0.1/servers?search=<q>&limit=30&version=latest[&cursor=]`; results with `status != "active"` are returned but `Server.Status` is set so the UI can mark deprecated/deleted.
  - `var ErrBlocked, ErrBadResponse error`

- [ ] **Step 1: Write failing tests:** `TestParsesRealSample` (assert name `com.pulsemcp/remote-filesystem`, package `npm`/`remote-filesystem-mcp-server`/`0.1.5`/`stdio`, env `GCS_PRIVATE_KEY` is `Secret`, `GCS_BUCKET` is `Required`); `TestRemoteStreamableHTTP`; `TestArgumentsDefaultsAndVariablesDecoded` (a sample with `runtimeArguments`, a required `packageArguments` entry without a value, an env `default`, and remote/header `variables` keeps all of them); `TestSanitizesHostileText` (name/description containing `\x1b]8;;http://x\x07`, bidi override and NUL come back clean); `TestDeprecatedStatusKept`; `TestCloudflareHTMLIsErrBlocked` (403 `text/html`); `TestTruncatedJSONIsErrBadResponse`; `TestRepeatingCursorStopsPagination` (server returns the same `nextCursor`; `Search` surfaces `Next==""` rather than looping); `TestCachedWithin10Minutes` (second identical search makes zero HTTP calls); `TestQueryIsURLEscaped`.
- [ ] **Step 2: Run** `go test ./internal/discover/mcpreg/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `Search` with decode into private wire structs (unknown fields ignored, unknown `registryType` kept as-is), sanitize at the boundary, cache key = full URL, 10 min TTL.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(mcpreg): official MCP Registry v0.1 search client`.

### Task 5: Registry entry → `AddSpec`

**Files:** Create `internal/discover/mcpreg/map.go`, `map_test.go`.

**Interfaces:**
- Consumes: Task 4 types; `actions.AddSpec`; `ident.Valid`.
- Produces:
  - `type Choice struct{ Label string; Package *Package; Remote *Remote }`
  - `func Choices(s Server) []Choice` (one per supported package and remote; unsupported `registryType`s are omitted)
  - `type Draft struct{ Spec actions.AddSpec; Prompts []Prompt; Provenance string }`
  - `type Prompt struct{ Name string; Secret, Required bool; Default, Description string }` (values the user must still supply; secret ones are **references**, never values)
  - `func ToDraft(s Server, c Choice, id string) (Draft, error)` (`id` is the suggested server id; it namespaces secret references per Decision 7)
  - `func SuggestID(name string, taken func(string) bool) string`
  - `func SecretRef(id, name string) string` → `${secret:<id>.<lower_snake_name>}`
- Mapping rules (pinned): `npm` → `Command:"npx"`, `Args:["-y","<identifier>@<version>"]`; `pypi` → `Command:"uvx"`, `Args:["<identifier>==<version>"]`; `oci` → `Command:"docker"`, `Args:["run","-i","--rm","<identifier>:<version>"]` plus a trust line `"image tag is mutable (not digest-pinned)"`; remote `streamable-http` → `Type:"http"`, `sse` → `Type:"sse"`, with `URL` and `Headers`. Header values and remote URLs may contain `{var}` placeholders: a placeholder whose variable is marked secret becomes `SecretRef(id, var)` in place (so `Bearer {api_key}` → `Bearer ${secret:<id>.api_key}`); a non-secret variable with a `default` takes the default; any other placeholder makes the choice unsupported. Stdio `Type:"stdio"`. Env: names must match `^[A-Za-z_][A-Za-z0-9_]*$` or the choice is unsupported; secret → value `SecretRef(id, name)`; non-secret required → a `Prompt` (with `Default` when given); non-secret optional → omitted. Runtime arguments (`runtimeArguments`, options for `npx`/`uvx`/`docker` themselves, which would have to go **before** the package token and can widen what the runtime does, such as a docker volume): any present makes the choice unsupported with the reason `sets runtime options this tool does not apply`. Package arguments (`packageArguments`): an argument with a fixed `value` (positional, or named as `--name value`) is appended after the package token, validated like an identifier except that commas are allowed (`McpAdd` escapes them); a **required** argument without a fixed value makes the choice unsupported with the reason `requires arguments the registry leaves to you: <names>`; optional arguments without a value are omitted. `SuggestID`: last `/` segment, lowercased, invalid runs → `-`, trimmed, must satisfy `ident.Valid`, de-duplicated with `-2`, `-3`.

- [ ] **Step 1: Write failing tests (table):** `TestNPMMapping`, `TestPyPIMapping`, `TestOCIMapping`, `TestRemoteHTTPAndSSE`, `TestSecretEnvBecomesSecretRef` (assert the draft contains no value, only `${secret:...}`), `TestRequiredPlainEnvBecomesPrompt`, `TestSuggestIDCases` (`com.pulsemcp/remote-filesystem`→`remote-filesystem`; `io.github.a/B C`→`b-c`; `../evil`→`evil`; empty→error; collisions → `x-2`), `TestSecretRefsAreNamespacedByServer` (two servers that both declare `API_KEY` get `${secret:a.api_key}` and `${secret:b.api_key}`), `TestBearerTemplateHeaderBecomesSecretRef`, `TestUndeclaredPlaceholderUnsupported`, `TestFixedPackageArgumentsAppended`, `TestRequiredArgumentWithoutValueUnsupported`, `TestRuntimeArgumentsUnsupported` (never placed after the package token), `TestEnvDefaultBecomesPromptDefault`, `TestDeclaredURLVariableWithDefaultResolved`, `TestInvalidEnvNameUnsupported` (`A-B`, `1X`, `X Y`), `TestOCITagGetsMutableTrustLine`, **injection refusals** `TestIdentifierWithLeadingDashRefused` (`--registry=evil`), `TestIdentifierWithCommaRefused` (`a,b`), `TestIdentifierWithSpaceOrBackslashRefused`, `TestVersionWithLeadingDashRefused`, `TestUnknownRegistryTypeOmittedFromChoices`, `TestDraftNeverCarriesRegistryCommand` (a wire payload with a `command` field is ignored).
- [ ] **Step 2: Run** `go test ./internal/discover/mcpreg/ -run 'Mapping|Suggest|Refused|Prompt|Secret|Omitted|Never' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** per the rules above; validate `Identifier`/`Version` per registryType with strict regexes (npm scoped/unscoped names; PEP 503 names; OCI refs without whitespace); on any violation return `ErrUnsafeIdentifier`. Then run `actions.McpAdd(draft.Spec)` in a test and assert it returns no error for every success case.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(mcpreg): derive install specs from registry packages and remotes`.

### Task 5A: Common MCP entry model and the `Source` interface

**Files:** Create `internal/discover/mcpsrc/source.go`, `source_test.go`.

**Interfaces:**
- Consumes: Task 4-5 types (`mcpreg.Server`, `mcpreg.Draft`, `mcpreg.Prompt`, `mcpreg.Choices`, `mcpreg.ToDraft`).
- Produces:
  - `type Entry struct{ Source, ID, Title, Description string; Trust []string; Deprecated bool; Installs []Install }`
  - `type Install struct{ Label string; Draft *mcpreg.Draft; Unsupported string }` (`Unsupported` non-empty means shown with that reason and not installable)
  - `type Page struct{ Entries []Entry; Next string }`
  - `type Source interface{ Name() string; Search(ctx context.Context, query, cursor string) (Page, error) }`
  - `func Official(c *mcpreg.Client) Source`: adapts the registry; `Trust` = `["unreviewed: the registry does not scan entries", "namespace ownership is verified at publish time"]`; the adapter computes the id with `SuggestID` (taken = none at this point; the form re-checks) and passes it to `ToDraft`; choices whose `ToDraft` fails become `Unsupported` with the error text.

- [ ] **Step 1: Write failing tests:** `TestOfficialAdapterMapsEntries`, `TestUnsafeIdentifierBecomesUnsupportedNotDropped` (the entry is still listed, with the refusal reason, and has no `Draft`), `TestDeprecatedCarriedThrough`, `TestTrustLinesAlwaysPresentForOfficial`, `TestSourceNameStable` (`"official"`).
- [ ] **Step 2: Run** `go test ./internal/discover/mcpsrc/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the types and the adapter.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(mcpsrc): common entry model and source interface`.

### Task 5B: Docker MCP Catalog source

**Files:** Modify `go.mod`/`go.sum` (YAML dependency); Create `internal/discover/dockercat/catalog.go`, `map.go`, `testdata/catalog.yaml` (trimmed real entries: `brave`, `context7`, `filesystem`, one `poci`, one remote with `oauth`), `testdata/alias_bomb.yaml`, `catalog_test.go`, `map_test.go`. (The host `desktop.docker.com` is already on the Task 3 allowlist.)

**Interfaces:**
- Consumes: `netx.Client.Get`, `cache.Cache`, `mcpsrc.Entry/Install/Page/Source`, `mcpreg.Draft/Prompt`, `actions.AddSpec`, `ident.Valid`.
- Produces: `func NewClient(h *netx.Client, c *cache.Cache) *Client`; `(*Client).Name() string` (`"docker"`); `(*Client).Search(ctx, query, cursor string) (mcpsrc.Page, error)` which downloads `https://desktop.docker.com/mcp/catalog/v3/catalog.yaml` (cap 4 MiB, cached 1 hour), filters client-side (case-insensitive substring over key, title, description), sorts by key, pages 30 at a time with `cursor` = decimal offset; `func ToDraft(key, id string, e Entry) (mcpreg.Draft, error)`.
- Pinned mapping:
  - `server` kind: install only if `image` matches `@sha256:[0-9a-f]{64}`, no `volumes`, and no `{{` in `command`, `env` values, or `secrets`. Draft: `Type:"stdio"`, `Command:"docker"`, `Args:["run","-i","--rm", (+"--network","none" when disableNetwork is true), "-e",<NAME> for every secret env and every env entry, "<image>", then each element of the entry's fixed `command`]` (arguments to the image's entrypoint, such as Stripe's `--tools=all`; without them some servers cannot start). A `command` element is validated like a fixed package argument (Task 5); one that fails, or any `{{` template in it, makes the entry unsupported rather than dropping it. `Env`: secrets → `NAME=` + `mcpreg.SecretRef(id, NAME)`; plain `env[]` → literal value. Env names must match `^[A-Za-z_][A-Za-z0-9_]*$` or the entry is unsupported. Secret values are never in argv.
  - `remote` kind: `streamable-http`→`Type:"http"`, `sse`→`Type:"sse"`; URL must be `https://`; `${NAME}` inside a header value → `mcpreg.SecretRef(id, NAME)` in place (so `Bearer ${TOKEN}` keeps its `Bearer ` prefix); any other templated header → unsupported. Entries with `oauth` → unsupported (`"requires an OAuth sign-in that agentsync cannot perform"`).
  - `poci` kind and image without a digest → unsupported with the reason.
  - `Trust` lines: `"Docker-reviewed catalog; image pinned by digest <first 12 hex>"`, `"image signatures and attestations are NOT verified by this tool"`, plus `"declares allowHosts, which plain docker run does not enforce"` when `allowHosts` is non-empty, plus `"requires Docker"`. `Provenance` = `docker catalog: <key> (digest <12 hex>)`.
  - `ID` suggestion = catalog key through `mcpreg.SuggestID`.

- [ ] **Step 1: Write failing tests:** `TestParsesTrimmedRealCatalog` (counts and kinds), `TestBraveMapsToDockerRunWithSecretRef` (asserts argv `run -i --rm -e BRAVE_API_KEY mcp/brave-search@sha256:…` and Env `BRAVE_API_KEY=${secret:brave.brave_api_key}`, and that the digest-pinned image is exactly the catalog's), `TestDisableNetworkAddsNetworkNone`, `TestFixedCommandAppendedAfterImage` (a `command: [--tools=all]` entry ends `… <image> --tools=all`), `TestContext7RemoteMapsToHTTPWithSecretHeader`, `TestFilesystemTemplateIsUnsupportedNotRendered` (the `{{filesystem.paths|volume|into}}` text never appears in any `Draft`), `TestImageWithoutDigestUnsupported`, `TestPociUnsupported`, `TestOAuthRemoteUnsupported`, `TestHttpURLRemoteUnsupported`, `TestAllowHostsAddsTrustWarning`, `TestSearchFiltersSortsAndPages`, `TestCachedForOneHour`, `TestSanitizesHostileText`, **`TestAliasBombRejectedQuickly`** (decode finishes in well under 1 s and returns an error), `TestOversizeCatalogRefused` (over 4 MiB), `TestHTMLInsteadOfYAMLIsErrBlocked`, and a table asserting `actions.McpAdd(draft.Spec)` succeeds for every installable entry.
- [ ] **Step 2: Run** `go test ./internal/discover/dockercat/... -v`. Expected: FAIL.
- [ ] **Step 3: Add the dependency and implement.** Before `go get`, check the module path, licence (MIT/Apache) and last release of `go.yaml.in/yaml/v3`; run `go get`, `go mod tidy`, `govulncheck`; if any check fails stop and report. Decode into typed structs only.
- [ ] **Step 4: Run** the package with `-race -count=3`. Expected: PASS. Mutation check: drop the digest requirement; `TestImageWithoutDigestUnsupported` must fail. Fuzz `FuzzDecodeCatalog` for 60 s: no panic, bounded memory.
- [ ] **Step 5: Commit** `feat(dockercat): Docker MCP Catalog as a second discovery source`.

### Task 6: "MCP discover" screen (official registry and Docker catalog)

**Files:** Create `internal/ui/discover/mcp.go`, `mcp_test.go`, `e2e_test.go`; Modify `internal/app/screens.go`, `internal/app/screens_test.go` (register `discover.NewMCP(d)` after Health; this is what makes Part 2 shippable on its own).

**Interfaces:**
- Consumes: Tasks 4-5B, `mcpsrc.Source`, `ui.Deps.Discover`, `ui.List`, `ui.Scroll`, `ui.Field`, `ui.Rounds`, `ui.KeyCapturer`, `ui.ScopeSetter`, `ui.Shutdowner`.
- Produces: `func NewMCP(d ui.Deps) *MCPScreen` (implements `ui.Screen`, `ui.KeyCapturer`, `ui.ScopeSetter`, `ui.Shutdowner`); title `"MCP discover"`. It builds its sources from `d.Discover` (`mcpsrc.Official`, `dockercat.NewClient`). Keys: `s` switches source (shown in the header as `source: official|docker`), `/` focus search, enter runs the search (also the only network trigger), `j/k` move, `enter` on a row opens detail, `n` next page, `i` install (emits `ui.ComposeMcpMsg`, Task 7), `esc` back. The detail pane always lists the entry's `Trust` lines; rows marked `unsupported` show their reason and `i` is disabled for them. The same server appearing in both sources is shown per source, never merged.

- [ ] **Step 1: Write failing tests (teatest + `netx` with an `httptest` registry):** `TestNoNetworkUntilEnter` (opening the tab and typing makes zero requests), `TestSearchRendersRowsAndMarksDeprecated`, `TestOfflineShowsMessageAndNoRequest`, `TestBlockedShowsOneErrorNoRetry`, `TestHostileDescriptionRenderedInert` (the output contains no ESC byte), `TestInstallEmitsComposeMsgForChosenPackage`, `TestSourceToggleSwitchesBackendsAndKeepsQuery`, `TestUnsupportedEntryShownWithReasonAndNotInstallable`, `TestSameServerInBothSourcesListedPerSource`, `TestTrustLinesShownInDetail`, `TestCancelledOnShutdown`, `TestResultsTaggedByOwnRounds` (a stale response after a newer search is ignored), and in `internal/app` `TestScreensOrderAndTitles` extended count-relative: one more tab than before, `"MCP discover"` directly after Health, and `"Source repo"` still last if that plan has landed.
- [ ] **Step 2: Run** `go test ./internal/ui/discover/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the screen with `ui.Rounds` tagging and an unexported message type, so the shell broadcast cannot cross-talk (see the `Screen` doc in `internal/ui/app.go`).
- [ ] **Step 4: Run** the package tests and `go test -race -count=3`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): MCP discover screen over the official registry and Docker catalog`.

### Task 7: Prefilled add form and shell hand-off

**Files:** Create `internal/ui/compose.go`; Modify `internal/ui/app.go` (handle the message), `internal/ui/servers/form.go`, `servers.go`; Test `internal/ui/compose_test.go`, `internal/ui/servers/prefill_test.go`.

**Interfaces:**
- Produces: `type ui.ComposeMcpMsg struct{ Spec actions.AddSpec; Prompts []FieldPrompt; Provenance string; Trust []string; RefID string }` (`RefID` = the id the generated secret references were built with) with `type ui.FieldPrompt struct{ Name string; Secret, Required bool; Default, Description string }` (defined in `ui` so `ui` never imports `mcpreg`; Task 6 converts); `func (s *Screen) OpenPrefilled(m ui.ComposeMcpMsg)` on the servers screen. The shell activates the MCP servers tab and forwards the message to it; the form shows the `Provenance` banner `"from <source>: <name>@<version>: the command below runs code on your machine"` (the `Provenance` string from the `Draft`).

- [ ] **Step 1: Write failing tests:** `TestComposeActivatesServersTabAndOpensForm`, `TestPrefilledFormValuesAreMaskedLikeManualEntry` (a `${secret:x}` ref shows as typed; a literal value typed into a secret-named field is masked), `TestPrefilledSubmitBuildsSameActionAsManual` (compare `Action.Argv` and `Display()` with `McpAdd` of the same `AddSpec`), `TestPrefilledStillGoesThroughConfirmModal`, `TestTakenIDIsRefused`, `TestSecretRefsFollowEditedID` (the draft's id `x` is taken, the user changes it to `y`: every generated `${secret:x.<name>}` becomes `${secret:y.<name>}` before submit, a reference the user edited by hand is left as typed, and the after-add note checks the final references), `TestRequiredPromptsMustBeFilledBeforeSubmit`, `TestProvenanceBannerShown`, `TestScopeSwitchClosesPrefilledForm`, `TestAfterAddNoteListsSecretsToSet` (after a successful add the note names every `${secret:...}` key the new server references that `secret list` does not contain, and points to the Secrets tab; it never offers to set a value itself).
- [ ] **Step 2: Run** `go test ./internal/ui/... -run 'Compose|Prefill' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** reusing `form.spec()`/`submit()`; the prefilled form is the existing form with fields set, not a second code path. `ComposeMcpMsg` also carries the draft's id, and when the ID field changes, every value still equal to `SecretRef(oldID, name)` is rewritten to `SecretRef(newID, name)`, so a renamed server never silently reuses another server's vault entry (Decision 7).
- [ ] **Step 4: Run** full gate. Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): open the add form prefilled from a registry entry`.

---

## Part 3: skills (independently shippable)

### Task 8: skills.sh search client

**Files:** Create `internal/discover/skillsh/client.go`, `testdata/search.json` (the real sample), `client_test.go`.

**Interfaces:**
- Produces: `type Skill struct{ ID, Source, SkillID, Name string; Installs int }`; `func NewClient(h *netx.Client, c *cache.Cache) *Client`; `func (c *Client) Search(ctx context.Context, q string, limit int) ([]Skill, error)` → `GET https://skills.sh/api/search?q=<q>&limit=<n>` (limit clamped 1-50); `func ParseSource(s string) (owner, repo string, err error)` accepting `owner/repo` or `https://github.com/owner/repo[.git]` only; `var ErrBlocked error`.

- [ ] **Step 1: Write failing tests:** `TestParsesRealSample`, `TestSanitizesHostileText`, `TestRejectsNonGitHubSource` (`source` of `gitlab.com/...`, `../x`, `a/b/c`, with spaces → skipped, not returned), `TestParseSourceTable` (accepts `vercel-labs/agent-skills`, `https://github.com/vercel-labs/agent-skills.git`; refuses `http://`, other hosts, `owner`, `a b/c`, `-x/y`), `Test401403429AreErrBlockedNoRetry`, `TestCloudflareHTMLIsErrBlocked`, `TestCachedFor10Minutes`, `TestEmptyQueryNoRequest`, `TestLimitClamped`.
- [ ] **Step 2: Run** `go test ./internal/discover/skillsh/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement** like Task 4 (private wire structs, sanitize at the boundary, drop entries that fail `ParseSource`).
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(skillsh): search client for skills.sh`.

### Task 9: Download, safe extract, and skill discovery

**Files:** Create `internal/discover/stage/fetch.go`, `extract.go`, `find.go`, `stage_test.go`, `testdata/` tarball builders (generated in-test with `archive/tar`, not committed binaries).

**Interfaces:**
- Consumes: `netx.Client.Stream`, `ident.Valid`, `mask.Sanitize`.
- Produces:
  - `type Limits struct{ MaxCompressed, MaxExtracted int64; MaxFiles int }` (defaults 20 MiB, 50 MiB, 2000)
  - `type FileInfo struct{ Rel string; Size int64; Exec bool; Binary bool }`
  - `type FoundSkill struct{ Name, Rel string; Files []FileInfo; Manifest string }` (`Manifest` = sha256 over sorted `Rel`+size+exec bit+file-content hashes, so a permission-only change after review also changes it)
  - `type Staged struct{ Root string; ArchiveSHA256 string; Bytes int64; Skills []FoundSkill }` with `func (s *Staged) Cleanup() error`
  - `func Fetch(ctx context.Context, h *netx.Client, owner, repo, tmpDir string, lim Limits) (*Staged, error)` → `https://codeload.github.com/<owner>/<repo>/tar.gz/HEAD`
  - `func SafeExtract(r io.Reader, dst string, lim Limits) error` (exported for testing)
- Discovery: a skill is any directory containing `SKILL.md`, searched at repo root, `skills/*/`, `.claude/skills/*/`, `.agents/skills/*/` (depth ≤3 below those); `Name` = directory name (the repo name at root), which must satisfy `ident.Valid`, else the skill is listed as unsafe and not installable.

- [ ] **Step 1: Write failing tests (crafted tarballs):** `TestExtractsNormalRepo`; **rejections** `TestRejectsPathTraversal` (`../x`, `a/../../x`), `TestRejectsAbsolutePath`, `TestRejectsSymlink`, `TestRejectsHardlink`, `TestRejectsDeviceAndFifo`, `TestRejectsControlCharsInName`, `TestRejectsCaseFoldCollision` (`Skill.md` vs `skill.md`), `TestRejectsOverlongPath`, `TestCompressedCapEnforced`, `TestExtractedCapEnforcedForGzipBomb`, `TestFileCountCapEnforced`, `TestDropsDotGit`; **discovery** `TestFindsRootSkill`, `TestFindsSkillsSubdirSkills`, `TestSkillWithoutSKILLmdIgnored`, `TestUnsafeSkillNameListedNotInstallable`, `TestManifestChangesWhenAFileChanges`, `TestManifestChangesWhenOnlyTheExecBitChanges`, `TestFetchUsesCodeloadURLAndCleansUpOnError`, `TestStagingDirIs0700`. Files are written `0644` (or `0755` only if the archive entry was executable); `Exec` and `Binary` (NUL in first 8 KiB) are recorded for the review screen.
- [ ] **Step 2: Run** `go test ./internal/discover/stage/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement** streaming extraction with per-entry validation before any write; on any error remove the partial staging dir. Use `filepath.IsLocal` plus an explicit case-folded seen-set.
- [ ] **Step 4: Run** with `-race -count=3`. Expected: PASS. Fuzz target `FuzzSafeExtract` runs 60 s: no panic, no path outside `dst`.
- [ ] **Step 5: Commit** `feat(stage): fetch and safely extract skill repositories for review`.

### Task 10: The single permitted direct write

**Files:** Create `internal/install/skill.go`, `skill_test.go`.

**Interfaces:**
- Consumes: `stage.Staged`, `stage.FoundSkill`, `ident.Valid`.
- Produces: `func Skill(home string, st *stage.Staged, sk stage.FoundSkill, name, reviewedManifest string) (Result, error)`; `type Result struct{ Dir string; Files int; Bytes int64 }`; `var ErrExists, ErrChanged, ErrUnsafe error`.
- Behavior: refuses unless `ident.Valid(name)`; target is exactly `<home>/skills/<name>`; **refuses if it exists** (never overwrites); recomputes the manifest from the staged files and refuses with `ErrChanged` if it differs from `reviewedManifest`; refuses any non-regular file or symlink in the staged tree; copies into a temporary sibling directory, recomputes the manifest **from the copied files** and refuses with `ErrChanged` if it differs from `reviewedManifest` (a same-user process could change a staged file between the first check and the copy), then `os.Rename` (atomic); on any failure removes the temporary directory and leaves the home unchanged; preserves only the exec bit; never writes outside `<home>/skills`.

- [ ] **Step 1: Write failing tests:** `TestInstallsFilesAndReturnsCounts`, `TestRefusesExistingDirectory` (and leaves it untouched), `TestRefusesWhenManifestChangedSinceReview`, `TestRefusesWhenFileChangesDuringCopy` (a test hook changes a staged file after the first check; the copy is refused and the temporary directory removed), `TestRefusesSymlinkPlantedAfterReview`, `TestRefusesInvalidName` (`../x`, `a b`, empty, `x/y`), `TestFailureMidCopyLeavesNothing` (inject a failing write), `TestNeverWritesOutsideSkillsDir`, `TestHomeWithoutSkillsDirIsCreated`, `TestCaseInsensitiveCollisionRefused` (existing `Foo`, installing `foo`), `TestExecBitPreserved0755Only`.
- [ ] **Step 2: Run** `go test ./internal/install/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement** with `os.MkdirTemp(<home>/skills, ".install-*")`, copy with `io.Copy` into `O_EXCL` files, `fsync` is not required.
- [ ] **Step 4: Run** with `-race -count=3`. Expected: PASS. Mutation check: delete the manifest comparison; `TestRefusesWhenManifestChangedSinceReview` must fail.
- [ ] **Step 5: Commit** `feat(install): copy a reviewed skill into the agentsync home atomically`.

### Task 11: Shell confirm for skill install

**Files:** Modify `internal/ui/compose.go`, `internal/ui/app.go`, `internal/ui/modal.go` (only if a title/warning variant is needed); Test `internal/ui/install_test.go`.

**Interfaces:**
- Produces: `type ui.InstallSkillMsg struct{ Title, Warning string; Scope runner.Scope; Run func(ctx context.Context) (install.Result, error) }`. The shell opens the existing confirm modal (`y`, not destructive) showing Title and Warning, runs `Run` off the UI goroutine on confirm, refuses if the scope changed since the message was built (same rule as `DoMsg`), logs one activity-log line (source repo, file count, bytes, manifest hash, outcome; no file contents), and on success runs `agentsync check` through the existing `Do` path and posts the outcome note, which also says the skill reaches the agents only after an apply and names the Apply tab (the apply preview gate is unchanged).

- [ ] **Step 1: Write failing tests:** `TestInstallNeedsConfirm`, `TestInstallRefusedAfterScopeSwitch`, `TestDeclinedConfirmRunsNothing` (the `Run` func is never called), `TestSuccessTriggersAgentsyncCheck`, `TestActivityLogHasNoFileContents`, `TestSecondInstallWhileOneInFlightRefused`, `TestRunPanicIsContained`.
- [ ] **Step 2: Run** `go test ./internal/ui/ -run 'Install' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** by reusing the single-action-in-flight and confirm machinery already used for `DoMsg`; do not add a second confirm path.
- [ ] **Step 4: Run** full gate. Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): confirm and log skill installs through the shell`.

### Task 12: "Skill search" screen and review view

**Files:** Create `internal/ui/discover/skills.go`, `review.go`, `skills_test.go`, `review_test.go`.

**Interfaces:**
- Consumes: Tasks 8-11; `ui.List`, `ui.Scroll`, `ui.Field`.
- Produces: `func NewSkills(d ui.Deps) *SkillsScreen` (title `"Skill search"`). Two entry points: search skills.sh (enter) **or** type/paste `owner/repo` (works without skills.sh and is the only mode when skills.sh returns `ErrBlocked`). Selecting a result runs `stage.Fetch` (user-initiated, shows progress and the size caps). The review view lists every skill found, then for the chosen one: file list with size, flags `script` (exec bit, `scripts/`, `*.sh|*.py|*.js`), `binary`, `large` (>256 KiB), the SKILL.md rendered through `mask.Sanitize`, a fixed notice `"Skills steer the agent and may carry executable scripts. Read SKILL.md and every flagged file before installing."`, `enter` on any listed file opens it read-only in a `ui.Scroll` (through `mask.Sanitize`, capped at `ui.MaxOutput`, refused through a symlink, `esc` back to the review; no key writes), a name field (default = the SKILL.md frontmatter `name:` when present and `ident.Valid`, else the directory name; both are shown when they differ; the frontmatter is read with a line scan of the leading `---` block, no YAML library, so Part 3 stays independent of Task 5B; `ident.Valid` enforced live), the skill matching the search result's `SkillID` preselected, and `i` to install (emits `ui.InstallSkillMsg` whose `Run` calls `install.Skill` with the manifest from the review). Staging is cleaned on leaving the view, on error, on a declined or scope-refused install, and on `Shutdown`, except while an install is running: once `InstallSkillMsg` is confirmed, its `Run` owns the staging directory and removes it after `install.Skill` returns, and the screen's cleanup skips it until then. The install target home is the screen's current scope home (from `SetScope`), and the message carries that scope so the shell refuses it after a scope switch.

- [ ] **Step 1: Write failing tests:** `TestNoNetworkUntilEnter`, `TestPasteOwnerRepoSkipsSkillsSh` (zero requests to `skills.sh`), `TestBlockedSkillsShFallsBackToPasteWithMessage`, `TestReviewShowsFlagsForScriptsAndBinaries`, `TestSkillMdRenderedInert` (no ESC/bidi in output), `TestInvalidNameBlocksInstall`, `TestInstallEmitsMsgWithReviewedManifest`, `TestStagingRemovedOnLeaveErrorAndShutdown`, `TestStagingKeptWhileInstallRuns` (leave or Shutdown during `Run`: the install still completes, and staging is gone afterwards), `TestFlaggedFileViewerReadOnly` (a flagged script's text is shown; the staged file is unchanged after any key), `TestOfflineDisablesBothModes`, `TestRejectedArchiveShownAsErrorNotPartialList`, `TestFrontmatterNamePreferredAndMismatchShown`, `TestSearchResultSkillPreselected`, `TestProjectScopeInstallsIntoProjectHome`.
- [ ] **Step 2: Run** `go test ./internal/ui/discover/... -run 'Skill|Review' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the two views; reuse `ui.Rounds` for stale-response protection.
- [ ] **Step 4: Run** the package with `-race -count=3`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): Skill search screen with a review step before install`.

### Task 13: Registration, end-to-end, docs

**Files:** Modify `internal/app/screens.go`, `internal/app/screens_test.go`, `README.md`, `test/e2e/`; Test `internal/ui/discover/e2e_test.go` (extend).

- [ ] **Step 1: Write failing tests:** `TestScreensOrderAndTitles`, count-relative (never a hard-coded total): one more tab than after Task 6, with `"Skill search"` directly after `"MCP discover"`, both after Health (so digits 1-9 are unchanged), and `"Source repo"` still last if the Source repo plan has landed; `TestEveryScreenImplementsScopeSetter` still passes; e2e (`test/e2e`, hermetic `testenv`, `httptest` registry + codeload fake): `TestRegistryToAddFormToRealAgentsyncMcpAdd` (search, install, confirm; assert `mcp/<id>.toml` content via the real CLI `mcp list` and `check`), `TestDockerCatalogEntryToRealAgentsyncMcpAdd` (fake catalog; assert the written TOML has `command='docker'`, the digest-pinned image, the `-e NAME` pass-through and a `${secret:...}` env ref, and that `agentsync check` fails only on the unresolved secret until `secret set`), `TestSkillSearchReviewInstallThenAgentsyncCheckPasses` (fake archive containing a valid skill; after install the real `agentsync skill list` shows it and `agentsync check` exits 0), `TestOfflineStartsWithNoNetwork`.
- [ ] **Step 2: Run** `go test ./internal/app/... ./test/e2e/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement** registration (`discover.NewSkills(d)` after `"MCP discover"`, which Task 6 registered; before `"Source repo"` if present) and finish the README: usage, keys, threat model (unreviewed third-party content; what the TUI does and does not check), data sources and their terms, `--offline`, how to remove an installed skill (delete `skills/<name>` then `agentsync apply`).
- [ ] **Step 4: Run** the full gate three times, plus `go test -tags live` locally with `AGENTSYNC_TUI_LIVE=1` against the real registry and skills.sh search (one request each). Expected: PASS; record the live result in the PR.
- [ ] **Step 5: Commit** `feat: register MCP discover and Skill search tabs; end-to-end coverage and docs`.

---

## Self-review

- **Spec coverage:** skills.sh (Tasks 8-12) and MCP discovery from two sources (Tasks 4-7 incl. 5A, 5B), plus the foundation (1-3) and registration/e2e/docs (13). Decisions 1-5 each have an owning task; every rejected source is recorded with its reason rather than silently dropped.
- **Type consistency:** `AddSpec` is the existing `actions.AddSpec{ID,Type,Command,URL,Args,Env,Headers,Agents}`; `ToDraft` takes the id in both sources (`mcpreg.ToDraft(s, c, id)`, `dockercat.ToDraft(key, id, e)`) and both use `mcpreg.SecretRef`; `Prompt`/`FieldPrompt` bridging is defined in Task 7 to avoid an import cycle; `ErrBlocked` exists in both clients; `Manifest` is produced in Task 9 and consumed in Tasks 10 and 12.
- **Review Focus mapping:** 1 → Tasks 4, 5B, 8, 12; 2 → Tasks 5, 5B; 3 → Tasks 5B, 9; 4 → Tasks 1, 4, 8; 5 → Tasks 5, 10.
- **Review pass (2026-11-07) fixed:** secret references shared across unrelated servers (Decision 7); remote header/URL templates losing their `Bearer ` prefix; required package arguments silently dropped; env names not validated; mutable OCI tags not flagged; the frontmatter name ignored; no note telling the user which secrets to set or that a skill needs an apply; a duplicate allowlist edit in Task 5B; a stale tab name in Task 13; and the resolved comma-escaping question.
- **Proportion:** the plan states signatures, rules, and test names with assertions; bodies are left to the implementer except the pinned mapping rules in Task 5.

## Not verified (carry into execution)

- skills.sh `/api/search` rate limits and stability (undocumented); a 429 or block is handled by the paste path.
- Whether `https://codeload.github.com/<o>/<r>/tar.gz/HEAD` always resolves for repos whose default branch is not `main` (expected yes: `HEAD` follows the default branch). Task 9's live smoke test checks one repo.
- (Resolved during review: `McpAdd`'s `addArgs` already escapes commas in `--args` as `\,`, so fixed package arguments may contain commas; identifiers still may not, because no valid npm, PyPI or OCI name contains one.)
- mcpservers.org entry contents and ToS (blocked by Cloudflare during research); mcp.directory ownership.
- Docker catalog: whether `allowHosts`/`disableNetwork` are enforced outside Docker's MCP gateway (the plan only adds `--network none` for `disableNetwork` and warns for `allowHosts`); whether image signatures verify (not checked by the TUI); update cadence of `catalog.yaml`; that `docker run -e NAME` receives the variable agentsync's rendered config sets for the server (Task 13's e2e checks the generated config, not a live Docker run).
- Whether Docker's catalog URL terms permit programmatic use beyond the MIT repo licence (no separate terms were found); Task 3's README text should say the source and link the repository.
