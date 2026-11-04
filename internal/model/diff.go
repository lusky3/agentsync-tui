package model

import "fmt"

// Hunk is one differing key: Source is the rendered canonical content and
// Dest is what is on disk.
type Hunk struct {
	Path    string `json:"path"`
	Pointer string `json:"pointer"`
	Source  string `json:"source"`
	Dest    string `json:"dest"`
}

// ParseDiff parses `agentsync diff --json` stdout ({"hunks":[...]}). Hunks
// carry file content, so errors never echo input bytes.
func ParseDiff(stdout string) ([]Hunk, error) {
	var d struct {
		Hunks []Hunk `json:"hunks"`
	}
	if err := decode(stdout, &d, false); err != nil {
		return nil, fmt.Errorf("parse diff: %w", err)
	}
	return d.Hunks, nil
}

// Provenance is the metadata `agentsync explain <path>#<pointer> --json`
// prints (never content).
type Provenance struct {
	Path      string  `json:"path"`
	Pointer   string  `json:"pointer"`
	Scope     string  `json:"scope"`
	Unmanaged bool    `json:"unmanaged"`
	Owners    []Owner `json:"owners"`
}

// Owner groups explained items by agent.
type Owner struct {
	Agent string           `json:"agent"`
	Items []ProvenanceItem `json:"items"`
}

// ProvenanceItem says which canonical file produced a key and its drift class.
type ProvenanceItem struct {
	Pointer   string `json:"pointer"`
	Ownership string `json:"ownership"`
	Component string `json:"component"`
	Name      string `json:"name"`
	Source    struct {
		File     string `json:"file"`
		Multiple bool   `json:"multiple"`
	} `json:"source"`
	Drift   string   `json:"drift"`
	Secrets []string `json:"secrets"` // references only, e.g. ${env:NAME}
}

// ParseExplain parses `agentsync explain ... --json` stdout.
func ParseExplain(stdout string) (Provenance, error) {
	var p Provenance
	if err := decode(stdout, &p, true); err != nil {
		return Provenance{}, fmt.Errorf("parse explain: %w", err)
	}
	return p, nil
}
