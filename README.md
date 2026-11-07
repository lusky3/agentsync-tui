# agentsync-tui

A terminal UI over the [agentsync](https://github.com/spxrogers/agentsync) CLI.
It lists MCP servers, drift, agents, plugins, secrets (names only), skills,
hooks and more. Every change the TUI makes itself is a real `agentsync`
command that you see and confirm. There are two exceptions: edit (`E`), which
opens the file in your editor, and the Source repo tab, which runs `git` on
the repository that holds your agentsync home, again shown and confirmed (see
the safety model).

## Install

```sh
mise use -g github:lusky3/agentsync-tui
# or
go install github.com/lusky3/agentsync-tui@latest
```

Release binaries (linux and darwin, amd64 and arm64) are on the
[releases page](https://github.com/lusky3/agentsync-tui/releases) as
`agentsync-tui_<version>_<os>_<arch>.tar.gz`.

You also need the `agentsync` CLI on your `PATH`. The Source repo tab needs
`git` 2.25 or newer on `PATH` (it says so when git is missing), and pushing
from it needs git 2.31 or newer: the pre-push scan reads the outgoing commits
with `--diff-merges=first-parent`, which older git rejects, so the push is
refused rather than sent unscanned. `chezmoi` is optional: when it is on
`PATH`, the tab uses `chezmoi source-path` (read-only) to recognise a home
that chezmoi manages.

**Supported agentsync version:** tested with v0.17.0. agentsync is pre-1.0 and
its text output is not a stable interface, so other versions may show parse
notices instead of data.

## Usage

```sh
agentsync-tui                    # user scope
agentsync-tui --project ./repo   # project scope
agentsync-tui --version
agentsync-tui --write-timeout 10m     # bound writes too (default: no bound)
agentsync-tui --read-timeout 0       # no bound on network reads (default 2m)
agentsync-tui --debug-log ./tui-debug.log   # keep masked samples of unparseable output
```

`--read-timeout` bounds the network reads: import previews of plugins or a
whole agent, and the plugins screen's `plugin outdated`. Local reads (the
lists, status, diff, explain, doctor and check a screen or the header chip
runs, and the dry-run previews) are bounded at `min(30s, --read-timeout)`: the flag can lower that
bound but not lift it. `--write-timeout` bounds every write (apply, revert,
import, `plugin upgrade`, ...). Both take a Go duration such as `90s` or
`10m`; `0` means no bound (for the network reads or the writes) for
agentsync commands. Git commands are always bounded, because stopping git
never corrupts the repository (its ref updates are atomic and its index
writes use a lock file): `fetch`, `pull` and `push` use `--read-timeout`
and keep the 2m default when it is `0` (raise it for a very slow remote
instead of disabling it), and `add` and `commit` use `--write-timeout`, or
2m when it is `0`, which covers a slow commit hook. While a command runs,
the status line shows how long it has been running.

agentsync's text output is not a stable interface, so a screen may report
`<command> output unreadable` after an agentsync upgrade. `--debug-log
<file>` then appends, for every such read, the command name, exit code,
parse error and the first 4 KiB of stdout and stderr, masked like
everything else the TUI shows (the file is kept at mode 0600 and rotated at
1 MiB like the activity log). Attach it to a bug report. It is off by
default and never records raw output. Writes are unbounded by default because a write
cut off partway (for example `plugin upgrade --all`) can leave agentsync's
state and the destination files out of step.

The user-scope canonical tree is resolved as the CLI resolves it:
`$AGENTSYNC_TARGET_ROOT/.agentsync` if that redirect is set, else
`$AGENTSYNC_HOME`, else `~/.agentsync`. The project tree is always
`<project>/.agentsync`.

Screens load when they are first shown: startup runs the version check, the
header's `check` and `status --exit-code`, and the reads of the first tab
only. A screen whose data may be stale (never shown, shown under the other
scope, or shown before a write or an edit ran elsewhere) refreshes when it is
next shown; `r` refreshes it on demand. A refresh, a scope switch and quitting
cancel the reads they supersede. The Reconcile tab's `reconcile --auto-safe`
takes agentsync's write lock, so it runs only while that tab is on show.

Stopping a command sends SIGINT to its process group (agentsync's, so a helper
it spawned, such as git, is interrupted too; the TUI's own git and chezmoi
children lead a group of their own) and, if anything is still running
10 seconds later, SIGKILL. `ctrl+c` cancels a running read at once. A running write needs
`ctrl+c` twice within 3 seconds; `q` during a write quits once the write has
finished (or, after a `ctrl+c`, once it has stopped). agentsync v0.17.0 does
not handle SIGINT itself, so an interrupted write still stops where it is:
its single files are written atomically, but a multi-file run can be left
half done, and `check` or a new preview shows what changed. The activity log
records a stopped read as `cancelled` and a stopped write as `interrupted`.
If the TUI itself is ended from outside (SIGTERM, SIGHUP when the terminal
closes, or SIGINT while its input is not a terminal), a command still in
flight is logged as `abandoned`, its process group is sent the same signal
(SIGTERM, or SIGHUP as the terminal would have sent it), given 10 seconds
for a write and 2 for a read, and then killed; a quit does the same with
SIGINT for what is left of the cancelled reads. What became of each child
(`exited`, `killed`, or `orphaned` if even that failed) is logged. While the
children are being ended the TUI keeps catching `ctrl+c`, SIGTERM and
SIGHUP, and a second one kills every group at once. Nothing the TUI started
outlives it. Because agentsync runs in its
own process group behind the TUI, its children get `GIT_TERMINAL_PROMPT=0`
and `SSH_ASKPASS_REQUIRE=never` (and no `GIT_ASKPASS`), so a git clone that
would ask for credentials on the terminal fails with an error instead of
hanging the run. The git and chezmoi commands the Source repo tab runs go
further: each runs in its own session, with no controlling terminal, so ssh,
a credential helper, gpg's pinentry or a hook that wants to prompt fails at
once instead of stopping behind the TUI's screen. `fetch`, `pull` and `push`
also get `GIT_SSH_COMMAND="ssh -o BatchMode=yes"` unless you set
`GIT_SSH_COMMAND`, `GIT_SSH` or `core.sshCommand` yourself, in which case
yours is used. Variables that would point git at another repository
(`GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` and the like) are removed
from their environment, so a TUI started from a git hook still acts on the
home's repository, and `GIT_NO_REPLACE_OBJECTS=1` is set, so what the tab
shows and scans is the commits as stored: a `git replace` object would
change what `log` shows but not what a push sends.

### Navigation

| Key | Action |
| --- | --- |
| `tab` / `shift+tab` | next / previous screen (wraps) |
| `1`-`9` | jump to the first nine screens; tabs 10 and up are reached with `tab` / `shift+tab` (Source repo, the last tab, is one `shift+tab` from the first) |
| `s` | toggle user / project scope |
| `l` | toggle the activity log |
| `?` | help |
| `q` | quit (waits for a running write; `ctrl+c` cancels a running read, twice for a write) |

Every list and long view uses the same keys: `up`/`k` and `down`/`j` move
one row, `ctrl+u`/`pgup` and `ctrl+d`/`pgdown` a page (`space` also pages
down in the long views), `home`/`end` jump to the first or last row, and `r`
refreshes the screen. The cursor row and the
key hints at the bottom always stay visible. Confirm dialogs take
`y` to run and `n`/`esc` to cancel. `enter` does not confirm, so a double
`enter` on a form cannot run the command before you have read it. Destructive
actions ask you to type a word instead (see the safety model).

### Screens

| # | Screen | What it does | Keys |
| --- | --- | --- | --- |
| 1 | MCP servers | canonical servers and per-agent render state | `enter` detail, `e`/`x` enable-disable / remove, `a` add form, `E` edit in `$EDITOR` |
| 2 | Drift | `status` grouped by agent | `f` all / drift only, `enter` diff and explain, `g` legend |
| 3 | Apply | preview and apply, or revert to a checkpoint | `m` (or `[` / `]`) switch apply / revert, `e` edit the `--agents` filter, `p` preview, `a` apply; revert: `e` edit the agent, `t` target, `A` all, `v` revert |
| 4 | Reconcile | read-only auto-safe report, then agentsync's own reconcile | `x` run `agentsync reconcile` interactively |
| 5 | Import | import existing agent config | `a`/`A` agent, `c`/`C` component, `n` name, `p` preview, `i` import |
| 6 | Agents | registered agents | `a` add, `e` enable / disable, `x` remove, `D` disable and purge |
| 7 | Plugins | plugins and marketplaces | `m` switch pane, `enter` detail, `o` outdated, `a` add, `e` enable / disable, `u` upgrade, `U` upgrade all, `x` remove |
| 8 | Secrets | secret key names only | `n` new key, `u` set value (agentsync prompts), `E` edit, `x` remove |
| 9 | Skills | canonical skills | `E`/`enter` edit, then `check` runs |
| 10-14 | Subagents, Commands, Hooks, LSP, Memory | same as Skills | `E`/`enter` edit, then `check` runs |
| 15 | Health | `doctor` and `check` results | `r` re-run |
| 16 | Source repo | git status of the home, masked diff, commit, fetch, pull, push | `space` select, `a` all, `enter`/`d` diff, `c` commit, `f` fetch, `p` pull, `P` push, `i` clone a home (when missing), `r` refresh; after a pull, `D` Drift / `A` Apply |

Long views (command output, diffs, the plugin detail) scroll with the same keys as the lists.

## Safety model

- Every change the TUI itself makes goes through an `agentsync` command. The
  command line is shown (masked, best-effort) and you confirm it before it
  runs. Read-only commands (dry-run previews, `check`) run without a prompt,
  and screens read state directly with `list`, `status`, `diff`, `explain`,
  `doctor`, `check`, `version`, `status --legend`, `plugin outdated`,
  `reconcile --auto-safe` and similar read-only calls. Quitting, a refresh and
  a scope switch cancel any of these still in flight (the child gets SIGINT,
  then SIGKILL). The TUI writes no agentsync files directly. The exceptions,
  each described below: `E` hands a canonical file to your editor, which
  writes it; and on the Source repo tab a confirmed `git commit` records
  files of the home in its repository's history (and `git add` stages them
  in its index), and a confirmed `git pull` changes files and history inside
  the home.
