# Source repo (git for the canonical home) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A "Source repo" tab that shows the git state of the current scope's agentsync home (masked), and lets the user commit selected agentsync files, fetch, fast-forward pull and push, each as an exact `git` command line confirmed in the existing modal. It never runs git underneath chezmoi and never touches agentsync's destination-backup history.

**Architecture:** A generic hardened child runner (`runner.Program`, sharing `Exec`'s process-group, cancel and output-cap code) runs `git` and, read-only, `chezmoi`. `Action` gains a `Program` field so git argv flows through the same `DoMsg` → confirm modal → `RunMsg` → activity-log path as agentsync argv. A pure package `internal/gitrepo` does detection (manager, layout, pathspec), porcelain v2 `-z` parsing, file classification, the secret scan of added lines and diff masking. A new screen `internal/ui/sourcerepo` drives it.

**Tech Stack:** Go 1.26 stdlib only (`os/exec`, `bytes`, `path/filepath`, `unicode/utf8`); Bubble Tea v2 (existing). **No new module dependency.** Runtime: `git` ≥ 2.25 on PATH for the tab to work (porcelain v2 and `--literal-pathspecs` are older than that). The tab says so if git is missing.

**Spec:** the brief of 2026-11-07, restated as the "Decisions" below (facts verified that day against agentsync, git 2.43 and the maintainer's `~/git/agent-config`). Existing contracts: `docs/superpowers/plans/2026-11-03-agentsync-tui.md` (Global Constraints), `README.md` (safety model), and the sibling plan `/home/cody/tmp/agentsync-tui-discover-plan.md` (it adds tabs 16-17 and two README exceptions; this plan adds tab 18 and a third exception).

## Verified facts this plan relies on

- agentsync v0.17.0 has no commit/push/pull. `agentsync init [<git-url>]` clones into the user home only ("A git-URL clone is only supported at user scope") and refuses a populated destination. Scaffolded homes contain `.gitignore` with `/.state/`, plus `agentsync.toml`, `commands/ hooks/ lsp/ marketplaces/ mcp/ memory/ plugins/ secrets/ skills/ subagents/`. `secrets/` is mode 0700 and holds the age vault.
- `[destination_directory_git_backup]` ("Local-only git history of each managed dir (~/.claude, ~/.kiro, ~/.gemini/config) … Never pushed — rendered files may contain resolved secrets") commits as `author_email = "agentsync@localhost"` by default. On the maintainer's machine `mode = "on"` but `~/.claude/.git` does **not** exist, so the backup git dir lives somewhere else (see "Not verified").
- Maintainer setup: `~/.agentsync` → symlink → `~/git/agent-config`, branch `main` tracking `origin/main`, remote `https://github.com/lusky3/agent-config.git`. Its tracked content includes non-agentsync dirs (`kiro-agents-src/`, `steering/`, `agents/`, `setup.sh`, `README.md`).
- The TUI has **no first-run path** today. Nothing checks whether the home exists, and no screen offers `init`.
- `runner.childEnv` already drops `GIT_ASKPASS` and sets `GIT_TERMINAL_PROMPT=0` and `SSH_ASKPASS_REQUIRE=never` for every child (README, "Because agentsync runs in its own process group…"). `Program` reuses it.
- `ui.resolveInside` (edit.go) already calls `filepath.EvalSymlinks` on the home **and** the file before comparing, and README says a symlinked home is resolved first. No test pins the symlinked-home case, so Task 1 adds it (expected to pass without a code change).
- Global keys the screen cannot use: `q tab shift+tab 1-9 s l ?`. Writes are unbounded by default (`Timeouts.Write == 0`). `finish` always runs `a.next()`. Nothing in the shell switches tabs on request.

## Decisions (read first)

**Status: final (2026-11-07).** D1-D11 come from the brief. R1-R8 are refinements this plan makes where the brief left a choice or where a fact above forced one. Each line names the tasks it affects.

- **D1 Exception to "the CLI is the API", limited to the `git` CLI** (plus read-only `chezmoi source-path`). Argv is built only in `internal/actions/git.go`, runs through `runner.Program`, and is shown and confirmed like agentsync argv. Hardening: `--no-pager -c core.pager=cat -c color.ui=never -c core.fsmonitor=false --literal-pathspecs`, env `LC_ALL=C GIT_PAGER=cat GIT_EDITOR=true GIT_MERGE_AUTOEDIT=no`, `GIT_OPTIONAL_LOCKS=0` for reads (contexts not `runner.MarkWrite`d), porcelain v2 `-z`. `core.hooksPath` is **not** overridden: commit and push hooks are the user's, and the README says so. *Tasks 2, 3, 4.*
- **D2 Status view first, local only.** It shows the detected manager, branch, upstream, ahead/behind, changed and untracked files under the home pathspec, and a masked per-file diff. It refreshes on focus when stale and on `r`, like the other tabs. `fetch` is a separate explicit key. *Tasks 5-8.*
- **D3 Commit = explicit files.** The default selection is every changed file under the agentsync-owned set (`agentsync.toml`, `.gitignore`, `mcp/ memory/ skills/ subagents/ commands/ hooks/ lsp/ plugins/ marketplaces/`). Other files are listed unselected with a note. Argv: `git -C <top> add -- <paths>` then `git -C <top> commit --message=<msg> -- <paths>`. Never `add -A`/`.`, never `commit -a`. *Tasks 3, 6, 8.*
- **D4 Guards before commit.** Each refusal states its reason: `agentsync check` fails; a selected path is under `.state/` or sensitive (R6); the added-line scan finds a hard finding (R5); HEAD is detached; a merge, rebase, cherry-pick, revert or bisect is in progress; `index.lock` is present; the repo is agentsync's destination-backup history. *Tasks 5, 7, 8.*
- **D5 Pull = `git pull --ff-only --no-rebase --no-autostash`.** It is a Network action with the read bound (R3). On a non-fast-forward it explains and shows the commands to run outside the TUI; there is no terminal hand-off. After a successful pull the screen says the changes reach agents only after an apply, and offers jumps to Drift and Apply. *Tasks 4, 9.*
- **D6 Push = `git -C <top> push --no-follow-tags --no-recurse-submodules <remote> <sha>:refs/heads/<branch>`.** One explicit, non-forced, single-branch refspec: the commit the pre-push guard scanned, to the current branch's name on its upstream's remote. *Amended 2026-11-07 (ledger Ruling 32):* the source was `refs/heads/<branch>`, so a commit made on the branch while the confirm modal was open went out unscanned (reproduced). The guard round now reads `rev-parse --verify HEAD^{commit}` and `@{u}^{commit}` in the same round as the scan, scans `<upstream sha>..<sha>` and passes `<sha>` (exactly 40 or 64 lowercase hex, `actions.ValidObjectID`). Verified with git 2.43: the remote branch ends at exactly that sha, a non-fast-forward is still rejected (also with a forced `remote.<name>.push`), and git updates the remote-tracking ref itself through the fetch refspec, so the next status shows the right ahead/behind. No `--force*`, no `+` in any refspec, no wildcard, no `--tags/--all/--mirror/--delete`. *Amended 2026-11-07 after the PR A bot review (ledger Ruling 9):* the earlier `-c push.default=simple push` with no refspec was steerable by repository config, because `remote.<name>.push` refspecs take precedence over `push.default` (with `+refs/heads/*:refs/heads/*` it force-pushed every branch; verified with git 2.43) and `push.followTags` added tags. An explicit refspec on the command line overrides `remote.<name>.push` and `push.default`, `--no-follow-tags` overrides `push.followTags`, `--no-recurse-submodules` overrides `push.recurseSubmodules` (`on-demand` pushed an unpushed submodule commit to the submodule's own remote, `check` aborted the push over another repository's state; both verified), and git rejects a non-fast-forward instead of forcing it. With `remote.<name>.mirror=true` git refuses the push (`--mirror can't be combined with refspecs`) and changes nothing; Task 9 refuses a mirror remote up front so the user never sees that confusing error. With no upstream it refuses and shows the commands to add one. The TUI never creates remotes or GitHub repos. It warns, without refusing, when the remote host is a public hosting service, since it cannot see the repository's visibility. *Tasks 3, 9.*
- **D7 Fetch = `f`.** `git -C <top> fetch <remote>` is ReadOnly and Network (no modal, logged) and updates ahead/behind. *Tasks 3, 9.*
- **D8 Chezmoi-managed home.** The tab shows a read-only notice plus the chezmoi commands to use, and offers no git action. *Tasks 5, 8.*
- **D9 Symlinked home.** Repo detection uses `filepath.EvalSymlinks`. The edit inside-home check is pinned by tests (Task 1). *Tasks 1, 5.*
- **D10 Bootstrap.** No first-run path exists, so it lives on this tab. In user scope, when the home is missing or an empty directory, `i` takes a URL and emits `agentsync init <url>` (Network, confirm `y`, the existing write bound). The URL must be https or ssh (`ssh://` or scp form). Rejected: `ext::` and any `<transport>::`, `file://`, `http://`, a leading `-`, `--upload-pack`, whitespace and control characters, and https userinfo. *Tasks 3, 9.*
- **D11 Never:** agentsync's backup repos, committing `.state/`, `--force`, `reset`, `clean`, `checkout`, `restore`, `stash`, `rebase`, `--no-verify`, `remote add/set-url`, `config` writes. A test enforces this over every builder (Task 3).
- **R1 Children get their own session (`Setsid`) instead of only `Setpgid`.** `ssh`, a credential helper, gpg pinentry-tty or a hook then has no controlling terminal: it fails at once instead of stopping on SIGTTIN behind the alt-screen. `kill(-pid)` still reaches the group because the session leader's pgid is its pid. `GIT_SSH_COMMAND="ssh -o BatchMode=yes"` is added only for `fetch`/`pull`/`push` when none of `GIT_SSH_COMMAND`, `GIT_SSH` or `core.sshCommand` (read with the caller's leading `-C`/`-c` pairs, so a `-c core.sshCommand=` in the args also counts) is set, so a user's own ssh command always wins. *Task 2.*
- **R2 Repository-selecting env is scrubbed for git children:** `GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_NAMESPACE GIT_CEILING_DIRECTORIES GIT_GLOB_PATHSPECS GIT_NOGLOB_PATHSPECS GIT_ICASE_PATHSPECS GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT`. Run from a git hook, the TUI would otherwise inherit `GIT_DIR` and act on another repository. *Task 2.*
- **R3 Git actions are always bounded**, because git's ref updates are atomic and its index writes use a lock file, so stopping git halfway never corrupts the repository (unlike a multi-file agentsync write). It is not always clean: a pull stopped during its checkout can leave some worktree files at the new version while HEAD and the index still name the old commit, and a SIGKILL can leave `index.lock` behind. So when a pull ends by timeout or interrupt, the note says "the pull was stopped; the worktree may be partly updated. Nothing was rolled back" and shows `git -C <top> status` to inspect (the fetched commits are already in `@{u}`); a left-over lock is reported by `InspectGitDir` and never deleted (Task 9). Network git actions (fetch, pull, push) get `Timeouts.Read` (default 2m). Local git writes (add, commit) get `Timeouts.Write` if set, else `GitLocalWriteBound = 2*time.Minute`, which covers slow hooks. Local reads get `Timeouts.Local()`. `agentsync init <url>` keeps the existing write semantics. *Task 4.*
- **R4 Add-then-commit is one confirmation.** `DoMsg` gains `Then []actions.Action`. The modal shows every command line. Each step is logged and broadcast, and a failure stops the chain. If `add` succeeds and `commit` fails (a hook rejects it), the note says the files are now staged and shows `git -C <top> restore --staged -- …` for the user to run. The TUI does not run it. *Tasks 4, 8.*
- **R5 Two classes of scan finding.** **Hard** (refuse, everywhere): known token shapes (`ghp_`, `github_pat_`, `gg_pat_`, `sk-`, `xox?-`, `AKIA…`, JWT), an Authorization/Bearer/x-api-key/cookie value that is not a reference, URL userinfo, `-----BEGIN … PRIVATE KEY-----`, `AGE-SECRET-KEY-1`. Also hard in the TOML config files (`agentsync.toml`, `mcp/**`, `hooks/*.toml`, `lsp/*.toml`): a secret-named key assigned a literal, and a `mask.TokenShaped` word that is not a reference **when it is an env value, a header value, or a URL query value**. A TokenShaped word elsewhere in a config file (for example inside `args`) is **soft**, and these are never findings at all: an OCI digest `@sha256:[0-9a-f]{64}`, a `sha256:`/`sha512:` integrity value, a 40-hex git commit pinned in an argument (as in `go install …@<sha>`), and a UUID that is not under a secret-named key. Reason: the discover plan writes digest-pinned Docker images (`mcp/brave-search@sha256:…`) into `mcp/*.toml`, and a hard refusal there would make every Docker-catalog install uncommittable from the TUI. **Soft** (allowed only after typing `commit`, findings listed): `TokenShaped` words and secret-named assignments in prose files (skills, memory, …), plus binary or over-1 MiB files that were not scanned. Reason: skills legitimately contain digests, UUIDs and "password:" prose, and a hard block there would make the tab useless. Every refusal shows `file:line`, the masked line, and the outside-the-TUI command for a known false positive. *Tasks 4, 7, 8.*
- **R6 `secrets/` and `*.age`.** A file whose content contains `AGE-SECRET-KEY-` is sensitive (never selectable), whatever its name. An encrypted vault (starts `age-encryption.org/v1`) is listed **unselected** with "encrypted vault: commit only if you mean to share the ciphertext". Also never selectable: `.state/**`, basename `key.txt`, `*.pem`, `id_*` except `*.pub`, `.env`, `.env.*`. *Tasks 6, 7.*
- **R7 Push and pull only when the repository is exactly the home** (Layout `AtRoot`). When the repo root is above the home (a dotfiles repo, or a project repo with `.agentsync/` inside), status, diff, commit and fetch are pathspec-limited to the home. Push and pull are refused with an explanation and the commands, because they would move commits and files the TUI does not own. *Tasks 5, 9.*
- **R8 Execution: subagent-driven, two PRs.** PR A = Tasks 1-4 (foundation, no new tab). PR B = Tasks 5-10. Both end with a whole-branch review. Reason: R1-R4 change shared shell and runner code that every screen depends on, and keeping them separate keeps the review focused.

## Relationship to the discover plan (read before executing either)

- **Order: execute this plan first.** It delivers what the maintainer needs now (versioning the home that already lives in a private repo), it touches no network service other than the user's own remote, and its PR A (`runner.Program`, `Action.Program`, `DoChain`, `GotoMsg`) is shared-shell work the discover plan can reuse: the discover plan's Task 11 (skill-install confirm) should be built on this plan's chain and confirm changes, not beside them.
- **Shared files:** both plans modify `internal/ui/app.go` (Deps), `exec.go`, `modal.go`, `internal/app/screens.go`, `main.go` and the README safety model. Whichever lands second rebases onto the first and keeps one confirm path, one exception list in the README, and count-relative tab tests.
- **Secret scan vs Docker digests:** R5 exempts OCI digests and pinned commit hashes so that MCP servers installed from the Docker catalog (digest-pinned `docker run` args) can be committed from this tab. Keep `TestImageDigestInArgsNotAFinding` green when either plan changes.
- **Tabs:** this tab is always appended last; the discover plan appends its two tabs after Health. Both tests are count-relative.

## Global Constraints

- Go `1.26.0` (go.mod), toolchain `go1.26.10`; Bubble Tea `charm.land/bubbletea/v2`; **no new module dependency**.
- Binaries the TUI may execute: `agentsync`, `git` and `chezmoi` (only `chezmoi source-path [<path>]`), the user's editor (existing), nothing else. Git argv is built only in `internal/actions/git.go`; the screen never assembles argv.
- Every git argv starts `-C <absolute toplevel>`, puts `--` before any path, and passes paths relative to the toplevel that satisfy `filepath.IsLocal`, lie under the home pathspec, are valid UTF-8 and contain no control or format characters (`mask.Sanitize(p) == p`). A commit message is one argv element, `--message=<msg>`.
- The forbidden list in D11 is enforced by `TestGitBuildersNeverEmitForbidden` (Task 3).
- Diff or file content is never written to the activity log, the status line, `--debug-log` or a `RunMsg`-derived note. It is only rendered, through `gitrepo.MaskPatchLine` and capped at `ui.MaxOutput`. A scan finding is shown as `file:line` plus `mask.Text(line)` cut to 120 columns.
- The network is touched only on the keys `f`, `p`, `P` and the bootstrap's enter, never on refresh, focus or a timer.
- Tests are hermetic. Git tests use `gitrepo/gittest` (temp `HOME`, `GIT_CONFIG_GLOBAL` file, `GIT_CONFIG_NOSYSTEM=1`, scrubbed `GIT_*`, a bare repo as the remote) and `t.Skip` when `git` is not on PATH. agentsync tests use `testenv` and skip without `agentsync`.
- Commit messages: conventional commits, **no `Co-Authored-By`, `Claude-Session` or "Generated with" lines**. Never push to `main`, never force-push, merge only when asked.
- Gate per commit: `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && golangci-lint run && go test -race -count=1 ./...` (if `golangci-lint` is not on PATH use `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run`). Run each check separately or under `set -e`; a chained `&&` inside a script without `set -e` has hidden a gofmt failure before.

## Review Focus

Failure modes the spec implies; each has its test in the owning task.

1. **Option injection.** A file named `-rf`, `--exec=x`, `--output=/tmp/x` or `--upload-pack=x`, a commit message `--amend` or `-a`, a remote named `--receive-pack=x`, a bootstrap URL `-u`/`ext::sh -c x`. Expected: paths always follow `--`, the message is a single `--message=` element, and names and URLs are refused before an `Action` exists. Tasks 3, 8.
2. **Hostile file names.** A newline, a tab, `"`, a backslash, bidi or zero-width characters, invalid UTF-8, glob characters (`*`, `?`, `[`), a leading space. Expected: `-z` parsing keeps the exact bytes; `*`/`?` match literally (`--literal-pathspecs`); a name with control or format characters is shown quoted and marked "unusual name, not selectable". Two names that sanitise to the same string never look identical. Tasks 6, 8.
3. **Credential leakage.** Through the diff pane, the scan finding lines, the activity log (`Display` of a commit with a token-shaped message), the status line (a push error echoing `https://user:token@host`), and `--debug-log`. Expected: masked or absent everywhere, and diff bytes never logged. Tasks 3, 7, 8, 9.
4. **Repository root above the home.** The pathspec must not escape. A rename from outside into the home commits only the in-home side. Push and pull are refused (R7). A symlinked home whose target lies outside the repo is refused. Tasks 5, 6, 9.
5. **Concurrent or stuck git.** `index.lock` held by the user's own git; a merge or rebase in progress; a push to a remote that never answers; an ssh host-key or passphrase prompt; a credential helper that wants a TTY. Expected: refuse with a clear message and never delete the lock. On a hang: bounded, SIGINT then SIGKILL after `runner.InterruptGrace`, no TTY prompt (R1). Tasks 2, 4, 5, 9.
6. **Inherited repository env.** The TUI is started with `GIT_DIR`/`GIT_WORK_TREE` set (from a hook or a script). Expected: scrubbed, so the command acts on the home's repo (R2). Task 2.

## File Structure

| Path | Responsibility |
|---|---|
| `internal/runner/program.go` (new) | `Program`: hardened runner for a helper binary (git, chezmoi); shares the run core with `Exec` |
| `internal/runner/runner.go`, `live.go`, `pgroup_*.go` (modify) | Extract the shared run core; `Ended.Program`; `setSession` |
| `internal/actions/actions.go` (modify) | `Action.Program`, `TypedConfirm`; `Display`/`ConfirmVerb` per program |
| `internal/actions/git.go` (new) | Git and `init <url>` builders + validators |
| `internal/ui/exec.go`, `modal.go`, `app.go` (modify) | Route by program, git bounds, `DoMsg.Then` chain, `GotoMsg`, repo line in the modal, `Deps.Git/Chezmoi` |
| `internal/gitrepo/detect.go` (new) | Manager/layout/pathspec/backup-repo detection; gitdir state (lock, in-progress) |
| `internal/gitrepo/status.go` (new) | Porcelain v2 `-z` parser, classification, default selection |
| `internal/gitrepo/scan.go`, `diffmask.go` (new) | Added-line parsing, findings (hard/soft), patch-line masking |
| `internal/gitrepo/gittest/gittest.go` (new) | Hermetic git helpers for tests |
| `internal/mask/credential.go` (new) | `Credential(s) (kind string, ok bool)`: the hard detectors, reusing `mask.go`'s rules |
| `internal/ui/sourcerepo/{sourcerepo,commit,remote,view}.go` (new) | The tab |
| `internal/app/screens.go`, `main.go`, `README.md`, `test/e2e/sourcerepo_test.go` | Registration, wiring, docs, end-to-end |

Dependency order: 1 is independent; 2 → 3 → 4; 5 needs 2; 6 and 7 need 5; 8 needs 3, 4, 6, 7; 9 needs 8; 10 last.

---

## Part A: Foundation (PR A)

### Task 1: Pin the edit check under a symlinked home

**Files:** Test `internal/ui/edit_test.go`, `internal/ui/library/e2e_test.go`; Modify `internal/ui/edit.go` **only if** a test fails.

**Interfaces:** Consumes `resolveInside(home, p string) (string, error)` and `EditFileMsg` (unchanged).

- [ ] **Step 1: Write the tests.** Setup for all of them: `home = <tmp>/link` → symlink → `<tmp>/real`.
  - `TestEditFileAcceptsFileUnderSymlinkedHome`: `Path = <tmp>/link/mcp/srv.toml`. Assert the editor runs with argv path `<tmp>/real/mcp/srv.toml` and the status has no "refused".
  - `TestEditFileAcceptsResolvedPathUnderSymlinkedHome`: `Path` given as `<tmp>/real/mcp/srv.toml`. Accepted.
  - `TestEditFileRefusesEscapeFromSymlinkedHome`: `<tmp>/link/mcp/evil.toml` is a symlink to `<tmp>/outside.toml`. Refused, and the outside file is unchanged.
  - `TestEditFileRefusesSiblingOfSymlinkTarget`: `<tmp>/real2/x.toml` is refused (prefix trap: `real` vs `real2`).
  - `TestLibraryEditUnderSymlinkedHome` (library e2e): with `AgentsyncHome` symlinked, `E` on a skill opens the editor.
- [ ] **Step 2: Run** `go test ./internal/ui/ ./internal/ui/library/ -run 'Symlinked|SiblingOfSymlink' -v`. Expected: PASS (code reading: `resolveInside` resolves both sides). If any FAIL, that is the bug from D9. Fix it in `resolveInside` and keep its doc comment's guarantee.
- [ ] **Step 3: Run** the gate.
- [ ] **Step 4: Commit** `test(ui): pin the edit inside-home check for a symlinked home` (or `fix(ui): …` if Step 2 needed a change).

### Task 2: `runner.Program`, a hardened helper runner

**Files:** Create `internal/runner/program.go`, `program_test.go`, `testdata/fake-git.sh` (prints argv, selected env and `tty` status). Modify `runner.go` (extract `runCore`), `live.go` (`Ended.Program`), `pgroup_unix.go`/`pgroup_other.go` (`setSession`).

**Interfaces:**
- Produces:
  - `type Program struct{ Env []string /* nil = os.Environ() */; ... }`, implementing `Runner`.
  - `func NewGit(bin string) *Program`: fixed pre-args `--no-pager -c core.pager=cat -c color.ui=never -c core.fsmonitor=false --literal-pathspecs`; env per D1, R1, R2.
  - `func NewChezmoi(bin string) *Program`: no pre-args; env `childEnv` + `LC_ALL=C` + `NO_COLOR=1`.
  - `func (p *Program) Name() string` (`"git"` / `"chezmoi"`).
  - `type MissingError struct{ Name string }` (message `"<name> is not installed"`), returned when the binary is not found.
  - `Ended.Program string` (`""` = agentsync). `track` records it.
  - internal `runCore(ctx context.Context, cmd *exec.Cmd, args []string, grace time.Duration, program string) (Result, error)`: the existing body of `Exec.Run` from `setProcessGroup` to the exit-code switch, unchanged for `Exec`.
- Rules: `Program` calls `setSession(cmd)` (`Setsid: true`; on non-unix the same as `setProcessGroup`). `GIT_OPTIONAL_LOCKS=0` only when `!IsWrite(ctx)`. BatchMode (R1) only when `args` contains the subcommand `fetch`, `pull` or `push` **and** the env has no `GIT_SSH_COMMAND`/`GIT_SSH` **and** `git -C <dir> config --get core.sshCommand` (run through the same core with the local bound) exits 1. the probe runs `git <leading -C/-c pairs of args> config --get core.sshCommand` through the same core, so the caller's own `-c core.sshCommand=…` counts as set.

- [ ] **Step 1: Write failing tests** (fake script and real git where named):
  - `TestGitPreArgsPrecedeCallerArgs`: argv seen = pre-args then `-C /r status`.
  - `TestGitEnvHardened`: `LC_ALL=C`, `GIT_PAGER=cat`, `GIT_EDITOR=true`, `GIT_TERMINAL_PROMPT=0`, `SSH_ASKPASS_REQUIRE=never`, no `GIT_ASKPASS`.
  - `TestOptionalLocksOnlyForReads`: present without `MarkWrite`, absent with it.
  - `TestRepoSelectingEnvScrubbed`: set `GIT_DIR=/elsewhere GIT_WORK_TREE=/x GIT_INDEX_FILE=/y GIT_CONFIG_PARAMETERS=…`; the child sees none of them.
  - `TestBatchModeAddedOnlyWhenNoUserSSH`: five cases (the fifth: a caller `-c core.sshCommand=…` → not added), namely none set → added; `GIT_SSH_COMMAND` set → untouched; `GIT_SSH` set → not added; `core.sshCommand` in the repo config (real git, gittest) → not added. Also: not added for `status`.
  - `TestChildHasNoControllingTerminal`: the fake prints `tty` (or `ps -o tty= $$`) and the output is `not a tty`/`?`.
  - `TestProgramCancelInterruptsThenKillsGroup`: reuse `trap-int.sh`/`spawn-child.sh`; same assertions as the existing `Exec` interrupt tests.
  - `TestMissingBinaryIsMissingError` (`errors.As`, message `git is not installed`).
  - `TestOutputCappedLikeExec`.
  - `TestEndedCarriesProgram` (Shutdown of a live `Program` child reports `Program=="git"`).
  - `TestExecUnchanged`: the existing runner tests still pass, run unmodified.
- [ ] **Step 2: Run** `go test ./internal/runner/... -run 'Git|Program|Optional|Scrubbed|BatchMode|Controlling|Ended' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** by extracting `runCore` first (no behaviour change for `Exec`; run the old tests), then `Program`. Env is built as `childEnv(base)` minus the R2 names plus the D1 values.
- [ ] **Step 4: Run** `go test -race -count=3 ./internal/runner/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(runner): hardened Program runner for git and chezmoi`.

### Task 3: Git actions and validators

**Files:** Modify `internal/actions/actions.go`; Create `internal/actions/git.go`, `git_test.go`.

**Interfaces:**
- Produces:
  - `Action.Program string` (`""` = agentsync; `const ProgramGit = "git"`) and `Action.TypedConfirm bool` (the modal asks for the typed verb without the `[destructive]` badge).
  - `Display()` starts with `Program` or `"agentsync"`.
  - `ConfirmVerb()` for git = the subcommand after the leading `-C <dir>` and `-c k=v` pairs.
  - `type GitRepo struct{ Top string }` (absolute and clean, else every builder errors).
  - `func GitAdd(r GitRepo, paths []string) (Action, error)` → `-C top add -- p…`.
  - `func GitCommit(r GitRepo, msg string, paths []string) (Action, error)` → `-C top commit --message=<msg> -- p…`.
  - `func GitFetch(r GitRepo, remote string) (Action, error)` → `-C top fetch <remote>`; `ReadOnly, Network`.
  - `func GitPull(r GitRepo) (Action, error)` → `-C top pull --ff-only --no-rebase --no-autostash`; `Network`.
  - `func GitPush(r GitRepo, remote, branch, sha string) (Action, error)` → `-C top push --no-follow-tags --no-recurse-submodules <remote> <sha>:refs/heads/<branch>`; `Network`. `remote` must pass `ValidRemoteName`, `branch` `ValidBranchName` and `sha` `ValidObjectID` (exactly 40 or 64 lowercase hex); a bad one is refused, never escaped (D6, Ruling 32).
  - `func InitFromURL(url string) (Action, error)` → `init <url>`; `Network`, title `"Clone agentsync home"`.
  - `func ValidGitPath(p string) error`; `func ValidCommitMessage(m string) error`; `func ValidRemoteName(n string) error`; `func ValidBranchName(b string) error` (git check-ref-format rules for `refs/heads/<b>`, plus no leading `-`, no `+`, not `@`/`HEAD`, ≤ 255 bytes); `func ValidCloneURL(u string) error`.
- Rules (pinned):
  - Paths: 1 to 5000, each `ValidGitPath`: non-empty, `filepath.IsLocal`, valid UTF-8, `mask.Sanitize(p)==p`, no NUL, ≤ 4096 bytes.
  - Message: trimmed and non-empty, ≤ 200 runes, single line, no control or format characters, `mask.Credential(m)` false, no `TokenShaped` word.
  - Remote: `^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`, no `..`.
  - Clone URL: ≤ 2048 bytes; `https://host/path` without userinfo, or `ssh://[user@]host[:port]/path`, or scp form `[user@]host:path` where host has no `/` and path does not start with `-` or `/`. Refused: anything containing `::`, a leading `-`, `file:`, `http:`, whitespace, control characters, `--upload-pack`/`--config`/`-u` anywhere.
  - Display of add and commit lists at most 10 paths, then `… (+N more)` (set via the pre-masked `display` field). Argv always has all of them.

- [ ] **Step 1: Write failing tests:**
  - `TestGitAddArgvAndDashPaths`: paths `-rf`, `--exec=x` come after `--`, verbatim.
  - `TestGitCommitMessageIsOneElement`: messages `--amend`, `-a`, `x --no-verify` produce exactly one `--message=…` element, and `--amend`/`-a`/`--no-verify` never appear as their own elements.
  - `TestCommitMessageRefusals`: empty, newline, ESC, bidi, 201 runes, `ghp_…`, a TokenShaped word.
  - `TestGitPathRefusals`: `../x`, `/abs`, `a/../../b`, `"a\nb"`, `"\xff"`, `"a‮b"`, empty, 5001 paths.
  - `TestGlobCharsAllowedInPaths` (`a*b?[c]` accepted; literal semantics come from Task 2's pre-args).
  - `TestFetchRemoteRefusals` (`--receive-pack=x`, `-x`, `a b`, `../r`).
  - `TestPullAndPushArgvPinned` (exact argv slices).
  - `TestValidBranchName` (table; every accepted name also passes real `git check-ref-format refs/heads/<b>`), and real-git push tests against a bare remote: `TestGitPushIgnoresConfiguredRefspecsAndTags` (with `remote.origin.push=+refs/heads/*:refs/heads/*` and `push.followTags=true`, only the current branch moves; a diverged second branch and an annotated tag are untouched), `TestGitPushNonFastForwardRejected`, `TestGitPushToMirrorRemoteChangesNothing`, `TestGitPushNeverRecursesIntoSubmodules` (`push.recurseSubmodules` = `on-demand` and `check`; the submodule's remote is unchanged and the superproject's branch moves).
  - `TestCloneURLTable` accepts `https://github.com/o/r.git`, `ssh://git@github.com/o/r.git`, `git@github.com:o/r.git`, `host:r.git`. It refuses `ext::sh -c x`, `fd::0`, `file:///x`, `http://h/r`, `-uX`, `https://tok@github.com/o/r`, `https://u:p@h/r`, `git@host:-x`, `a b`, `x --upload-pack=y`.
  - `TestGitDisplayStartsWithGitAndIsMasked`: a commit whose message would be token-shaped never builds. A path `skills/ghp_x/SKILL.md` shows masked in `Display` but unchanged in `Argv`.
  - `TestDisplayAbbreviatesManyPaths`.
  - `TestConfirmVerbForGit` (`commit`, `push`).
  - `TestGitBuildersNeverEmitForbidden`: over a table of every builder × hostile inputs, no argv element equals or starts with `--force`, `-f`, `--force-with-lease`, `--no-verify`, `-n`, `-a`, `--all`, `-A`, `--amend`, `--mirror`, `--tags`, `--delete`, `-d`, `--upload-pack`, `--receive-pack`, `--exec`. No argv element before `--` starts with `+`, and no refspec contains `+`; the hostile table includes `+main`, `+refs/heads/*:refs/heads/*`, `:main` and `main:other` as remote and branch arguments. No subcommand outside `{add, commit, fetch, pull, push}`; for `init` only `init`.
  - `TestAgentsyncDisplayUnchanged` (existing display tests pass).
- [ ] **Step 2: Run** `go test ./internal/actions/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** Add `mask.Credential` here (Create `internal/mask/credential.go`, test `credential_test.go` with `TestCredentialHardKinds` for every R5 hard kind and `TestCredentialIgnoresReferences` (`Bearer ${secret:x}`, `${env:T}`) and plain URLs with query strings). It reuses the existing `textRules`/`shapeRes` minus the URL-structure pass and returns a kind name (`"token-shape"`, `"auth-header"`, `"url-userinfo"`, `"private-key"`, `"age-identity"`, `"secret-assignment"`).
- [ ] **Step 4: Run** `go test -race ./internal/actions/... ./internal/mask/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(actions): validated git and init-from-URL builders`.

### Task 4: Shell support: program routing, git bounds, chains, tab jumps

**Files:** Modify `internal/ui/app.go` (Deps), `exec.go`, `modal.go`, `keys.go` (nothing else); Test `internal/ui/git_exec_test.go`, `modal_test.go`.

**Interfaces:**
- Consumes: Task 2 `runner.Program`, `Ended.Program`; Task 3 `Action.Program`, `TypedConfirm`.
- Produces:
  - `Deps.Git runner.Runner` and `Deps.Chezmoi runner.Runner` (nil = unavailable).
  - `DoMsg.Then []actions.Action` and `func DoChain(scope runner.Scope, first actions.Action, then ...actions.Action) tea.Cmd`.
  - `type GotoMsg struct{ Title string }`: the shell selects the first screen with that title (refreshing it if stale); an unknown title only sets a warn status.
  - `const GitLocalWriteBound = 2 * time.Minute`.
  - `func gitLimit(act actions.Action, t Timeouts) time.Duration` per R3.
- Rules:
  - `start` picks `a.d.Git` for `Program == "git"`; nil → `RunMsg{Err: errNoGit}` ("git is not installed"). Git actions are never `Interactive`.
  - The modal's scope line for git reads `repo: <mask.Sanitize(top)>`. A chain renders each `Display()` on its own `$` line, in order.
  - `TypedConfirm` uses the verb path without the destructive badge.
  - Chain: `running.then` keeps the rest. `finish` starts the next step directly (no modal) only when `Err == nil && ExitCode == 0`. Otherwise it drops the rest and the status ends `; not run: <titles>`. Each step is recorded and broadcast.
  - The scope check in `request` applies to the whole chain.
  - `RecordShutdown` uses `Action{Program: e.Program, Argv: e.Args}`.

- [ ] **Step 1: Write failing tests** (runnertest `Fake` as both `Runner` and `Git`):
  - `TestGitActionRunsOnGitRunner`: the agentsync fake gets zero calls.
  - `TestGitActionWithoutGitRunnerFails`.
  - `TestGitNetworkActionBoundedByRead`: a push with `Timeouts{Read: 50ms}` on a blocking fake ends as `timed out after`.
  - `TestGitLocalWriteDefaultBound`: commit with `Write: 0` gets a context with a deadline about 2m away. Use `Fake.Ctxs()`.
  - `TestGitLocalWriteUsesWriteWhenSet`.
  - `TestChainRunsSecondOnlyAfterSuccess`.
  - `TestChainStopsOnFailureAndSaysWhatDidNotRun`.
  - `TestChainLogsEachStep`.
  - `TestChainOneConfirm`: exactly one modal, then two runs.
  - `TestChainRefusedAfterScopeSwitch`.
  - `TestModalShowsRepoLineForGit`.
  - `TestModalShowsEveryChainCommand`.
  - `TestTypedConfirmNeedsVerbNoDestructiveBadge`.
  - `TestGotoSelectsByTitleAndRefreshesStale`.
  - `TestGotoUnknownTitleWarns`.
  - `TestRecordShutdownShowsGitProgram`.
  - `TestRunMsgStderrWithTokenURLMaskedInStatus`: stderr `fatal: unable to access 'https://u:ghp_x@github.com/o/r/'`, and the status line contains neither `ghp_x` nor `u:`.
- [ ] **Step 2: Run** `go test ./internal/ui/ -run 'Git|Chain|Goto|TypedConfirm|RepoLine|RecordShutdown' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** with the smallest changes to `request`/`start`/`finish`/`newModal`. Do not add a second confirm path.
- [ ] **Step 4: Run** the full gate (every existing shell test must pass unchanged).
- [ ] **Step 5: Commit** `feat(ui): run git actions through the shell with bounds and confirm chains`. **End of PR A.**

---

## Part B: The Source repo tab (PR B)

### Task 5: Detection: manager, layout, pathspec, refusals

**Files:** Create `internal/gitrepo/detect.go`, `detect_test.go`, `internal/gitrepo/gittest/gittest.go`, `gittest_test.go`.

**Interfaces:**
- Consumes: `runner.Runner` (git, chezmoi), `ui.Read`-style bounded calls. To avoid importing `ui`, a local `read(ctx, r, timeout, argv...)` does the same thing.
- Produces:
  - `type Manager int` (`Missing`, `None`, `Git`, `Chezmoi`), `type Layout int` (`AtRoot`, `BelowRoot`).
  - `type Repo struct{ Manager Manager; Layout Layout; Home, ResolvedHome, Top, GitDir, Pathspec, ChezmoiSource, ObjectFormat, Refusal string }`. `Pathspec` is `"."` or the slash-separated home path relative to `Top`. A non-empty `Refusal` disables every git write.
  - `func Detect(ctx context.Context, git, chezmoi runner.Runner, home string, timeout time.Duration) (Repo, error)`.
  - `type GitDirState struct{ Locked bool; InProgress string }` and `func InspectGitDir(gitDir string) GitDirState`. It checks `index.lock`, `MERGE_HEAD`, `rebase-merge/`, `rebase-apply/`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `BISECT_LOG`, using `os.Lstat` only and never removing anything.
  - `func BackupAuthor(home string) string`: reads `[destination_directory_git_backup].author_email` from `<home>/agentsync.toml` with the existing TOML dependency, defaulting to `agentsync@localhost`.
  - gittest: `Need(t)`, `Env(t) []string`, `Init(t, dir)`, `Bare(t) string`, `Commit(t, dir, msg string, files map[string]string)`, `Git(t, dir string, args ...string) string`, `NewGit(t) *runner.Program` (with `Env` set).
- Detection order (pinned):
  1. `os.Lstat(home)`: missing, or an empty directory → `Missing`.
  2. `EvalSymlinks` → `ResolvedHome`.
  3. If `chezmoi != nil`: `chezmoi source-path` → `src`. If `ResolvedHome` is inside `EvalSymlinks(src)`, or `chezmoi source-path <ResolvedHome>` exits 0 → `Chezmoi`.
  4. `git -C <ResolvedHome> rev-parse --show-toplevel --absolute-git-dir`. Exit 128 with "not a git repository" → `None`. Not exactly two lines → `Refusal "repository path has a line break"`.
  5. `rel = filepath.Rel(EvalSymlinks(top), ResolvedHome)`. It must be `"."` or local, else `Refusal "the home is outside the repository"`.
  6. Any commit reachable from HEAD is authored by `BackupAuthor`: `git -C top log -1 --fixed-strings --author=<BackupAuthor in angle brackets> --format=%H` prints a hash → `Refusal "this is agentsync's local-only destination history; it must never be committed or pushed"`. (Checking only the latest author would miss a backup repo the user has committed to by hand.)
  7. A `dubious ownership` stderr → `Refusal` with git's first line. The TUI never adds `safe.directory`.

- [ ] **Step 1: Write failing tests** (real git through gittest; fake chezmoi through runnertest):
  - `TestMissingHome`, `TestEmptyHomeIsMissing`, `TestNoRepoIsNone`.
  - `TestRepoAtHome` (`Pathspec "."`, `AtRoot`).
  - `TestHomeBelowDotfilesRoot` (`Pathspec ".agentsync"`, `BelowRoot`).
  - `TestProjectScopeSubdir` (`<proj>/.agentsync`).
  - `TestSymlinkedHomeResolved` (the maintainer layout: `~/.agentsync` → `~/git/agent-config`, so `Top` equals the target and `Pathspec "."`).
  - `TestSymlinkedHomeIntoOtherRepoSubdir`.
  - `TestBackupAuthorRepoRefused` (HEAD authored by `agentsync@localhost`).
  - `TestBackupRepoRefusedAfterManualCommit` (an `agentsync@localhost` commit followed by one with another author is still refused).
  - `TestCustomBackupAuthorFromToml`.
  - `TestChezmoiSourceContainsHome`, `TestChezmoiManagedTarget`, `TestChezmoiAbsentFallsThroughToGit`.
  - `TestInspectLockAndInProgress` (creates each marker; asserts the lock file still exists afterwards).
  - `TestDetectDoesNotWriteToRepo`: mtimes of `.git/index` unchanged, made possible by `GIT_OPTIONAL_LOCKS=0`.
  - `TestDetectBounded` (blocking fake → error after the timeout).
- [ ] **Step 2: Run** `go test ./internal/gitrepo/... -run 'Detect|Home|Repo|Chezmoi|Inspect|Backup' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** per the order above.
- [ ] **Step 4: Run** `go test -race -count=3 ./internal/gitrepo/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(gitrepo): detect how the agentsync home is versioned`.

### Task 6: Status parsing and classification

**Files:** Create `internal/gitrepo/status.go`, `status_test.go`, `testdata/porcelain-*.bin` (generated from real git by a `go test -run TestRegenerate -update` helper, committed).

**Interfaces:**
- Consumes: Task 5 `Repo`.
- Produces:
  - `func StatusArgv(r Repo) []string` → `-C top status --porcelain=v2 -z --branch --untracked-files=all --ignore-submodules=all -- <pathspec>`.
  - `type Branch struct{ Head string; Detached, Unborn bool; Upstream string; Ahead, Behind int; ABKnown bool }`.
  - `type Kind int` (`Modified, Added, Deleted, Renamed, Untracked, Conflict, TypeChanged`), `type Class int` (`Owned, Other, StateDir, Sensitive, Vault, Unusual`).
  - `type File struct{ Path, Orig, HomeRel string; Kind Kind; Class Class; Selectable, Default bool; Note string }`.
  - `type Status struct{ Branch Branch; Files []File }`.
  - `func ParseStatus(out []byte, r Repo) (Status, error)`.
  - `func Classify(homeRel string, head []byte, ageIdentity bool) (Class, string)`. `head` is up to the first 4 KiB of the worktree file, read with `os.Lstat` then open, never through a symlink. `ageIdentity` comes from `func HasAgeIdentity(path string) (bool, error)`, which streams the **whole** file (same Lstat-then-open, no symlink; the marker is matched across read-buffer boundaries) for `AGE-SECRET-KEY-`, so a marker past the first 4 KiB, or in a file over the 1 MiB scan cap, still makes the file Sensitive. A read error counts as found.
  - `func DisplayName(p string) string`: `p` itself when `mask.Sanitize(p)==p && utf8.ValidString(p)`, else `strconv.QuoteToASCII(p)` + ` (unusual name)`. In both branches a name in which `mask.Credential` finds a token is shown through `mask.Text` with ` (name looks like a credential)`. Argv keeps the original path.
- Parsing rules (pinned):
  - Split on NUL.
  - A record starting `# ` is a header: `branch.head` (`(detached)` → Detached), `branch.oid` (`(initial)` → Unborn), `branch.upstream`, `branch.ab +A -B`.
  - `1 ` → 8 space-separated fields, then the path (the rest of the record, spaces kept).
  - `2 ` → 9 fields then the path, and the **next** NUL record is `Orig`.
  - `u ` → 10 fields then the path (Conflict).
  - `? ` → Untracked. `! ` → ignored and skipped.
  - Anything else → error (the error message never echoes bytes).
  - A path not under the pathspec, or failing `ValidGitPath`'s locality test, is dropped and counted in a note.
- Classification (pinned):
  - `.state/` prefix → StateDir (not selectable).
  - Contains `AGE-SECRET-KEY-`, basename `key.txt`, `*.pem`, `id_*` except `*.pub`, `.env`, `.env.*` → Sensitive (not selectable).
  - Starts `age-encryption.org/v1` → Vault (selectable, not default).
  - Unusual name, or a name in which `mask.Credential` finds a token (committing it would publish the token in the tree; masked names could also look alike) → Unusual (not selectable).
  - Owned set from D3 → Owned (default when selectable).
  - Else Other (selectable, not default, note "not an agentsync file").
  - A rename into the home whose `Orig` is outside the home gets the note "the old path is outside the home and is not part of this commit".
  - Conflict → not selectable.

- [ ] **Step 1: Write failing tests:**
  - `TestParseHeaders` (ahead 2 behind 1, upstream `origin/main`).
  - `TestParseUnbornAndDetached`.
  - `TestParseOrdinaryRenamedUnmergedUntracked` (from real-git fixtures).
  - `TestPathWithSpacesNewlineTabQuoteBackslashKeptExact`.
  - `TestInvalidUTF8AndBidiAreUnusual`.
  - `TestDisplayNamesNeverCollide` (`"a​b"` vs `"ab"` render differently).
  - `TestTokenInFileNameMaskedAndNotSelectable` (`skills/ghp_<20 chars>/SKILL.md`: the token is absent from the row, the file is Unusual).
  - `TestGlobNamedFileMatchedLiterally`: real git with files `a*` and `ab`, where `StatusArgv` for pathspec `a*` lists only `a*` (proves `--literal-pathspecs` from Task 2).
  - `TestOutsidePathspecDropped`.
  - `TestClassifyTable` (every rule above, including `id_ed25519.pub` → Owned/Other, not Sensitive).
  - `TestAgeIdentityByContentWhateverName`, `TestAgeIdentityPastFirst4KiB` (marker at offset 5000, and in a 2 MiB file, straddling a read-buffer boundary).
  - `TestVaultUnselectedByDefault`.
  - `TestStateDirNeverSelectable`.
  - `TestRenameAcrossHomeBoundaryNoted`.
  - `TestMaintainerLayoutDefaults` (fixture with `mcp/x.toml`, `kiro-agents-src/a`, `setup.sh`: only `mcp/x.toml` is Default).
  - `FuzzParseStatus` (60 s, no panic, every returned `Path` passes the locality check).
- [ ] **Step 2: Run** `go test ./internal/gitrepo/ -run 'Parse|Classify|Display|Glob|Outside|Vault|StateDir|Rename|Maintainer' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** with `-race`, plus `go test -fuzz FuzzParseStatus -fuzztime 60s ./internal/gitrepo/`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(gitrepo): parse porcelain v2 status and classify home files`.

### Task 7: Secret scan of added lines and diff masking

**Files:** Create `internal/gitrepo/scan.go`, `diffmask.go`, `scan_test.go`, `diffmask_test.go`.

**Interfaces:**
- Consumes: `mask.Credential`, `mask.TokenShaped`, `mask.IsRef`, `mask.Text`, `mask.Value`, `mask.Sanitize`.
- Produces:
  - `func DiffArgv(r Repo, path string, unified int) []string` → `-C top diff --no-ext-diff --no-textconv --no-color -U<n> HEAD -- <path>` (when Unborn, the empty tree of the repository's object format replaces `HEAD`: `4b825dc642cb6eb9a060e54bf8d69288fbee4904` for sha1, `6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321` for sha256. `Detect` reads the format once with `git -C top rev-parse --show-object-format`, into `Repo.ObjectFormat`; on git older than 2.29, which has no sha256 repositories and rejects that option, it is sha1. A sha1 id in a sha256 repository is "unknown revision".)
  - `type Line struct{ N int; Text string }` and `func AddedLines(patch string) []Line`, which parses `@@ -a,b +c,d @@` and counts `+` lines. `+++ `/`--- ` are file headers only **outside** a hunk (before the first `@@` of a file, or after a hunk's old and new line counts are used up); inside a hunk every `+` line is an added line, so a file line `++ ghp_…` (diff line `+++ ghp_…`) is scanned.
  - `type Finding struct{ Path string; Line int; Shown string; Hard bool; Kind string }`.
  - `func ScanLines(homeRel string, lines []Line) []Finding`. *Amended after the PR #4 bot review:* `func ScanAdded(homeRel string, lines []Line, post string) []Finding` is ScanLines with the file's post-image as context: with `-U0` an edit of only the body of an existing TOML multi-line string (`'''`/`"""`) sends only the body lines, so an added line inside a multi-line value opened by a secret-named or header key is also classified as that key's value (hard in a config file, soft in prose; a header literal hard everywhere; references and blank lines are not findings), shown fully masked. TOML only; JSON has no multi-line strings and YAML block scalars are not followed. The commit guard passes the worktree file (`ReadRegular`, first 1 MiB; a cut file adds a soft "not scanned: over 1 MiB"); the push guard passes each section's file as its commit records it, `git -C <top> cat-file blob <sha>:<path>` (raw blob, no textconv; through the runner and its cap; a gitlink section is skipped; over 2000 reads refuse as "too many files"). *Round 2:* the push guard streams: each section's blob is read, scanned against and dropped (a copy of its first 1 MiB) before the next, so one post-image is held at a time; the whole outgoing scan has one deadline, `outgoingScanBound` = `ui.DefaultReadTimeout`, past which the push is refused ("the outgoing scan did not finish"); 2000 caps distinct paths ("too many files") and a separate 20000 caps file changes, i.e. blob reads ("too many changes to scan"). A cut post-image no longer adds "not scanned: over 1 MiB": the added lines are always scanned, and only an added line past the cut gets the soft "multi-line context not checked beyond 1 MiB". In a `"""` string a delimiter preceded by an odd run of backslashes is escaped. `BackupAuthor` follows a symlinked agentsync.toml to a regular file inside the resolved home.
  - `func ScanNewFile(homeRel string, content []byte) []Finding`: binary (NUL in the first 8 KiB) or > 1 MiB → one soft finding `"not scanned: binary"`/`"not scanned: over 1 MiB"`.
  - `func IsConfigFile(homeRel string) bool` (`agentsync.toml`, `mcp/**`, `hooks/*.toml`, `lsp/*.toml`).
  - `func MaskPatchLine(homeRel, line string) string`.
- Rules (pinned):
  - Hard/soft per R5. `Shown` = `mask.Text` of the line, sanitised, cut to 120 columns. It never contains the raw value.
  - `MaskPatchLine`: diff header lines (`diff `, `index `, `--- `, `+++ `, `@@`) → `mask.Text(mask.Sanitize(line))`, so a token in a tracked file name is masked. `MaskPatchLine` is per line and cannot tell a header from an added line `++ …` inside a hunk; masking headers with `mask.Text` covers that case too. Content lines keep their first byte (`+`/`-`/` `). For `IsConfigFile` paths, a `key = value` line whose key is not in `{type, command, url, enabled, description}` → `key = ` + `mask.Value(value)`, so references stay visible. Everything else → `mask.Text`.

- [ ] **Step 1: Write failing tests:**
  - `TestAddedLinesNumbers` (two hunks, line numbers right).
  - `TestAddedLineStartingPlusPlusInsideHunk` (an added `++ ghp_…` line is reported; the `+++ b/x` header is not).
  - `TestHardEverywhereKinds` (one per R5 hard kind, in `skills/x/SKILL.md`).
  - `TestConfigLiteralSecretHard` (`mcp/a.toml`: `API_KEY = 'abc123def456ghi789'` → hard; `API_KEY = '${secret:a.api_key}'` → no finding).
  - `TestBearerRefNotAFinding`.
  - `TestProseTokenShapedIsSoft` (a sha256 digest in a skill).
  - `TestProsePasswordWordIsSoft`.
  - `TestUUIDUnderSecretKeyIsHard` (`API_KEY = '<uuid>'` in `mcp/a.toml`) and `TestUUIDElsewhereNotAFinding` (`project_id = '<uuid>'`).
  - `TestImageDigestInArgsNotAFinding` (`args = ['run','-i','--rm','mcp/brave-search@sha256:<64 hex>']` in `mcp/brave.toml` → no finding; the discover plan's Docker installs depend on this).
  - `TestCommitShaInArgsNotAFinding` (`go install x@<40 hex>`).
  - `TestTokenShapedInArgsIsSoft`.
  - `TestTokenShapedEnvValueIsHard` (`[server.env] TOKEN = '<token-shaped literal>'`).
  - `TestBinaryAndHugeNewFilesSoft`.
  - `TestFindingShownIsMasked` (the raw token never appears in `Shown`).
  - `TestMaskPatchLineConfigValues` (`command = 'npx'` visible; `X-Api-Key = 'literal'` → `••••`; `${env:T}` visible).
  - `TestMaskPatchLineStripsEscapes`.
  - `TestMaskPatchLineHeaderTokens` (`diff --git a/skills/ghp_<20>/x b/…` and an added line `+++ ghp_<20>`: the token never survives).
  - `TestMaskPatchLineMaintainerSample`: the shell-style `${VAR}` and `Bearer ${VAR}` lines from the maintainer's mcp files stay visible.
  - `FuzzMaskPatchLine` (output never contains ESC; a planted `ghp_` token never survives).
- [ ] **Step 2: Run** `go test ./internal/gitrepo/ -run 'Added|Hard|Config|Bearer|Prose|UUID|Binary|Finding|MaskPatch' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** with `-race` plus a 60 s fuzz. Expected: PASS. Mutation checks: make a TokenShaped env value soft, `TestTokenShapedEnvValueIsHard` must fail; drop the digest exemption, `TestImageDigestInArgsNotAFinding` must fail.
- [ ] **Step 5: Commit** `feat(gitrepo): scan added lines for credentials and mask patch lines`.

### Task 8: The screen, local part: status, diff, select, commit

**Files:** Create `internal/ui/sourcerepo/sourcerepo.go`, `commit.go`, `view.go`, `sourcerepo_test.go`, `commit_test.go`.

**Interfaces:**
- Consumes: Tasks 3-7; `ui.Deps` (`Git`, `Chezmoi`, `Runner`), `ui.Rounds`, `ui.List`, `ui.Scroll`, `ui.Field`, `ui.DoChain`, `ui.Do`, `ui.ReadNote`.
- Produces: `func New(d ui.Deps) *Screen` (title `"Source repo"`), implementing `ui.Screen`, `ui.KeyCapturer` (while a field is focused), `ui.ScopeSetter` (re-detect for the new home; drop everything loaded) and `ui.Shutdowner`.
- Views and keys:
  - **List** (`j/k`, `space` toggle, `a` toggle all selectable, `enter`/`d` diff, `c` commit, `r` refresh). The header shows the manager, `repo: <top>`, `home: <pathspec>`, the branch, `↑A ↓B` (or "not fetched"), and `upstream: <name>` (or "no upstream"). A row shows the kind, `DisplayName`, the class badge and the note.
  - **Diff** (scroll, `esc`): untracked files show their content as all-added lines (read capped at `ui.MaxOutput`, refused through a symlink), every line through `MaskPatchLine`.
  - **Commit**: a message `Field`. `enter` runs the guards in a background round:
    1. Re-read the status: every selected path must still be listed, else "changed since listed: refreshed".
    2. `agentsync check` through `d.Runner` with the local bound: a non-zero exit is refused with `ReadNote`.
    3. `InspectGitDir`: lock or in-progress state is refused.
    4. Detached HEAD is refused.
    5. `Repo.Refusal` is refused.
    6. Scan: tracked files via `DiffArgv(-U0)` + `AddedLines` + `ScanAdded` (the worktree file as context); untracked files via `ScanNewFile`. A tracked patch that holds `Binary files … differ` (binary content, or a `-diff` attribute) gets one soft finding `"not scanned: binary"`, and a patch cut by the runner (stdout ends with `runner.TruncatedMark`) gets one soft finding `"not scanned: patch over 4 MiB"`, so neither can be committed without the typed confirmation (R5: content that was not scanned is soft).
    - Any hard finding → refuse, listing the findings and `git -C <top> commit …` as the outside path.
    - Soft findings only → the commit `Action` has `TypedConfirm` and a `Warning` like `"N possible secrets listed on the Source repo tab; type commit to proceed"`.
    - Then `DoChain(scope, GitAdd, GitCommit)`. The selection includes both sides of a rename when both are inside the home.
  - After the chain's `RunMsg` (matched by `Action.Key()`): success → note `committed: <subject>`. A failed commit after a successful add → the staged note from R4.
  - **Chezmoi** manager → a read-only notice listing `chezmoi re-add`, `chezmoi git -- status`, `chezmoi git -- add -- <path>`, `chezmoi git -- commit -m <msg>`, `chezmoi git -- push`, `chezmoi update`. Every git key is disabled with "managed by chezmoi".
  - **None** → "not a git repository" plus `git -C <home> init` / `git -C <home> remote add origin <url>` as text. The TUI does not run them.
  - `Refusal` → shown in red; every write key is disabled.
  - A warning row when any `StateDir` file is listed: "`.state/` is not ignored: add `/.state/` to .gitignore".
- Rules: reads use `ui.Rounds` and an unexported message type. Diff and file bytes never reach a `tea.Msg` other than the screen's own diff message, which carries already-masked lines. The screen never calls `ui.Debug*` with git output.

- [ ] **Step 1: Write failing tests** (teatest + gittest real repos + runnertest for agentsync):
  - `TestListsChangedFilesWithDefaults`.
  - `TestNoNetworkOnRefreshOrFocus`: the git fake records no `fetch`/`pull`/`push`.
  - `TestDiffViewMasked`: a planted literal in `mcp/x.toml` shows `••••`, and output has no ESC.
  - `TestUnusualNameShownQuotedNotSelectable` (a name with a newline).
  - `TestCommitEmitsChainWithExplicitPaths`: a `DoMsg` whose argv equals `GitAdd`/`GitCommit` of the selected paths. `-rf` is a file there, and it follows `--`.
  - `TestCommitRefusedWhenCheckFails`.
  - `TestCommitRefusedOnHardFinding`: `mcp add` with `--env API_KEY=ghp_…` written by hand. Nothing is emitted and `git log` count is unchanged.
  - `TestSoftFindingNeedsTypedCommit`.
  - `TestTrackedBinaryChangeNeedsTypedCommit` and `TestTruncatedPatchNeedsTypedCommit` (a fake git returns a patch ending in `runner.TruncatedMark`).
  - `TestCommitRefusedWhenIndexLockPresent` (the lock is still there afterwards).
  - `TestCommitRefusedDuringMerge`.
  - `TestCommitRefusedDetached`.
  - `TestCommitRefusedForBackupRepo`.
  - `TestStateFileNeverInArgv`.
  - `TestSelectionIsFilesNeverDirectories`: every path handed to `GitAdd`/`GitCommit` is a file record from `ParseStatus`, never a directory, so a pathspec cannot sweep in a tracked `skills/.state/…` descendant (the builders cannot tell a directory from a file, and `--literal-pathspecs` disables `:(exclude)`).
  - `TestChangedSinceListedRefreshes`.
  - `TestHomeBelowRootPathspecOnly` (a dirty file outside the home in the same repo never appears and never reaches argv).
  - `TestChezmoiNoticeAndNoGitKeys`.
  - `TestNotARepoShowsCommandsOnly`.
  - `TestScopeSwitchRedetects` (user home repo vs project subdir).
  - `TestStaleResultIgnored`.
  - `TestActivityLogHasNoDiffContent`: after a commit, the log file contains the masked command line and no line of file content.
  - `TestCommitAfterAddFailureShowsStagedNote`.
- [ ] **Step 2: Run** `go test ./internal/ui/sourcerepo/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race -count=3 ./internal/ui/sourcerepo/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): Source repo tab with masked status, diff and guarded commit`.

### Task 9: The screen, network part: fetch, pull, push, bootstrap

**Files:** Create `internal/ui/sourcerepo/remote.go`, `remote_test.go`, `bootstrap_test.go`; Modify `sourcerepo.go`/`view.go`.

**Interfaces:**
- Consumes: Task 3 `GitFetch/GitPull/GitPush/InitFromURL`, Task 4 `GotoMsg`, Task 7 scan.
- Produces (keys):
  - `f` fetch: needs an upstream; `remote` comes from `git -C top for-each-ref --format=%(upstream:remotename) refs/heads/<head>`, read once per refresh.
  - `p` pull: `AtRoot` only, upstream set, not detached, no lock or in-progress state.
  - `P` push, under the same preconditions plus the outgoing guard. It emits `GitPush(GitRepo{Top}, remote, Branch.Head, sha)` with the same `remote` as `f`, where `sha` is `rev-parse --verify HEAD^{commit}` read in the guard round (Ruling 32). The explicit remote overrides `branch.<b>.pushRemote` and `remote.pushDefault`, and the explicit sha pins the content, so the push sends exactly the commits `<upstream sha>..<sha>` the guard scanned. Refused when the upstream's branch (`%(upstream:remoteref)`) is not `refs/heads/<head>` (the explicit refspec would otherwise create a new remote branch; this is the rule `push.default=simple` applied) and when `git -C top config --get --type=bool remote.<remote>.mirror` prints `true` ("the remote is configured as a mirror; push outside the TUI").
  - `D`/`A` after a successful pull emit `GotoMsg{"Drift"}` / `GotoMsg{"Apply"}`.
  - `i` bootstrap (user scope, `Missing`).
- Rules (pinned):
  - **Push guard** (ranges as amended by Ruling 32: `<base>..<sha>` with both ends read by `rev-parse --verify` in the guard round; Task 9 also adds `--no-renames --diff-merges=first-parent --src-prefix=a/ --dst-prefix=b/`; *amended after the PR #4 bot review:* both reads also pass `--full-history`, so a side branch merged with `merge -s ours` (TREESAME to its first parent) is never simplified away from the path-limited log — git 2.43 already keeps it because `--diff-merges=first-parent` turns simplification off, but that is implicit; without either option it drops the side commits, verified): `git -C top log --format= --name-only -z @{u}..HEAD -- <pathspec>` (every path any outgoing commit touches, not only the net diff). A `.state/` or Sensitive path is refused. Each outgoing commit's own patch is then scanned (`git -C top log -p --no-ext-diff --no-textconv --no-color -U0 --format=%H @{u}..HEAD -- <pathspec>`, split per commit, through `AddedLines` and `ScanAdded` with the file at that commit as context), so a credential added in one commit and removed in a later one is still found: the push would publish it in history. A binary or truncated patch is soft, as in Task 8. Hard → refuse with findings and `git -C <top> log -p @{u}..HEAD` to inspect. Soft → `TypedConfirm`. More than 2000 files → **refused** "too many files to scan; review and push outside the TUI" (an unscanned push cannot be taken back, and unlike one large file the user cannot review 2000 files on this screen).
  - Behind > 0 with `ABKnown` → refuse "behind N: pull first".
  - No upstream → refuse with text:
    ```
    No upstream. Create a PRIVATE repository, then run outside the TUI:
      git -C <top> remote add origin <url>
      git -C <top> push -u origin <branch>
    With the GitHub CLI: gh repo create <name> --private --source <top> --push
    ```
  - **Public-host warning:** `git -C top remote get-url --push --all <remote>` → one host per line (the push URLs git will use: `pushurl` entries when set, else `url` with `pushInsteadOf` applied; plain `get-url` prints the fetch URL). Every listed host is checked. If it is in `{github.com, gitlab.com, bitbucket.org, codeberg.org, gitea.com, git.sr.ht}`, push `Warning` = `"pushes to <host>: make sure the repository is private; the TUI cannot see its visibility"`. A URL with https userinfo adds `"the remote URL embeds a credential"`. The URL is shown only through `mask.Text`.
  - **Pull, non-fast-forward** (exit ≠ 0 and stderr contains `Not possible to fast-forward` or `diverging`): note "local and remote have diverged; nothing was changed. Resolve outside the TUI:" followed by `git -C <top> pull --rebase` or `git -C <top> merge @{u}`, and `then press r`.
  - **Pull success:** note `"pulled: agents see these changes only after an apply. D = Drift, A = Apply"`. The shell's `finish` already marks other tabs stale.
  - **Push rejected** (`non-fast-forward`/`fetch first`) → "remote has new commits: fetch (f) and pull (p) first".
  - **`BelowRoot` layout:** `p`/`P` show "the repository at <top> holds more than the agentsync home; pull and push it outside the TUI" plus the two commands.
  - **Bootstrap view:** "No agentsync home at <home>." `i` focuses a URL `Field`. `enter` → `InitFromURL` (a validation error shows in the note, nothing emitted) → `Do(act, scope)`. After success: refresh (Detect now sees the cloned repo), and the note suggests `agentsync check` on the Health tab. In project scope, `i` is disabled with "init from a URL is user scope only".

- Review additions (PR A bot review, 2026-11-07):
  - **Local-branch upstream:** when `%(upstream:remotename)` is `.` (the branch tracks a local branch), `f`/`p`/`P` refuse with "the upstream is a local branch; fetch, pull and push it outside the TUI" instead of surfacing `ValidRemoteName`'s generic error. Test: `TestLocalBranchUpstreamRefusedWithReason`.
  - **Show where the push goes:** the push confirm modal always shows the masked push URL(s) from `git -C top remote get-url --push --all <remote>` (this applies `pushurl` and `pushInsteadOf`; several URLs are all listed), not only when the host is public. Test: `TestPushModalShowsEveryPushURLMasked`.
  - **Signed pushes:** add `--no-signed` to `GitPush` only if a user reports a pinentry prompt (git refuses cleanly when the server does not support signed pushes); not done in PR A.
  - **Branch names with `+`:** `ValidBranchName` refuses `+` anywhere as extra caution (the refspec always starts with `refs/`, so it could not force). If a user hits it on a legitimate name (`deps+bump`, `c++`), relax it to a leading `+` only, with a test.
- [ ] **Step 1: Write failing tests** (gittest bare remote; real git through `runner.Program` with `gittest.Env`):
  - `TestFetchUpdatesAheadBehind` (another clone pushes; `f`; header shows `↓1`).
  - `TestFetchIsReadOnlyNoModal`.
  - `TestPullFastForwardsAndOffersApply` (`A` emits `GotoMsg{"Apply"}`).
  - `TestPullDivergedExplainsAndChangesNothing` (HEAD unchanged).
  - `TestPushRoundTripToBare`.
  - `TestPushNeverForce` (argv check: the argv equals `GitPush(…, remote, head, sha)`).
  - `TestPushSendsOnlyTheScannedCommit` (a commit made after the DoMsg, before the push runs, never reaches the remote; Ruling 32).
  - `TestPushRefusedMirrorRemote`, `TestPushRefusedWhenUpstreamNameDiffers` (local `main` tracking `origin/master`).
  - `TestPushRefusedNoUpstreamShowsGuidance`.
  - `TestPushRefusedWhenBehind`.
  - `TestPushRefusedOutgoingHardFinding` (a commit made outside the TUI with `ghp_…` in `mcp/x.toml`).
  - `TestPushRefusedTokenAddedThenRemoved` (one outgoing commit adds `ghp_…`, the next removes it; the net diff is clean).
  - `TestPushRefusedTooManyFiles` (over 2000 outgoing paths; nothing emitted).
  - `TestPushWarnsPublicHost` (fetch `url` a local bare repo, `pushurl` `https://github.com/o/r.git`, read with `get-url --push --all`).
  - `TestPushStderrTokenMasked`.
  - `TestPullPushRefusedBelowRoot`.
  - `TestPullStoppedSaysWorktreeMayBePartlyUpdated` (a pull that times out gets the R3 note and `git -C <top> status`, never "nothing was changed").
  - `TestHangingRemoteTimesOut`: the remote is a local `http://127.0.0.1:<port>` listener that accepts and never answers (set via `pushurl` in the test repo's config, not through a builder), with `Timeouts.Read=200ms`. The status ends with `timed out` and no child is left (`runner` live set empty).
  - `TestNoTTYPromptOnSSH`: `GIT_SSH_COMMAND` unset and an `ssh://` remote to an unknown host. The push fails within the bound with "Host key verification failed" or similar, never stopped.
  - Bootstrap: `TestBootstrapShownWhenHomeMissing`, `TestBootstrapRejectsBadURLs` (table from Task 3, nothing emitted), `TestBootstrapEmitsInitAction`, `TestBootstrapDisabledInProjectScope`.
- [ ] **Step 2: Run** `go test ./internal/ui/sourcerepo/... -run 'Fetch|Pull|Push|Hanging|TTY|Bootstrap' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race -count=3 ./internal/ui/sourcerepo/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): fetch, fast-forward pull, guarded push and bootstrap on the Source repo tab`.

### Task 10: Registration, wiring, docs, end-to-end

**Files:** Modify `internal/app/screens.go`, `internal/app/screens_test.go`, `main.go`, `main_test.go`, `README.md`, `docs/superpowers/plans/2026-11-03-agentsync-tui.md` (a Global Constraints note pointing here); Create `test/e2e/sourcerepo_test.go`.

**Interfaces:**
- Consumes everything above.
- Produces:
  - `sourcerepo.New(d)` appended **last** in `Screens`. That makes it tab 18 if the discover plan's two tabs have landed, else tab 16. The test asserts the last title is `"Source repo"` and the count is the previous count + 1. Tab 18 is reachable with `shift+tab` from tab 1.
  - `main` sets `deps.Git = runner.NewGit("git")` always (a missing binary is reported by the tab) and `deps.Chezmoi = runner.NewChezmoi(path)` only when `exec.LookPath("chezmoi")` succeeds.

- [ ] **Step 1: Write failing tests:**
  - `TestScreensOrderAndTitles` (last is `"Source repo"`).
  - `TestEveryScreenImplementsScopeSetter`.
  - `TestMainWiresGitAndOptionalChezmoi` (with PATH pointed at a temp dir with or without a fake `chezmoi`).
  - e2e (`test/e2e`, real agentsync + real git, `testenv` + `gittest.Env` merged; skip without either):
    - `TestSourceRepoCommitPushCloneRoundTrip`:
      1. `env.Init`, then `git init -b main` in `AgentsyncHome`.
      2. Commit `agentsync.toml` + `.gitignore`, add a bare `origin`, `push -u`.
      3. `agentsync mcp add e2e-srv --command echo`.
      4. Open the TUI and `shift+tab` to the last tab. Wait for `mcp/e2e-srv.toml`.
      5. `c`, type `feat: add e2e-srv`, `enter`. Wait for a modal showing `git -C` twice. `y`. Wait for `committed`.
      6. `P`, `y`, wait for `done`.
      7. `git clone <bare> <tmp>/other` and assert that `other/mcp/e2e-srv.toml` contains `command = 'echo'`, that `other/.state` does not exist, and that the activity log has two `git -C … ` entries and no TOML content.
    - `TestSourceRepoRefusesLiteralTokenEndToEnd`: `mcp add leak --command echo --env API_KEY=ghp_<36 chars>`, then commit is refused, `git rev-list --count HEAD` is unchanged, and the screen shows `mcp/leak.toml:` with `••••`.
    - `TestSourceRepoSymlinkedHome`: `AGENTSYNC_HOME` is a symlink to a repo dir; commit works and the modal shows the resolved `-C` path.
- [ ] **Step 2: Run** `go test . ./internal/app/... ./test/e2e/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement** registration and wiring. README:
  - Screens table row `18 | Source repo | git status of the home, masked diff, commit, fetch, pull, push | space select, a all, enter diff, c commit, f fetch, p pull, P push, i clone a home (when missing)`.
  - Navigation: "Tabs 10 and up are reached with `tab` / `shift+tab`".
  - Safety model: a third exception paragraph. The TUI runs `git` (only `add`, `commit`, `fetch`, `pull --ff-only`, `push`, plus read-only `status`, `diff`, `rev-parse`, `log`, `for-each-ref`, `remote get-url`, `config --get`) on the home's repository, with the exact command shown and confirmed. A commit and a pull change files and history inside the home. Never `--force`, `reset`, `clean`, `rebase`, `--no-verify`. Your hooks and signing run as configured, with no terminal (a prompting hook, ssh or pinentry fails instead of hanging). Git commands are bounded (R3). The secret scan is heuristic (R5), and a hard refusal can be bypassed only by committing outside the TUI. agentsync's own destination history is never touched. chezmoi-managed homes are left to chezmoi.
  - Reword the "writes no agentsync files" sentence to name all exceptions.
  - Known limits: the scan covers added lines only, push/pull need the repo to be the home itself (R7), and the scan-to-commit window is like `E`'s check-then-use. The window runs to the commit itself, not to the add: `git commit -- <paths>` records the worktree contents of those paths, not what `add` staged (verified with git 2.43), so a same-user process that rewrites a selected file after the scan changes what is committed.
- [ ] **Step 4: Run** the full gate three times. Locally (never in CI), run once against a **clone** of a real home layout: `git clone --no-hardlinks ~/git/agent-config "$CLAUDE_JOB_DIR/tmp/ac"` then `git -C "$CLAUDE_JOB_DIR/tmp/ac" remote remove origin`, and `AGENTSYNC_HOME="$CLAUDE_JOB_DIR/tmp/ac" agentsync-tui`. Use `git clone`, **never `cp -a`**: the real home's ignored `.state/` holds pre-apply backups of agent configs that contain a live API key, and a copy would spread it into a temp directory. Delete the clone afterwards. Check the default selection excludes `kiro-agents-src/`, `steering/`, `setup.sh`. Record the result in the PR. Do not run it against the real `~/.agentsync`.
- [ ] **Step 5: Commit** `feat: register the Source repo tab; end-to-end coverage and docs`. **End of PR B.**

---

## Self-review

- **Spec coverage:** D1 → Tasks 2-4; D2 → 5-8; D3 → 3, 6, 8; D4 → 5, 7, 8; D5 → 4, 9; D6 → 3, 9; D7 → 3, 9; D8 → 5, 8; D9 → 1, 5, e2e in 10; D10 → 3, 9; D11 → 3 (`TestGitBuildersNeverEmitForbidden`); tests with temp repos, a bare remote and the real-agentsync e2e → gittest (5), Tasks 8-10. The brief's "first-run path" did not exist, so D10 places it on this tab (stated in Facts).
- **Brief deviations, each argued:** commit flag `--message=` instead of `-m` (one argv element, no ambiguity); push names one explicit non-forced refspec plus `--no-follow-tags --no-recurse-submodules` (a `matching` default, a forced `remote.<name>.push` refspec, `push.followTags` or `push.recurseSubmodules` would otherwise push other branches, tags or repositories; D6); pull adds `--no-rebase --no-autostash` (user config could otherwise rebase or stash); scan is split hard/soft (R5); push/pull refused below root (R7); chezmoi detection uses `chezmoi source-path` instead of the `managed` list (one call answers "is this path a target").
- **Type consistency:** `actions.GitRepo{Top}` is built from `gitrepo.Repo.Top` (Tasks 8, 9). `Action.Program`/`TypedConfirm` are defined in 3 and used in 4, 8, 9. `DoChain`, `GotoMsg`, `Deps.Git/Chezmoi` are defined in 4. `mask.Credential` is defined in 3 and used in 3 and 7. `Finding` is defined in 7 and used in 8 and 9. `Ended.Program` is defined in 2 and used in 4.
- **Review Focus mapping:** 1 → Tasks 3, 8; 2 → 6, 8; 3 → 3, 4, 7, 8, 9; 4 → 5, 6, 9; 5 → 2, 4, 5, 9; 6 → 2.
- **Controller review (2026-11-07) fixed:** R5 would have hard-refused every digest-pinned Docker image from the discover plan (exemptions and five tests added); Task 10's local check used `cp -a` on the real home, which would have copied a live API key from the ignored `.state/` backups into a temp dir (now `git clone`, tracked files only); and the two plans had no stated order or shared-file policy (new section above).
- **Proportion:** signatures, pinned rules, test names and assertions; the only algorithm text is the porcelain v2 field counts and the detection order, which the signatures do not determine.

## Not verified (carry into execution)

- **Where agentsync keeps the destination-backup git dir.** `mode = "on"` but there is no `~/.claude/.git` on the maintainer's machine. Before Task 5, read agentsync's source (search `destination_directory_git_backup`) and add a path rule to `Detect` if the git dir could ever be found from a canonical home. The author-email rule may be the only practical signal.
- **The age identity file location** (not printed by `agentsync secret --help`). The content rule in R6 does not depend on it.
- That `git pull --ff-only --no-rebase` overrides `pull.rebase=true` on git 2.25-2.33 (documented for newer git). Task 9 has the real-git test on CI's git; record CI's `git --version`.
- That `Setsid` children are still reached by `runner.Shutdown`'s group signalling on macOS (Linux is covered by Task 2's tests; CI is ubuntu only).
- **`TestNoTTYPromptOnSSH` depends on `ssh` being on the CI runner.** Skip it when it is not.
- That `chezmoi source-path <target>` exits non-zero for an unmanaged path on current chezmoi (no chezmoi locally; the tests use a fake). Verify once with a real chezmoi before release.
- Whether the discover plan's tabs land first (affects only the tab number in README; the test is count-relative).
