package library

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/actions"
)

// PathFor returns the canonical file for an item. The name must be a valid
// identifier (checked first) and the result is verified to stay under home.
// It never touches the filesystem, so symlinks are not followed here; the
// shell's EditFileMsg handling resolves them and refuses escapes.
func PathFor(home string, kind Kind, name string) (string, error) {
	if !actions.ValidID(name) {
		return "", errors.New("invalid item name")
	}
	if home == "" {
		return "", errors.New("no agentsync home")
	}
	var rel string
	switch kind {
	case Skill:
		rel = filepath.Join("skills", name, "SKILL.md")
	case Subagent:
		rel = filepath.Join("subagents", name+".md")
	case Command:
		rel = filepath.Join("commands", name+".md")
	case Hook:
		rel = filepath.Join("hooks", name+".toml")
	case LSP:
		rel = filepath.Join("lsp", name+".toml")
	case Memory:
		if name == memoryRoot {
			rel = filepath.Join("memory", "AGENTS.md")
		} else {
			rel = filepath.Join("memory", "fragments", name+".md")
		}
	default:
		return "", errors.New("unknown kind")
	}
	p := filepath.Join(home, rel)
	r, err := filepath.Rel(home, p)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", errors.New("path escapes the agentsync home")
	}
	return p, nil
}