- The one exception is `E` (edit) on MCP servers and on Skills, Subagents,
  Commands, Hooks, LSP and Memory: it opens the canonical file in `$VISUAL`,
  then `$EDITOR`, falling back to `vi`, immediately and with no confirm dialog.
  Your editor writes the file, not the TUI. If the editor exits successfully,
  the TUI then runs `agentsync check`. The path must resolve inside the
  current scope's agentsync home (the user tree, or `<project>/.agentsync` in
  project scope). (Secrets `E` runs `agentsync secret edit`, a confirmed
  command.)
- The second exception is the Source repo tab, which runs `git` (and,
  read-only, `chezmoi source-path`) on the repository that holds the home.
  Its only git writes are exactly these, each shown as its full command line
  and confirmed, except `fetch <remote>`, which updates what the remote's
  fetch refspec names (remote-tracking refs by default, plus tags) and runs
  without a dialog (logged): `add -- <paths>`
  and `commit --message=<msg> -- <paths>` (one confirmation for both; only
  the files you selected, never `add -A` or `commit -a`), `fetch <remote>`,
  `pull --ff-only --no-rebase --no-autostash`, and `push --no-follow-tags
  --no-recurse-submodules <remote> <scanned sha>^{commit}:refs/heads/<branch>`:
  the commit the pre-push scan read (peeled, so a ref named like the object
  id cannot stand in for it), to the current branch's name on its
  upstream's remote. A push is never forced and never sends another branch,
  a tag or a submodule, whatever the repository's push configuration says.
  Everything else it runs is read-only: `status`, `diff`, `rev-parse`,
  `log`, `for-each-ref`, `config --get`, `remote get-url` and `rev-list`. It
  never runs `--force`, `reset`, `clean`, `checkout`, `restore`, `stash`,
  `rebase`, `--no-verify`, `remote add`/`set-url` or a `config` write, and it
  never creates a remote or a hosted repository. A commit is refused when
  `agentsync check` fails, a selected file is under `.state/` or looks
  sensitive (a key, `.env`, an age identity or a private key in its
  content), the scan of the added lines
  finds a likely secret, HEAD is detached, a merge, rebase, cherry-pick,
  revert or bisect is in progress, or `index.lock` is present (the TUI never
  deletes it). A push is scanned the same way first, the messages of the
  outgoing commits included. When the remote pushes to a URL other than the
  one it fetches from (a `pushurl`, a `pushInsteadOf`, a second `url`), the
  fetched upstream does not tell what that URL lacks, so the branch's whole
  history is scanned instead. Your hooks and signing
  run as configured, with no terminal (see above). Git commands are always
  bounded (see Usage). agentsync's own destination-backup repositories are
  recognised (by commit author and the `agentsync.managed` marker) and
  refused, and a home that chezmoi manages gets a read-only notice
  with the chezmoi commands to use instead. Diff and file content are only
  rendered, masked (a diff that holds a private key or an age identity is
  not shown at all): never written to the activity log, the status line or
  `--debug-log`.
