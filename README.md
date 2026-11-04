# agentsync-tui

A terminal UI over the [agentsync](https://github.com/spxrogers/agentsync) CLI.
It lists MCP servers, drift, agents, plugins, secrets (names only), skills,
hooks and more, and drives every change by running real `agentsync` commands
that you confirm. It never edits agentsync's files itself.

## Install

```sh
mise use -g github:lusky3/agentsync-tui
# or
go install github.com/lusky3/agentsync-tui@latest
```

Release binaries (linux and darwin, amd64 and arm64) are on the
[releases page](https://github.com/lusky3/agentsync-tui/releases) as
`agentsync-tui_<version>_<os>_<arch>.tar.gz`.

You also need the `agentsync` CLI on your `PATH`.

**Supported agentsync version:** tested with v0.17.0. agentsync is pre-1.0 and
its text output is not a stable interface, so other versions may show parse
notices instead of data.

## Usage

```sh
agentsync-tui                    # user scope
agentsync-tui --project ./repo   # project scope
agentsync-tui --version
```

### Navigation

| Key | Action |
| --- | --- |
| `tab` / `shift+tab` | next / previous screen (wraps) |
| `1`-`9` | jump to the first nine screens; Skills and later (10-15) are reached with `tab` / `shift+tab` |
| `s` | toggle user / project scope |
| `l` | toggle the activity log |
| `?` | help |
| `q` | quit (`ctrl+c` cancels a running action) |

In lists, `up`/`down` or `k`/`j` move and `r` refreshes. Confirm dialogs take
`y`/`enter` to run and `n`/`esc` to cancel.

### Screens

| # | Screen | What it does | Keys |
| --- | --- | --- | --- |
| 1 | MCP servers | canonical servers and per-agent render state | `enter` detail, `e`/`x` enable-disable / remove, `a` add form, `E` edit in `$EDITOR` |
| 2 | Drift | `status` grouped by agent | `f` all / drift only, `enter` diff and explain, `g` legend |
| 3 | Apply | preview and apply, or revert to a checkpoint | `m` switch apply / revert, `p` preview, `a` apply; revert: `e` agent, `t` target, `A` all, `v` revert |
| 4 | Reconcile | read-only auto-safe report, then agentsync's own reconcile | `x` run `agentsync reconcile` interactively |
| 5 | Import | import existing agent config | `a`/`A` agent, `c`/`C` component, `n` name, `p` preview, `i` import |
| 6 | Agents | registered agents | `a` add, `e` enable / disable, `x` remove, `D` disable and purge |
| 7 | Plugins | plugins and marketplaces | `m` switch pane, `enter` detail, `o` outdated, `a` add, `e` enable / disable, `u` upgrade, `U` upgrade all, `x` remove |
| 8 | Secrets | secret key names only | `n` new key, `u` set value (agentsync prompts), `E` edit, `x` remove |
| 9 | Skills | canonical skills | `E`/`enter` edit, then `check` runs |
| 10-14 | Subagents, Commands, Hooks, LSP, Memory | same as Skills | `E`/`enter` edit, then `check` runs |
| 15 | Health | `doctor` and `check` results | `r` re-run |

Long views scroll with `ctrl+d`/`ctrl+u` or `pgdown`/`pgup`.

## Safety model

- Every mutation is an `agentsync` command shown to you (exact argv, secrets
  masked) and confirmed before it runs. There are no direct file writes.
- Destructive actions (remove, revert, secret removal) need a typed verb
  (the subcommand, for example `remove`). `agent disable --purge` needs the
  word `purge`, so it cannot be confirmed as a plain disable.
- Apply, revert and import show a preview first.
- Secret values are never displayed or logged. The secrets screen handles key
  names only; there is no `secret get`, and values are set through
  agentsync's own prompt.
- Masking is best-effort heuristics. It covers known secret-looking flags,
  env values, headers and token shapes, but a literal secret in a free-form
  argument or command line cannot always be recognized. Prefer `${env:NAME}`
  or `${secret:NAME}` references over literals.
- Terminal control bytes in output are stripped before rendering.

## Known limits

- In project scope some screens still read the user's agentsync home
  (`AGENTSYNC_HOME` or `~/.agentsync`), because agentsync keeps skills,
  secrets and similar sources there.
- Reconcile resolution is handed to agentsync's own interactive `reconcile` on
  purpose. In-UI write-back is not offered while upstream issues #239, #255
  and #256 are open.
- Tabs after the ninth have no digit shortcut.

## Development

```sh
mise run test    # go test ./...
mise run lint    # go vet + gofmt check
mise run build
```

Tests are hermetic: `HOME`, `AGENTSYNC_HOME` and `XDG_*` point at temp
directories, so nothing reads your real home. Tests that drive the real CLI
need `agentsync` (and some need `age-keygen`) on `PATH` and skip otherwise.
CI runs them against agentsync v0.17.0, including an end-to-end smoke test in
`test/e2e`.

## License

MIT, see [LICENSE](LICENSE).
