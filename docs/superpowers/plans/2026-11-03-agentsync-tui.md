# agentsync-tui Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A terminal UI that manages everything `agentsync` manages (MCP servers, drift, apply/revert, reconcile, import, agents, plugins, marketplaces, secrets, library items, health) by driving the `agentsync` CLI.

**Architecture:** A Go Bubble Tea app that treats the `agentsync` CLI as its API. Reads come from `--json` output (`status`, `diff`, `explain`), from text output parsed against captured fixtures (everything else), and from the canonical `~/.agentsync/mcp/*.toml` files (read-only). Every mutation is an `Action` (an exact argv) shown to the user, confirmed, executed through one `Runner`, and logged with secrets masked. Interactive CLI flows (reconcile, secret prompts, `$EDITOR`) hand the terminal over with `tea.ExecProcess`.

**Tech Stack:** Go 1.26 (agentsync's own `go.mod` is `go 1.26.2`), Bubble Tea + Bubbles + Lip Gloss (pin the latest stable at Task 0 via `go list -m -u`), `pelletier/go-toml/v2` (same TOML library agentsync uses), `charmbracelet/x/exp/teatest` for UI tests, GoReleaser + GitHub Actions, mise for tooling.

**Spec:** No separate spec document. The scope is the "Feature scope" section below, derived from the planning conversation and from running `agentsync` v0.17.0 on 2026-11-03 against `spxrogers/agentsync` (45k LOC Go, MIT, pre-1.0).

## Feature scope and verified facts

Verified against agentsync v0.17.0:
- `--json` exists only on `status`, `diff`, `explain`. `mcp list`, `agent list`, `plugin list`, `marketplace list`, `skill|hook|command|subagent|lsp list`, `secret list`, `doctor`, `check` print plain text. `mcp list` prints bare IDs, one per line.
- `status --json` printed an `ℹ INFO ...` line before the JSON in a `2>&1` capture. Whether it goes to stdout or stderr is **unverified** (Task 2 settles it).
- `status --exit-code` and `diff --exit-code` exit 2 on drift. Exit 2 is a result, not a failure.
- `diff --json` returns `{"hunks":[{"path","pointer","source","dest"}]}`. `source` and `dest` carry file content that may include resolved secrets, so they must be masked.
- Mutating CLI verbs: `mcp add|remove|enable|disable`, `apply [--dry-run]`, `revert [<agent>] [--all] [--to] [--dry-run]`, `reconcile`, `import <agent>[:component[:name]] [--dry-run]`, `agent add|remove|enable|disable`, `plugin add|remove|enable|disable|upgrade`, `marketplace add|remove`, `secret set|edit|remove`.
- `skill|hook|command|subagent|lsp` have **no `add`** by design: items are files created by hand or captured with `import`.
- Global flags: `--no-input`, `--color`, `--scope user|project`, `--project <path>`.
- Open upstream bugs the TUI must not trip over: #239 (`reconcile` override writes `${env:…}`/`${secret:…}` literally into live files), #255 (bulk `[W]` writes back ForeignCollision items), #256 (reconcile diff labels reversed), #254 (diff/reconcile cannot mask a rotated secret), #218 (control bytes can reach the terminal).
- Motivating live example: 21 drift hunks on an AGY config, including `disabled` flags that exist only on the AGY side.

Not yet known (each is resolved by a named discovery step, with a fixture captured from a **hermetic** agentsync home): `status --json` item field names; `agent list --all` and the other text formats; where `mcp disable` records its state; whether `mcp add` on an existing ID errors or overwrites; `agentsync version` output format.

| Feature | Task |
|---|---|
| Hermetic dev/test environment | 0 |
| CLI runner, version guard, exit-code semantics | 1 |
| Status/diff/explain models | 2 |
| Canonical MCP source reader | 3 |
| Secret masking + terminal sanitising | 4 |
| Action pipeline (argv builders, validation) | 5 |
| App shell: tabs, modal, activity log, exec handover, scope switch | 6 |
| MCP servers: list, detail, enable/disable, add, edit, remove | 7 |
| Drift: status, diff, explain, legend | 8 |
| Apply (dry-run first) and revert | 9 |
| Reconcile hand-off and import | 10 |
| Agents: list, add, enable, disable, remove | 11 |
| Plugins and marketplaces | 12 |
| Secrets (keys only, never values) | 13 |
| Library: skills, subagents, commands, hooks, LSP, memory | 14 |
| Health: doctor, check | 15 |
| Packaging, CI, docs, upstream proposal | 16 |

## Global Constraints

- Module path `github.com/lusky3/agentsync-tui`, binary `agentsync-tui`, license MIT.
- The TUI **never writes** `~/.agentsync/**`, destination agent files (`~/.claude.json`, `~/.gemini/**`, `~/.kiro/**`), or the secrets vault itself. All mutations go through the `agentsync` CLI. The only direct file access is read-only parsing of `~/.agentsync/mcp/*.toml`, plus opening a canonical file in the user's `$EDITOR` through `tea.ExecProcess`.
- Every CLI call includes `--no-input --color never`. Interactive handovers omit `--no-input`.
- Secret values are never displayed, logged, or cached. There is no `secret get` action, and secret values are entered only inside agentsync's own prompt via `tea.ExecProcess`.
- Server and agent IDs must match `^[A-Za-z0-9][A-Za-z0-9._-]*$` before they go into argv or file paths.
- Supported agentsync: tested with `v0.17.0`. A different minor version shows a warning banner, not a hard failure. A missing binary shows an install hint.
- Tests are hermetic: `HOME`, `AGENTSYNC_HOME`, and `XDG_*` point at `t.TempDir()`. No test reads the real home directory (agentsync's own #270 shows what an ambient `AGENTSYNC_HOME` costs).
- Never run `agentsync apply`, `revert`, `import`, or `reconcile` against the real `~/.agentsync` during development, only in a hermetic home.
- Git: work on a branch, open a PR, no direct push to `main`, no force-push. Commit messages and PR bodies carry **no** `Co-Authored-By`, `Claude-Session`, or "Generated with" lines.

## Review Focus

Failure modes the feature scope implies that no single task's happy path covers, most likely first. Each is pinned by a named test in the owning task.

1. **Secret leakage via unexpected places.** A token inline in `args` (as in a legacy MCP server entry, `--header "Authorization: Bearer gg_pat_…"`), resolved values inside `diff` hunks, and the activity log. Expected: masked everywhere; `${secret:X}`/`${env:X}` refs stay visible. → Tasks 4, 7, 8.
2. **Hostile or odd text.** IDs containing `../` or newlines; ANSI/control bytes inside diff content or server names (agentsync #218). Expected: rejected before argv/path use, or stripped before rendering. → Tasks 4, 5, 14.
3. **Exit codes and stderr noise.** Exit 2 from `status --exit-code`/`diff --exit-code`; `INFO` lines before JSON; agentsync missing, wrong version, or an unreadable `agentsync.toml`. Expected: results shown, banner with guidance, no crash. → Tasks 1, 2, 6.
4. **State changing underneath the UI.** `apply` or a hand-edit between load and action; `mcp add` on an existing ID; two TUI instances. Expected: refresh before every mutation, and no silent overwrite. → Tasks 5, 7.
5. **Terminal handover and long operations.** Terminal state restored after `reconcile`/`$EDITOR`; Ctrl-C during `apply`; `plugin upgrade` (network) timing out. Expected: UI intact, command cancelled, error shown. → Tasks 6, 9, 12.

Plus empty states (no servers, no registered agents, `--scope project` outside a project), covered in Tasks 6–8 and 11.

---

## Task 0: Repository scaffold and hermetic test environment

**Files:**
- Create: `go.mod`, `main.go`, `mise.toml`, `.gitignore`, `internal/testenv/testenv.go`, `internal/testenv/testenv_test.go`
- Copy this plan to: `docs/superpowers/plans/2026-11-03-agentsync-tui.md`

**Interfaces:**
- Produces: `testenv.New(t *testing.T) *Env` with `Env{Home, AgentsyncHome string; Env []string}` and `(*Env) Agentsync(t, args ...string) (stdout, stderr string, exit int)` which runs the real `agentsync` with the hermetic environment, skipping the test if the binary is absent. `(*Env) Init(t)` runs `agentsync init` inside the hermetic home.

- [ ] **Step 1: Create the repo** with `gh repo create lusky3/agentsync-tui --public --clone` (the repo is public, so keep personal paths, hosts and tokens out of it). Create branch `feat/scaffold`; never commit to `main`.
- [ ] **Step 2: Write the failing test** `TestEnvNeverTouchesRealHome`: snapshot the names under the real `os.UserHomeDir()/.agentsync` (or note its absence), run `env.Agentsync(t, "init")`, then assert that snapshot is unchanged and that `env.AgentsyncHome` is under `t.TempDir()`.
- [ ] **Step 3: Run** `go test ./internal/testenv -run TestEnvNeverTouchesRealHome -v`. Expected: FAIL (package missing).
- [ ] **Step 4: Implement `testenv.New`** by setting `HOME`, `AGENTSYNC_HOME`, `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME` to subdirectories of `t.TempDir()` and building `Env.Env` from `os.Environ()` with those overridden. Also write `go.mod` (`go 1.26`), a `main.go` that prints a version, and `mise.toml` with `go = "1.26"` and tasks `test`, `lint`, `build`.
- [ ] **Step 5: Run the test again.** Expected: PASS (or SKIP when `agentsync` is missing; the real-home assertion still runs).
- [ ] **Step 6: Commit** `feat: scaffold module and hermetic test env`.

## Task 1: CLI runner, exit-code semantics, version guard

**Files:**
- Create: `internal/runner/runner.go`, `internal/runner/version.go`, `internal/runner/runner_test.go`, `internal/runner/testdata/fake-agentsync.sh`

**Interfaces:**
- Produces:
  - `type Scope string` with `ScopeUser`, `ScopeProject`.
  - `type Result struct{ Stdout, Stderr string; ExitCode int }`.
  - `type Runner interface{ Run(ctx context.Context, args ...string) (Result, error) }`; the error is non-nil only for spawn failure or context cancellation, never for a non-zero exit.
  - `func NewExec(bin string, scope Scope, projectDir string) *Exec`.
  - `func (e *Exec) Interactive(args ...string) *exec.Cmd` (no `--no-input`, for `tea.ExecProcess`).
  - `func CheckVersion(ctx context.Context, r Runner) (v string, supported bool, err error)` with `const TestedVersion = "v0.17.0"`.

- [ ] **Step 1: Write failing tests** with a fake binary script that echoes its argv to stdout and a marker to stderr:
  - `TestRunAppendsGlobalFlags`: argv ends with `--no-input --color never` plus `--scope user`.
  - `TestRunProjectScope`: adds `--project <dir>` when scope is project.
  - `TestRunSeparatesStreams`: stdout and stderr land in the right fields.
  - `TestNonZeroExitIsResultNotError`: script exits 2 → `Result.ExitCode == 2`, `err == nil`.
  - `TestContextCancelKillsProcess`: script sleeps 30s, context cancelled at 100ms, returns within 2s with `ctx.Err()`.
  - `TestMissingBinary`: `NewExec("does-not-exist", …).Run` returns `ErrNotInstalled`.
  - `TestCheckVersionWarnsOnOtherMinor`: fixture `v0.18.2` → `supported == false`, no error.
- [ ] **Step 2: Run** `go test ./internal/runner -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `Exec` with `exec.CommandContext`, separate buffers, and `ExitError` → `Result`. Record the real `agentsync version` output into `testdata/version.txt` (from a hermetic run) and parse against that format.
- [ ] **Step 4: Run tests.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(runner): CLI runner with exit-code and version semantics`.

## Task 2: Status, diff, explain models

**Files:**
- Create: `internal/model/status.go`, `internal/model/diff.go`, `internal/model/status_test.go`, `internal/model/testdata/{status-clean,status-drift,diff-drift,explain-drift}.json`

**Interfaces:**
- Consumes: `runner.Result` (Task 1).
- Produces:
  - `type StatusReport struct{ Agents []AgentStatus }`, `type AgentStatus struct{ Agent string; Items []StatusItem }`.
  - `type StatusItem struct{ Path, Pointer, State string }`. Final field names are taken from the captured fixture in Step 1. `State` holds the raw classification string from `agentsync status --legend`.
  - `type Hunk struct{ Path, Pointer, Source, Dest string }`.
  - `func ParseStatus(stdout string) (StatusReport, error)`, `func ParseDiff(stdout string) ([]Hunk, error)`, `func ParseExplain(stdout string) (Provenance, error)`.
  - `func (r StatusReport) Drifted() []StatusItem`.

- [ ] **Step 1: Capture fixtures from a hermetic home.** Using `testenv`: `Init`, add one stdio and one http server with `mcp add`, enable the `antigravity` agent, `apply`, then hand-edit the destination JSON to add `"disabled": true` on one server. Save `status --json`, `diff --json`, and `explain <path>#/mcpServers/<id> --json` outputs. Record which stream carries the `INFO` line.
- [ ] **Step 2: Write failing tests:**
  - `TestParseStatusDrift`: fixture parses; `Drifted()` returns exactly the edited server.
  - `TestParseStatusToleratesLeadingInfoLines`: input with `ℹ INFO …\n` before the `{` parses.
  - `TestParseStatusIgnoresUnknownFields`: extra keys do not error.
  - `TestParseDiffHunks`: hunks carry path, pointer, source and dest.
  - `TestParseErrors`: empty input and truncated JSON return errors that include the first 80 bytes, with secrets not echoed.
- [ ] **Step 3: Run.** Expected: FAIL.
- [ ] **Step 4: Implement** the parsers: strip any bytes before the first `{`, then `encoding/json` with lenient decoding.
- [ ] **Step 5: Run.** Expected: PASS.
- [ ] **Step 6: Commit** `feat(model): status/diff/explain parsers with fixtures`.

## Task 3: Canonical MCP source reader

**Files:**
- Create: `internal/source/servers.go`, `internal/source/servers_test.go`, `internal/source/testdata/*.toml`

**Interfaces:**
- Produces:
  - `type Server struct{ ID, Type, Command, URL string; Args []string; Env, Headers map[string]string; Disabled bool; Extra map[string]any }`.
  - `func LoadServers(agentsyncHome string) ([]Server, error)` (sorted by ID, read-only).
  - `func ServerPath(agentsyncHome, id string) (string, error)` (validates the ID).

- [ ] **Step 1: Discover disable storage.** In a hermetic home, run `mcp add x --command echo` and `mcp disable x`; diff the TOML before and after; record the exact key in a fixture and in the type's doc comment. Also probe `mcp add x` a second time and record whether it errors or overwrites (used by Task 7).
- [ ] **Step 2: Write failing tests:**
  - `TestLoadStdioServer` (shape of `context7.toml`: `[server] type='stdio'`, `command`, `args`, `[server.env]`).
  - `TestLoadHTTPServer` (shape of `google-developer-knowledge.toml`: `url`, `[server.headers]`).
  - `TestLoadDisabledServer` (the key found in Step 1).
  - `TestUnknownKeysLandInExtra`.
  - `TestServerPathRejectsTraversal`: `../x`, `a/b`, `""`, `a\nb` all error.
  - `TestLoadEmptyDirReturnsEmptySlice`.
- [ ] **Step 3: Run.** Expected: FAIL.
- [ ] **Step 4: Implement** using `go-toml/v2` unmarshal into a struct plus a second decode into `map[string]any` for `Extra`.
- [ ] **Step 5: Run.** Expected: PASS.
- [ ] **Step 6: Commit** `feat(source): read-only canonical MCP reader`.

## Task 4: Masking and terminal sanitising

**Files:**
- Create: `internal/mask/mask.go`, `internal/mask/sanitize.go`, `internal/mask/mask_test.go`

**Interfaces:**
- Produces:
  - `func IsRef(s string) bool` (true for `${secret:NAME}` and `${env:NAME}` and `${NAME}` forms).
  - `func Value(key, val string) string` (refs pass through; anything else becomes the fixed string `••••`, with no characters of the original revealed).
  - `func Args(args []string) []string`.
  - `func Text(s string) string` (scrubs known token shapes in free text: `Bearer\s+\S+`, `gg_pat_\S+`, `ghp_\S+`, `github_pat_\S+`, `sk-\S+`, `xox[bap]-\S+`, `AKIA[0-9A-Z]{16}`, and any 24+ char `[A-Za-z0-9_\-]` run following `token|key|secret|password=`).
  - `func Hunk(h model.Hunk) model.Hunk` (applies `Text` to `Source` and `Dest`).
  - `func Sanitize(s string) string` (strips C0/C1 control bytes and ANSI escapes except `\n` and `\t`).

- [ ] **Step 1: Write failing tests.** Use a canary constant `CANARY = "gg_pat_CANARYCANARYCANARYCANARY0123456789"`:
  - `TestArgsMasksInlineBearerToken`: `["-y","mcp-remote","https://x","--header","Authorization: Bearer "+CANARY]` → no output element contains `CANARY`.
  - `TestValueKeepsRefs`: `${secret:GG}` unchanged; `abcdef123456` masked.
  - `TestHunkMasksResolvedSecrets`: source/dest JSON containing `CANARY` in a header value → masked.
  - `TestSanitizeStripsAnsiAndControl`: `"a\x1b[31mb\x07c\r\nd"` → `"abc\nd"`.
  - `TestTextLeavesPlainURLsAlone`.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement** with precompiled regexes. Err toward over-masking; never log the unmasked input.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(mask): secret masking and terminal sanitising`.

## Task 5: Action pipeline

**Files:**
- Create: `internal/actions/actions.go`, `internal/actions/builders.go`, `internal/actions/actions_test.go`

**Interfaces:**
- Consumes: `mask.Args` (Task 4), `runner.Runner` (Task 1).
- Produces:
  - `type Action struct{ Title string; Argv []string; Destructive, Interactive, Network bool }`; `func (a Action) Display() string` (masked, shell-quoted).
  - `func ValidID(s string) bool`.
  - Builders, all returning `(Action, error)` and validating IDs: `McpEnable(id)`, `McpDisable(id)`, `McpRemove(id)` (Destructive), `McpAdd(AddSpec)`, `Apply(dry bool, agents ...string)`, `Revert(agent string, to string, all, dry bool)` (Destructive unless dry), `Import(agent, component, name string, dry bool)`, `AgentAdd|AgentEnable|AgentDisable|AgentRemove(name)`, `PluginAdd|PluginEnable|PluginDisable|PluginRemove|PluginUpgrade(name)` (Upgrade is Network), `MarketplaceAdd(src)|MarketplaceRemove(name)`, `SecretSet(key)|SecretEdit()|SecretRemove(key)` (Set/Edit are Interactive; Remove is Destructive), `Reconcile()` (Interactive), `Check()`, `Doctor()`.
  - `type AddSpec struct{ ID, Type, Command, URL string; Args []string; Env map[string]string; Headers []string; Agents []string }`.
  - **Deliberately absent:** any `secret get` builder.

- [ ] **Step 1: Write failing tests:**
  - `TestMcpAddEscapesCommasInArgs`: args `["--list=a,b"]` → `--args` value `--list=a\,b`.
  - `TestMcpAddEnvAndHeaders`: env sorted `A=1,B=2`; each header becomes its own `--header "Name: Value"`.
  - `TestMcpAddHTTPRequiresURL` and `TestMcpAddStdioRequiresCommand`.
  - `TestBuildersRejectBadIDs`: `""`, `../x`, `a b`, `x\n`, `-rf` (leading dash) all error.
  - `TestDestructiveFlags`: `McpRemove`, `Revert(…, dry=false)`, `SecretRemove` are Destructive; `Revert(…, dry=true)` is not.
  - `TestDisplayMasksSecretsInArgv`: a header value `Bearer CANARY…` is masked in `Display()`.
  - `TestNoSecretGetBuilder`: reflect over the package's exported funcs and assert none is named `*SecretGet*`.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement** the builders (pure functions, no I/O). Reject IDs beginning with `-`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(actions): validated argv builders`.

## Task 6: App shell

**Files:**
- Create: `internal/ui/app.go`, `internal/ui/keys.go`, `internal/ui/modal.go`, `internal/ui/activity.go`, `internal/ui/exec.go`, `internal/ui/banner.go`, `internal/ui/app_test.go`; Modify: `main.go`

**Interfaces:**
- Consumes: `runner.Runner`, `actions.Action`, `mask`.
- Produces:
  - `type Screen interface{ tea.Model; Title() string; Refresh() tea.Cmd }`.
  - `type Deps struct{ Runner runner.Runner; AgentsyncHome string; Scope runner.Scope }`.
  - `func NewApp(d Deps, screens ...Screen) *App`.
  - `type RunMsg struct{ Action actions.Action; Result runner.Result; Err error }`.
  - `func (a *App) Do(act actions.Action) tea.Cmd`: shows the confirm modal (title, masked argv, Destructive highlighted), runs through `Runner` or `tea.ExecProcess` for Interactive, appends to the activity log, then refreshes the active screen.
  - Keys: `tab`/`shift+tab` or `1-9` switch screens, `s` toggles scope (user/project), `l` toggles the activity log, `?` help, `q` quit.

- [ ] **Step 1: Write failing teatest tests:**
  - `TestTabSwitching`, `TestQuitKey`, `TestResizeReflows`.
  - `TestConfirmModalCancelDoesNotRun`: fake runner records zero calls after `esc`.
  - `TestConfirmModalRunsOnYes` and `TestDestructiveNeedsTypedConfirm` (destructive actions require typing the verb, e.g. `remove`).
  - `TestActivityLogMasksArgv`: an action with `CANARY` in argv never appears in `View()` or the log file.
  - `TestMissingAgentsyncShowsBanner` and `TestUnsupportedVersionShowsWarningNotError`.
  - `TestScopeProjectOutsideProjectShowsEmptyState`.
  - `TestInteractiveHandoffRestoresTerminal`: after `ExecProcess` returns, the model receives a refresh and the alt screen is re-entered (use a fake exec command `true`).
  - `TestCtrlCCancelsRunningAction`.
- [ ] **Step 2: Run** `go test ./internal/ui -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the shell. The log is an in-memory ring buffer plus a masked append-only file at `$XDG_STATE_HOME/agentsync-tui/activity.log` (0600). All rendered strings pass through `mask.Sanitize`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): app shell, modal, activity log, exec handover`.

## Task 7: MCP servers screen

**Files:**
- Create: `internal/ui/servers/servers.go`, `internal/ui/servers/form.go`, `internal/ui/servers/servers_test.go`

**Interfaces:**
- Consumes: `source.LoadServers` (3), `model.StatusReport` (2), `actions.Mcp*` (5), `App.Do` (6).
- Produces: `func New(d ui.Deps) ui.Screen`. Columns: ID, type, target (command or URL), enabled, per-agent render state (clean/drift/missing, from `status --json`). Keys: `enter` detail (masked), `e` toggle enable/disable, `a` add form, `E` edit in `$EDITOR`, `x` remove, `r` refresh.

- [ ] **Step 1: Write failing tests:**
  - `TestListShowsServersAndPerAgentState` (fixtures from Tasks 2–3).
  - `TestDetailMasksEnvHeadersAndInlineArgs`: server fixture with `CANARY` in `args` and `env` → `View()` has no `CANARY`, refs visible.
  - `TestToggleEnableBuildsCorrectAction` (disabled → `mcp enable <id>`, enabled → `mcp disable <id>`).
  - `TestAddFormBuildsAddSpec`: stdio and http flows produce the expected `AddSpec`; invalid ID blocks submit.
  - `TestAddExistingIDRequiresExplicitOverwrite`: behaviour follows the Task 3 Step 1 finding (if `mcp add` overwrites, the form warns and requires confirmation; if it errors, the form pre-checks).
  - `TestEditOpensEditorThenRunsCheck`: `Interactive` action on `ServerPath`, followed by `check`.
  - `TestStaleListRefreshesBeforeMutation`: file removed after load → refresh runs before the action and the UI reports the server gone.
  - `TestEmptyState`.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement** with `bubbles/table` and `bubbles/textinput`; refresh = `LoadServers` + `status --json` in parallel via `tea.Batch`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): MCP servers screen`.

## Task 8: Drift screen

**Files:**
- Create: `internal/ui/drift/drift.go`, `internal/ui/drift/drift_test.go`

**Interfaces:**
- Consumes: `model.ParseStatus|ParseDiff|ParseExplain` (2), `mask.Hunk`, `mask.Sanitize` (4).
- Produces: `func New(d ui.Deps) ui.Screen`. Grouped by agent; filter toggle (all/drift only); `enter` loads `diff <path> --json` and `explain <path>#<pointer> --json` for the item; `g` shows the `status --legend` glossary.

- [ ] **Step 1: Write failing tests:**
  - `TestGroupsByAgentAndFiltersDrift`.
  - `TestDetailShowsMaskedHunk`: fixture hunk containing `CANARY` → masked in view.
  - `TestDetailSanitisesControlBytes`: hunk dest with `\x1b[2J` → no escape in `View()`.
  - `TestExitCode2IsNotAnError`: fake runner returns exit 2 with JSON → rendered, no error banner.
  - `TestLegendRendered`.
  - `TestCleanState` ("no drift").
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.** The screen only reads; resolving drift is Task 10.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): drift screen with masked diff and explain`.

## Task 9: Apply and revert

**Files:**
- Create: `internal/ui/apply/apply.go`, `internal/ui/apply/apply_test.go`

**Interfaces:**
- Consumes: `actions.Apply|Revert` (5), `App.Do` (6).
- Produces: `func New(d ui.Deps) ui.Screen`. Flow: choose agents (default all enabled) → run `apply --dry-run` and show the plan → `apply` behind the confirm modal. Revert: choose agent or all, optional `--to` checkpoint text, `revert --dry-run` preview → destructive confirm → run; after revert, show agentsync's notice that the next `apply` will re-render from the canonical source.

- [ ] **Step 1: Write failing tests:**
  - `TestApplyAlwaysDryRunsFirst`: the first runner call is `apply --dry-run`; real `apply` only runs after the user confirms the preview.
  - `TestApplyAgentSubset`: argv includes `--agents antigravity,claude`.
  - `TestRevertPreviewThenDestructiveConfirm`.
  - `TestRevertToCheckpointValidatesRef`: `HEAD~2` and 7–40 hex chars accepted; `; rm -rf` and `--all` rejected.
  - `TestRevertNoticeShown`.
  - `TestCancelDuringApplyKillsProcessAndRefreshes` (Review Focus 5).
  - `TestApplyFailureShowsMaskedStderr`.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.** The preview pane shows the CLI's own text after `mask.Text` and `mask.Sanitize`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): apply and revert`.

## Task 10: Reconcile hand-off and import

**Files:**
- Create: `internal/ui/reconcile/reconcile.go`, `internal/ui/importer/importer.go`, tests beside each

**Interfaces:**
- Consumes: `actions.Reconcile|Import` (5), `App.Do` (6).
- Produces: two `ui.Screen` constructors.
  - Reconcile: shows the `reconcile --auto-safe` report (items needing a human), then "resolve interactively" → `tea.ExecProcess(agentsync reconcile)`. **No in-TUI writeback/override or bulk hotkeys**, because of upstream #239/#255/#256. A banner states why.
  - Import: pick agent (from `agent list`) and component (`mcp|skill|subagent|command|hook|lsp|memory|plugin|all`) and optional name → `import … --dry-run` preview → confirm → run.

- [ ] **Step 1: Write failing tests:**
  - `TestReconcileShowsAutoSafeReport`.
  - `TestReconcileHandsOffInteractively`: action is `Interactive` and has no `--auto-writeback`/`--auto-override`.
  - `TestImportSelectorGrammar`: `("claude","mcp","")`→`claude:mcp`, `("claude","","")`→`claude`, `("claude","mcp","github")`→`claude:mcp:github`; bad component/name rejected.
  - `TestImportPreviewBeforeRun`.
  - `TestNoBulkResolveKeys`: key map contains no `W`/`O`/`S` bindings.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): reconcile hand-off and import`.

## Task 11: Agents screen

**Files:**
- Create: `internal/model/agents.go` (+ test, fixture `testdata/agent-list{,-all}.txt`), `internal/ui/agents/agents.go` (+ test)

**Interfaces:**
- Produces: `type Agent struct{ Name string; Enabled bool; Scope string; Registered bool }`, `func ParseAgentList(stdout string) ([]Agent, error)`; `func New(d ui.Deps) ui.Screen` (list, add from `agent list --all`, enable, disable, remove).

- [ ] **Step 1: Capture fixtures** from a hermetic home: `agent list` (known format `antigravity enabled=true scope=user`) and `agent list --all`.
- [ ] **Step 2: Write failing tests:** `TestParseAgentList`, `TestParseAgentListAllMarksUnregistered`, `TestRemoveAndPurgeWarnsAboutDestinationFiles` (destructive confirm text mentions purge), `TestEmptyRegisteredAgents`.
- [ ] **Step 3: Run.** Expected: FAIL.
- [ ] **Step 4: Implement** the parser and screen.
- [ ] **Step 5: Run.** Expected: PASS.
- [ ] **Step 6: Commit** `feat(ui): agents screen`.

## Task 12: Plugins and marketplaces

**Files:**
- Create: `internal/model/plugins.go` (+ test, fixtures from `plugin list`, `plugin outdated`, `marketplace list`), `internal/ui/plugins/plugins.go` (+ test)

**Interfaces:**
- Produces: `ParsePluginList`, `ParseOutdated`, `ParseMarketplaces`; `func New(d ui.Deps) ui.Screen` with two panes (plugins, marketplaces). Actions: enable/disable/remove, add (Network), upgrade one or all pending (Network), marketplace add/remove, `plugin explain` detail.

- [ ] **Step 1: Capture fixtures** from a hermetic home with a local-path marketplace so tests need no network.
- [ ] **Step 2: Write failing tests:** parser tests per fixture; `TestUpgradeIsNetworkAndNeedsConfirm`; `TestNetworkActionHasTimeout` (30s default; on timeout the process is killed and an error banner shows); `TestOutdatedRunsOnDemandNotOnStartup` (no network on launch).
- [ ] **Step 3: Run.** Expected: FAIL.
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run.** Expected: PASS.
- [ ] **Step 6: Commit** `feat(ui): plugins and marketplaces`.

## Task 13: Secrets screen

**Files:**
- Create: `internal/ui/secrets/secrets.go`, `internal/ui/secrets/secrets_test.go`

**Interfaces:**
- Consumes: `actions.SecretSet|SecretEdit|SecretRemove` (5), `source.LoadServers` (3).
- Produces: `func New(d ui.Deps) ui.Screen`: lists vault **keys** (`secret list`); `n` set (handover to agentsync's secure prompt), `E` edit (handover to `$EDITOR`), `x` remove (destructive). Cross-check: any `${secret:X}` ref in the canonical servers with no matching key shows a warning row.

- [ ] **Step 1: Write failing tests:**
  - `TestListsKeysOnly`: fake `secret list` output → keys shown.
  - `TestSetIsInteractiveAndTUINeverSeesValue`: the action is `Interactive`; no text input in this screen captures a value.
  - `TestRemoveRequiresTypedConfirm`.
  - `TestMissingKeyWarning`: a server with `${secret:MISSING}` and no such key → warning row.
  - `TestNoRevealKey`: key map has no reveal/get binding.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): secrets screen (keys only)`.

