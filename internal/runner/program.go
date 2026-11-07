package runner

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// MissingError is returned by Program.Run when its binary is not installed.
type MissingError struct{ Name string }

func (e *MissingError) Error() string { return e.Name + " is not installed" }

// Program is a Runner for a helper CLI other than agentsync: git (D1) and,
// read-only, chezmoi. It shares Exec's cancel, grace, output cap and live
// tracking (runCore), but runs the child in its own session (no controlling
// terminal, R1) with a hardened environment, and puts fixed pre-args before
// the caller's argv. Build one with NewGit or NewChezmoi.
type Program struct {
	// Env, when non-nil, is the base of the child environment (default:
	// the current process environment); the hardening is applied on top.
	Env []string

	bin   string
	name  string
	pre   []string
	git   bool
	grace time.Duration // SIGINT-to-SIGKILL delay on cancel (InterruptGrace)
}

// gitHardening is the fixed argv every git run starts with: no pager, no
// colour, no fsmonitor daemon, and pathspecs taken literally (a file named
// `*` matches only itself).
var gitHardening = []string{"--no-pager", "-c", "core.pager=cat", "-c", "color.ui=never", "-c", "core.fsmonitor=false", "--literal-pathspecs"}

// gitScrubbed are the inherited variables that would make git act on
// another repository or with injected configuration (R2): run from a git
// hook, the TUI would otherwise inherit GIT_DIR.
var gitScrubbed = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
	"GIT_CEILING_DIRECTORIES", "GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS",
	"GIT_ICASE_PATHSPECS", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
}

// gitBatchSSH is the ssh command a network git run gets when the user has
// chosen none, so a host-key or passphrase prompt fails instead of waiting.
const gitBatchSSH = "ssh -o BatchMode=yes"

// sshProbeBound bounds the `config --get core.sshCommand` probe.
const sshProbeBound = 5 * time.Second

// NewGit returns a Program for the git binary bin.
func NewGit(bin string) *Program {
	return &Program{bin: bin, name: "git", pre: gitHardening, git: true, grace: InterruptGrace}
}

// NewChezmoi returns a Program for the chezmoi binary bin (used only for
// the read-only `chezmoi source-path`).
func NewChezmoi(bin string) *Program {
	return &Program{bin: bin, name: "chezmoi", grace: InterruptGrace}
}

// Name is the program's name: "git" or "chezmoi".
func (p *Program) Name() string { return p.name }

// Run executes the program with its pre-args followed by args. As with
// Exec, the error is non-nil only for a spawn failure (*MissingError when
// the binary is not installed) or ctx's end, never for a non-zero exit.
func (p *Program) Run(ctx context.Context, args ...string) (Result, error) {
	env := p.env(ctx)
	if p.git && wantsBatchSSH(args, env) && !p.repoSetsSSHCommand(ctx, args, env) {
		env = append(env, "GIT_SSH_COMMAND="+gitBatchSSH)
	}
	return p.run(ctx, env, args)
}

// run starts the program with env and args through runCore.
func (p *Program) run(ctx context.Context, env, args []string) (Result, error) {
	cmd := exec.CommandContext(ctx, p.bin, append(append([]string{}, p.pre...), args...)...)
	setSession(cmd)
	cmd.Env = env
	res, err := runCore(ctx, cmd, args, effectiveGrace(p.grace), p.name)
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return res, &MissingError{Name: p.name}
	}
	return res, err
}

// env is the child environment without the BatchMode decision: childEnv
// (no git or ssh prompts) with, for git, the R2 names scrubbed and the D1
// values set (GIT_OPTIONAL_LOCKS=0 only for a read), and for chezmoi
// LC_ALL=C and NO_COLOR=1. Git also gets GIT_NO_REPLACE_OBJECTS=1: a
// replacement object (git replace) changes what log, diff and cat-file
// show but not what a push sends, so the scans read the objects as
// stored.
func (p *Program) env(ctx context.Context) []string {
	set := []string{"LC_ALL=C", "NO_COLOR=1"}
	drop := []string{"LC_ALL", "NO_COLOR"}
	if p.git {
		set = []string{"LC_ALL=C", "GIT_PAGER=cat", "GIT_EDITOR=true", "GIT_MERGE_AUTOEDIT=no", "GIT_NO_REPLACE_OBJECTS=1"}
		if !IsWrite(ctx) {
			set = append(set, "GIT_OPTIONAL_LOCKS=0")
		}
		drop = append([]string{"LC_ALL", "GIT_PAGER", "GIT_EDITOR", "GIT_MERGE_AUTOEDIT", "GIT_NO_REPLACE_OBJECTS", "GIT_OPTIONAL_LOCKS"}, gitScrubbed...)
	}
	base := childEnv(p.Env)
	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !slices.Contains(drop, k) {
			out = append(out, kv)
		}
	}
	return append(out, set...)
}

// wantsBatchSSH reports whether args run fetch, pull or push and env names
// no ssh command of the user's (GIT_SSH_COMMAND or GIT_SSH).
func wantsBatchSSH(args, env []string) bool {
	switch gitSubcommand(args) {
	case "fetch", "pull", "push":
	default:
		return false
	}
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); (k == "GIT_SSH_COMMAND" || k == "GIT_SSH") && v != "" {
			return false
		}
	}
	return true
}

// repoSetsSSHCommand reports whether the command configures
// core.sshCommand: in the target repository, or through a `-c` of the
// caller's. The probe runs with the same leading -C and -c options as args,
// so it sees exactly the configuration the command will. Only exit 1 of
// `git config --get` means unset; anything else, including a probe that
// could not run, counts as set, so a command the user chose that could not
// be read is never overridden.
func (p *Program) repoSetsSSHCommand(ctx context.Context, args, env []string) bool {
	probe := append(globalOpts(args), "config", "--get", "core.sshCommand")
	pctx, cancel := context.WithTimeout(ctx, sshProbeBound)
	defer cancel()
	res, err := p.run(pctx, env, probe)
	return err != nil || res.ExitCode != 1
}

// gitSubcommand is the first argument of args that is not a global option
// (a -C or -c value is skipped with its option).
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-C" || a == "-c":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// globalOpts is the leading -C and -c option pairs of args, in order (the
// git builders put only these before the subcommand).
func globalOpts(args []string) []string {
	var out []string
	for i := 0; i+1 < len(args) && (args[i] == "-C" || args[i] == "-c"); i += 2 {
		out = append(out, args[i], args[i+1])
	}
	return out
}

var _ Runner = (*Program)(nil)
