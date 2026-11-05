// Package source reads the canonical MCP server definitions at
// <AGENTSYNC_HOME>/mcp/<id>.toml. It is strictly read-only: there is no write
// path here, all mutation goes through the agentsync CLI.
package source

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/lusky3/agentsync-tui/internal/ident"
)

// Server is one canonical MCP server definition.
type Server struct {
	ID      string
	Type    string // "stdio", "http" or "sse"
	Command string
	URL     string
	Args    []string
	// Env and Headers values are `${...}` references, never inline secrets.
	Env     map[string]string
	Headers map[string]string
	// Disabled is true when the [server] table has `enabled = false`
	// (written by `agentsync mcp disable <id>`, verified on agentsync
	// v0.17.0). The key is absent for enabled servers; `mcp add` writes
	// `agents = ['*']` but no `enabled` key.
	Disabled bool
	// Extra holds every [server] key not modelled above (e.g. agents). It is
	// never nil for servers returned by LoadServers.
	Extra map[string]any
}

type serverTOML struct {
	Server struct {
		Type    string            `toml:"type"`
		Command string            `toml:"command"`
		URL     string            `toml:"url"`
		Args    []string          `toml:"args"`
		Env     map[string]string `toml:"env"`
		Headers map[string]string `toml:"headers"`
		Enabled *bool             `toml:"enabled"`
	} `toml:"server"`
}

// modelled lists the [server] keys represented by Server fields.
var modelled = []string{"type", "command", "url", "args", "env", "headers", "enabled"}

// ServerPath returns the canonical file path for a server ID, rejecting IDs
// that are not safe to use in a path or argv.
func ServerPath(agentsyncHome, id string) (string, error) {
	if !ident.Valid(id) {
		return "", fmt.Errorf("invalid server id %q", id)
	}
	return filepath.Join(agentsyncHome, "mcp", id+".toml"), nil
}

// LoadServers reads every valid server definition, sorted by ID. Bad files
// are skipped and described (by base name only) in the returned joined error;
// the good servers are still returned. A missing mcp dir yields an empty slice.
func LoadServers(agentsyncHome string) ([]Server, error) {
	servers := []Server{}
	dir := filepath.Join(agentsyncHome, "mcp")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return servers, nil
	}
	if err != nil {
		return servers, err
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".toml") {
			continue
		}
		if !e.Type().IsRegular() {
			// Symlinks are not followed; a FIFO or device would block the
			// read (and the UI, which loads inside Update) forever.
			errs = append(errs, fmt.Errorf("%s: not a regular file", name))
			continue
		}
		id := strings.TrimSuffix(name, ".toml")
		if !ident.Valid(id) {
			errs = append(errs, fmt.Errorf("%s: invalid server id", name))
			continue
		}
		s, err := loadFile(filepath.Join(dir, name), id)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %s", name, tomlMsg(err)))
			continue
		}
		servers = append(servers, s)
	}
	slices.SortFunc(servers, func(a, b Server) int { return strings.Compare(a.ID, b.ID) })
	return servers, errors.Join(errs...)
}

// tomlMsg describes a parse error without echoing file contents (decode
// errors from go-toml can quote source lines, which may hold secrets).
func tomlMsg(err error) string {
	var de *toml.DecodeError
	if errors.As(err, &de) {
		line, col := de.Position()
		return fmt.Sprintf("invalid TOML at line %d, column %d", line, col)
	}
	var se *toml.StrictMissingError
	if errors.As(err, &se) {
		return "invalid TOML"
	}
	return "unreadable or invalid"
}

func loadFile(path, id string) (Server, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Server{}, err
	}
	var t serverTOML
	if err := toml.Unmarshal(b, &t); err != nil {
		return Server{}, err
	}
	var raw struct {
		Server map[string]any `toml:"server"`
	}
	if err := toml.Unmarshal(b, &raw); err != nil {
		return Server{}, err
	}
	extra := raw.Server
	if extra == nil {
		extra = map[string]any{}
	}
	for _, k := range modelled {
		delete(extra, k)
	}
	s := t.Server
	return Server{
		ID: id, Type: s.Type, Command: s.Command, URL: s.URL, Args: s.Args,
		Env: s.Env, Headers: s.Headers,
		Disabled: s.Enabled != nil && !*s.Enabled,
		Extra:    extra,
	}, nil
}