## Task 14: Library browsers (skills, subagents, commands, hooks, LSP, memory)

**Files:**
- Create: `internal/ui/library/library.go`, `internal/ui/library/library_test.go`

**Interfaces:**
- Consumes: `source.ServerPath`-style ID validation, `App.Do`.
- Produces: `func New(d ui.Deps, kind Kind) ui.Screen` for `Kind` in `skill|subagent|command|hook|lsp|memory`. Lists via `<kind> list`; `E` opens the canonical file or directory entry in `$EDITOR` (`skills/<name>/SKILL.md`, `subagents/<name>.md`, `commands/<name>.md`, `hooks/<event>.toml`, `lsp/<id>.toml`, `memory/AGENTS.md` and `memory/fragments/*.md`); after the editor exits it runs `check` then offers `apply --dry-run`. No create/delete (agentsync has no `add`; creation is via `import` in Task 10).

- [ ] **Step 1: Write failing tests:** `TestPathForKind` (table of kinds → relative path), `TestPathRejectsTraversalAndSymlinkEscape` (a symlink inside the source pointing outside `AGENTSYNC_HOME` is refused), `TestEditorHandoffThenCheck`, `TestRespectsEditorEnvAndFallsBackToVi`.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement** with `filepath.EvalSymlinks` + prefix check.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): library browsers with editor hand-off`.

## Task 15: Health screen

**Files:**
- Create: `internal/ui/health/health.go`, `internal/ui/health/health_test.go`; Modify: `internal/ui/app.go` (header chip)

**Interfaces:**
- Produces: `func New(d ui.Deps) ui.Screen` showing `doctor` and `check` output panes (sanitised, masked). The app header shows a ✓/✗ chip driven by `check` and `status --exit-code` run once at startup in the background.

- [ ] **Step 1: Write failing tests:** `TestChipReflectsCheckFailure`, `TestChipReflectsDrift`, `TestHealthPanesMasked`, `TestStartupChecksDoNotBlockFirstRender` (first `View()` returns before the fake runner's delayed responses).
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement** with `tea.Batch` async commands.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): health screen and header chip`.