- Destructive actions (removing a server, agent, plugin, marketplace or
  secret; revert) need a typed verb (the subcommand, for example `remove`).
  `agent disable --purge` needs the word `purge`, so it cannot be confirmed as
  a plain disable.
- Apply, revert and import require a successful dry-run preview of the same
  command before the real run is offered. Any other write, and any `E` edit,
  drops the preview, so you preview again after it.
- The secrets screen handles key names only: it uses `secret list`, there is
  no `secret get`, and values are entered in agentsync's own prompt or in your
  editor via `secret edit`.
- Other output that can carry resolved values (diffs, `explain`, command
  output, logs) is masked on a best-effort basis. Each output stream is kept
  up to 4 MiB (then marked truncated) before it is masked, and a screen stores
  at most 64 KiB of the masked text. The masking is heuristic: it
  covers known secret-looking flags, env values, headers and token shapes, but
  a literal secret in a free-form argument or command line cannot always be
  recognized, and a rotated secret inside a diff cannot be masked (upstream
  #254). Prefer `${env:NAME}` or `${secret:NAME}` references over literals.
- The activity log (in memory, and a file at
  `$XDG_STATE_HOME/agentsync-tui/activity.log`, default `~/.local/state`)
  records only the masked command line, outcome and duration of each action
  the TUI runs, not command output or error text. The file and its directory
  are kept at mode 0600 and 0700 (a pre-existing looser mode is tightened,
  a symlink in their place is refused and the file log is disabled with a
  notice in the log pane), and the file is rotated at 1 MiB: `activity.log.1`
  to `.3` are the older files, the oldest is dropped.
- Text from outside (command output, errors, file names) has terminal control
  bytes stripped before it is rendered.
- A literal value typed into the add-server form's env or header fields is
  passed to `agentsync mcp add` on its command line (the CLI has no other
  input for them), where another local user can read it from
  `/proc/<pid>/cmdline` for the milliseconds the command runs, and agentsync
  then stores it in `mcp/<id>.toml`. The form says so: prefer
  `${env:NAME}` or `${secret:NAME}` references, which are passed as written.
- `E` resolves the file (symlinks included) and checks that it is a regular
  file inside the current scope's home in the same step that hands it to
  the editor, so the check-then-use window is the length of that step; a
  same-user writer who could swap a path component inside it could edit
  the file directly anyway. A home that is itself a symlink (a dotfiles
  checkout) is resolved first, so "inside the home" means inside the link's
  target. What the editor does with the file (vim modelines and the like)
  is the editor's own policy.

## Known limits

- Project scope (`--project <dir>` or the `s` key) uses `<dir>/.agentsync` as
  the canonical tree, the one `agentsync init --scope project` creates and
  every scope-aware command runs against with `--project <dir>`: MCP servers,
  the memory list, `E` edits and the secrets cross-check all read from there,
  and the confirm dialog shows which scope a command runs under. The secrets
  vault, plugins and marketplaces stay user-level in both scopes because the
  CLI refuses `--scope`/`--project` on those command groups (the dialog says
  `scope: n/a (user-level)` for them). The secrets screen therefore lists the
  same keys in both scopes, but its "used by" cross-check scans the servers of
  the current scope's tree. A project directory without `.agentsync` shows
  empty screens, not errors.
- Reconcile resolution is handed to agentsync's own interactive `reconcile` on
  purpose. In-UI write-back is not offered while upstream issues #239, #255
  and #256 are open.
- Tabs after the ninth have no digit shortcut.
- Source repo: the secret scan is heuristic. It reads only the lines a
  commit or push would add. A hard finding (a known token shape, a literal
  credential in an Authorization/Bearer header, URL userinfo, a private
  key, a literal value under a secret-named key in a config file) refuses
  the commit or push; a soft finding (a token-shaped word in prose, a file
  too large or binary to scan) asks you to type `commit` first. An all-caps
  `$NAME` is treated as a reference to a variable, so a literal that looks
  like one (`$PASSWORD123`) is not flagged. A hard refusal can be bypassed
  only by committing outside the TUI; the refusal shows the commands.
- Source repo: the scan reads a line inside a TOML multi-line string as
  the value of the key that opened it, but not a multi-line string that is
  an array element (`KEYS = ['''`): its lines are scanned on their own.
- Source repo: YAML block scalars (`key: |`) are not followed either; each
  of their lines is scanned on its own.
- Source repo: the scan and the commit are separate steps, like `E`'s
  check-then-use. The window runs to the commit itself, not to the add:
  `git commit -- <paths>` records what those paths hold in the worktree
  when it runs, so a same-user process that rewrites a selected file, or
  recreates a rename's old path, after the scan and before you confirm
  changes what is committed, unscanned. The push has no such gap: it sends
  the exact commit the scan read, so commits made after the scan (while the
  dialog was open) stay local, and the note after the push says how many.
- Source repo: pull and push are offered only when the repository is the
  home itself. When the home sits inside a larger repository (a dotfiles
  repo), status, diff, commit and fetch are limited to the home and pull
  and push are refused with the commands to run, because they would move
  commits and files the TUI does not own. A push is also refused for a
  mirror remote, an upstream whose branch name differs from the local
  branch, an upstream that is a local branch, a branch with no upstream
  (the commands to add one are shown), more than 2000 outgoing files, more
  than 20000 file changes across the outgoing commits, and a scan that does
  not finish within two minutes.
- Source repo: signed commits and pushes and pre-commit or pre-push hooks
  run with no terminal, so one that prompts (pinentry, a passphrase, an
  interactive hook) fails instead of hanging. Use an agent (gpg-agent,
  ssh-agent) or commit and push outside the TUI.
- Source repo: a home that chezmoi manages is left to chezmoi (read-only
  notice). agentsync's destination-backup repositories, recognised by their
  author and the `agentsync.managed` config marker, are never touched.
- Source repo: push sends to the remote you configured and warns when its
  host is a public hosting service, since the TUI cannot see whether the
  repository is private.

## Development

```sh
mise run test    # go test ./...
mise run lint    # go vet + gofmt check
mise run build
```

Tests are hermetic: `HOME`, `AGENTSYNC_HOME` and `XDG_*` point at temp
directories and every `AGENTSYNC_*` variable (including the CLI's own
`AGENTSYNC_TARGET_ROOT` redirect) plus `GROK_HOME` is scrubbed from the
agentsync children, so nothing reads your real home. agentsync looks for a
project tree (`.agentsync/`) above its working directory, so the temp root
the real-CLI tests use is made where no ancestor holds `.agentsync` (under
`$TMPDIR`, or `/tmp` when `$TMPDIR` lies below your real home) and the
children run from the fake home. Git tests use a temp global config,
`GIT_CONFIG_NOSYSTEM=1` and a scrubbed `GIT_*` environment, and skip when
`git` is missing. Tests that drive the real CLI
need `agentsync` (and some need `age-keygen`) on `PATH` and skip otherwise.
CI runs them against agentsync v0.17.0, including an end-to-end smoke test in
`test/e2e`.

## Releasing

Pushing a `v*` tag runs the tests, lint and govulncheck, then publishes a
release with GoReleaser: the archives, `checksums.txt`, one SPDX SBOM per
archive (`<archive>.sbom.json`) and a SLSA build-provenance attestation
signed through GitHub's Sigstore instance. A pre-release tag
(`v1.0.0-rc1`) publishes a GitHub pre-release.

Verify a downloaded archive against its provenance with:

```sh
gh attestation verify agentsync-tui_<version>_<os>_<arch>.tar.gz --repo lusky3/agentsync-tui
```

The release workflow runs on any `v*` tag on any commit, so maintainers
should add a repository tag ruleset that limits who may create `v*` tags
(and, if wanted, requires them to be signed and to point at commits on
`main`). The attestation records the workflow, commit and tag a binary was
built from; there is no separate signing key to manage. Dependabot keeps the
Go modules and the pinned Actions current.

## License

MIT, see [LICENSE](LICENSE).
