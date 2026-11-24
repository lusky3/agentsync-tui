package dockercat

import (
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestBudgetRefusesDuplicateAndMergeKeys checks withinBudget directly:
// yaml.v3's Decode compares every key pair of a mapping and formats an
// error per duplicate, so such entries must never reach it.
func TestBudgetRefusesDuplicateAndMergeKeys(t *testing.T) {
	for src, want := range map[string]bool{
		"a: 1\nb: 2\n":                 true,
		"a: 1\nb: {c: 1, d: 2}\n":      true,
		"a: 1\na: 2\n":                 false,
		"a: 1\nb: {c: 1, c: 2}\n":      false,
		"a: [{x: 1, x: 1}]\n":          false,
		"<<: {title: x}\n":             false,
		"a: {<<: [{b: 1}]}\n":          false,
		"!!merge m: {title: x}\n":      false,
		"? [k]\n: v\n":                 false,
		"? {k: 1}\n: v\n":              false,
		"'1': a\n1: b\n":               false,
		"title: '<<'\n":                true, // a merge-looking value is fine
		"a: {b: [1, 2, {c: 3}]}\n":     true,
		"a: 1\nb: 1\nc: {a: 1}\n":      true, // the same key in another mapping
		"x: !!merge notakey\n":         true, // only keys are refused
		"a: {d: 1, e: {d: 1}}\n":       true,
		"a: 1\n\"a\": 2\n":             false,
		"list: [a, a, a]\n":            true, // sequences may repeat
		"a: {b: 1}\nc: {b: 1, b: 1}\n": false,
	} {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if got := withinBudget(doc.Content[0]); got != want {
			t.Errorf("withinBudget(%q) = %v, want %v", src, got, want)
		}
	}
}
