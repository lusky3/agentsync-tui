package ui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

const installHint = "agentsync not found on PATH; install: mise use -g go:github.com/spxrogers/agentsync/cmd/agentsync"

// versionCheckTimeout bounds the background `agentsync version` call.
const versionCheckTimeout = 10 * time.Second

type bannerKind int

const (
	bannerNone bannerKind = iota
	bannerError
	bannerWarn
	bannerInfo
	bannerDim
)

type banner struct {
	kind bannerKind
	text string
}

// versionMsg carries the result of the startup version check.
type versionMsg struct {
	v         string
	supported bool
	err       error
}

// checkVersionCmd runs runner.CheckVersion in the background.
func checkVersionCmd(r runner.Runner) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), versionCheckTimeout)
		defer cancel()
		v, ok, err := runner.CheckVersion(ctx, r)
		return versionMsg{v: v, supported: ok, err: err}
	}
}

// bannerFor maps a version check to the banner shown above the screens.
// Only a missing binary is an error; nothing here is fatal.
func bannerFor(m versionMsg) banner {
	switch {
	case errors.Is(m.err, runner.ErrNotInstalled):
		return banner{bannerError, installHint}
	case m.err != nil:
		return banner{bannerDim, "version check failed: " + firstLine(mask.Text(m.err.Error()))}
	case m.v == "dev":
		return banner{bannerInfo, "agentsync dev build (version unknown)"}
	case m.v == "":
		return banner{bannerDim, "agentsync version unknown"}
	case !m.supported:
		return banner{bannerWarn, "agentsync " + mask.Sanitize(m.v) + " (tested with " + runner.TestedVersion + ")"}
	}
	return banner{}
}