## Task 16: Packaging, CI, docs, upstream proposal

**Files:**
- Create: `.goreleaser.yaml`, `.github/workflows/{ci,release}.yaml`, `README.md`, `test/e2e/smoke_test.go`

**Interfaces:**
- Produces: release archives named `agentsync-tui_<version>_<os>_<arch>.tar.gz` (linux and darwin, amd64 and arm64), so `mise use -g github:lusky3/agentsync-tui` works.

- [ ] **Step 1: Write `TestE2ESmoke`** (skips without `agentsync`): hermetic home, `Init`, `mcp add`, launch the app under `teatest`, assert the server appears on the Servers screen, toggle it, assert the TOML changed, quit.
- [ ] **Step 2: Run** `go test ./test/e2e -v`. Expected: FAIL, then PASS after wiring `main.go` to `NewApp` with all screens.
- [ ] **Step 3: Add CI** (`go vet`, `go test ./...`, `golangci-lint`, `goreleaser check`) and the tag-triggered release workflow. Pin action versions with a version-lookup tool at implementation time.
- [ ] **Step 4: Write the README:** install (`mise use -g github:lusky3/agentsync-tui`), supported agentsync version, screen tour, the "no secret values, no direct file writes" guarantees.
- [ ] **Step 5: Verify install** with `mise use -g github:lusky3/agentsync-tui@<tag>` on a clean shell, then `agentsync-tui --version`.
- [ ] **Step 6: Draft (do not post) an upstream issue** for `spxrogers/agentsync` proposing an in-tree `agentsync tui` or a documented `--json` for the list commands. **Posting is outward-facing; get explicit approval first.**
- [ ] **Step 7: Commit** `chore: packaging, CI, docs`; open a PR; merge only when you ask.

---

## Self-review

- **Spec coverage:** every feature in the table maps to one task; the verified-facts list drives Tasks 1, 2, 7, 10 and 13. Not covered on purpose: in-TUI reconcile writeback/override (blocked by upstream #239/#255/#256), `secret get`, creating skills/hooks/commands (agentsync has no `add`).
- **Step scan:** discovery steps (Tasks 1, 2, 3, 11, 12) exist because the exact text formats and field names were not verified; those tasks capture fixtures before writing parsers.
- **Type consistency:** `Action`, `Runner`, `Result`, `Server`, `Hunk`, `StatusItem` and `Deps` are defined once (Tasks 1–6) and used by the same names later.
- **Review Focus:** each of the five items has a named test (1: Tasks 4, 7, 8; 2: Tasks 4, 5, 14; 3: Tasks 1, 2, 6; 4: Tasks 5, 7; 5: Tasks 6, 9, 12).
- **Proportion:** tests are named with their assertions; function bodies are not written out.
