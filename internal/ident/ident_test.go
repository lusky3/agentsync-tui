package ident

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	ok := []string{"a", "A1", "ctx.7_a-b", "0", strings.Repeat("x", MaxLen)}
	for _, s := range ok {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false", s)
		}
	}
	bad := []string{"", "-x", "--flag", ".hidden", "..", "a/b", "a b", "a\tb", "a\x1b[31m", "é",
		strings.Repeat("x", MaxLen+1)}
	for _, s := range bad {
		if Valid(s) {
			t.Errorf("Valid(%q) = true", s)
		}
	}
}
